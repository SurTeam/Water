package goui

import (
	"image"
	"math"
	"testing"

	"github.com/SurTeam/Water/internal/govt"
)

func TestWideGlyphFitPreservesInkAndCentersBothAxes(t *testing.T) {
	cell := image.Rect(40, 80, 72, 124)
	for _, ink := range []image.Rectangle{image.Rect(-9, -5, 48, 61), image.Rect(3, 9, 17, 31), image.Rect(-4, 2, 36, 18)} {
		scale, x, y := fitTerminalGlyph(ink, cell)
		left, top := x+float64(ink.Min.X)*scale, y+float64(ink.Min.Y)*scale
		right, bottom := x+float64(ink.Max.X)*scale, y+float64(ink.Max.Y)*scale
		if scale > 1 || left < float64(cell.Min.X+1)-1e-9 || right > float64(cell.Max.X-1)+1e-9 || top < float64(cell.Min.Y+1)-1e-9 || bottom > float64(cell.Max.Y-1)+1e-9 {
			t.Fatalf("ink clipped: %v scale=%g (%g,%g)-(%g,%g)", ink, scale, left, top, right, bottom)
		}
		if math.Abs((left+right)/2-float64(cell.Min.X+cell.Max.X)/2) > 1e-9 || math.Abs((top+bottom)/2-float64(cell.Min.Y+cell.Max.Y)/2) > 1e-9 {
			t.Fatal("glyph is not centered")
		}
		if ink.Dx() < cell.Dx()-2 && ink.Dy() < cell.Dy()-2 && scale != 1 {
			t.Fatal("small glyph resized")
		}
	}
}

func TestWideUnicodeStaysAtomicBesideLigatures(t *testing.T) {
	e := govt.New(30, 2, 0)
	defer e.Close()
	e.Write([]byte("abcd界ef"))
	v := &TerminalView{Ligatures: true}
	row := v.prepareRow(e.Snapshot().RowsData[0])
	for _, tc := range []struct {
		text   string
		column int
		width  int
	}{{"", 2, 1}, {"界", 5, 2}} {
		found := false
		for _, run := range row.text {
			if run.text == tc.text {
				found = true
				if run.wide != (tc.width == 2) || run.spanColumns != tc.width || run.drawColumns != tc.width || run.startColumn != tc.column {
					t.Fatalf("wide run: %+v", run)
				}
			}
		}
		if !found {
			t.Fatalf("%s merged into neighboring text: %+v", tc.text, row.text)
		}
	}
}

func TestIconCanUseFollowingBlankWithoutConsumingIt(t *testing.T) {
	for _, text := range []string{" X", "X", ""} {
		e := govt.New(8, 2, 0)
		e.Write([]byte(text))
		v := &TerminalView{Ligatures: true}
		run := v.prepareRow(e.Snapshot().RowsData[0]).text[0]
		want := 2
		if text == "X" {
			want = 1
		}
		if run.spanColumns != 1 || run.drawColumns != want || e.Snapshot().CursorX != len([]rune(text)) {
			t.Fatalf("logical/visual width for %q: %+v", text, run)
		}
		e.Close()
	}
}
