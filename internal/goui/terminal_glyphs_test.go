package goui

import (
	"image"
	"testing"

	"github.com/SurTeam/Water/internal/govt"
)

func TestTerminalBlockEdgesMeetAcrossRows(t *testing.T) {
	for _, size := range []image.Point{{17, 41}, {20, 48}} {
		upper, _ := terminalBlockRects("▀", size.X, size.Y)
		lower, _ := terminalBlockRects("▄", size.X, size.Y)
		if upper[0].Max.Y != lower[0].Min.Y || lower[0].Max.Y != size.Y {
			t.Fatalf("half blocks leave a seam at %v: %v %v", size, upper, lower)
		}
		for _, value := range []string{"█", "▌", "▐", "▏", "│", "┃", "║"} {
			rects, ok := terminalDrawingRects(value, size.X, size.Y)
			if !ok || rects[0].Min.Y != 0 || rects[0].Max.Y != size.Y {
				t.Fatalf("%s does not span the full row: %v", value, rects)
			}
		}
	}
}

func TestTerminalCellGlyphsStaySeparateFromLigatureRuns(t *testing.T) {
	e := govt.New(20, 2, 0)
	defer e.Close()
	e.Write([]byte("ab█▄╹🍺cd"))
	v := &TerminalView{Ligatures: true}
	row := v.prepareRow(e.Snapshot().RowsData[0])
	for _, glyph := range []string{"█", "▄", "╹", "🍺"} {
		found := false
		for _, run := range row.text {
			if run.text == glyph {
				found = true
				if glyph == "🍺" && run.spanColumns != 2 {
					t.Fatal("brew emoji must retain its two terminal columns")
				}
			}
		}
		if !found {
			t.Fatalf("%s merged into a text run: %+v", glyph, row.text)
		}
	}
}

func TestTerminalBorderEndpointsMeetAdjacentRows(t *testing.T) {
	for _, size := range []image.Point{{17, 41}, {20, 48}} {
		for _, pair := range [][2]string{{"│", "╵"}, {"┃", "╹"}} {
			line, _ := terminalDrawingRects(pair[0], size.X, size.Y)
			end, ok := terminalDrawingRects(pair[1], size.X, size.Y)
			if !ok || end[0].Min.Y != 0 || end[0].Max.Y != size.Y/2 ||
				line[0].Min.X != end[0].Min.X || line[0].Max.X != end[0].Max.X {
				t.Fatalf("%s/%s border has a gap or changes weight: %v %v", pair[0], pair[1], line, end)
			}
		}
		for _, glyph := range []string{"╽", "╿"} {
			rects, ok := terminalDrawingRects(glyph, size.X, size.Y)
			if !ok || rects[0].Min.Y != 0 || rects[0].Max.Y != rects[1].Min.Y || rects[1].Max.Y != size.Y {
				t.Fatalf("mixed-weight border %s has a vertical gap: %v", glyph, rects)
			}
		}
	}
}
