package goui

import (
	"github.com/SurTeam/Water/internal/govt"
	"testing"
)

func TestSelectionIncludesWholeGlyphFromEitherHalf(t *testing.T) {
	for _, value := range []string{"界", "🍺", "界\u0301", " "} {
		e := govt.New(12, 3, 10)
		e.Write([]byte("A" + value + "B"))
		snap := e.Snapshot()
		for _, endpoints := range [][2]int{{1, 1}, {2, 2}, {1, 2}, {2, 1}, {0, 1}, {2, 3}, {3, 2}} {
			sel := Selection{AnchorCol: endpoints[0], FocusCol: endpoints[1], Active: true}
			left, right, ok := selectionColumnsForSnapshot(snap, sel, 0)
			if !ok || left > 1 || right < 2 {
				t.Fatalf("%q endpoints %v: highlight %d..%d", value, endpoints, left, right)
			}
			want := value
			if value == " " {
				want = ""
			}
			if left == 0 {
				want = "A" + want
			}
			if right == 3 {
				want = "A" + value + "B"
				if left != 0 {
					want = value + "B"
				}
			}
			if got := SelectedText(snap, sel); got != want {
				t.Fatalf("%q endpoints %v: copied %q want %q", value, endpoints, got, want)
			}
			if got := e.SelectionText(0, endpoints[0], 0, endpoints[1]); got != want {
				t.Fatalf("absolute buffer copy %q want %q", got, want)
			}
		}
		e.Close()
	}
	e := govt.New(8, 2, 0)
	defer e.Close()
	e.Write([]byte("AB"))
	for column, want := range []string{"A", "", "B"} {
		sel := Selection{AnchorCol: column, FocusCol: column, Active: true}
		left, right, ok := selectionColumnsForSnapshot(e.Snapshot(), sel, 0)
		if !ok || left != column || right != column || SelectedText(e.Snapshot(), sel) != want {
			t.Fatal("adjacent narrow characters expanded")
		}
	}
}

func TestAbsoluteSelectionWideBoundariesAcrossScrolledViewport(t *testing.T) {
	e := govt.New(8, 3, 10)
	defer e.Close()
	e.Write([]byte("A界B\r\nC🍺D\r\nE界F\r\nG🍺H"))
	snap := e.Snapshot()
	for _, reverse := range []bool{false, true} {
		sel := Selection{AnchorCol: 2, AnchorRow: snap.YDisp, FocusCol: 1, FocusRow: snap.YDisp + 1, Absolute: true, Active: true}
		if reverse {
			sel.AnchorCol, sel.FocusCol = sel.FocusCol, sel.AnchorCol
			sel.AnchorRow, sel.FocusRow = sel.FocusRow, sel.AnchorRow
		}
		left, _, ok := selectionColumnsForSnapshot(snap, sel, 0)
		if !ok || left != 1 {
			t.Fatal("start cuts trailing half")
		}
		_, right, ok := selectionColumnsForSnapshot(snap, sel, 1)
		if !ok || right != 2 {
			t.Fatal("end cuts leading half")
		}
		startCol, startRow, endCol, endRow := sel.normalized()
		if SelectedText(snap, sel) != e.SelectionText(startRow, startCol, endRow, endCol) {
			t.Fatal("viewport and buffer copies differ")
		}
	}
}
