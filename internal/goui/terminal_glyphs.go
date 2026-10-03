package goui

import (
	"image"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Block elements describe fractions of a terminal cell, not font glyphs.
// Their edges must meet even when line height exceeds the font's ink bounds.
func terminalBlockRects(value string, width, height int) ([]image.Rectangle, bool) {
	r, n := utf8.DecodeRuneInString(value)
	if n != len(value) || r < '\u2580' || r > '\u259f' {
		return nil, false
	}
	xmid, ymid := width/2, height/2
	switch {
	case r == '\u2580':
		return []image.Rectangle{image.Rect(0, 0, width, ymid)}, true
	case r >= '\u2581' && r <= '\u2588':
		return []image.Rectangle{image.Rect(0, height*int('\u2588'-r)/8, width, height)}, true
	case r >= '\u2589' && r <= '\u258f':
		return []image.Rectangle{image.Rect(0, 0, width*int('\u2590'-r)/8, height)}, true
	case r == '\u2590':
		return []image.Rectangle{image.Rect(xmid, 0, width, height)}, true
	case r >= '\u2591' && r <= '\u2593':
		var rects []image.Rectangle
		for y := 0; y < height; y++ {
			for x := 0; x < width; x++ {
				if (x+2*y)%4 < int(r-'\u2590') {
					rects = append(rects, image.Rect(x, y, x+1, y+1))
				}
			}
		}
		return rects, true
	case r == '\u2594':
		return []image.Rectangle{image.Rect(0, 0, width, max(1, height/8))}, true
	case r == '\u2595':
		return []image.Rectangle{image.Rect(width-max(1, width/8), 0, width, height)}, true
	default:
		// Bits are upper-left, upper-right, lower-left, lower-right.
		mask := []uint8{4, 8, 1, 13, 9, 7, 11, 2, 6, 14}[r-'\u2596']
		quadrants := []image.Rectangle{
			image.Rect(0, 0, xmid, ymid), image.Rect(xmid, 0, width, ymid),
			image.Rect(0, ymid, xmid, height), image.Rect(xmid, ymid, width, height),
		}
		var rects []image.Rectangle
		for n, rect := range quadrants {
			if mask&(1<<n) != 0 {
				rects = append(rects, rect)
			}
		}
		return rects, true
	}
}

func terminalEmoji(value string) bool {
	r, _ := utf8.DecodeRuneInString(value)
	return r >= 0x1f000 && r <= 0x1faff || strings.ContainsRune(value, '\ufe0f')
}

func terminalCellGlyph(value string) bool {
	r, n := utf8.DecodeRuneInString(value)
	return n == len(value) && (r >= 0x2500 && r <= 0x259f || r >= 0x2800 && r <= 0x28ff || r >= 0x1fb00 && r <= 0x1fb3b) || terminalEmoji(value) || terminalPrivateIcon(value)
}

func terminalPrivateIcon(value string) bool {
	r, _ := utf8.DecodeRuneInString(value)
	return unicode.Is(unicode.Co, r)
}

func terminalDrawingRects(value string, width, height int) ([]image.Rectangle, bool) {
	if rects, ok := terminalBlockRects(value, width, height); ok {
		return rects, true
	}
	r, n := utf8.DecodeRuneInString(value)
	if n != len(value) {
		return nil, false
	}
	if r >= 0x2800 && r <= 0x28ff {
		return terminalBrailleRects(r, width, height), true
	}
	if r >= 0x1fb00 && r <= 0x1fb3b {
		mask := int(r-0x1fb00) + 1
		if mask >= 21 {
			mask++
		}
		if mask >= 42 {
			mask++
		}
		var rects []image.Rectangle
		for bit := 0; bit < 6; bit++ {
			if mask&(1<<bit) != 0 {
				x, y := bit%2, bit/2
				rects = append(rects, image.Rect(width*x/2, height*y/3, width*(x+1)/2, height*(y+1)/3))
			}
		}
		return rects, true
	}
	if r >= 0x2500 && r <= 0x2573 {
		return terminalBoxRects(r, width, height), true
	}
	thin := max(1, min(width, height)/8)
	thick := max(1, 2*thin)
	vertical := func(stroke int) image.Rectangle {
		return image.Rect((width-stroke)/2, 0, (width-stroke)/2+stroke, height)
	}
	horizontal := func(stroke int) image.Rectangle {
		return image.Rect(0, (height-stroke)/2, width, (height-stroke)/2+stroke)
	}
	// Short lines must use the same stroke and center as their full-length
	// neighbors. In particular, OpenCode ends its left border with U+2579.
	if r >= '╴' && r <= '╻' {
		stroke := thin
		if r >= '╸' {
			stroke = thick
		}
		var rect image.Rectangle
		switch (r - '╴') % 4 {
		case 0:
			rect = horizontal(stroke)
			rect.Max.X = width / 2
		case 1:
			rect = vertical(stroke)
			rect.Max.Y = height / 2
		case 2:
			rect = horizontal(stroke)
			rect.Min.X = width / 2
		case 3:
			rect = vertical(stroke)
			rect.Min.Y = height / 2
		}
		return []image.Rectangle{rect}, true
	}
	if r >= '╼' && r <= '╿' {
		if r == '╼' || r == '╾' {
			left, right := horizontal(thin), horizontal(thick)
			if r == '╾' {
				left, right = right, left
			}
			left.Max.X, right.Min.X = width/2, width/2
			return []image.Rectangle{left, right}, true
		}
		up, down := vertical(thin), vertical(thick)
		if r == '╿' {
			up, down = down, up
		}
		up.Max.Y, down.Min.Y = height/2, height/2
		return []image.Rectangle{up, down}, true
	}
	return nil, false
}
