package goui

import (
	"image"
	"image/color"
	"math"

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

func drawTerminalGlyph(dst *ebiten.Image, value string, face text.Face, bounds image.Rectangle, cell nativeCellMetrics, fg color.NRGBA) {
	op := &text.DrawOptions{}
	scale, x, y := terminalGlyphPlacement(value, face, bounds, cell)
	op.GeoM.Scale(scale, scale)
	op.GeoM.Translate(x, y)
	op.ColorScale.ScaleWithColor(fg)
	text.Draw(dst.SubImage(bounds.Intersect(dst.Bounds())).(*ebiten.Image), value, face, op)
}
