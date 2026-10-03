package goui

import (
	"image"
	"testing"
)

func drawingPixels(r rune, size image.Point) map[image.Point]bool {
	rects, _ := terminalDrawingRects(string(r), size.X, size.Y)
	pixels := map[image.Point]bool{}
	for _, rect := range rects {
		for y := rect.Min.Y; y < rect.Max.Y; y++ {
			for x := rect.Min.X; x < rect.Max.X; x++ {
				pixels[image.Pt(x, y)] = true
			}
		}
	}
	return pixels
}

func TestTerminalDrawingRepertoires(t *testing.T) {
	for _, size := range []image.Point{{8, 16}, {17, 41}, {20, 48}} {
		bounds := image.Rectangle{Max: size}
		for _, interval := range [][2]rune{{0x2500, 0x259f}, {0x2800, 0x28ff}, {0x1fb00, 0x1fb3b}} {
			for r := interval[0]; r <= interval[1]; r++ {
				rects, ok := terminalDrawingRects(string(r), size.X, size.Y)
				if !ok || !terminalCellGlyph(string(r)) {
					t.Fatalf("U+%04X bypasses physical drawing", r)
				}
				if len(rects) == 0 && r != 0x2800 {
					t.Fatalf("U+%04X unexpectedly blank at %v", r, size)
				}
				for _, rect := range rects {
					if rect.Empty() || !rect.In(bounds) {
						t.Fatalf("U+%04X invalid geometry %v in %v", r, rect, bounds)
					}
				}
			}
		}
	}
}

func TestTerminalBoxConnections(t *testing.T) {
	for _, size := range []image.Point{{17, 41}, {20, 48}} {
		// Corners, tees, crosses, mixed weights, double tracks and round corners
		// must present exactly the same edge pixels as their adjacent straight line.
		for _, sample := range []struct {
			glyph     rune
			neighbors [4]rune
		}{
			{'┌', [4]rune{0, '│', 0, '─'}}, {'┘', [4]rune{'│', 0, '─', 0}},
			{'┼', [4]rune{'│', '│', '─', '─'}}, {'┢', [4]rune{'│', '┃', 0, '━'}},
			{'╔', [4]rune{0, '║', 0, '═'}}, {'╩', [4]rune{'║', 0, '═', '═'}},
			{'╬', [4]rune{'║', '║', '═', '═'}}, {'╟', [4]rune{'║', '║', 0, '─'}},
			{'╭', [4]rune{0, '│', 0, '─'}}, {'╯', [4]rune{'│', 0, '─', 0}},
		} {
			pixels := drawingPixels(sample.glyph, size)
			for side, neighbor := range sample.neighbors {
				edge := drawingPixels(neighbor, size)
				length := size.X
				if side >= 2 {
					length = size.Y
				}
				for i := 0; i < length; i++ {
					p := image.Pt(i, 0)
					if side == 1 {
						p.Y = size.Y - 1
					}
					if side == 2 {
						p = image.Pt(0, i)
					}
					if side == 3 {
						p = image.Pt(size.X-1, i)
					}
					if pixels[p] != edge[p] {
						t.Fatalf("%c edge %d differs from %c at %v, size %v", sample.glyph, side, neighbor, p, size)
					}
				}
			}
		}
	}
}

func TestTerminalBrailleDotPositions(t *testing.T) {
	size := image.Pt(16, 32)
	for bit, position := range []image.Point{{4, 4}, {4, 12}, {4, 20}, {12, 4}, {12, 12}, {12, 20}, {4, 28}, {12, 28}} {
		pixels := drawingPixels(rune(0x2800+1<<bit), size)
		if !pixels[position] {
			t.Fatalf("braille dot %d missing at %v", bit+1, position)
		}
		for p := range pixels {
			if p.X < position.X-2 || p.X > position.X+2 || p.Y < position.Y-2 || p.Y > position.Y+2 {
				t.Fatalf("braille dot %d leaks into another dot: %v", bit+1, p)
			}
		}
	}
	if len(drawingPixels(0x2800, size)) != 0 {
		t.Fatal("braille blank contains ink")
	}
}

func TestTerminalDashesAndDiagonals(t *testing.T) {
	size := image.Pt(24, 48)
	for _, sample := range []struct {
		glyph rune
		count int
	}{{'╌', 2}, {'┄', 3}, {'┈', 4}} {
		pixels := drawingPixels(sample.glyph, size)
		count, previous := 0, false
		for x := 0; x < size.X; x++ {
			ink := pixels[image.Pt(x, size.Y/2)]
			if ink && !previous {
				count++
			}
			previous = ink
		}
		if count != sample.count {
			t.Fatalf("%c has %d dashes, want %d", sample.glyph, count, sample.count)
		}
	}
	for _, sample := range []struct {
		glyph rune
		ends  [2]image.Point
	}{
		{'╱', [2]image.Point{{23, 0}, {0, 47}}},
		{'╲', [2]image.Point{{0, 0}, {23, 47}}},
	} {
		pixels := drawingPixels(sample.glyph, size)
		for _, p := range sample.ends {
			if !pixels[p] {
				t.Fatalf("%c does not reach corner %v", sample.glyph, p)
			}
		}
		for y := 0; y < size.Y; y++ {
			found := false
			for x := 0; x < size.X; x++ {
				found = found || pixels[image.Pt(x, y)]
			}
			if !found {
				t.Fatalf("%c has a break in row %d", sample.glyph, y)
			}
		}
	}
}

func TestTerminalSextantPartitions(t *testing.T) {
	size := image.Pt(17, 41)
	left := drawingPixels(0x1fb14, size) // U+1FB14 is sextant 235, skipping the left-half alias.
	if !left[image.Pt(12, 3)] || !left[image.Pt(3, 20)] || left[image.Pt(3, 3)] {
		t.Fatal("sextant bit ordering changed")
	}
	full := map[image.Point]bool{}
	for _, r := range []rune{0x1fb00, 0x1fb01, 0x1fb03, 0x1fb07, 0x1fb0f, 0x1fb1e} {
		for p := range drawingPixels(r, size) {
			if full[p] {
				t.Fatalf("sextants overlap at %v", p)
			}
			full[p] = true
		}
	}
	if len(full) != size.X*size.Y {
		t.Fatalf("sextants leave %d missing pixels", size.X*size.Y-len(full))
	}
}
