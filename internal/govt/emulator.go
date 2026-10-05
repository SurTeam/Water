package govt

import (
	"bytes"
	"hash/fnv"
	"strings"
	"sync"
	"sync/atomic"

	xterm "github.com/SurTeam/Water/internal/xterm"
)

type ColorMode uint8

const (
	ColorDefault ColorMode = iota
	ColorPalette
	ColorRGB
)

type Color struct {
	Mode  ColorMode
	Value uint32
}

type Cell struct {
	Text          string
	LinkURI       string
	URLID         int
	FG            Color
	BG            Color
	Width         uint8
	Bold          bool
	Italic        bool
	Dim           bool
	Underline     bool
	Strikethrough bool
	Inverse       bool
	Invisible     bool
}

type Row struct {
	Cells   []Cell
	Hash    uint64
	Wrapped bool
}

type Snapshot struct {
	Cols                int
	Rows                int
	CursorX             int
	CursorY             int // Viewport row; outside [0, Rows) when the cursor is offscreen.
	CursorHide          bool
	CursorStyle         string
	CursorBlink         bool
	YBase               int
	YDisp               int
	AltScreen           bool
	ApplicationCursor   bool
	BracketedPaste      bool
	MouseTracking       string
	MouseEncoding       string
	RowsData            []Row
	Images              []TerminalImage
	ColorOverrides      map[int]uint32
	WorkingDirectoryURI string
}

type MouseButton uint8

const (
	MouseLeft MouseButton = iota
	MouseMiddle
	MouseRight
	MouseWheel
	MouseNone
)

type MouseAction uint8

const (
	MouseUp MouseAction = iota
	MouseDown
	MouseMove
)

type MouseEvent struct {
	Col, Row int
	X, Y     int
	Button   MouseButton
	Action   MouseAction
	Ctrl     bool
	Alt      bool
	Shift    bool
}

type Emulator struct {
	frameMu   sync.Mutex
	frameRows map[*xterm.BufferLine]cachedFrameRow
	bells     atomic.Uint64
	progress  atomic.Uint32 // zero means no OSC 9;4 report; otherwise state + 1
	mu        sync.RWMutex
	term      *xterm.Terminal

	responseMu        sync.Mutex
	responses         [][]byte
	responseBytes     int
	suppressResponses bool

	titleMu sync.RWMutex
	title   string

	linkParser           osc8Parser
	defaultColors        atomic.Pointer[[259]uint32]
	colorOverrides       map[int]uint32
	graphics             *graphicsState
	windowMetrics        atomic.Pointer[WindowMetrics]
	workingDirectoryURI  string
	clipboardWrite       []byte
	clipboardMu          sync.Mutex
	colorSchemeUpdates   atomic.Bool
	backgroundOverridden atomic.Bool
}

func New(cols, rows, scrollback int) *Emulator {
	if cols < 2 {
		cols = 80
	}
	if rows < 1 {
		rows = 24
	}
	if scrollback < 1 {
		scrollback = 2000
	}
	e := &Emulator{
		colorOverrides: make(map[int]uint32),
		graphics:       newGraphicsState(),
		term: xterm.New(
			xterm.WithCols(cols),
			xterm.WithRows(rows),
			xterm.WithScrollback(scrollback),
		),
	}
	e.term.SetCursorDefaults(xterm.CursorStyleBlock, true)
	e.term.OnData(func(data string) {
		e.enqueueResponse([]byte(data))
	})
	e.term.OnColor(e.handleColors)
	e.term.OnBell(func() { e.bells.Add(1) })
	e.term.OnRequestWindowsOptionsReport(e.reportWindow)
	e.term.OnRequestColorSchemeQuery(func() { e.reportColorScheme() })
	e.registerOSCMetadata()
	e.term.OnBinary(func(data string) {
		e.enqueueResponse([]byte(data))
	})
	e.term.OnTitleChange(func(title string) {
		e.titleMu.Lock()
		e.title = title
		e.titleMu.Unlock()
	})
	return e
}

func (e *Emulator) BellCount() uint64 { return e.bells.Load() }

func (e *Emulator) Close() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.graphics != nil {
		e.graphics.close()
	}
	e.term.Dispose()
}

func (e *Emulator) Write(p []byte) {
	e.write(p, false)
}

func (e *Emulator) WriteReplay(p []byte) {
	e.write(p, true)
}

func (e *Emulator) write(p []byte, replay bool) {
	e.mu.Lock()
	previous := e.suppressResponses
	e.suppressResponses = replay
	if e.canFastWriteOrdinary(p) {
		_, _ = e.term.Write(p)
		e.suppressResponses = previous
		e.colorSchemeUpdates.Store(e.term.DecPrivateModes().ColorSchemeUpdates)
		e.mu.Unlock()
		return
	}

	e.applyGraphicsEraseLocked(p)
	e.linkParser.feed(p)

	events := e.graphics.parser.feed(p)
	consumed := 0
	for _, event := range events {
		end := event.endOffset
		if end < consumed {
			end = consumed
		}
		if end > len(p) {
			end = len(p)
		}
		if end > consumed {
			_, _ = e.term.Write(p[consumed:end])
		}
		rows, response := e.graphics.handle(e.term, event)
		if len(response) > 0 {
			e.enqueueResponse(response)
		}
		if rows > 0 {
			advance := make([]byte, 0, 2+rows)
			advance = append(advance, '\r', '\n')
			for n := 1; n < rows; n++ {
				advance = append(advance, '\n')
			}
			_, _ = e.term.Write(advance)
		}
		consumed = end
	}
	if consumed < len(p) {
		_, _ = e.term.Write(p[consumed:])
	}
	e.suppressResponses = previous
	e.colorSchemeUpdates.Store(e.term.DecPrivateModes().ColorSchemeUpdates)
	e.mu.Unlock()
}

func (e *Emulator) canFastWriteOrdinary(p []byte) bool {
	if len(p) == 0 {
		return false
	}
	if e.graphics != nil && len(e.graphics.parser.buffer) != 0 {
		return false
	}
	if e.linkParser.state != osc8Normal {
		return false
	}
	for _, b := range p {
		if b == 0x1b || b >= 0x80 {
			return false
		}
	}
	return true
}

func (e *Emulator) applyGraphicsEraseLocked(p []byte) {
	if e.graphics == nil || len(p) == 0 || bytes.IndexByte(p, 0x1b) < 0 {
		return
	}
	if bytes.Contains(p, []byte("\x1b[3J")) {
		e.graphics.eraseScrollback(e.term)
	}
	if e.term.IsAltBufferActive() && bytes.Contains(p, []byte("\x1b[2J")) {
		e.graphics.eraseVisible(e.term)
	}
}

func (e *Emulator) enqueueResponse(data []byte) {
	if len(data) == 0 || e.suppressResponses {
		return
	}
	e.enqueueLiveResponse(data)
}

func (e *Emulator) enqueueLiveResponse(data []byte) {
	e.responseMu.Lock()
	defer e.responseMu.Unlock()
	if len(e.responses) >= 1024 || e.responseBytes+len(data) > 256*1024 {
		return
	}
	e.responses = append(e.responses, append([]byte(nil), data...))
	e.responseBytes += len(data)
}

func (e *Emulator) SetCellSize(width, height int) {
	e.mu.Lock()
	if e.graphics != nil {
		e.graphics.setCellSize(width, height)
	}
	e.mu.Unlock()
}

func (e *Emulator) ImageBytes() int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.graphics == nil {
		return 0
	}
	return e.graphics.storedBytes
}

func (e *Emulator) Resize(cols, rows int) {
	e.mu.Lock()
	e.term.Resize(cols, rows)
	e.mu.Unlock()
}

func (e *Emulator) Scroll(lines int) {
	e.mu.Lock()
	e.term.ScrollLines(lines)
	e.mu.Unlock()
}

func (e *Emulator) ScrollToBottom() {
	e.mu.Lock()
	e.term.ScrollToBottom()
	e.mu.Unlock()
}

func (e *Emulator) SelectionText(startRow, startCol, endRow, endCol int) string {
	e.mu.RLock()
	defer e.mu.RUnlock()

	if startRow > endRow || (startRow == endRow && startCol > endCol) {
		startRow, endRow = endRow, startRow
		startCol, endCol = endCol, startCol
	}
	buf := e.term.Buffer()
	if buf == nil || buf.Lines.Length() == 0 {
		return ""
	}
	cols := e.term.Cols()
	if cols <= 0 {
		return ""
	}
	if startRow < 0 {
		startRow = 0
	}
	if endRow >= buf.Lines.Length() {
		endRow = buf.Lines.Length() - 1
	}
	if startRow > endRow {
		return ""
	}

	var out strings.Builder
	var raw xterm.CellData
	for row := startRow; row <= endRow; row++ {
		line := buf.Lines.Get(row)
		if line == nil {
			continue
		}
		left, right := 0, cols-1
		if row == startRow {
			left = startCol
		}
		if row == endRow {
			right = endCol
		}
		if left < 0 {
			left = 0
		}
		if right >= cols {
			right = cols - 1
		}
		if left > right {
			continue
		}
		var ok bool
		left, right, ok = ExpandSelectionColumns(left, right, min(cols, line.Len), func(column int) int { return bufferDrawingColumns(line, column) })
		if !ok {
			continue
		}

		var rowText strings.Builder
		for col := left; col <= right && col < line.Len; col++ {
			line.LoadCell(col, &raw)
			if raw.GetWidth() == 0 {
				continue
			}
			text := raw.GetChars()
			if text == "" {
				rowText.WriteByte(' ')
			} else {
				rowText.WriteString(text)
			}
		}
		out.WriteString(strings.TrimRight(rowText.String(), " "))
		if row != endRow {
			next := buf.Lines.Get(row + 1)
			if next == nil || !next.IsWrapped {
				out.WriteByte('\n')
			}
		}
	}
	return out.String()
}

func (e *Emulator) Text() string {
	e.mu.RLock()
	s := e.term.String()
	e.mu.RUnlock()
	return s
}

func (e *Emulator) Cursor() (int, int) {
	e.mu.RLock()
	x, y := e.term.CursorX(), e.term.CursorY()
	e.mu.RUnlock()
	return x, y
}

func (e *Emulator) Title() string {
	e.titleMu.RLock()
	title := e.title
	e.titleMu.RUnlock()
	return title
}

func (e *Emulator) Mouse(ev MouseEvent) bool {
	e.mu.Lock()
	defer e.mu.Unlock()

	button := xterm.MouseButtonLeft
	switch ev.Button {
	case MouseMiddle:
		button = xterm.MouseButtonMiddle
	case MouseRight:
		button = xterm.MouseButtonRight
	case MouseWheel:
		button = xterm.MouseButtonWheel
	case MouseNone:
		button = xterm.MouseButtonNone
	}
	action := xterm.MouseActionDown
	switch ev.Action {
	case MouseUp:
		action = xterm.MouseActionUp
	case MouseMove:
		action = xterm.MouseActionMove
	}
	return e.term.TriggerMouseEvent(xterm.CoreMouseEvent{
		Col: ev.Col, Row: ev.Row, X: ev.X, Y: ev.Y,
		Button: button, Action: action,
		Ctrl: ev.Ctrl, Alt: ev.Alt, Shift: ev.Shift,
	})
}

func (e *Emulator) TakeResponses() [][]byte {
	e.responseMu.Lock()
	out := e.responses
	e.responses = nil
	e.responseBytes = 0
	e.responseMu.Unlock()
	return out
}

func (e *Emulator) Snapshot() Snapshot {
	return e.snapshot(false)
}

type cachedFrameRow struct {
	revision uint64
	row      Row
}

// FrameSnapshot returns an immutable visible view. Unchanged rows share their
// cell storage with earlier frames; changed rows receive fresh storage. The
// cache retains only the current viewport, never the scrollback. Callers must
// not mutate cells. Snapshot remains the independently owned copy API.
func (e *Emulator) FrameSnapshot() Snapshot {
	e.frameMu.Lock()
	defer e.frameMu.Unlock()
	return e.snapshot(true)
}

func (e *Emulator) snapshot(reuseRows bool) Snapshot {
	e.mu.RLock()
	defer e.mu.RUnlock()

	term := e.term
	buf := term.Buffer()
	s := Snapshot{
		WorkingDirectoryURI: e.workingDirectoryURI,
		Cols:                term.Cols(),
		Rows:                term.Rows(),
		CursorX:             term.CursorX(),
		CursorY:             buf.YBase + term.CursorY() - buf.YDisp,
		CursorHide:          term.IsCursorHidden(),
		YBase:               buf.YBase,
		YDisp:               buf.YDisp,
		AltScreen:           term.IsAltBufferActive(),
		RowsData:            make([]Row, term.Rows()),
	}
	if len(e.colorOverrides) > 0 {
		s.ColorOverrides = make(map[int]uint32, len(e.colorOverrides))
		for index, rgb := range e.colorOverrides {
			s.ColorOverrides[index] = rgb
		}
	}
	modes := term.DecPrivateModes()
	style, blink := term.CursorDefaults()
	s.CursorStyle = string(style)
	s.CursorBlink = blink
	if modes.CursorStyle != nil {
		s.CursorStyle = string(*modes.CursorStyle)
	}
	if modes.CursorBlinkOverride != nil {
		s.CursorBlink = *modes.CursorBlinkOverride
	}
	s.ApplicationCursor = modes.ApplicationCursorKeys
	s.BracketedPaste = modes.BracketedPasteMode
	s.MouseTracking = modes.MouseTrackingMode
	s.MouseEncoding = modes.MouseEncoding

	var nextRows map[*xterm.BufferLine]cachedFrameRow
	if reuseRows {
		nextRows = make(map[*xterm.BufferLine]cachedFrameRow, term.Rows())
	}
	for row := 0; row < term.Rows(); row++ {
		line := buf.Lines.Get(buf.YDisp + row)
		if line == nil {
			s.RowsData[row] = Row{Cells: make([]Cell, term.Cols())}
			continue
		}
		if reuseRows {
			if cached, ok := e.frameRows[line]; ok && cached.revision == line.Revision() && cached.row.Wrapped == line.IsWrapped && len(cached.row.Cells) == term.Cols() {
				s.RowsData[row] = cached.row
				nextRows[line] = cached
				continue
			}
		}
		cells := make([]Cell, term.Cols())
		h := fnv.New64a()
		var raw xterm.CellData
		for col := 0; col < term.Cols(); col++ {
			line.LoadCell(col, &raw)
			cell := Cell{
				Text:          raw.GetChars(),
				Width:         uint8(raw.GetWidth()),
				FG:            colorFromAttrs(&raw.AttributeData, true),
				BG:            colorFromAttrs(&raw.AttributeData, false),
				Bold:          raw.IsBold() != 0,
				Italic:        raw.IsItalic() != 0,
				Dim:           raw.IsDim() != 0,
				Underline:     raw.IsUnderline() != 0,
				Strikethrough: raw.IsStrikethrough() != 0,
				Inverse:       raw.IsInverse() != 0,
				Invisible:     raw.IsInvisible() != 0,
			}
			if raw.Extended != nil {
				cell.URLID = raw.Extended.URLID()
				cell.LinkURI = e.term.HyperlinkURI(cell.URLID)
			}
			if cell.Width == 0 && cell.Text == "" {
				// xterm uses width zero for the trailing half of wide glyphs.
			} else if cell.Width == 0 {
				cell.Width = 1
			}
			cells[col] = cell

			_, _ = h.Write([]byte(cell.Text))
			var packed [16]byte
			packed[0] = byte(cell.Width)
			packed[1] = byte(cell.FG.Mode)
			packed[2] = byte(cell.BG.Mode)
			packed[3] = boolByte(cell.Bold) |
				boolByte(cell.Italic)<<1 |
				boolByte(cell.Dim)<<2 |
				boolByte(cell.Underline)<<3 |
				boolByte(cell.Strikethrough)<<4 |
				boolByte(cell.Inverse)<<5 |
				boolByte(cell.Invisible)<<6
			put32(packed[4:8], cell.FG.Value)
			put32(packed[8:12], cell.BG.Value)
			put32(packed[12:16], uint32(cell.URLID))
			_, _ = h.Write(packed[:])
		}
		s.RowsData[row] = Row{Cells: cells, Hash: h.Sum64(), Wrapped: line.IsWrapped}
		if reuseRows {
			nextRows[line] = cachedFrameRow{revision: line.Revision(), row: s.RowsData[row]}
		}
	}
	if reuseRows {
		e.frameRows = nextRows
	}
	if e.graphics != nil {
		s.Images = e.graphics.snapshot(term, s.RowsData)
	}
	return s
}

func boolByte(v bool) byte {
	if v {
		return 1
	}
	return 0
}

func put32(dst []byte, v uint32) {
	dst[0] = byte(v >> 24)
	dst[1] = byte(v >> 16)
	dst[2] = byte(v >> 8)
	dst[3] = byte(v)
}

func colorFromAttrs(attr *xterm.AttributeData, foreground bool) Color {
	var mode uint32
	var value int
	if foreground {
		mode = attr.GetFgColorMode()
		value = attr.GetFgColor()
	} else {
		mode = attr.GetBgColorMode()
		value = attr.GetBgColor()
	}
	switch mode {
	case xterm.AttrCMP16, xterm.AttrCMP256:
		return Color{Mode: ColorPalette, Value: uint32(value)}
	case xterm.AttrCMRGB:
		return Color{Mode: ColorRGB, Value: uint32(value)}
	default:
		return Color{Mode: ColorDefault}
	}
}
