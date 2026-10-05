package goui

import (
	"image"
	"testing"
)

func TestCaretSessionUsesLogicalFramebufferBounds(t *testing.T) {
	// Coordinates come from the rendered terminal grid, including its display
	// scale. The text-input backend owns conversion to native platform units.
	for _, scale := range []float64{1, 2} {
		w := EbitengineWindow{scale: scale}
		bounds := image.Rect(w.dp(176), w.dp(652), w.dp(184), w.dp(674))
		opts := w.caretSessionOptions(bounds)
		if opts.CaretBounds != bounds {
			t.Fatalf("scale %v: session bounds %v, rendered bounds %v", scale, opts.CaretBounds, bounds)
		}
		if w.inputCaret != bounds {
			t.Fatalf("scale %v: reported bounds %v, rendered bounds %v", scale, w.inputCaret, bounds)
		}
	}
}
