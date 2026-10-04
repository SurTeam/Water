package goui

import (
	"image"
	"testing"
)

func TestCaretBoundsInPoints(t *testing.T) {
	for _, tc := range []struct {
		bounds image.Rectangle
		scale  float64
		want   image.Rectangle
	}{
		{image.Rect(200, 100, 220, 140), 2, image.Rect(100, 50, 110, 70)},
		{image.Rect(201, 101, 202, 141), 2, image.Rect(100, 50, 101, 71)},
		{image.Rect(200, 100, 220, 140), 1, image.Rect(200, 100, 220, 140)},
	} {
		if got := caretBoundsInPoints(tc.bounds, tc.scale); got != tc.want {
			t.Fatalf("%v at %v: got %v want %v", tc.bounds, tc.scale, got, tc.want)
		}
	}
}
