package goui

import (
	"image/color"
	"testing"

	"github.com/SurTeam/Water/internal/govt"
)

func TestPreparedRowEmptyCellsPreserveBackgroundAndDecorations(t *testing.T) {
	v := NewTerminalView()
	row := govt.Row{Cells: []govt.Cell{
		{Width: 1},
		{Width: 1, BG: govt.Color{Mode: govt.ColorRGB, Value: 0x123456}},
		{Width: 1, Underline: true},
		{Width: 1, Strikethrough: true},
		{Width: 1, LinkURI: "https://example.test"},
		{Width: 1, Text: " "},
		{Width: 1, Text: "x"},
		{Width: 1},
		{Width: 1, Text: "y"},
	}}
	v.Hyperlinks = true
	p := v.prepareRow(row)
	if len(p.backgrounds) != 1 || p.backgrounds[0].startColumn != 1 || p.backgrounds[0].color != (color.NRGBA{R: 0x12, G: 0x34, B: 0x56, A: 255}) {
		t.Fatalf("lost blank background: %+v", p.backgrounds)
	}
	if len(p.text) != 5 {
		t.Fatalf("unexpected runs: %+v", p.text)
	}
	for i, column := range []int{2, 3, 4, 5, 8} {
		if p.text[i].startColumn != column {
			t.Fatalf("run %d moved: %+v", i, p.text[i])
		}
	}
	if !p.text[0].style.underline || !p.text[1].style.strikethrough || p.text[2].style.linkURI == "" || p.text[3].text != " x" || p.text[4].text != "y" {
		t.Fatalf("blank/text semantics lost: %+v", p.text)
	}
	v.Hyperlinks = false
	p = v.prepareRow(row)
	for _, run := range p.text {
		if run.startColumn == 4 {
			t.Fatal("disabled link produced empty glyph run")
		}
	}
}

func TestPreparedRowBlankGridHasNoTextRuns(t *testing.T) {
	e := govt.New(160, 3, 100)
	defer e.Close()
	for _, ligatures := range []bool{true, false} {
		v := NewTerminalView()
		v.Ligatures = ligatures
		for _, row := range e.FrameSnapshot().RowsData {
			if p := v.prepareRow(row); len(p.text) != 0 {
				t.Fatalf("blank grid generated text: %+v", p.text)
			}
		}
	}
}
