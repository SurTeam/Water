package govt

import (
	"hash/fnv"
	"sync"

	xterm "github.com/gitpod-io/xterm-go"
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
	Width         uint8
	FG            Color
	BG            Color
	Bold          bool
	Italic        bool
	Dim           bool
	Underline     bool
	Strikethrough bool
	Inverse       bool
	Invisible     bool
	URLID         int
	LinkURI       string
}

type Row struct {
	Cells   []Cell
	Hash    uint64
	Wrapped bool
}

type Snapshot struct {
	Cols       int
	Rows       int
	CursorX    int
	CursorY    int
	CursorHide bool
	YBase      int
	YDisp      int
	AltScreen         bool
	ApplicationCursor bool
	BracketedPaste    bool
	MouseTracking     string
	MouseEncoding     string
	RowsData          []Row
	Images            []TerminalImage
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
	mu   sync.RWMutex
	term *xterm.Terminal

	responseMu sync.Mutex
	responses  [][]byte
	suppressResponses bool

	titleMu sync.RWMutex
	title   string

	links    *osc8Tracker
	graphics *graphicsState
	pendingCellSizeQuery []byte
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
		links: newOSC8Tracker(),
		graphics: newGraphicsState(),
		term: xterm.New(
			xterm.WithCols(cols),
			xterm.WithRows(rows),
			xterm.WithScrollback(scrollback),
		),
	}
	e.term.OnData(func(data string) {
		e.enqueueResponse([]byte(data))
	})
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

func (e *Emulator) Close() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.graphics!=nil { e.graphics.close() }
	e.term.Dispose()
}

func (e *Emulator) Write(p []byte) {
	e.write(p,false)
}

func (e *Emulator) WriteReplay(p []byte) {
	e.write(p,true)
}

func (e *Emulator) write(p []byte,replay bool) {
	e.mu.Lock()
	previous:=e.suppressResponses
	e.suppressResponses=replay
	p=e.filterCellSizeQueryLocked(p)
	e.applyGraphicsEraseLocked(p)
	e.links.feed(p)

	events:=e.graphics.parser.feed(p)
	consumed:=0
	for _,event:=range events {
		end:=event.endOffset
		if end<consumed {end=consumed}
		if end>len(p) {end=len(p)}
		if end>consumed {
			_,_ = e.term.Write(p[consumed:end])
		}
		rows,response:=e.graphics.handle(e.term,event)
		if len(response)>0 { e.enqueueResponse(response) }
		if rows>0 {
			advance:=make([]byte,0,2+rows)
			advance=append(advance,'\r','\n')
			for n:=1;n<rows;n++ { advance=append(advance,'\n') }
			_,_ = e.term.Write(advance)
		}
		consumed=end
	}
	if consumed<len(p) {
		_,_ = e.term.Write(p[consumed:])
	}
	e.pruneLinksLocked()
	e.suppressResponses=previous
	e.mu.Unlock()
}

func (e *Emulator) filterCellSizeQueryLocked(p []byte) []byte {
	const query="\x1b[16t"
	if len(e.pendingCellSizeQuery)==0 && !bytesContains(p,[]byte{0x1b}) {
		return p
	}
	input:=make([]byte,0,len(e.pendingCellSizeQuery)+len(p))
	input=append(input,e.pendingCellSizeQuery...)
	e.pendingCellSizeQuery=e.pendingCellSizeQuery[:0]
	input=append(input,p...)
	filtered:=make([]byte,0,len(input))
	for i:=0;i<len(input); {
		remaining:=input[i:]
		if len(remaining)<len(query) && string(query[:len(remaining)])==string(remaining) {
			e.pendingCellSizeQuery=append(e.pendingCellSizeQuery,remaining...)
			break
		}
		if len(remaining)>=len(query) && string(remaining[:len(query)])==query {
			width,height:=8,16
			if e.graphics!=nil {
				width=maxInt(e.graphics.cellWidth,1)
				height=maxInt(e.graphics.cellHeight,1)
			}
			e.enqueueResponse([]byte("\x1b[6;"+itoaPositive(height)+";"+itoaPositive(width)+"t"))
			i+=len(query)
			continue
		}
		filtered=append(filtered,input[i])
		i++
	}
	return filtered
}

func (e *Emulator) applyGraphicsEraseLocked(p []byte) {
	if e.graphics==nil || len(p)==0 { return }
	if bytesContains(p,[]byte("\x1b[3J")) {
		e.graphics.eraseScrollback(e.term)
	}
	if e.term.IsAltBufferActive() && bytesContains(p,[]byte("\x1b[2J")) {
		e.graphics.eraseVisible(e.term)
	}
}

func bytesContains(haystack,needle []byte)bool {
	if len(needle)==0{return true}
	if len(haystack)<len(needle){return false}
	for i:=0;i+len(needle)<=len(haystack);i++ {
		match:=true
		for j:=range needle {
			if haystack[i+j]!=needle[j]{match=false;break}
		}
		if match{return true}
	}
	return false
}

func itoaPositive(v int)string {
	if v<=0{return "0"}
	var buf [20]byte
	i:=len(buf)
	for v>0 { i--;buf[i]=byte('0'+v%10);v/=10 }
	return string(buf[i:])
}

func (e *Emulator) enqueueResponse(data []byte) {
	if len(data)==0 || e.suppressResponses { return }
	e.responseMu.Lock()
	e.responses=append(e.responses,append([]byte(nil),data...))
	e.responseMu.Unlock()
}

func (e *Emulator) pruneLinksLocked() {
	if e.links==nil || (len(e.links.uriByID)==0 && e.links.activeID==0){return}
	buf:=e.term.Buffer()
	live:=make(map[int]struct{})
	var raw xterm.CellData
	for row:=0;row<buf.Lines.Length();row++{
		line:=buf.Lines.Get(row)
		if line==nil{continue}
		for col:=0;col<line.Len;col++{
			line.LoadCell(col,&raw)
			if raw.Extended==nil{continue}
			if id:=raw.Extended.URLID();id!=0{live[id]=struct{}{}}
		}
	}
	e.links.prune(live)
}

func (e *Emulator) SetCellSize(width,height int) {
	e.mu.Lock()
	if e.graphics!=nil { e.graphics.setCellSize(width,height) }
	e.mu.Unlock()
}

func (e *Emulator) ImageBytes() int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.graphics==nil { return 0 }
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

	button:=xterm.MouseButtonLeft
	switch ev.Button {
	case MouseMiddle:
		button=xterm.MouseButtonMiddle
	case MouseRight:
		button=xterm.MouseButtonRight
	case MouseWheel:
		button=xterm.MouseButtonWheel
	case MouseNone:
		button=xterm.MouseButtonNone
	}
	action:=xterm.MouseActionDown
	switch ev.Action {
	case MouseUp:
		action=xterm.MouseActionUp
	case MouseMove:
		action=xterm.MouseActionMove
	}
	return e.term.TriggerMouseEvent(xterm.CoreMouseEvent{
		Col:ev.Col,Row:ev.Row,X:ev.X,Y:ev.Y,
		Button:button,Action:action,
		Ctrl:ev.Ctrl,Alt:ev.Alt,Shift:ev.Shift,
	})
}

func (e *Emulator) TakeResponses() [][]byte {
	e.responseMu.Lock()
	out := e.responses
	e.responses = nil
	e.responseMu.Unlock()
	return out
}

func (e *Emulator) Snapshot() Snapshot {
	e.mu.RLock()
	defer e.mu.RUnlock()

	term := e.term
	buf := term.Buffer()
	s := Snapshot{
		Cols:       term.Cols(),
		Rows:       term.Rows(),
		CursorX:    term.CursorX(),
		CursorY:    term.CursorY(),
		CursorHide: term.IsCursorHidden(),
		YBase:      buf.YBase,
		YDisp:      buf.YDisp,
		AltScreen:  term.IsAltBufferActive(),
		RowsData:   make([]Row, term.Rows()),
	}
	modes := term.DecPrivateModes()
	s.ApplicationCursor = modes.ApplicationCursorKeys
	s.BracketedPaste = modes.BracketedPasteMode
	s.MouseTracking = modes.MouseTrackingMode
	s.MouseEncoding = modes.MouseEncoding

	for row := 0; row < term.Rows(); row++ {
		line := buf.Lines.Get(buf.YDisp + row)
		if line == nil {
			s.RowsData[row] = Row{Cells: make([]Cell, term.Cols())}
			continue
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
				cell.LinkURI = e.links.uri(cell.URLID)
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
	}
	if e.graphics!=nil {
		s.Images=e.graphics.snapshot(term,s.RowsData)
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
