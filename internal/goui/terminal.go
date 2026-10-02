package goui

import (
	"image"
	"image/color"
	"sync"
	"strings"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget/material"

	"github.com/SurTeam/Water/internal/govt"
)

type TerminalTheme struct {
	Foreground color.NRGBA
	Background color.NRGBA
	Cursor     color.NRGBA
	Selection  color.NRGBA
	Palette    [256]color.NRGBA
}

func DefaultTerminalTheme() TerminalTheme {
	t := TerminalTheme{
		Foreground: color.NRGBA{R: 0xd8, G: 0xde, B: 0xe9, A: 0xff},
		Background: color.NRGBA{R: 0x16, G: 0x1b, B: 0x22, A: 0xff},
		Cursor:     color.NRGBA{R: 0xa3, G: 0xbe, B: 0x8c, A: 0xff},
		Selection:  color.NRGBA{R: 0x5e, G: 0x81, B: 0xac, A: 0x88},
	}
	base := [16]color.NRGBA{
		{0x2e, 0x34, 0x40, 0xff}, {0xbf, 0x61, 0x6a, 0xff},
		{0xa3, 0xbe, 0x8c, 0xff}, {0xeb, 0xcb, 0x8b, 0xff},
		{0x81, 0xa1, 0xc1, 0xff}, {0xb4, 0x8e, 0xad, 0xff},
		{0x88, 0xc0, 0xd0, 0xff}, {0xe5, 0xe9, 0xf0, 0xff},
		{0x4c, 0x56, 0x6a, 0xff}, {0xbf, 0x61, 0x6a, 0xff},
		{0xa3, 0xbe, 0x8c, 0xff}, {0xeb, 0xcb, 0x8b, 0xff},
		{0x81, 0xa1, 0xc1, 0xff}, {0xb4, 0x8e, 0xad, 0xff},
		{0x8f, 0xbc, 0xbb, 0xff}, {0xec, 0xef, 0xf4, 0xff},
	}
	copy(t.Palette[:16], base[:])
	steps := [6]uint8{0, 95, 135, 175, 215, 255}
	index := 16
	for _, r := range steps {
		for _, g := range steps {
			for _, b := range steps {
				t.Palette[index] = color.NRGBA{R: r, G: g, B: b, A: 0xff}
				index++
			}
		}
	}
	for i := 0; i < 24; i++ {
		v := uint8(8 + i*10)
		t.Palette[232+i] = color.NRGBA{R: v, G: v, B: v, A: 0xff}
	}
	return t
}

type terminalStyle struct {
	fg            color.NRGBA
	bold          bool
	italic        bool
	dim           bool
	underline     bool
	strikethrough bool
}

type textRun struct {
	startColumn int
	spanColumns int
	text        string
	style       terminalStyle
}

type backgroundRun struct {
	startColumn int
	spanColumns int
	color       color.NRGBA
}

type preparedRow struct {
	hash        uint64
	text        []textRun
	backgrounds []backgroundRun
}

type Selection struct {
	AnchorCol int
	AnchorRow int
	FocusCol  int
	FocusRow  int
	Active    bool
}

func (s Selection) normalized() (startCol,startRow,endCol,endRow int) {
	startCol,startRow=s.AnchorCol,s.AnchorRow
	endCol,endRow=s.FocusCol,s.FocusRow
	if startRow>endRow || (startRow==endRow && startCol>endCol) {
		startCol,endCol=endCol,startCol
		startRow,endRow=endRow,startRow
	}
	return
}

func SelectedText(snap govt.Snapshot, selection Selection) string {
	if !selection.Active || snap.Rows<=0 || snap.Cols<=0 { return "" }
	startCol,startRow,endCol,endRow:=selection.normalized()
	if startRow<0{startRow=0};if endRow>=snap.Rows{endRow=snap.Rows-1}
	if startRow>endRow{return ""}

	var out strings.Builder
	for row:=startRow;row<=endRow;row++ {
		if row>startRow { out.WriteByte('\n') }
		if row<0 || row>=len(snap.RowsData) { continue }
		line:=snap.RowsData[row]
		left,right:=0,snap.Cols-1
		if row==startRow { left=startCol }
		if row==endRow { right=endCol }
		if left<0{left=0};if right>=snap.Cols{right=snap.Cols-1}
		if left>right{continue}
		var rowText strings.Builder
		for col:=left;col<=right && col<len(line.Cells);col++ {
			cell:=line.Cells[col]
			if cell.Width==0 { continue }
			if cell.Text=="" { rowText.WriteByte(' ') } else { rowText.WriteString(cell.Text) }
		}
		text:=rowText.String()
		if right==snap.Cols-1 {
			text=strings.TrimRight(text," ")
		}
		out.WriteString(text)
	}
	return out.String()
}

type TerminalView struct {
	Theme      TerminalTheme
	FontSize   unit.Sp
	CellWidth  unit.Dp
	LineHeight unit.Dp
	FontFamily string
	Hyperlinks bool

	mu    sync.Mutex
	cache map[int]preparedRow
}

func NewTerminalView() *TerminalView {
	return &TerminalView{
		Theme:      DefaultTerminalTheme(),
		FontSize:   unit.Sp(14),
		CellWidth:  unit.Dp(8.4),
		LineHeight: unit.Dp(20),
		FontFamily: "monospace",
		Hyperlinks: true,
		cache:      make(map[int]preparedRow),
	}
}

func (v *TerminalView) Layout(gtx layout.Context, th *material.Theme, snap govt.Snapshot, selection Selection) layout.Dimensions {
	cellWidth := gtx.Dp(v.CellWidth)
	lineHeight := gtx.Dp(v.LineHeight)
	width := snap.Cols * cellWidth
	height := snap.Rows * lineHeight
	if width < 1 {
		width = 1
	}
	if height < 1 {
		height = 1
	}
	size := image.Pt(width, height)

	paint.FillShape(gtx.Ops, v.Theme.Background, clip.Rect{Max: size}.Op())

	v.mu.Lock()
	for rowIndex, row := range snap.RowsData {
		prepared, ok := v.cache[rowIndex]
		if !ok || prepared.hash != row.Hash {
			prepared = v.prepareRow(row)
			v.cache[rowIndex] = prepared
		}
		y := rowIndex * lineHeight

		for _, bg := range prepared.backgrounds {
			x0 := bg.startColumn * cellWidth
			x1 := (bg.startColumn + bg.spanColumns) * cellWidth
			rect := image.Rect(x0, y, x1, y+lineHeight)
			paint.FillShape(gtx.Ops, bg.color, clip.Rect(rect).Op())
		}
		if left,right,ok:=selectionColumns(selection,rowIndex,snap.Cols);ok {
			rect:=image.Rect(left*cellWidth,y,(right+1)*cellWidth,y+lineHeight)
			paint.FillShape(gtx.Ops,v.Theme.Selection,clip.Rect(rect).Op())
		}
		for _, run := range prepared.text {
			if run.text == "" {
				continue
			}
			x := run.startColumn * cellWidth
			spanWidth := run.spanColumns * cellWidth
			if spanWidth <= 0 {
				continue
			}
			tr := op.Offset(image.Pt(x, y)).Push(gtx.Ops)
			child := gtx
			child.Constraints.Min = image.Point{}
			child.Constraints.Max = image.Pt(spanWidth, lineHeight)
			label := material.Label(th, v.FontSize, run.text)
			label.MaxLines = 1
			label.Color = run.style.fg
			label.Font.Typeface = font.Typeface(v.FontFamily)
			if run.style.bold {
				label.Font.Weight = font.Bold
			}
			if run.style.italic {
				label.Font.Style = font.Italic
			}
			if run.style.dim {
				label.Color.A = uint8(uint16(label.Color.A) * 2 / 3)
			}
			label.Layout(child)
			tr.Pop()

			if run.style.underline {
				uy := y + lineHeight - 2
				rect := image.Rect(x, uy, x+spanWidth, uy+1)
				paint.FillShape(gtx.Ops, run.style.fg, clip.Rect(rect).Op())
			}
			if run.style.strikethrough {
				sy := y + lineHeight/2
				rect := image.Rect(x, sy, x+spanWidth, sy+1)
				paint.FillShape(gtx.Ops, run.style.fg, clip.Rect(rect).Op())
			}
		}
	}
	for row := len(snap.RowsData); row < len(v.cache); row++ {
		delete(v.cache, row)
	}
	v.mu.Unlock()

	if !snap.CursorHide && snap.CursorY >= 0 && snap.CursorY < snap.Rows &&
		snap.CursorX >= 0 && snap.CursorX < snap.Cols {
		x := snap.CursorX * cellWidth
		y := snap.CursorY * lineHeight
		rect := image.Rect(x, y+lineHeight-2, x+cellWidth, y+lineHeight)
		paint.FillShape(gtx.Ops, v.Theme.Cursor, clip.Rect(rect).Op())
	}

	return layout.Dimensions{Size: gtx.Constraints.Constrain(size)}
}

func (v *TerminalView) prepareRow(row govt.Row) preparedRow {
	prepared := preparedRow{hash: row.Hash}
	var currentText *textRun
	var currentBG *backgroundRun

	for column, cell := range row.Cells {
		fg, bg := v.resolveColors(cell)
		if bg != v.Theme.Background {
			if currentBG != nil && currentBG.color == bg &&
				currentBG.startColumn+currentBG.spanColumns == column {
				currentBG.spanColumns++
			} else {
				prepared.backgrounds = append(prepared.backgrounds, backgroundRun{
					startColumn: column,
					spanColumns: 1,
					color:       bg,
				})
				currentBG = &prepared.backgrounds[len(prepared.backgrounds)-1]
			}
		} else {
			currentBG = nil
		}

		if cell.Width == 0 || cell.Invisible {
			currentText = nil
			continue
		}
		text := cell.Text
		if text == "" {
			text = " "
		}
		style := terminalStyle{
			fg:            fg,
			bold:          cell.Bold,
			italic:        cell.Italic,
			dim:           cell.Dim,
			underline:     cell.Underline || (v.Hyperlinks && cell.URLID != 0),
			strikethrough: cell.Strikethrough,
		}
		width := int(cell.Width)
		if width < 1 {
			width = 1
		}
		if currentText != nil && currentText.style == style &&
			currentText.startColumn+currentText.spanColumns == column {
			currentText.text += text
			currentText.spanColumns += width
		} else {
			prepared.text = append(prepared.text, textRun{
				startColumn: column,
				spanColumns: width,
				text:        text,
				style:       style,
			})
			currentText = &prepared.text[len(prepared.text)-1]
		}
	}
	return prepared
}

func (v *TerminalView) resolveColors(cell govt.Cell) (color.NRGBA, color.NRGBA) {
	fg := resolveColor(cell.FG, v.Theme.Foreground, v.Theme)
	bg := resolveColor(cell.BG, v.Theme.Background, v.Theme)
	if cell.Inverse {
		return bg, fg
	}
	return fg, bg
}

func resolveColor(c govt.Color, fallback color.NRGBA, theme TerminalTheme) color.NRGBA {
	switch c.Mode {
	case govt.ColorPalette:
		if c.Value < 256 {
			return theme.Palette[c.Value]
		}
	case govt.ColorRGB:
		return color.NRGBA{
			R: uint8(c.Value >> 16),
			G: uint8(c.Value >> 8),
			B: uint8(c.Value),
			A: 0xff,
		}
	}
	return fallback
}


func selectionColumns(selection Selection,row,cols int)(int,int,bool){
	if !selection.Active || cols<=0 { return 0,0,false }
	startCol,startRow,endCol,endRow:=selection.normalized()
	if row<startRow || row>endRow { return 0,0,false }
	left,right:=0,cols-1
	if row==startRow { left=startCol }
	if row==endRow { right=endCol }
	if left<0{left=0};if right>=cols{right=cols-1}
	if left>right{return 0,0,false}
	return left,right,true
}
