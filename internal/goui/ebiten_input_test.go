package goui

import (
	"gioui.org/io/key"
	"gioui.org/widget"
	"github.com/SurTeam/Water/internal/govt"
	"github.com/hajimehoshi/ebiten/v2"
	"image"
	"reflect"
	"testing"
)

func TestNativePhysicalKeysReachVTEncoder(t *testing.T) {
	for _, tc := range []struct {
		physical   ebiten.Key
		mods, want string
	}{{ebiten.KeyArrowUp, "", "\x1b[A"}, {ebiten.KeyArrowDown, "", "\x1b[B"},
		{ebiten.KeyArrowLeft, "", "\x1b[D"}, {ebiten.KeyArrowRight, "", "\x1b[C"},
		{ebiten.KeyBackspace, "", "\x7f"}, {ebiten.KeyC, "ctrl-", "\x03"},
		{ebiten.KeySpace, "ctrl-", "\x00"}} {
		specs := nativeKeySpecs(tc.mods, false, func(k ebiten.Key) int {
			if k == tc.physical {
				return 1
			}
			return 0
		})
		if len(specs) != 1 {
			t.Fatalf("missing physical key %s", tc.physical)
		}
		ev, ok := automationInputEvent(specs[0])
		pressed, isKey := ev.(key.Event)
		if !ok || !isKey {
			t.Fatalf("not a key event: %s", specs[0])
		}
		if got := string(EncodeKey(pressed, false)); got != tc.want {
			t.Fatalf("%s: got %q, want %q", specs[0], got, tc.want)
		}
	}
}

func TestNativeKeyboardAllPhysicalKeysHaveDefinedRoles(t *testing.T) {
	osKeys := map[ebiten.Key]bool{
		ebiten.KeyAlt: true, ebiten.KeyAltLeft: true, ebiten.KeyAltRight: true,
		ebiten.KeyControl: true, ebiten.KeyControlLeft: true, ebiten.KeyControlRight: true,
		ebiten.KeyShift: true, ebiten.KeyShiftLeft: true, ebiten.KeyShiftRight: true,
		ebiten.KeyMeta: true, ebiten.KeyMetaLeft: true, ebiten.KeyMetaRight: true,
		ebiten.KeyCapsLock: true, ebiten.KeyNumLock: true, ebiten.KeyScrollLock: true,
		ebiten.KeyContextMenu: true, ebiten.KeyPause: true, ebiten.KeyPrintScreen: true,
	}
	for k := ebiten.Key(0); k <= ebiten.KeyMax; k++ {
		specs := nativeKeySpecs("ctrl-", false, func(physical ebiten.Key) int {
			if physical == k {
				return 1
			}
			return 0
		})
		if osKeys[k] {
			if len(specs) != 0 {
				t.Fatalf("OS/modifier key emitted terminal input: %s", k)
			}
			continue
		}
		if len(specs) != 1 {
			t.Fatalf("unclassified physical key: %s", k)
		}
		if ev, ok := automationInputEvent(specs[0]); !ok || ev == nil {
			t.Fatalf("unparseable key: %s", specs[0])
		}
		_, printable := nativeKeyName(k, "")
		if printable {
			plain := nativeKeySpecs("", false, func(physical ebiten.Key) int {
				if physical == k {
					return 1
				}
				return 0
			})
			if len(plain) != 0 {
				t.Fatalf("printable key duplicates OS text: %s", k)
			}
		}
	}
}

func TestNativeKeyboardControlAltFunctionAndKeypadEncoding(t *testing.T) {
	for _, tc := range []struct {
		physical   ebiten.Key
		mods, want string
	}{
		{ebiten.KeyA, "ctrl-", "\x01"}, {ebiten.KeyZ, "ctrl-", "\x1a"},
		{ebiten.KeyBracketLeft, "ctrl-", "\x1b"}, {ebiten.KeyBackslash, "ctrl-", "\x1c"},
		{ebiten.KeyBracketRight, "ctrl-", "\x1d"}, {ebiten.KeyDigit6, "ctrl-", "\x1e"},
		{ebiten.KeyMinus, "ctrl-shift-", "\x1f"}, {ebiten.KeySlash, "ctrl-shift-", "\x7f"},
		{ebiten.KeyDigit2, "ctrl-shift-", "\x00"}, {ebiten.KeyDigit3, "ctrl-", "\x1b"},
		{ebiten.KeyX, "alt-", "\x1bx"}, {ebiten.KeyX, "alt-shift-", "\x1bX"},
		{ebiten.KeyQuote, "alt-", "\x1b'"}, {ebiten.KeySemicolon, "alt-shift-", "\x1b:"},
		{ebiten.KeyBackquote, "alt-shift-", "\x1b~"}, {ebiten.KeyEqual, "alt-shift-", "\x1b+"},
		{ebiten.KeyC, "ctrl-alt-", "\x1b\x03"}, {ebiten.KeyNumpadEnter, "", "\r"},
		{ebiten.KeyNumpad2, "ctrl-", "\x00"}, {ebiten.KeyNumpadAdd, "alt-", "\x1b+"},
		{ebiten.KeyInsert, "", "\x1b[2~"}, {ebiten.KeyInsert, "ctrl-", "\x1b[2;5~"},
		{ebiten.KeyF1, "", "\x1bOP"}, {ebiten.KeyF12, "", "\x1b[24~"},
		{ebiten.KeyF13, "", "\x1b[25~"}, {ebiten.KeyF20, "ctrl-", "\x1b[34;5~"},
		{ebiten.KeyF24, "", "\x1b[45~"}, {ebiten.KeyArrowLeft, "ctrl-alt-shift-", "\x1b[1;8D"},
	} {
		specs := nativeKeySpecs(tc.mods, false, func(k ebiten.Key) int {
			if k == tc.physical {
				return 1
			}
			return 0
		})
		if len(specs) != 1 {
			t.Fatalf("missing key %s", tc.physical)
		}
		ev, ok := automationInputEvent(specs[0])
		pressed, isKey := ev.(key.Event)
		if !ok || !isKey {
			t.Fatalf("not a key event: %s", specs[0])
		}
		if got := string(EncodeKey(pressed, false)); got != tc.want {
			t.Fatalf("%s: got %q, want %q", specs[0], got, tc.want)
		}
	}
}

func TestNativePhysicalKeysIncludeKeysBeforeDigitZero(t *testing.T) {
	for _, tc := range []struct {
		key        ebiten.Key
		mods, want string
	}{{ebiten.KeyArrowLeft, "", "Left"}, {ebiten.KeyArrowRight, "", "Right"},
		{ebiten.KeyArrowUp, "", "Up"}, {ebiten.KeyArrowDown, "", "Down"},
		{ebiten.KeyBackspace, "", "Backspace"}, {ebiten.KeyC, "ctrl-", "ctrl-C"},
		{ebiten.KeyA, "cmd-", "cmd-A"}, {ebiten.KeySpace, "ctrl-", "ctrl-Space"}} {
		t.Run(tc.want, func(t *testing.T) {
			for _, duration := range []int{1, 22, 25} {
				got := nativeKeySpecs(tc.mods, false, func(k ebiten.Key) int {
					if k == tc.key {
						return duration
					}
					return 0
				})
				if !reflect.DeepEqual(got, []string{tc.want}) {
					t.Fatalf("duration %d: got %v", duration, got)
				}
			}
		})
	}
}

func TestNativeKeysRespectCompositionAndRepeatDelay(t *testing.T) {
	for _, tc := range []struct {
		composing bool
		duration  int
	}{{true, 1}, {false, 2}, {false, 23}} {
		got := nativeKeySpecs("", tc.composing, func(k ebiten.Key) int {
			if k == ebiten.KeyBackspace {
				return tc.duration
			}
			return 0
		})
		if len(got) != 0 {
			t.Fatalf("unexpected key dispatch: %v", got)
		}
	}
}

func TestNativeEditorUnicodeSelectionAndReplacement(t *testing.T) {
	var editor widget.Editor
	editor.SetText("水🌊abc")
	editor.SetCaret(1, 3)
	if !editNativeEditor(&editor, "text:界") || editor.Text() != "水界bc" {
		t.Fatalf("Unicode selection must use rune positions: %q", editor.Text())
	}
	if start, end := editor.Selection(); start != 2 || end != 2 {
		t.Fatalf("caret after replacement: %d,%d", start, end)
	}
	if !editNativeEditor(&editor, "Backspace") || editor.Text() != "水bc" {
		t.Fatalf("delete a complete Unicode character: %q", editor.Text())
	}
	if !editNativeEditor(&editor, "cmd-a") || !editNativeEditor(&editor, "text:新字体") || editor.Text() != "新字体" {
		t.Fatalf("select-all and replace: %q", editor.Text())
	}
}

func TestNativeEditorKeyboardSelection(t *testing.T) {
	var editor widget.Editor
	editor.SetText("a🌊b")
	editor.SetCaret(3, 3)
	if !editNativeEditor(&editor, "shift-left") || !editNativeEditor(&editor, "shift-left") {
		t.Fatal("shift-arrow selection was not handled")
	}
	if !editNativeEditor(&editor, "text:水") || editor.Text() != "a水" {
		t.Fatalf("keyboard selection replacement: %q", editor.Text())
	}
	editor.SetCaret(0, 0)
	if !editNativeEditor(&editor, "Delete") || editor.Text() != "水" {
		t.Fatalf("forward deletion: %q", editor.Text())
	}
}

func TestClosedNativeWindowRejectsControlRequests(t *testing.T) {
	w := &EbitengineWindow{queue: make(chan nativeRequest), done: make(chan struct{})}
	close(w.done)
	if _, err := w.request(nil, "ui.keystroke", nil); err == nil {
		t.Fatal("closed window accepted input")
	}
}

func TestNativeMouseUsesVTOneBasedCoordinates(t *testing.T) {
	pane := nativePane{rect: image.Rect(100, 200, 180, 240), cw: 8, lh: 20}
	ev := nativeMouseEvent(pane, image.Pt(100, 200), govt.MouseDown, govt.MouseLeft)
	if ev.Col != 1 || ev.Row != 1 || ev.X != 1 || ev.Y != 1 {
		t.Fatalf("top-left VT position must start at one: %+v", ev)
	}
	ev = nativeMouseEvent(pane, image.Pt(179, 239), govt.MouseMove, govt.MouseLeft)
	if ev.Col != 10 || ev.Row != 2 {
		t.Fatalf("last cell: %+v", ev)
	}
	ev = nativeMouseEvent(pane, image.Pt(999, -5), govt.MouseUp, govt.MouseLeft)
	if ev.Col != 10 || ev.Row != 1 {
		t.Fatalf("drag outside pane must clamp to terminal bounds: %+v", ev)
	}
}
