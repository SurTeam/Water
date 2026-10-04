package goui

import (
	"image"
	"image/color"
	"math"
	"sync"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/text/v2"
)

// Use ink bounds, not the font advance: icon/fallback glyphs may overhang
// their advertised advance, including to the left of their origin.
func terminalGlyphInk(value string, face text.Face) image.Rectangle {
	var ink image.Rectangle
	for _, glyph := range text.AppendLazyGlyphs(nil, value, face, nil) {
		ink = ink.Union(glyph.ImageBounds)
	}
	return ink
}

func fitTerminalGlyph(ink, cell image.Rectangle) (scale, x, y float64) {
	if ink.Empty() || cell.Empty() {
		return 1, float64(cell.Min.X), float64(cell.Min.Y)
	}
	// Preserve an antialiased pixel on each side; never enlarge a small glyph.
	width, height := max(1, cell.Dx()-2), max(1, cell.Dy()-2)
	scale = math.Min(1, math.Min(float64(width)/float64(ink.Dx()), float64(height)/float64(ink.Dy())))
	x = float64(cell.Min.X) + (float64(cell.Dx())-float64(ink.Dx())*scale)/2 - float64(ink.Min.X)*scale
	y = float64(cell.Min.Y) + (float64(cell.Dy())-float64(ink.Dy())*scale)/2 - float64(ink.Min.Y)*scale
	return
}

func terminalGlyphPlacement(value string, face text.Face, bounds image.Rectangle, cell nativeCellMetrics) (scale, x, y float64) {
	// Cell width is a grid allocation, not an instruction to center glyph ink.
	// Text (especially CJK punctuation and combining marks) keeps font bearings
	// and the common baseline. Only pictorial emoji and private icons are fitted.
	if terminalEmoji(value) || terminalPrivateIcon(value) {
		return fitTerminalGlyph(terminalGlyphInk(value, face), bounds)
	}
	return 1, float64(bounds.Min.X), float64(bounds.Min.Y) + cell.baseline - face.Metrics().HAscent
}

var terminalGlyphPool = sync.Pool{New: func() any { return new([]text.Glyph) }}

func drawTerminalGlyph(dst *ebiten.Image, value string, face text.Face, bounds image.Rectangle, cell nativeCellMetrics, fg color.NRGBA) {
	scale, x, y := terminalGlyphPlacement(value, face, bounds, cell)
	bounds = bounds.Intersect(dst.Bounds())
	if bounds.Empty() {
		return
	}
	glyphs := terminalGlyphPool.Get().(*[]text.Glyph)
	*glyphs = text.AppendGlyphs((*glyphs)[:0], value, face, nil)
	defer func() {
		clear(*glyphs) // Do not keep glyph textures alive through the pool.
		*glyphs = (*glyphs)[:0]
		terminalGlyphPool.Put(glyphs)
	}()
	var clipped *ebiten.Image
	for _, glyph := range *glyphs {
		if glyph.Image == nil {
			continue
		}
		left, top := x+glyph.X*scale, y+glyph.Y*scale
		right := left + float64(glyph.Image.Bounds().Dx())*scale
		bottom := top + float64(glyph.Image.Bounds().Dy())*scale
		if right <= float64(bounds.Min.X) || bottom <= float64(bounds.Min.Y) || left >= float64(bounds.Max.X) || top >= float64(bounds.Max.Y) {
			continue
		}
		target := dst
		// Most glyphs fit their cells. Giving every cell its own scissor
		// rectangle breaks GPU batching and creates thousands of native calls.
		// Keep exact clipping only for ink that actually crosses a cell edge.
		if left < float64(bounds.Min.X) || top < float64(bounds.Min.Y) || right > float64(bounds.Max.X) || bottom > float64(bounds.Max.Y) {
			if clipped == nil {
				clipped = dst.SubImage(bounds).(*ebiten.Image)
			}
			target = clipped
		}
		var op ebiten.DrawImageOptions
		op.GeoM.Scale(scale, scale)
		op.GeoM.Translate(left, top)
		if glyph.Colored {
			op.ColorScale.ScaleAlpha(float32(fg.A) / 255)
		} else {
			op.ColorScale.ScaleWithColor(fg)
		}
		target.DrawImage(glyph.Image, &op)
	}
}
