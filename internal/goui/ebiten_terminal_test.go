package goui

import (
	"testing"

	"github.com/hajimehoshi/ebiten/v2/text/v2"
	"golang.org/x/image/font/gofont/gomono"
	"golang.org/x/image/font/gofont/gomonobold"
	"golang.org/x/image/font/gofont/gomonoitalic"
)

func TestNativeCellPreservesDescendersAtTightLineHeights(t *testing.T) {
	for _, size := range []float64{12, 16, 18, 32} {
		var faces []text.Face
		for _, data := range [][]byte{gomono.TTF, gomonobold.TTF, gomonoitalic.TTF} {
			faces = append(faces, &text.GoTextFace{Source: fontSource(data), Size: size})
		}
		cell := measureNativeCell(faces, int(size))
		for _, face := range faces {
			m := face.Metrics()
			if cell.baseline-m.HAscent < 0 || cell.baseline+m.HDescent > float64(cell.height) {
				t.Fatalf("clipped font metrics: %+v, %+v", cell, m)
			}
			for _, glyph := range text.AppendLazyGlyphs(nil, "gypqj", face, nil) {
				bounds := glyph.ImageBounds
				y := cell.baseline - m.HAscent
				if float64(bounds.Min.Y)+y < 0 || float64(bounds.Max.Y)+y > float64(cell.height) {
					t.Fatalf("size %g: descender glyph clipped: bounds=%v offset=%g height=%d", size, bounds, y, cell.height)
				}
			}
		}
	}
}
