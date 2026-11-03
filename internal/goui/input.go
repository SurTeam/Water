package goui

import (
	"image"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"gioui.org/f32"
	"gioui.org/io/clipboard"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/io/transfer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"

	"github.com/SurTeam/Water/internal/goconfig"
	"github.com/SurTeam/Water/internal/govt"
)

type TerminalInput struct {
	tag      struct{}
	mouseTag struct{}

	OnInput                  func([]byte)
	OnShortcut               func(key.Event) bool
	Shortcuts                *goconfig.ShortcutConfig
	OnMouse                  func(govt.MouseEvent) bool
	OnScroll                 func(int)
	OnSelectionStart         func(col, row, clickCount int)
	OnSelectionBoundaryStart func(col, row int, extend bool)
	OnSelectionMove          func(col, row int)
	OnSelectionEnd           func(col, row int)
	OnSelectionAutoScroll    func(col, row, lines int)
	OnCopy                   func() string
	OnHyperlink              func(uri string)
	BracketedPaste           bool
	Hyperlinks               bool
	HyperlinkCommandClick    bool

	composing                bool
	pendingComposition       string
	pressedMouse             govt.MouseButton
	mousePressed             bool
	selecting                bool
	hyperlinkPressed         string
	hyperlinkCol             int
	hyperlinkRow             int
	lastClickTime            time.Duration
	lastClickCol             int
	lastClickRow             int
	clickCount               int
	selectionAutoScrollLines int
	selectionAutoScrollCol   int
	selectionAutoScrollRow   int
	selectionAutoScrollAt    time.Time

	imeInitialized       bool
	imeCaret             key.Caret
	imeCompositionBounds image.Rectangle
	shortcutKeys         map[key.Name]struct{}
}

func (i *TerminalInput) Process(gtx layout.Context, snap govt.Snapshot, cellWidth, lineHeight int) {
	if cellWidth < 1 {
		cellWidth = 1
	}
	if lineHeight < 1 {
		lineHeight = 1
	}
	for {
		ev, ok := gtx.Event(pointer.Filter{
			Target:  &i.mouseTag,
			Kinds:   pointer.Press | pointer.Release | pointer.Move | pointer.Drag | pointer.Scroll | pointer.Cancel,
			ScrollX: pointer.ScrollRange{Min: -10000, Max: 10000},
			ScrollY: pointer.ScrollRange{Min: -10000, Max: 10000},
		})
		if !ok {
			break
		}
		pe, ok := ev.(pointer.Event)
		if !ok || pe.Source != pointer.Mouse {
			continue
		}
		if pe.Kind == pointer.Press {
			gtx.Execute(key.FocusCmd{Tag: &i.tag})
			gtx.Execute(key.SoftKeyboardCmd{Show: true})
		}

		col0 := int(pe.Position.X) / cellWidth
		row0 := int(pe.Position.Y) / lineHeight
		if col0 < 0 {
			col0 = 0
		}
		if col0 >= snap.Cols {
			col0 = snap.Cols - 1
		}
		if row0 < 0 {
			row0 = 0
		}
		if row0 >= snap.Rows {
			row0 = snap.Rows - 1
		}
		base := govt.MouseEvent{
			Col: col0 + 1, Row: row0 + 1,
			X: int(pe.Position.X) + 1, Y: int(pe.Position.Y) + 1,
			Ctrl:  pe.Modifiers.Contain(key.ModCtrl),
			Alt:   pe.Modifiers.Contain(key.ModAlt),
			Shift: pe.Modifiers.Contain(key.ModShift),
		}
		tracking := snap.MouseTracking != "" && snap.MouseTracking != "NONE"
		localOverride := pe.Modifiers.Contain(key.ModShift)

		switch pe.Kind {
		case pointer.Press:
			button, ok := mouseButton(pe.Buttons)
			if !ok {
				continue
			}
			clickCount := 1
			if button == govt.MouseLeft {
				clickCount = i.nextClickCount(pe.Time, col0, row0)
			}
			uri := hyperlinkURIAt(snap, col0, row0)
			if button == govt.MouseLeft && clickCount == 1 && i.Hyperlinks && uri != "" &&
				hyperlinkClickAllowed(i.HyperlinkCommandClick, pe.Modifiers) {
				i.hyperlinkPressed = uri
				i.hyperlinkCol = col0
				i.hyperlinkRow = row0
				i.mousePressed = false
				i.selecting = false
				continue
			}
			if button == govt.MouseLeft && i.OnSelectionStart != nil && (!tracking || localOverride) {
				i.selecting = clickCount == 1
				i.mousePressed = false
				i.OnSelectionStart(col0, row0, clickCount)
				continue
			}
			i.pressedMouse = button
			i.mousePressed = true
			base.Button = button
			base.Action = govt.MouseDown
			if tracking && i.OnMouse != nil {
				_ = i.OnMouse(base)
			}
		case pointer.Release:
			if i.hyperlinkPressed != "" {
				uri := i.hyperlinkPressed
				same := i.hyperlinkCol == col0 && i.hyperlinkRow == row0 && hyperlinkURIAt(snap, col0, row0) == uri
				i.hyperlinkPressed = ""
				if same && i.OnHyperlink != nil {
					i.OnHyperlink(uri)
				}
				continue
			}
			if i.selecting {
				if i.OnSelectionEnd != nil {
					i.OnSelectionEnd(col0, row0)
				}
				i.selecting = false
				i.clearSelectionAutoScroll()
				continue
			}
			if !i.mousePressed {
				continue
			}
			base.Button = i.pressedMouse
			base.Action = govt.MouseUp
			if tracking && i.OnMouse != nil {
				_ = i.OnMouse(base)
			}
			i.mousePressed = false
		case pointer.Move, pointer.Drag:
			if i.hyperlinkPressed != "" {
				if i.hyperlinkCol != col0 || i.hyperlinkRow != row0 {
					i.hyperlinkPressed = ""
				}
				continue
			}
			if i.selecting {
				if i.OnSelectionMove != nil {
					i.OnSelectionMove(col0, row0)
				}
				i.updateSelectionAutoScroll(float32(pe.Position.Y), snap.Rows, lineHeight, col0, gtx.Now)
				continue
			}
			if !tracking {
				continue
			}
			if i.mousePressed {
				base.Button = i.pressedMouse
			} else {
				base.Button = govt.MouseNone
			}
			base.Action = govt.MouseMove
			if i.OnMouse != nil {
				_ = i.OnMouse(base)
			}
		case pointer.Scroll:
			steps := terminalWheelSteps(float64(pe.Scroll.Y), float64(gtx.Dp(40)))
			if steps == 0 {
				continue
			}
			if tracking && !localOverride && i.OnMouse != nil {
				action := govt.MouseDown
				if steps < 0 {
					action = govt.MouseUp
					steps = -steps
				}
				base.Button = govt.MouseWheel
				base.Action = action
				accepted := false
				for n := 0; n < min(steps, 64); n++ {
					if i.OnMouse(base) {
						accepted = true
					}
				}
				if accepted {
					continue
				}
			}
			if i.OnScroll != nil {
				lines := terminalWheelSteps(float64(pe.Scroll.Y), float64(gtx.Dp(40)))
				i.OnScroll(lines)
			}
		case pointer.Cancel:
			i.mousePressed = false
			i.selecting = false
			i.hyperlinkPressed = ""
			i.clearSelectionAutoScroll()
		}
	}

	i.processSelectionAutoScroll(gtx)

	allMods := key.ModCtrl | key.ModAlt | key.ModShift | key.ModCommand | key.ModSuper
	filters := []event.Filter{
		key.FocusFilter{Target: &i.tag},
		transfer.TargetFilter{Target: &i.tag, Type: "application/text"},
		key.Filter{Focus: &i.tag, Name: "V", Required: key.ModShortcut},
		key.Filter{Focus: &i.tag, Name: "C", Required: key.ModShortcut},
		key.Filter{Focus: &i.tag, Name: key.NameReturn, Optional: allMods},
		key.Filter{Focus: &i.tag, Name: key.NameEnter, Optional: allMods},
		key.Filter{Focus: &i.tag, Name: key.NameEscape, Optional: allMods},
		key.Filter{Focus: &i.tag, Name: key.NameTab, Optional: allMods},
		key.Filter{Focus: &i.tag, Name: key.NameDeleteBackward, Optional: allMods},
		key.Filter{Focus: &i.tag, Name: key.NameDeleteForward, Optional: allMods},
		key.Filter{Focus: &i.tag, Name: key.NameLeftArrow, Optional: allMods},
		key.Filter{Focus: &i.tag, Name: key.NameRightArrow, Optional: allMods},
		key.Filter{Focus: &i.tag, Name: key.NameUpArrow, Optional: allMods},
		key.Filter{Focus: &i.tag, Name: key.NameDownArrow, Optional: allMods},
		key.Filter{Focus: &i.tag, Name: key.NameHome, Optional: allMods},
		key.Filter{Focus: &i.tag, Name: key.NameEnd, Optional: allMods},
		key.Filter{Focus: &i.tag, Name: key.NamePageUp, Optional: allMods},
		key.Filter{Focus: &i.tag, Name: key.NamePageDown, Optional: allMods},
		key.Filter{Focus: &i.tag, Name: key.NameF1, Optional: allMods},
		key.Filter{Focus: &i.tag, Name: key.NameF2, Optional: allMods},
		key.Filter{Focus: &i.tag, Name: key.NameF3, Optional: allMods},
		key.Filter{Focus: &i.tag, Name: key.NameF4, Optional: allMods},
		key.Filter{Focus: &i.tag, Name: key.NameF5, Optional: allMods},
		key.Filter{Focus: &i.tag, Name: key.NameF6, Optional: allMods},
		key.Filter{Focus: &i.tag, Name: key.NameF7, Optional: allMods},
		key.Filter{Focus: &i.tag, Name: key.NameF8, Optional: allMods},
		key.Filter{Focus: &i.tag, Name: key.NameF9, Optional: allMods},
		key.Filter{Focus: &i.tag, Name: key.NameF10, Optional: allMods},
		key.Filter{Focus: &i.tag, Name: key.NameF11, Optional: allMods},
		key.Filter{Focus: &i.tag, Name: key.NameF12, Optional: allMods},
		key.Filter{Focus: &i.tag, Optional: allMods},
	}

	for {
		ev, ok := gtx.Event(filters...)
		if !ok {
			break
		}
		switch ev := ev.(type) {
		case transfer.DataEvent:
			reader := ev.Open()
			if reader == nil {
				continue
			}
			data, err := io.ReadAll(reader)
			_ = reader.Close()
			if err != nil || len(data) == 0 {
				continue
			}
			if i.BracketedPaste && snap.BracketedPaste {
				wrapped := make([]byte, 0, len(data)+12)
				wrapped = append(wrapped, []byte("[200~")...)
				wrapped = append(wrapped, data...)
				wrapped = append(wrapped, []byte("[201~")...)
				i.emit(wrapped)
			} else {
				i.emit(data)
			}
		case key.FocusEvent:
			i.imeInitialized = false
			if !ev.Focus {
				i.resetComposition()
			}
		case key.CompositionEvent:
			i.handleComposition(ev)
		case key.EditEvent:
			i.handleEditWithFlags(ev, snap.KittyKeyboardFlags)
		case key.SnippetEvent, key.SelectionEvent:
			// A terminal has no editable backing document. Re-publish the
			// zero-length snippet/selection at the terminal cursor instead of
			// allowing the platform IME to move through screen contents.
			i.imeInitialized = false
		case key.Event:
			if ev.State != key.Press && snap.KittyKeyboardFlags&2 == 0 {
				if ev.State == key.Release {
					delete(i.shortcutKeys, ev.Name)
				}
				continue
			}
			if ev.State == key.Release {
				if _, ok := i.shortcutKeys[ev.Name]; ok {
					delete(i.shortcutKeys, ev.Name)
					continue
				}
			}
			if ev.State == key.Press && i.OnShortcut != nil && i.OnShortcut(ev) {
				if i.shortcutKeys == nil {
					i.shortcutKeys = make(map[key.Name]struct{})
				}
				i.shortcutKeys[ev.Name] = struct{}{}
				continue
			}
			if ev.State != key.Press {
				if seq := EncodeKeyAdvanced(ev, snap.ApplicationCursor, snap.KittyKeyboardFlags); len(seq) > 0 {
					i.emit(seq)
				}
				continue
			}
			bindings := goconfig.ShortcutConfig{Paste: "cmd-v", CopyOrInterrupt: "cmd-c", EOF: "ctrl-d", ScrollPageUp: "shift-pageup", ScrollPageDown: "shift-pagedown"}
			if i.Shortcuts != nil {
				bindings = *i.Shortcuts
			}
			if kittyCtrlDisambiguated(ev, snap.KittyKeyboardFlags) {
				if seq := EncodeKeyAdvanced(ev, snap.ApplicationCursor, snap.KittyKeyboardFlags); len(seq) > 0 {
					i.emit(seq)
				}
				continue
			}
			if shortcutMatches(ev, bindings.Paste) {
				gtx.Execute(clipboard.ReadCmd{Tag: &i.tag})
				continue
			}
			if shortcutMatches(ev, bindings.CopyOrInterrupt) {
				if i.OnCopy != nil {
					if selected := i.OnCopy(); selected != "" {
						gtx.Execute(clipboard.WriteCmd{
							Type: "application/text",
							Data: io.NopCloser(strings.NewReader(selected)),
						})
						continue
					}
				}
				if !ev.Modifiers.Contain(key.ModCommand) && !ev.Modifiers.Contain(key.ModSuper) {
					i.emit([]byte{0x03})
				}
				continue
			}
			if shortcutMatches(ev, bindings.EOF) {
				if !ev.Modifiers.Contain(key.ModCommand) && !ev.Modifiers.Contain(key.ModSuper) {
					i.emit([]byte{0x04})
				}
				continue
			}
			if shortcutMatches(ev, bindings.ScrollPageUp) {
				if i.OnScroll != nil {
					i.OnScroll(-snap.Rows)
				}
				continue
			}
			if shortcutMatches(ev, bindings.ScrollPageDown) {
				if i.OnScroll != nil {
					i.OnScroll(snap.Rows)
				}
				continue
			}
			if ev.Modifiers.Contain(key.ModCommand) || ev.Modifiers.Contain(key.ModSuper) {
				continue
			}
			if seq := EncodeKeyAdvanced(ev, snap.ApplicationCursor, snap.KittyKeyboardFlags); len(seq) > 0 {
				i.emit(seq)
			}
		}
	}
	i.syncIME(gtx, snap, cellWidth, lineHeight)
}

// Kitty disambiguation reports Ctrl+letter as CSI-u so applications can
// distinguish it from a plain control byte. The GUI's Ctrl-C/Ctrl-D shortcuts
// must not consume those presses before the terminal encoder sees them.
func kittyCtrlDisambiguated(ev key.Event, flags int) bool {
	if flags&(kittyDisambiguate|kittyReportAllKeys) == 0 || ev.State != key.Press {
		return false
	}
	if !ev.Modifiers.Contain(key.ModCtrl) || ev.Modifiers.Contain(key.ModAlt) ||
		ev.Modifiers.Contain(key.ModCommand) || ev.Modifiers.Contain(key.ModSuper) {
		return false
	}
	name := string(ev.Name)
	if len(name) != 1 {
		return false
	}
	r := rune(name[0])
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'
}

func (i *TerminalInput) Add(gtx layout.Context, size image.Point) {
	stack := clip.Rect{Max: size}.Push(gtx.Ops)
	event.Op(gtx.Ops, &i.tag)
	event.Op(gtx.Ops, &i.mouseTag)
	key.InputHintOp{Tag: &i.tag, Hint: key.HintText}.Add(gtx.Ops)
	stack.Pop()
}

func (i *TerminalInput) CompositionState() (string, bool) {
	if !i.composing {
		return "", false
	}
	return i.pendingComposition, true
}

func (i *TerminalInput) CompositionText() string {
	text, _ := i.CompositionState()
	return text
}

func (i *TerminalInput) Focus(gtx layout.Context) {
	gtx.Execute(key.FocusCmd{Tag: &i.tag})
}

func compositionActive(rng key.Range) bool {
	return rng.Start >= 0 && rng.End >= 0
}

func (i *TerminalInput) handleComposition(ev key.CompositionEvent) {
	active := compositionActive(key.Range(ev))
	i.composing = active
	if !active {
		// The platform IME sends the committed EditEvent after ending the
		// composition. Never leak the last preedit into the PTY here.
		i.pendingComposition = ""
	}
	i.imeInitialized = false
}

func (i *TerminalInput) handleEdit(ev key.EditEvent) {
	i.handleEditWithFlags(ev, 0)
}

func (i *TerminalInput) handleEditWithFlags(ev key.EditEvent, kittyFlags int) {
	if i.composing {
		i.pendingComposition = ev.Text
		i.imeInitialized = false
		return
	}
	i.pendingComposition = ""
	i.handleTerminalText(ev.Text, kittyFlags)
	i.imeInitialized = false
}

func (i *TerminalInput) handleTerminalText(text string, kittyFlags int) {
	if text == "" || kittyFlags&kittyReportAllKeys != 0 {
		// With all keys reported, text-producing presses arrive as CSI-u key
		// events; the platform's text event would duplicate those bytes.
		return
	}
	i.emit([]byte(text))
}

func (i *TerminalInput) resetComposition() {
	i.composing = false
	i.pendingComposition = ""
	i.imeInitialized = false
}

func (i *TerminalInput) syncIME(gtx layout.Context, snap govt.Snapshot, cellWidth, lineHeight int) {
	if !gtx.Focused(&i.tag) {
		i.imeInitialized = false
		return
	}
	if cellWidth < 1 {
		cellWidth = 1
	}
	if lineHeight < 1 {
		lineHeight = 1
	}

	cursorX := snap.CursorX
	cursorY := snap.CursorY
	if cursorX < 0 {
		cursorX = 0
	}
	if cursorY < 0 {
		cursorY = 0
	}
	if snap.Cols > 0 && cursorX >= snap.Cols {
		cursorX = snap.Cols - 1
	}
	if snap.Rows > 0 && cursorY >= snap.Rows {
		cursorY = snap.Rows - 1
	}

	x := cursorX * cellWidth
	top := cursorY * lineHeight
	baseline := top + lineHeight - 2
	if baseline < top {
		baseline = top
	}
	caret := key.Caret{
		Pos:     f32.Pt(float32(x), float32(baseline)),
		Ascent:  float32(baseline - top),
		Descent: float32(top + lineHeight - baseline),
	}
	compositionBounds := image.Rectangle{}
	if i.composing && i.pendingComposition != "" {
		columns := utf8.RuneCountInString(i.pendingComposition)
		if columns < 1 {
			columns = 1
		}
		compositionBounds = image.Rect(x, top, x+columns*cellWidth, top+lineHeight)
	}

	if !i.imeInitialized {
		gtx.Execute(key.SnippetCmd{
			Tag:     &i.tag,
			Snippet: key.Snippet{Range: key.Range{Start: 0, End: 0}, Text: ""},
		})
	}
	if !i.imeInitialized || i.imeCaret != caret || i.imeCompositionBounds != compositionBounds {
		gtx.Execute(key.SelectionCmd{
			Tag:               &i.tag,
			Range:             key.Range{Start: 0, End: 0},
			Caret:             caret,
			CompositionBounds: compositionBounds,
		})
		i.imeCaret = caret
		i.imeCompositionBounds = compositionBounds
	}
	i.imeInitialized = true
}

func (i *TerminalInput) emit(data []byte) {
	if len(data) == 0 || i.OnInput == nil {
		return
	}
	i.OnInput(data)
}

func EncodeKey(ev key.Event, applicationCursor bool) []byte {
	return EncodeKeyAdvanced(ev, applicationCursor, 0)
}

// EncodeKeyAdvanced encodes a key event using the legacy protocol or the
// active Kitty keyboard protocol flags.
func EncodeKeyAdvanced(ev key.Event, applicationCursor bool, kittyFlags int) []byte {
	if kittyFlags == 0 {
		return encodeLegacyKey(ev, applicationCursor)
	}
	return encodeKittyKey(ev, applicationCursor, kittyFlags)
}

const (
	kittyDisambiguate  = 1
	kittyReportEvents  = 2
	kittyReportAllKeys = 8
	kittyModifierShift = 1
	kittyModifierAlt   = 2
	kittyModifierCtrl  = 4
	kittyModifierSuper = 8
	kittyKeyEscape     = 27
	kittyKeyEnter      = 13
	kittyKeyTab        = 9
	kittyKeyBackspace  = 127
	kittyKeyF13        = 57376
)

func encodeKittyKey(ev key.Event, applicationCursor bool, flags int) []byte {
	if ev.State == key.Release && flags&kittyReportEvents == 0 {
		return nil
	}
	mods := ev.Modifiers
	modParam := kittyModifierParameter(mods)
	eventType := 0
	switch ev.State {
	case key.Release:
		eventType = 3
	}
	allKeys := flags&kittyReportAllKeys != 0
	disambiguate := flags&kittyDisambiguate != 0 || allKeys

	code, trailer, ok := kittyFunctionalKey(ev.Name)
	if ok {
		switch ev.Name {
		case key.NameEscape:
			if !allKeys && !disambiguate && eventType == 0 {
				return encodeLegacyKey(ev, applicationCursor)
			}
		case key.NameReturn, key.NameEnter:
			if eventType != 0 && !allKeys {
				return nil
			}
			if !allKeys && eventType == 0 && mods&(key.ModShift|key.ModAlt|key.ModCtrl|key.ModSuper) == 0 {
				return []byte{'\r'}
			}
		case key.NameTab:
			if eventType != 0 && !allKeys {
				return nil
			}
			if !allKeys && eventType == 0 && mods == 0 {
				return []byte{'\t'}
			}
		case key.NameDeleteBackward:
			if eventType != 0 && !allKeys {
				return nil
			}
			if !allKeys && eventType == 0 && mods == 0 {
				return []byte{0x7f}
			}
		}
		if trailer == 'A' || trailer == 'B' || trailer == 'C' || trailer == 'D' || trailer == 'H' || trailer == 'F' {
			// Cursor keys only use the SS3 form in legacy mode with no
			// modifiers. Any Kitty enhancement keeps the CSI form.
			if !allKeys && !disambiguate && eventType == 0 && mods == 0 && applicationCursor {
				return []byte{'\x1b', 'O', trailer}
			}
		}
		return kittyCSI(code, trailer, modParam, eventType, flags&kittyReportEvents != 0)
	}

	if fn := kittyFunctionKeyNumber(ev.Name); fn != 0 {
		code, trailer := kittyFunctionKey(fn)
		return kittyCSI(code, trailer, modParam, eventType, flags&kittyReportEvents != 0)
	}

	name := string(ev.Name)
	r, count := utf8.DecodeRuneInString(name)
	if r == utf8.RuneError || count != len(name) || !unicode.IsPrint(r) {
		return nil
	}
	keyCode := kittyBaseRune(r)
	if allKeys {
		return kittyCSI(int(keyCode), 'u', modParam, eventType, flags&kittyReportEvents != 0)
	}
	if eventType != 0 {
		return kittyCSI(int(keyCode), 'u', modParam, eventType, flags&kittyReportEvents != 0)
	}
	if disambiguate && mods&(key.ModAlt|key.ModCtrl|key.ModSuper) != 0 {
		return kittyCSI(int(keyCode), 'u', modParam, eventType, flags&kittyReportEvents != 0)
	}
	return encodeLegacyKey(ev, applicationCursor)
}

// kittyCSI serializes CSI key-code:alternates;modifiers:event-type trailer.
// Functional keys use legacy-compatible numbers and trailers, not the private
// use area numbers Kitty reserves for F13 and above.
func kittyCSI(code int, trailer byte, modParam, eventType int, reportEventTypes bool) []byte {
	b := []byte("\x1b[")
	if code != 1 || modParam > 1 || eventType > 1 {
		b = strconv.AppendInt(b, int64(code), 10)
	}
	if modParam > 1 || eventType > 1 {
		b = append(b, ';')
		b = strconv.AppendInt(b, int64(modParam), 10)
		if eventType > 1 && reportEventTypes {
			b = append(b, ':')
			b = strconv.AppendInt(b, int64(eventType), 10)
		}
	}
	b = append(b, trailer)
	return b
}

func kittyFunctionalKey(name key.Name) (code int, trailer byte, ok bool) {
	switch name {
	case key.NameEscape:
		return kittyKeyEscape, 'u', true
	case key.NameReturn, key.NameEnter:
		return kittyKeyEnter, 'u', true
	case key.NameTab:
		return kittyKeyTab, 'u', true
	case key.NameDeleteBackward:
		return kittyKeyBackspace, 'u', true
	case key.Name("Insert"):
		return 2, '~', true
	case key.NameDeleteForward:
		return 3, '~', true
	case key.NameLeftArrow:
		return 1, 'D', true
	case key.NameRightArrow:
		return 1, 'C', true
	case key.NameUpArrow:
		return 1, 'A', true
	case key.NameDownArrow:
		return 1, 'B', true
	case key.NamePageUp:
		return 5, '~', true
	case key.NamePageDown:
		return 6, '~', true
	case key.NameHome:
		return 1, 'H', true
	case key.NameEnd:
		return 1, 'F', true
	}
	return 0, 0, false
}

func kittyFunctionKeyNumber(name key.Name) int {
	if !strings.HasPrefix(string(name), "F") {
		return 0
	}
	n, err := strconv.Atoi(string(name)[1:])
	if err != nil || n < 1 || n > 35 {
		return 0
	}
	return n
}

func kittyFunctionKey(fn int) (int, byte) {
	switch fn {
	case 1:
		return 1, 'P'
	case 2:
		return 1, 'Q'
	case 3:
		return 13, '~'
	case 4:
		return 1, 'S'
	case 5:
		return 15, '~'
	case 6:
		return 17, '~'
	case 7:
		return 18, '~'
	case 8:
		return 19, '~'
	case 9:
		return 20, '~'
	case 10:
		return 21, '~'
	case 11:
		return 23, '~'
	case 12:
		return 24, '~'
	}
	if fn >= 13 && fn <= 35 {
		return kittyKeyF13 + fn - 13, 'u'
	}
	return 0, 0
}

// kittyBaseRune maps a printable key to its unshifted base-layout rune. Gio
// reports the produced character, so shift punctuation must be reversed before
// it is reported alongside the shift modifier.
func kittyBaseRune(r rune) rune {
	if unicode.IsLetter(r) {
		return unicode.ToLower(r)
	}
	if base, ok := kittyShiftedRunes[r]; ok {
		return base
	}
	return r
}

var kittyShiftedRunes = map[rune]rune{
	'!': '1', '@': '2', '#': '3', '$': '4', '%': '5', '^': '6', '&': '7', '*': '8',
	'(': '9', ')': '0', '_': '-', '+': '=', '{': '[', '}': ']', '|': '\\',
	':': ';', '"': '\'', '<': ',', '>': '.', '?': '/', '~': '`',
}

func kittyModifierParameter(mods key.Modifiers) int {
	value := 1
	if mods.Contain(key.ModShift) {
		value += kittyModifierShift
	}
	if mods.Contain(key.ModAlt) {
		value += kittyModifierAlt
	}
	if mods.Contain(key.ModCtrl) {
		value += kittyModifierCtrl
	}
	if mods.Contain(key.ModCommand) || mods.Contain(key.ModSuper) {
		value += kittyModifierSuper
	}
	return value
}

func encodeLegacyKey(ev key.Event, applicationCursor bool) []byte {
	mods := ev.Modifiers
	if mods.Contain(key.ModCommand) || mods.Contain(key.ModSuper) {
		return nil
	}
	modParam := modifierParameter(mods)
	modified := mods&(key.ModShift|key.ModAlt|key.ModCtrl) != 0

	final := byte(0)
	switch ev.Name {
	case key.NameUpArrow:
		final = 'A'
	case key.NameDownArrow:
		final = 'B'
	case key.NameRightArrow:
		final = 'C'
	case key.NameLeftArrow:
		final = 'D'
	case key.NameHome:
		final = 'H'
	case key.NameEnd:
		final = 'F'
	}
	if final != 0 {
		if modified {
			return []byte("\x1b[1;" + itoaSmall(modParam) + string(final))
		}
		if applicationCursor {
			return []byte{'\x1b', 'O', final}
		}
		return []byte{'\x1b', '[', final}
	}

	switch ev.Name {
	case key.NameReturn, key.NameEnter:
		if mods.Contain(key.ModAlt) {
			return []byte("\x1b\r")
		}
		return []byte("\r")
	case key.NameEscape:
		return []byte{0x1b}
	case key.NameTab:
		if mods.Contain(key.ModShift) {
			return []byte("\x1b[Z")
		}
		return []byte("\t")
	case key.NameDeleteBackward:
		if mods.Contain(key.ModAlt) {
			return []byte{0x1b, 0x7f}
		}
		return []byte{0x7f}
	case key.NameDeleteForward:
		if modified {
			return []byte("\x1b[3;" + itoaSmall(modParam) + "~")
		}
		return []byte("\x1b[3~")
	case key.Name("Insert"):
		if modified {
			return []byte("\x1b[2;" + itoaSmall(modParam) + "~")
		}
		return []byte("\x1b[2~")
	case key.NamePageUp:
		if modified {
			return []byte("\x1b[5;" + itoaSmall(modParam) + "~")
		}
		return []byte("\x1b[5~")
	case key.NamePageDown:
		if modified {
			return []byte("\x1b[6;" + itoaSmall(modParam) + "~")
		}
		return []byte("\x1b[6~")
	}

	if fn := functionKeyNumber(ev.Name); fn != 0 {
		if fn <= 4 {
			finals := "PQRS"
			final := finals[fn-1]
			if modified {
				return []byte("\x1b[1;" + itoaSmall(modParam) + string(final))
			}
			return []byte{0x1b, 'O', final}
		}
		codes := []string{"15", "17", "18", "19", "20", "21", "23", "24", "25", "26", "28", "29", "31", "32", "33", "34", "42", "43", "44", "45"}
		code := codes[fn-5]
		if modified {
			return []byte("\x1b[" + code + ";" + itoaSmall(modParam) + "~")
		}
		return []byte("\x1b[" + code + "~")
	}

	name := string(ev.Name)
	r, count := utf8.DecodeRuneInString(name)
	if r == utf8.RuneError || count != len(name) {
		return nil
	}
	if mods.Contain(key.ModCtrl) {
		if b, ok := controlByte(r); ok {
			data := []byte{b}
			if mods.Contain(key.ModAlt) {
				data = append([]byte{0x1b}, data...)
			}
			return data
		}
	}
	if mods.Contain(key.ModAlt) && !mods.Contain(key.ModCtrl) {
		if !mods.Contain(key.ModShift) {
			name = string(unicode.ToLower(r))
		}
		return append([]byte{0x1b}, []byte(name)...)
	}
	return nil
}

func modifierParameter(mods key.Modifiers) int {
	value := 1
	if mods.Contain(key.ModShift) {
		value += 1
	}
	if mods.Contain(key.ModAlt) {
		value += 2
	}
	if mods.Contain(key.ModCtrl) {
		value += 4
	}
	return value
}

func controlByte(r rune) (byte, bool) {
	r = unicode.ToUpper(r)
	switch {
	case r >= '@' && r <= '_':
		return byte(r - '@'), true
	case r == '?':
		return 0x7f, true
	case r == ' ':
		return 0, true
	case r == '2':
		return 0, true
	case r == '3':
		return 0x1b, true
	case r == '4':
		return 0x1c, true
	case r == '5':
		return 0x1d, true
	case r == '6':
		return 0x1e, true
	case r == '7', r == '/', r == '-':
		return 0x1f, true
	case r == '8':
		return 0x7f, true
	}
	return 0, false
}

func functionKeyNumber(name key.Name) int {
	if strings.HasPrefix(string(name), "F") {
		if n, err := strconv.Atoi(string(name)[1:]); err == nil && n >= 1 && n <= 24 {
			return n
		}
	}
	return 0
}

func itoaSmall(v int) string {
	if v < 10 {
		return string(rune('0' + v))
	}
	var b strings.Builder
	b.WriteByte(byte('0' + v/10))
	b.WriteByte(byte('0' + v%10))
	return b.String()
}

func mouseButton(buttons pointer.Buttons) (govt.MouseButton, bool) {
	switch {
	case buttons&pointer.ButtonPrimary != 0:
		return govt.MouseLeft, true
	case buttons&pointer.ButtonTertiary != 0:
		return govt.MouseMiddle, true
	case buttons&pointer.ButtonSecondary != 0:
		return govt.MouseRight, true
	default:
		return govt.MouseNone, false
	}
}

func hyperlinkURIAt(snap govt.Snapshot, col, row int) string {
	if row < 0 || row >= len(snap.RowsData) || col < 0 || col >= snap.Cols {
		return ""
	}
	cells := snap.RowsData[row].Cells
	if col >= len(cells) {
		return ""
	}
	if col > 0 && govt.CellDrawingColumns(cells, col-1) == 2 {
		col--
	}
	return cells[col].LinkURI
}

func hyperlinkClickAllowed(requireShortcut bool, mods key.Modifiers) bool {
	if mods.Contain(key.ModShift) {
		return false
	}
	return !requireShortcut || mods.Contain(key.ModShortcut)
}

func (i *TerminalInput) nextClickCount(at time.Duration, col, row int) int {
	const multiClickWindow = 500 * time.Millisecond
	if i.clickCount > 0 &&
		col == i.lastClickCol && row == i.lastClickRow &&
		at >= i.lastClickTime && at-i.lastClickTime <= multiClickWindow {
		i.clickCount++
	} else {
		i.clickCount = 1
	}
	i.lastClickTime = at
	i.lastClickCol = col
	i.lastClickRow = row
	return i.clickCount
}

const (
	selectionAutoScrollMarginPx = 24
	selectionAutoScrollRows     = 3
	selectionAutoScrollInterval = 50 * time.Millisecond
)

func selectionAutoScrollDirection(y float32, height int) int {
	if height <= 0 {
		return 0
	}
	if y < float32(selectionAutoScrollMarginPx) {
		return -selectionAutoScrollRows
	}
	if y > float32(height-selectionAutoScrollMarginPx) {
		return selectionAutoScrollRows
	}
	return 0
}

func (i *TerminalInput) updateSelectionAutoScroll(y float32, rows, lineHeight, col int, now time.Time) {
	lines := selectionAutoScrollDirection(y, rows*lineHeight)
	if lines == 0 {
		i.clearSelectionAutoScroll()
		return
	}
	row := 0
	if lines > 0 {
		row = rows - 1
	}
	if row < 0 {
		row = 0
	}
	i.selectionAutoScrollLines = lines
	i.selectionAutoScrollCol = col
	i.selectionAutoScrollRow = row
	if i.selectionAutoScrollAt.IsZero() {
		i.selectionAutoScrollAt = now.Add(selectionAutoScrollInterval)
	}
}

func (i *TerminalInput) processSelectionAutoScroll(gtx layout.Context) {
	if !i.selecting || i.selectionAutoScrollLines == 0 || i.OnSelectionAutoScroll == nil {
		return
	}
	if i.selectionAutoScrollAt.IsZero() {
		i.selectionAutoScrollAt = gtx.Now.Add(selectionAutoScrollInterval)
	}
	if !gtx.Now.Before(i.selectionAutoScrollAt) {
		i.OnSelectionAutoScroll(
			i.selectionAutoScrollCol,
			i.selectionAutoScrollRow,
			i.selectionAutoScrollLines,
		)
		i.selectionAutoScrollAt = gtx.Now.Add(selectionAutoScrollInterval)
	}
	gtx.Execute(op.InvalidateCmd{At: i.selectionAutoScrollAt})
}

func (i *TerminalInput) clearSelectionAutoScroll() {
	i.selectionAutoScrollLines = 0
	i.selectionAutoScrollCol = 0
	i.selectionAutoScrollRow = 0
	i.selectionAutoScrollAt = time.Time{}
}
