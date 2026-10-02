package goui

import (
	"image"
	"image/color"
	"sync"
	"strings"
	"unicode"
	"unicode/utf8"

	"gioui.org/f32"
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
	Absolute  bool
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
	if selection.Absolute {
		startRow-=snap.YDisp
		endRow-=snap.YDisp
	}
	if startRow<0{startRow=0};if endRow>=snap.Rows{endRow=snap.Rows-1}
	if startRow>endRow{return ""}

	var out strings.Builder
	for row:=startRow;row<=endRow;row++ {
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
		out.WriteString(strings.TrimRight(rowText.String()," "))
		if row!=endRow {
			// xterm marks the following physical row as wrapped when this
			// row soft-wraps into it. Soft wraps must not become clipboard
			// newlines.
			nextWrapped:=row+1<len(snap.RowsData) && snap.RowsData[row+1].Wrapped
			if !nextWrapped { out.WriteByte('\n') }
		}
	}
	return out.String()
}

type selectionWordClass uint8

const (
	selectionWhitespace selectionWordClass = iota
	selectionCJK
	selectionAlphanumeric
	selectionPunctuation
)

type selectionSegment struct {
	start int
	end int
	class selectionWordClass
	contiguousFromPrevious bool
}

type selectionCharacter struct {
	row int
	col int
	text string
	class selectionWordClass
}

func MultiClickSelection(snap govt.Snapshot,col,row,clickCount int) Selection {
	absoluteRow:=snap.YDisp+row
	fallback:=Selection{
		AnchorCol:col,AnchorRow:absoluteRow,
		FocusCol:col,FocusRow:absoluteRow,
		Active:true,Absolute:true,
	}
	if clickCount<2 || row<0 || row>=len(snap.RowsData) || col<0 || col>=snap.Cols {
		return fallback
	}
	level:=(clickCount-2)/2
	chars,clicked:=selectionLogicalLine(snap,col,row)
	if len(chars)==0 || clicked<0 { return fallback }
	segments:=selectionSegments(chars)
	segmentIndex:=-1
	for index,segment:=range segments {
		if clicked>=segment.start && clicked<segment.end {segmentIndex=index;break}
	}
	if segmentIndex<0 { return fallback }
	first,last:=segmentIndex,segmentIndex
	for n:=0;n<level;n++ {
		left:=selectionExpandLeft(segments,first)
		right:=selectionExpandRight(segments,last)
		if left<0 && right<0 {break}
		if left>=0 {first=left}
		if right>=0 {last=right}
	}
	start:=chars[segments[first].start]
	finish:=chars[segments[last].end-1]
	return Selection{
		AnchorCol:start.col,AnchorRow:snap.YDisp+start.row,
		FocusCol:finish.col,FocusRow:snap.YDisp+finish.row,
		Active:true,Absolute:true,
	}
}

func selectionLogicalLine(snap govt.Snapshot,col,row int)([]selectionCharacter,int){
	first,last:=row,row
	for first>0 && first<len(snap.RowsData) && snap.RowsData[first].Wrapped { first-- }
	for last+1<len(snap.RowsData) && snap.RowsData[last+1].Wrapped { last++ }

	chars:=make([]selectionCharacter,0,(last-first+1)*snap.Cols)
	clicked:=-1
	for y:=first;y<=last;y++ {
		line:=snap.RowsData[y]
		for x:=0;x<snap.Cols && x<len(line.Cells);x++ {
			cell:=line.Cells[x]
			if cell.Width==0 { continue }
			text:=cell.Text
			if text=="" {text=" "}
			r,_:=utf8FirstRune(text)
			entry:=selectionCharacter{row:y,col:x,text:text,class:selectionClass(r)}
			if y==row && (x==col || (cell.Width==2 && col==x+1)) {clicked=len(chars)}
			chars=append(chars,entry)
		}
	}
	if clicked<0 && col>0 && row>=0 && row<len(snap.RowsData) {
		line:=snap.RowsData[row]
		if col<len(line.Cells) && line.Cells[col].Width==0 {
			for index:=range chars {
				if chars[index].row==row && chars[index].col==col-1 {clicked=index;break}
			}
		}
	}
	return chars,clicked
}

func selectionSegments(chars []selectionCharacter)[]selectionSegment{
	if len(chars)==0{return nil}
	segments:=make([]selectionSegment,0,len(chars))
	start:=0
	for end:=1;end<=len(chars);end++ {
		split:=end==len(chars)
		if !split {
			split=chars[end-1].class!=chars[end].class ||
				!selectionCharactersAdjacent(chars[end-1],chars[end])
		}
		if !split {continue}
		segments=append(segments,selectionSegment{
			start:start,end:end,class:chars[start].class,
			contiguousFromPrevious:start>0 && selectionCharactersAdjacent(chars[start-1],chars[start]),
		})
		start=end
	}
	return segments
}

func selectionCharactersAdjacent(left,right selectionCharacter)bool{
	if left.row==right.row {
		return right.col==left.col+1 || right.col==left.col+2
	}
	return right.row==left.row+1 && right.col==0
}

func selectionExpandLeft(segments []selectionSegment,index int)int{
	if index<0 || index>=len(segments){return -1}
	current:=segments[index]
	if current.class==selectionWhitespace{return -1}
	if current.class==selectionPunctuation {
		if index==0{return -1}
		previous:=segments[index-1]
		if current.contiguousFromPrevious && selectionTextClass(previous.class){return index-1}
		return -1
	}
	if index<2{return -1}
	punctuation:=segments[index-1]
	word:=segments[index-2]
	if current.contiguousFromPrevious &&
		punctuation.contiguousFromPrevious &&
		punctuation.class==selectionPunctuation &&
		selectionTextClass(word.class) {return index-2}
	return -1
}

func selectionExpandRight(segments []selectionSegment,index int)int{
	if index<0 || index>=len(segments){return -1}
	current:=segments[index]
	if current.class==selectionWhitespace{return -1}
	if current.class==selectionPunctuation {
		next:=index+1
		if next<len(segments) && segments[next].contiguousFromPrevious && selectionTextClass(segments[next].class){
			return next
		}
		return -1
	}
	punctuationIndex:=index+1
	wordIndex:=index+2
	if wordIndex>=len(segments){return -1}
	punctuation:=segments[punctuationIndex]
	word:=segments[wordIndex]
	if punctuation.class==selectionPunctuation &&
		punctuation.contiguousFromPrevious &&
		word.contiguousFromPrevious &&
		selectionTextClass(word.class) {return wordIndex}
	return -1
}

func selectionTextClass(class selectionWordClass)bool{
	return class==selectionCJK || class==selectionAlphanumeric
}

func selectionClass(r rune)selectionWordClass{
	if unicode.IsSpace(r){return selectionWhitespace}
	if selectionIsCJK(r){return selectionCJK}
	if unicode.IsLetter(r)||unicode.IsDigit(r){return selectionAlphanumeric}
	return selectionPunctuation
}

func selectionIsCJK(r rune)bool{
	v:=uint32(r)
	return v>=0x1100&&v<=0x11ff ||
		v>=0x3040&&v<=0x30ff ||
		v>=0x3130&&v<=0x318f ||
		v>=0x3400&&v<=0x4dbf ||
		v>=0x4e00&&v<=0x9fff ||
		v>=0xac00&&v<=0xd7af ||
		v>=0xf900&&v<=0xfaff ||
		v>=0x20000&&v<=0x2fa1f
}

func utf8FirstRune(value string)(rune,int){
	for _,r:=range value{return r,len(string(r))}
	return ' ',1
}

type TerminalView struct {
	Theme      TerminalTheme
	FontSize   unit.Sp
	CellWidth  unit.Dp
	LineHeight unit.Dp
	FontFamily string
	Hyperlinks bool

	mu         sync.Mutex
	cache      map[int]preparedRow
	imageCache map[uint64]paint.ImageOp
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
		imageCache: make(map[uint64]paint.ImageOp),
	}
}

type TerminalViewCacheStats struct {
	PreparedRows int
	ImageTextures int
}

func (v *TerminalView) CacheStats() TerminalViewCacheStats {
	v.mu.Lock()
	defer v.mu.Unlock()
	return TerminalViewCacheStats{
		PreparedRows: len(v.cache),
		ImageTextures: len(v.imageCache),
	}
}

func (v *TerminalView) Layout(gtx layout.Context, th *material.Theme, snap govt.Snapshot, selection Selection, composition string) layout.Dimensions {
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
		if left,right,ok:=selectionColumnsForSnapshot(snap,selection,rowIndex);ok {
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
	liveImages:=make(map[uint64]struct{},len(snap.Images))
	for _,terminalImage:=range snap.Images {
		if terminalImage.PixelWidth<=0 || terminalImage.PixelHeight<=0 ||
			terminalImage.SourceWidth<=0 || terminalImage.SourceHeight<=0 ||
			len(terminalImage.RGBA)<terminalImage.PixelWidth*terminalImage.PixelHeight*4 {
			continue
		}
		liveImages[terminalImage.ID]=struct{}{}
		imageOp,ok:=v.imageCache[terminalImage.ID]
		if !ok {
			rgba:=&image.NRGBA{
				Pix:terminalImage.RGBA,
				Stride:terminalImage.PixelWidth*4,
				Rect:image.Rect(0,0,terminalImage.PixelWidth,terminalImage.PixelHeight),
			}
			imageOp=paint.NewImageOp(rgba)
			v.imageCache[terminalImage.ID]=imageOp
		}
		left:=terminalImage.Column*cellWidth
		top:=terminalImage.Row*lineHeight
		destWidth:=terminalImage.Width*cellWidth
		destHeight:=terminalImage.Height*lineHeight
		if destWidth<=0 || destHeight<=0 { continue }
		right:=left+destWidth
		bottom:=top+destHeight
		clipStack:=clip.Rect(image.Rect(left,top,right,bottom)).Push(gtx.Ops)
		scaleX:=float32(destWidth)/float32(terminalImage.SourceWidth)
		scaleY:=float32(destHeight)/float32(terminalImage.SourceHeight)
		imageX:=float32(left)-float32(terminalImage.SourceX)*scaleX
		imageY:=float32(top)-float32(terminalImage.SourceY)*scaleY
		transform:=op.Affine(f32.NewAffine2D(scaleX,0,imageX,0,scaleY,imageY)).Push(gtx.Ops)
		imageOp.Add(gtx.Ops)
		paint.PaintOp{}.Add(gtx.Ops)
		transform.Pop()
		clipStack.Pop()
	}
	for id:=range v.imageCache {
		if _,ok:=liveImages[id];!ok { delete(v.imageCache,id) }
	}
	v.mu.Unlock()

	if composition!="" && snap.CursorY>=0 && snap.CursorY<snap.Rows &&
		snap.CursorX>=0 && snap.CursorX<snap.Cols {
		x:=snap.CursorX*cellWidth
		y:=snap.CursorY*lineHeight
		columns:=utf8.RuneCountInString(composition)
		if columns<1{columns=1}
		spanWidth:=columns*cellWidth
		if maxWidth:=width-x;spanWidth>maxWidth{spanWidth=maxWidth}
		if spanWidth>0 {
			paint.FillShape(gtx.Ops,v.Theme.Background,clip.Rect(image.Rect(x,y,x+spanWidth,y+lineHeight)).Op())
			tr:=op.Offset(image.Pt(x,y)).Push(gtx.Ops)
			child:=gtx
			child.Constraints.Min=image.Point{}
			child.Constraints.Max=image.Pt(spanWidth,lineHeight)
			label:=material.Label(th,v.FontSize,composition)
			label.MaxLines=1
			label.Color=v.Theme.Foreground
			label.Font.Typeface=font.Typeface(v.FontFamily)
			label.Layout(child)
			tr.Pop()
			underlineY:=y+lineHeight-2
			paint.FillShape(gtx.Ops,v.Theme.Foreground,clip.Rect(image.Rect(x,underlineY,x+spanWidth,underlineY+1)).Op())
		}
	}

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


func selectionColumnsForSnapshot(snap govt.Snapshot,selection Selection,viewportRow int)(int,int,bool){
	row:=viewportRow
	if selection.Absolute { row=snap.YDisp+viewportRow }
	return selectionColumns(selection,row,snap.Cols)
}
