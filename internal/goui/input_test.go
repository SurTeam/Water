package goui

import (
	"bytes"
	"image"
	"testing"
	"time"

	"gioui.org/io/event"
	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"

	"github.com/SurTeam/Water/internal/govt"
)

func TestEncodeKeyApplicationCursorAndModifiers(t *testing.T) {
	tests := []struct {
		name string
		ev   key.Event
		app  bool
		want []byte
	}{
		{"up-normal", key.Event{Name: key.NameUpArrow, State: key.Press}, false, []byte("\x1b[A")},
		{"up-app", key.Event{Name: key.NameUpArrow, State: key.Press}, true, []byte("\x1bOA")},
		{"ctrl-up", key.Event{Name: key.NameUpArrow, Modifiers: key.ModCtrl, State: key.Press}, true, []byte("\x1b[1;5A")},
		{"shift-tab", key.Event{Name: key.NameTab, Modifiers: key.ModShift, State: key.Press}, false, []byte("\x1b[Z")},
		{"f5", key.Event{Name: key.NameF5, State: key.Press}, false, []byte("\x1b[15~")},
		{"ctrl-c", key.Event{Name: key.Name("C"), Modifiers: key.ModCtrl, State: key.Press}, false, []byte{3}},
		{"alt-x", key.Event{Name: key.Name("X"), Modifiers: key.ModAlt, State: key.Press}, false, []byte{0x1b, 'x'}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := EncodeKey(tc.ev, tc.app)
			if !bytes.Equal(got, tc.want) {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestEncodeKeyKittyProtocol(t *testing.T) {
	tests := []struct {
		name  string
		ev    key.Event
		flags int
		want  string
	}{
		{"ctrl-c", key.Event{Name: key.Name("C"), Modifiers: key.ModCtrl, State: key.Press}, 1, "\x1b[99;5u"},
		{"alt-x", key.Event{Name: key.Name("X"), Modifiers: key.ModAlt, State: key.Press}, 1, "\x1b[120;3u"},
		{"ctrl-alt-x", key.Event{Name: key.Name("X"), Modifiers: key.ModCtrl | key.ModAlt, State: key.Press}, 1, "\x1b[120;7u"},
		{"shift-a", key.Event{Name: key.Name("A"), Modifiers: key.ModShift, State: key.Press}, 1, ""},
		{"plain-a", key.Event{Name: key.Name("A"), State: key.Press}, 1, ""},
		{"plain-a-flags2", key.Event{Name: key.Name("A"), State: key.Press}, 2, ""},
		{"ctrl-c-flags2", key.Event{Name: key.Name("C"), Modifiers: key.ModCtrl, State: key.Press}, 2, "\x03"},
		{"up", key.Event{Name: key.NameUpArrow, State: key.Press}, 1, "\x1b[A"},
		{"ctrl-up", key.Event{Name: key.NameUpArrow, Modifiers: key.ModCtrl, State: key.Press}, 1, "\x1b[1;5A"},
		{"down", key.Event{Name: key.NameDownArrow, State: key.Press}, 1, "\x1b[B"},
		{"right", key.Event{Name: key.NameRightArrow, State: key.Press}, 1, "\x1b[C"},
		{"alt-left", key.Event{Name: key.NameLeftArrow, Modifiers: key.ModAlt, State: key.Press}, 1, "\x1b[1;3D"},
		{"page-up", key.Event{Name: key.NamePageUp, State: key.Press}, 1, "\x1b[5~"},
		{"page-down", key.Event{Name: key.NamePageDown, State: key.Press}, 1, "\x1b[6~"},
		{"home", key.Event{Name: key.NameHome, State: key.Press}, 1, "\x1b[H"},
		{"end", key.Event{Name: key.NameEnd, State: key.Press}, 1, "\x1b[F"},
		{"f1", key.Event{Name: key.NameF1, State: key.Press}, 1, "\x1b[P"},
		{"ctrl-f1", key.Event{Name: key.NameF1, Modifiers: key.ModCtrl, State: key.Press}, 1, "\x1b[1;5P"},
		{"f3", key.Event{Name: key.NameF3, State: key.Press}, 1, "\x1b[13~"},
		{"f5", key.Event{Name: key.NameF5, State: key.Press}, 1, "\x1b[15~"},
		{"f13", key.Event{Name: key.Name("F13"), State: key.Press}, 1, "\x1b[57376u"},
		{"f35", key.Event{Name: key.Name("F35"), State: key.Press}, 1, "\x1b[57398u"},
		{"insert", key.Event{Name: key.Name("Insert"), State: key.Press}, 1, "\x1b[2~"},
		{"ctrl-insert", key.Event{Name: key.Name("Insert"), Modifiers: key.ModCtrl, State: key.Press}, 1, "\x1b[2;5~"},
		{"delete-forward", key.Event{Name: key.NameDeleteForward, State: key.Press}, 1, "\x1b[3~"},
		{"escape", key.Event{Name: key.NameEscape, State: key.Press}, 1, "\x1b[27u"},
		{"enter", key.Event{Name: key.NameReturn, State: key.Press}, 1, "\r"},
		{"ctrl-enter", key.Event{Name: key.NameReturn, Modifiers: key.ModCtrl, State: key.Press}, 1, "\x1b[13;5u"},
		{"tab", key.Event{Name: key.NameTab, State: key.Press}, 1, "\t"},
		{"shift-tab", key.Event{Name: key.NameTab, Modifiers: key.ModShift, State: key.Press}, 1, "\x1b[9;2u"},
		{"ctrl-tab", key.Event{Name: key.NameTab, Modifiers: key.ModCtrl, State: key.Press}, 1, "\x1b[9;5u"},
		{"backspace", key.Event{Name: key.NameDeleteBackward, State: key.Press}, 1, "\x7f"},
		{"alt-backspace", key.Event{Name: key.NameDeleteBackward, Modifiers: key.ModAlt, State: key.Press}, 1, "\x1b[127;3u"},
		{"ctrl-backspace", key.Event{Name: key.NameDeleteBackward, Modifiers: key.ModCtrl, State: key.Press}, 1, "\x1b[127;5u"},
		{"tab-all", key.Event{Name: key.NameTab, State: key.Press}, 8, "\x1b[9u"},
		{"backspace-all", key.Event{Name: key.NameDeleteBackward, State: key.Press}, 8, "\x1b[127u"},
		{"enter-all", key.Event{Name: key.NameReturn, State: key.Press}, 8, "\x1b[13u"},
		{"release", key.Event{Name: key.Name("C"), Modifiers: key.ModCtrl, State: key.Release}, 3, "\x1b[99;5:3u"},
		{"release-all", key.Event{Name: key.Name("C"), Modifiers: key.ModCtrl, State: key.Release}, 11, "\x1b[99;5:3u"},
		{"release-text-flags2", key.Event{Name: key.Name("A"), State: key.Release}, 2, "\x1b[97;1:3u"},
		{"release-arrow", key.Event{Name: key.NameUpArrow, State: key.Release}, 2, "\x1b[1;1:3A"},
		{"release-insert", key.Event{Name: key.Name("Insert"), State: key.Release}, 2, "\x1b[2;1:3~"},
		{"release-press-only", key.Event{Name: key.Name("C"), Modifiers: key.ModCtrl, State: key.Release}, 1, ""},
		{"enter-release", key.Event{Name: key.NameReturn, State: key.Release}, 2, ""},
		{"tab-release", key.Event{Name: key.NameTab, State: key.Release}, 2, ""},
		{"backspace-release", key.Event{Name: key.NameDeleteBackward, State: key.Release}, 2, ""},
		{"enter-release-all", key.Event{Name: key.NameReturn, State: key.Release}, 10, "\x1b[13;1:3u"},
		{"tab-release-all", key.Event{Name: key.NameTab, State: key.Release}, 10, "\x1b[9;1:3u"},
		{"backspace-release-all", key.Event{Name: key.NameDeleteBackward, State: key.Release}, 10, "\x1b[127;1:3u"},
		{"all-plain", key.Event{Name: key.Name("A"), State: key.Press}, 8, "\x1b[97u"},
		{"all-shift", key.Event{Name: key.Name("A"), Modifiers: key.ModShift, State: key.Press}, 8, "\x1b[97;2u"},
		{"all-shift-punctuation", key.Event{Name: key.Name("!"), Modifiers: key.ModShift, State: key.Press}, 8, "\x1b[49;2u"},
		{"all-release", key.Event{Name: key.Name("A"), State: key.Release}, 10, "\x1b[97;1:3u"},
		{"super-a", key.Event{Name: key.Name("A"), Modifiers: key.ModSuper, State: key.Press}, 1, "\x1b[97;9u"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := string(EncodeKeyAdvanced(tc.ev, false, tc.flags)); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestEncodeKeyLegacyDelegationUnchanged(t *testing.T) {
	ev := key.Event{Name: key.Name("X"), Modifiers: key.ModAlt, State: key.Press}
	if got, want := string(EncodeKeyAdvanced(ev, false, 0)), string(EncodeKey(ev, false)); got != want {
		t.Fatalf("legacy delegation = %q, want %q", got, want)
	}
}

func TestTerminalInputKittyReleaseAndShortcutHandling(t *testing.T) {
	var terminalInput TerminalInput
	var emitted []string
	terminalInput.OnInput = func(data []byte) { emitted = append(emitted, string(data)) }
	terminalInput.OnShortcut = func(ev key.Event) bool {
		return ev.Name == key.Name("Q")
	}

	snap := govt.Snapshot{Cols: 20, Rows: 4, KittyKeyboardFlags: 3}
	route := func(ev key.Event) {
		var router input.Router
		frame := func(focus bool) {
			var ops op.Ops
			gtx := layout.Context{Ops: &ops, Source: router.Source(), Constraints: layout.Exact(image.Pt(160, 80))}
			terminalInput.Add(gtx, gtx.Constraints.Max)
			if focus {
				terminalInput.Focus(gtx)
			}
			terminalInput.Process(gtx, snap, 8, 20)
			router.Frame(&ops)
		}
		frame(true)
		router.Queue(ev)
		frame(false)
	}

	route(key.Event{Name: key.NameUpArrow, State: key.Release})
	route(key.Event{Name: key.NameUpArrow, State: key.Press})
	route(key.Event{Name: key.Name("Q"), State: key.Press})
	route(key.Event{Name: key.Name("Q"), State: key.Release})
	route(key.Event{Name: key.Name("A"), State: key.Press})
	route(key.Event{Name: key.Name("A"), State: key.Release})
	if want := []string{"\x1b[1;1:3A", "\x1b[A", "\x1b[97;1:3u"}; len(emitted) != len(want) ||
		emitted[0] != want[0] || emitted[1] != want[1] || emitted[2] != want[2] {
		t.Fatalf("emitted = %q, want %q", emitted, want)
	}
}

func TestTerminalInputKittyAllKeysSuppressTextEvents(t *testing.T) {
	var terminalInput TerminalInput
	var emitted []string
	terminalInput.OnInput = func(data []byte) { emitted = append(emitted, string(data)) }

	snap := govt.Snapshot{Cols: 20, Rows: 4, KittyKeyboardFlags: 8}
	var router input.Router
	route := func(ev event.Event) {
		var ops op.Ops
		gtx := layout.Context{Ops: &ops, Source: router.Source(), Constraints: layout.Exact(image.Pt(160, 80))}
		terminalInput.Add(gtx, gtx.Constraints.Max)
		terminalInput.Focus(gtx)
		terminalInput.Process(gtx, snap, 8, 20)
		router.Frame(&ops)
		router.Queue(ev)
		ops.Reset()
		gtx.Ops = &ops
		gtx.Source = router.Source()
		terminalInput.Process(gtx, snap, 8, 20)
		router.Frame(&ops)
	}

	route(key.EditEvent{Text: "a"})
	route(key.Event{Name: key.Name("A"), State: key.Press})
	if want := []string{"\x1b[97u"}; len(emitted) != 1 || emitted[0] != want[0] {
		t.Fatalf("emitted = %q, want %q", emitted, want)
	}
}

func TestKittyCtrlCDBypassesLegacyShortcutBytes(t *testing.T) {
	for _, tc := range []struct {
		name  string
		flags int
		ev    key.Event
		want  string
	}{
		{"ctrl-c", 1, key.Event{Name: key.Name("C"), Modifiers: key.ModCtrl, State: key.Press}, "\x1b[99;5u"},
		{"ctrl-d", 1, key.Event{Name: key.Name("D"), Modifiers: key.ModCtrl, State: key.Press}, "\x1b[100;5u"},
		{"ctrl-c-all", 8, key.Event{Name: key.Name("C"), Modifiers: key.ModCtrl, State: key.Press}, "\x1b[99;5u"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got string
			terminal := &TerminalInput{OnInput: func(data []byte) { got += string(data) }}
			snap := govt.Snapshot{Cols: 80, Rows: 24, KittyKeyboardFlags: tc.flags}
			routeKittyKey(terminal, snap, tc.ev)
			if got != tc.want {
				t.Fatalf("emitted %q, want %q", got, tc.want)
			}
		})
	}
}

func routeKittyKey(terminal *TerminalInput, snap govt.Snapshot, ev event.Event) {
	var router input.Router
	frame := func(focus bool) {
		var ops op.Ops
		gtx := layout.Context{Ops: &ops, Source: router.Source(), Constraints: layout.Exact(image.Pt(800, 600))}
		terminal.Add(gtx, gtx.Constraints.Max)
		if focus {
			terminal.Focus(gtx)
		}
		terminal.Process(gtx, snap, 8, 16)
		router.Frame(&ops)
	}
	frame(true)
	router.Queue(ev)
	frame(false)
}

func TestHyperlinkClickModifierRules(t *testing.T) {
	if !hyperlinkClickAllowed(false, 0) {
		t.Fatal("plain hyperlink click should be allowed when shortcut is optional")
	}
	if hyperlinkClickAllowed(false, key.ModShift) {
		t.Fatal("shift must remain available for terminal selection")
	}
	if hyperlinkClickAllowed(true, 0) {
		t.Fatal("shortcut-required hyperlink activated without shortcut")
	}
	if !hyperlinkClickAllowed(true, key.ModShortcut) {
		t.Fatal("platform shortcut should activate hyperlink")
	}
	if hyperlinkClickAllowed(true, key.ModShortcut|key.ModShift) {
		t.Fatal("shift+shortcut should preserve selection override")
	}
}

func TestHyperlinkURIAt(t *testing.T) {
	snap := govt.Snapshot{
		Cols: 2, Rows: 1,
		RowsData: []govt.Row{{Cells: []govt.Cell{
			{Text: "x", Width: 1, LinkURI: "https://example.com"},
			{Text: "y", Width: 1},
		}}},
	}
	if got := hyperlinkURIAt(snap, 0, 0); got != "https://example.com" {
		t.Fatalf("link URI = %q", got)
	}
	for _, point := range [][2]int{{1, 0}, {2, 0}, {0, 1}, {-1, 0}} {
		if got := hyperlinkURIAt(snap, point[0], point[1]); got != "" {
			t.Fatalf("hyperlinkURIAt(%d,%d) = %q", point[0], point[1], got)
		}
	}
}

func TestHyperlinkClickUsesWholeVisualGlyph(t *testing.T) {
	for _, label := range []string{"界", "🍺", " "} {
		e := govt.New(12, 3, 0)
		e.Write([]byte("\x1b]8;;https://example.com/a\x1b\\" + label + "\x1b]8;;\x1b\\B"))
		for _, col := range []int{0, 1} {
			if got := hyperlinkURIAt(e.Snapshot(), col, 0); got != "https://example.com/a" {
				t.Fatalf("%q half %d opens %q", label, col, got)
			}
		}
		if got := hyperlinkURIAt(e.Snapshot(), 2, 0); got != "" {
			t.Fatalf("neighbor opens %q", got)
		}
		e.Close()
	}
	e := govt.New(12, 3, 0)
	defer e.Close()
	e.Write([]byte("\x1b]8;;https://example.com/a\x1b\\\x1b]8;;https://example.com/b\x1b\\ \x1b]8;;\x1b\\"))
	if got := hyperlinkURIAt(e.Snapshot(), 1, 0); got != "https://example.com/b" {
		t.Fatalf("distinct neighbor link borrowed: %q", got)
	}
}

func TestTerminalClickCountTracksSameCellWithinWindow(t *testing.T) {
	var input TerminalInput
	if got := input.nextClickCount(100*time.Millisecond, 2, 3); got != 1 {
		t.Fatalf("first click count = %d", got)
	}
	if got := input.nextClickCount(300*time.Millisecond, 2, 3); got != 2 {
		t.Fatalf("second click count = %d", got)
	}
	if got := input.nextClickCount(450*time.Millisecond, 2, 3); got != 3 {
		t.Fatalf("third click count = %d", got)
	}
	if got := input.nextClickCount(500*time.Millisecond, 3, 3); got != 1 {
		t.Fatalf("different cell did not reset click count: %d", got)
	}
	if got := input.nextClickCount(1200*time.Millisecond, 3, 3); got != 1 {
		t.Fatalf("expired click window did not reset count: %d", got)
	}
}

func TestSelectionAutoScrollDirection(t *testing.T) {
	const height = 200
	tests := []struct {
		y    float32
		want int
	}{
		{0, -3},
		{23, -3},
		{24, 0},
		{100, 0},
		{176, 0},
		{177, 3},
		{199, 3},
		{220, 3},
	}
	for _, tc := range tests {
		if got := selectionAutoScrollDirection(tc.y, height); got != tc.want {
			t.Fatalf("direction at y=%.1f = %d, want %d", tc.y, got, tc.want)
		}
	}
	if got := selectionAutoScrollDirection(0, 0); got != 0 {
		t.Fatalf("zero-height direction = %d", got)
	}
}

func TestTerminalIMEPreeditDoesNotLeakIntoPTY(t *testing.T) {
	var inputState TerminalInput
	var emitted [][]byte
	inputState.OnInput = func(data []byte) {
		emitted = append(emitted, append([]byte(nil), data...))
	}

	if compositionActive(key.Range{Start: -1, End: -1}) {
		t.Fatal("(-1,-1) must mean no active composition")
	}
	if !compositionActive(key.Range{Start: 0, End: 0}) {
		t.Fatal("zero-length preedit range is still an active composition")
	}

	inputState.handleComposition(key.CompositionEvent{Start: 0, End: 0})
	inputState.handleEdit(key.EditEvent{Text: "拼"})
	if len(emitted) != 0 {
		t.Fatalf("preedit leaked into PTY: %q", emitted)
	}
	if inputState.pendingComposition != "拼" || !inputState.composing {
		t.Fatalf("unexpected preedit state: composing=%v pending=%q", inputState.composing, inputState.pendingComposition)
	}

	inputState.handleComposition(key.CompositionEvent{Start: -1, End: -1})
	if len(emitted) != 0 {
		t.Fatalf("ending composition emitted stale preedit: %q", emitted)
	}
	if inputState.pendingComposition != "" || inputState.composing {
		t.Fatalf("composition did not reset: composing=%v pending=%q", inputState.composing, inputState.pendingComposition)
	}

	inputState.handleEdit(key.EditEvent{Text: "中"})
	if len(emitted) != 1 || string(emitted[0]) != "中" {
		t.Fatalf("commit = %q, want exactly one 中", emitted)
	}
}

func TestTerminalIMEPublishesCaretAndEmptySnippet(t *testing.T) {
	var terminalInput TerminalInput
	var router input.Router
	var ops op.Ops
	size := image.Pt(320, 200)

	snap := govt.Snapshot{Cols: 40, Rows: 10, CursorX: 3, CursorY: 2}
	gtx := layout.Context{
		Ops:         &ops,
		Source:      router.Source(),
		Constraints: layout.Exact(size),
	}
	// Register the same FocusFilter and input ops as a real terminal frame.
	terminalInput.Process(gtx, snap, 8, 20)
	terminalInput.Add(gtx, size)
	router.Frame(&ops)

	// Focus an already-registered terminal tag, then run the next real frame.
	router.Source().Execute(key.FocusCmd{Tag: &terminalInput.tag})
	if !router.Source().Focused(&terminalInput.tag) {
		t.Fatal("terminal input did not become focused")
	}
	ops.Reset()
	gtx.Ops = &ops
	gtx.Source = router.Source()
	terminalInput.Process(gtx, snap, 8, 20)
	terminalInput.Add(gtx, size)
	router.Frame(&ops)

	state := router.EditorState()
	if state.Snippet.Text != "" || state.Snippet.Range != (key.Range{Start: 0, End: 0}) {
		t.Fatalf("terminal IME snippet = %#v, want empty zero-range snippet", state.Snippet)
	}
	if state.Selection.Range != (key.Range{Start: 0, End: 0}) {
		t.Fatalf("terminal IME selection = %#v", state.Selection.Range)
	}
	if state.Selection.Caret.Pos.X != 24 || state.Selection.Caret.Pos.Y != 58 {
		t.Fatalf("terminal IME caret = %#v, want (24,58)", state.Selection.Caret)
	}

	terminalInput.handleComposition(key.CompositionEvent{Start: 0, End: 1})
	terminalInput.handleEdit(key.EditEvent{Text: "拼音"})
	ops.Reset()
	gtx.Ops = &ops
	gtx.Source = router.Source()
	terminalInput.syncIME(gtx, snap, 8, 20)
	terminalInput.Add(gtx, size)
	router.Frame(&ops)
	if bounds := router.EditorState().Selection.CompositionBounds; bounds.Empty() {
		t.Fatal("active terminal composition did not expose caret-local composition bounds")
	}
}
