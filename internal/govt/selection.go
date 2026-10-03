package govt

import (
	"unicode"
	"unicode/utf8"

	"github.com/SurTeam/Water/internal/xterm"
)

// CellDrawingColumns describes a glyph's visual footprint without changing
// logical Unicode widths. Private icons can use a following blank cell.
func CellDrawingColumns(cells []Cell, column int) int {
	cell := cells[column]
	columns := max(1, int(cell.Width))
	r, _ := utf8.DecodeRuneInString(cell.Text)
	if columns != 1 || !unicode.Is(unicode.Co, r) || column+1 >= len(cells) {
		return columns
	}
	next := cells[column+1]
	if next.URLID != 0 && next.URLID != cell.URLID {
		return columns
	}
	sameBG := cell.BG == next.BG && cell.Inverse == next.Inverse && (!cell.Inverse || cell.FG == next.FG)
	if next.Width == 1 && (next.Text == "" || next.Text == " ") && sameBG {
		return 2
	}
	return columns
}

// ExpandSelectionColumns makes both endpoints include complete glyphs. The
// caller supplies visual widths for either a viewport or an absolute buffer.
func ExpandSelectionColumns(left, right, cols int, widthAt func(int) int) (int, int, bool) {
	left, right = max(0, left), min(cols-1, right)
	if cols <= 0 || left > right {
		return 0, 0, false
	}
	if left > 0 && widthAt(left-1) == 2 {
		left--
	}
	if right+1 < cols && widthAt(right) == 2 {
		right++
	}
	return left, right, true
}

func bufferDrawingColumns(line *xterm.BufferLine, column int) int {
	var cells [2]Cell
	count := min(2, line.Len-column)
	for i := 0; i < count; i++ {
		var raw xterm.CellData
		line.LoadCell(column+i, &raw)
		cells[i] = Cell{Text: raw.GetChars(), Width: uint8(raw.GetWidth()), FG: colorFromAttrs(&raw.AttributeData, true), BG: colorFromAttrs(&raw.AttributeData, false), Inverse: raw.IsInverse() != 0}
		if raw.Extended != nil {
			cells[i].URLID = raw.Extended.URLID()
		}
	}
	return CellDrawingColumns(cells[:count], 0)
}
