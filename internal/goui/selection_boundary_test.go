package goui

import (
	"image"
	"testing"

	"github.com/SurTeam/Water/internal/govt"
)

func TestSelectionBoundaryMidpoint(t *testing.T) {
	pane := nativePane{rect: image.Rect(10, 20, 90, 60), cw: 10, lh: 20}
	for _, tc := range []struct{ x, want int }{{9, 0}, {10, 0}, {14, 0}, {15, 1}, {24, 1}, {25, 2}, {89, 8}, {100, 8}} {
		col, row := selectionBoundaryAt(pane, image.Pt(tc.x, 25))
		if col != tc.want || row != 0 {
			t.Fatalf("x=%d: got %d,%d want %d,0", tc.x, col, row, tc.want)
		}
	}
}

func TestBoundarySelectionCopyAndHighlight(t *testing.T) {
	e := govt.New(8, 3, 10)
	defer e.Close()
	e.Write([]byte("AB界CD\r\nEFGH"))
	snap := e.Snapshot()
	for _, tc := range []struct {
		a, b int
		want string
	}{{0, 1, "A"}, {1, 0, "A"}, {1, 2, "B"}, {2, 3, "界"}, {3, 4, "界"}, {2, 4, "界"}, {4, 6, "CD"}, {6, 4, "CD"}, {2, 2, ""}} {
		sel := Selection{AnchorCol: tc.a, FocusCol: tc.b, Active: true, Absolute: true, Boundaries: true}
		if got := SelectedText(snap, sel); got != tc.want {
			t.Fatalf("%d..%d got %q want %q", tc.a, tc.b, got, tc.want)
		}
		if sel.empty() {
			continue
		}
		ac, ar, bc, br := sel.normalized()
		if got := e.SelectionText(ar, ac, br, bc); got != tc.want {
			t.Fatalf("buffer copy got %q want %q", got, tc.want)
		}
	}
	sel := Selection{AnchorCol: 1, AnchorRow: 0, FocusCol: 2, FocusRow: 1, Active: true, Absolute: true, Boundaries: true}
	if got := SelectedText(snap, sel); got != "B界CD\nEF" {
		t.Fatalf("multiline got %q", got)
	}
}

func TestShiftSelectionPreservesAnchorAndCrossesIt(t *testing.T) {
	_, term := attachedScrollTestTerminal(t)
	term.mu.Lock()
	term.emu.Write([]byte("\x1b[2J\x1b[HABCDEFGH"))
	term.snapshot = term.emu.FrameSnapshot()
	term.mu.Unlock()
	term.input.OnSelectionBoundaryStart(1, 0, false)
	term.input.OnSelectionEnd(4, 0)
	if got := term.input.OnCopy(); got != "BCD" {
		t.Fatalf("initial got %q", got)
	}
	term.input.OnSelectionBoundaryStart(6, 0, true)
	term.input.OnSelectionEnd(6, 0)
	if term.selection.AnchorCol != 1 || term.input.OnCopy() != "BCDEF" {
		t.Fatal("shift lost anchor or did not extend")
	}
	term.input.OnSelectionBoundaryStart(0, 0, true)
	term.input.OnSelectionEnd(0, 0)
	if term.selection.AnchorCol != 1 || term.input.OnCopy() != "A" {
		t.Fatal("shift did not cross anchor")
	}
	term.input.OnSelectionBoundaryStart(1, 0, true)
	term.input.OnSelectionEnd(1, 0)
	if term.input.OnCopy() != "" || term.selection.Active {
		t.Fatal("empty selection retained")
	}
	term.input.OnSelectionStart(2, 0, 2)
	term.input.OnSelectionBoundaryStart(8, 0, true)
	if term.selection.AnchorCol != 0 || !term.selection.Boundaries || term.input.OnCopy() != "ABCDEFGH" {
		t.Fatal("word selection did not extend correctly")
	}
}

func TestWorkspaceDragThreshold(t *testing.T) {
	w := &EbitengineWindow{scale: 1, drag: &nativeDrag{kind: hitWorkspace, start: image.Pt(20, 20)}}
	w.pointerMove(nil, image.Pt(22, 22))
	if w.drag.moved {
		t.Fatal("click jitter starts drag")
	}
	w.pointerMove(nil, image.Pt(20, 26))
	if !w.drag.moved {
		t.Fatal("clear movement does not start drag")
	}
}
