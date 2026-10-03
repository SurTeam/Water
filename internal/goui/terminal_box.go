package goui

import (
	"image"
	"math"
)

// Each entry encodes up, down, left, right: absent, light, heavy, double.
// Dashed lines, arcs and diagonals have separate geometry below.
var terminalBoxArms = [128]string{
	"0011", "0022", "1100", "2200", "0000", "0000", "0000", "0000", "0000", "0000", "0000", "0000", "0101", "0102", "0201", "0202",
	"0110", "0120", "0210", "0220", "1001", "1002", "2001", "2002", "1010", "1020", "2010", "2020", "1101", "1102", "2101", "1201",
	"2201", "2102", "1202", "2202", "1110", "1120", "2110", "1210", "2210", "2120", "1220", "2220", "0111", "0121", "0112", "0122",
	"0211", "0221", "0212", "0222", "1011", "1021", "1012", "1022", "2011", "2021", "2012", "2022", "1111", "1121", "1112", "1122",
	"2111", "1211", "2211", "2121", "2112", "1221", "1212", "2122", "1222", "2221", "2212", "2222", "0000", "0000", "0000", "0000",
	"0033", "3300", "0103", "0301", "0303", "0130", "0310", "0330", "1003", "3001", "3003", "1030", "3010", "3030", "1103", "3301",
	"3303", "1130", "3310", "3330", "0133", "0311", "0333", "1033", "3011", "3033", "1133", "3311", "3333", "0000", "0000", "0000",
	"0000", "0000", "0000", "0000", "0010", "1000", "0001", "0100", "0020", "2000", "0002", "0200", "0012", "1200", "0021", "2100",
}

func terminalBoxRects(r rune, width, height int) []image.Rectangle {
	thin := max(1, min(width, height)/8)
	if r >= 0x2504 && r <= 0x250b || r >= 0x254c && r <= 0x254f {
		count, vertical := 3, (r-0x2504)%4 >= 2
		if r >= 0x2508 {
			count = 4
		}
		if r >= 0x254c {
			count, vertical = 2, r >= 0x254e
		}
		stroke := thin * (1 + int(r%2))
		length := width
		if vertical {
			length = height
		}
		var rects []image.Rectangle
		for i := 0; i < count; i++ {
			a, b := length*i/count, length*(i+1)/count
			gap := max(1, (b-a)/3)
			if i != count-1 {
				b -= gap
			}
			if vertical {
				rects = append(rects, image.Rect((width-stroke)/2, a, (width-stroke)/2+stroke, b))
			} else {
				rects = append(rects, image.Rect(a, (height-stroke)/2, b, (height-stroke)/2+stroke))
			}
		}
		return rects
	}
	if r >= 0x256d && r <= 0x2573 {
		return terminalCurvedBoxRects(r, width, height, thin)
	}
	arms := terminalBoxArms[r-0x2500]
	if arms[0] == arms[1] && arms[0] != '0' && arms[0] != '3' && arms[2:] == "00" {
		stroke := thin * int(arms[0]-'0')
		return []image.Rectangle{image.Rect((width-stroke)/2, 0, (width-stroke)/2+stroke, height)}
	}
	if arms[2] == arms[3] && arms[2] != '0' && arms[2] != '3' && arms[:2] == "00" {
		stroke := thin * int(arms[2]-'0')
		return []image.Rectangle{image.Rect(0, (height-stroke)/2, width, (height-stroke)/2+stroke)}
	}
	var rects, double []image.Rectangle
	for direction := 0; direction < 4; direction++ {
		weight := int(arms[direction] - '0')
		if weight == 0 {
			continue
		}
		stroke := thin * weight
		x, y := (width-stroke)/2, (height-stroke)/2
		rect := image.Rect(x, 0, x+stroke, height)
		// Overlap perpendicular strokes at junctions, including odd cell sizes.
		switch direction {
		case 0:
			rect.Max.Y = y + stroke
		case 1:
			rect.Min.Y = y
		case 2:
			rect = image.Rect(0, y, x+stroke, y+stroke)
		case 3:
			rect = image.Rect(x, y, width, y+stroke)
		}
		if weight == 3 {
			double = append(double, rect)
		} else {
			rects = append(rects, rect)
		}
	}
	if len(double) == 0 {
		return rects
	}
	// Double lines are the boundary of joined three-stroke-wide arms. Taking
	// the boundary of the union preserves both tracks around corners and tees.
	inside := func(x, y int) bool {
		for _, rect := range double {
			if image.Pt(x, y).In(rect) {
				return true
			}
		}
		return false
	}
	allDouble := true
	for _, weight := range arms {
		if weight == '1' || weight == '2' {
			allDouble = false
		}
	}
	return append(rects, terminalPixelRuns(width, height, func(x, y int) bool {
		if !inside(x, y) {
			return false
		}
		if !allDouble {
			// At single/double junctions each rail meets the single center line.
			vx, hy := (width-3*thin)/2, (height-3*thin)/2
			return (arms[0] == '3' || arms[1] == '3') && (x < vx+thin || x >= vx+2*thin) ||
				(arms[2] == '3' || arms[3] == '3') && (y < hy+thin || y >= hy+2*thin)
		}
		for _, d := range []image.Point{{thin, 0}, {-thin, 0}, {0, thin}, {0, -thin}} {
			nx, ny := x+d.X, y+d.Y
			// Cell boundaries are open: adjacent cells continue these tracks.
			if nx >= 0 && nx < width && ny >= 0 && ny < height && !inside(nx, ny) {
				return true
			}
		}
		return false
	})...)
}

// Scanline runs keep diagonals and dots within physical cell bounds, without
// per-pixel draw calls or relying on font ink bounds.
func terminalPixelRuns(width, height int, filled func(int, int) bool) []image.Rectangle {
	var rects []image.Rectangle
	previous := make(map[[2]int]int)
	for y := 0; y < height; y++ {
		current := make(map[[2]int]int)
		for x := 0; x < width; {
			if !filled(x, y) {
				x++
				continue
			}
			start := x
			for x < width && filled(x, y) {
				x++
			}
			key := [2]int{start, x}
			if index, ok := previous[key]; ok {
				rects[index].Max.Y = y + 1
				current[key] = index
			} else {
				current[key] = len(rects)
				rects = append(rects, image.Rect(start, y, x, y+1))
			}
		}
		previous = current
	}
	return rects
}

func terminalCurvedBoxRects(r rune, width, height, stroke int) []image.Rectangle {
	if r >= 0x2571 {
		return terminalPixelRuns(width, height, func(x, y int) bool {
			// Distance from the two corner-to-corner lines, in physical pixels.
			a := float64((2*x+1)*height - (2*y+1)*width)
			b := float64((2*x+1)*height + (2*y+1)*width - 2*width*height)
			limit := float64(stroke) * math.Hypot(float64(width), float64(height))
			return (r != 0x2571 && math.Abs(a) <= limit) || (r != 0x2572 && math.Abs(b) <= limit)
		})
	}
	// The quarter circle turns between the same center lines as straight arms.
	cx, cy := float64((width-stroke)/2)+float64(stroke)/2, float64((height-stroke)/2)+float64(stroke)/2
	radius := min(cx, cy) / 2
	return terminalPixelRuns(width, height, func(x, y int) bool {
		px, py := float64(x)+.5, float64(y)+.5
		if r == 0x256e || r == 0x256f {
			px = 2*cx - px
		}
		if r == 0x256f || r == 0x2570 {
			py = 2*cy - py
		}
		if px >= cx+radius {
			return math.Abs(py-cy) <= float64(stroke)/2
		}
		if py >= cy+radius {
			return math.Abs(px-cx) <= float64(stroke)/2
		}
		if px < cx || py < cy {
			return false
		}
		return math.Abs(math.Hypot(px-cx-radius, py-cy-radius)-radius) <= float64(stroke)/2
	})
}

func terminalBrailleRects(r rune, width, height int) []image.Rectangle {
	mask := int(r - 0x2800)
	radius := float64(max(1, min(width/4, height/8))) / 2
	positions := [8]image.Point{{0, 0}, {0, 1}, {0, 2}, {1, 0}, {1, 1}, {1, 2}, {0, 3}, {1, 3}}
	return terminalPixelRuns(width, height, func(x, y int) bool {
		for bit, p := range positions {
			if mask&(1<<bit) == 0 {
				continue
			}
			cx, cy := float64(width*(2*p.X+1))/4, float64(height*(2*p.Y+1))/8
			if math.Hypot(float64(x)+.5-cx, float64(y)+.5-cy) <= max(.75, radius) {
				return true
			}
		}
		return false
	})
}
