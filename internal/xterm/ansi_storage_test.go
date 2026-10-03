package xterm

import "testing"

func TestSingleUnderlineAndResetKeepPackedStorage(t *testing.T) {
	var handler InputHandler
	attr := AttributeData{Fg: AttrCMRGB | 0x123456}
	allocations := testing.AllocsPerRun(100, func() {
		handler.processUnderline(1, &attr)
		if attr.GetUnderlineStyle() != UnderlineStyleSingle || attr.Extended != nil || attr.HasExtendedAttrs() != 0 {
			t.Fatalf("single underline materialized side storage: %#v", attr)
		}
		handler.processUnderline(0, &attr)
		handler.processSGR0(&attr)
	})
	if allocations != 0 {
		t.Fatalf("ordinary underline/reset allocated %g objects", allocations)
	}
}

func TestUnderlineChangesPreserveColorAndPreviousCells(t *testing.T) {
	var handler InputHandler
	extended := NewExtendedAttrs(0, 0)
	extended.SetUnderlineColor(AttrCMRGB | 0x123456)
	extended.SetUnderlineStyle(UnderlineStyleCurly)
	attr := AttributeData{Fg: FgFlagUnderline, Bg: BgFlagHasExtended, Extended: extended}
	handler.processUnderline(1, &attr)
	if attr.Extended == extended || attr.GetUnderlineStyle() != UnderlineStyleSingle || attr.GetUnderlineColor() != 0x123456 {
		t.Fatalf("underline change lost color or aliased prior cell: %#v", attr)
	}
	handler.processUnderline(0, &attr)
	handler.processUnderline(1, &attr)
	if attr.GetUnderlineColor() != 0x123456 {
		t.Fatal("disabling underline lost its color for a later re-enable")
	}
	handler.processSGR0(&attr)
	if attr.Extended != nil || attr.GetUnderlineStyle() != UnderlineStyleNone {
		t.Fatalf("reset retained visual side storage: %#v", attr)
	}
	if extended.UnderlineStyle() != UnderlineStyleCurly || extended.UnderlineColor() != AttrCMRGB|0x123456 {
		t.Fatal("SGR changes mutated previously emitted cells")
	}
}

func TestPackedEraseClearsOnlyOverwrittenMetadata(t *testing.T) {
	line := NewBufferLine(8, nil, false)
	combined := CellDataFromCharData(NewCharData(0, "e\u0301", 1, 0))
	line.SetCell(1, combined)
	line.SetCell(6, combined)
	linked := CellDataFromCharData(NewCharData(0, "X", 1, 'X'))
	linked.Bg |= BgFlagHasExtended
	linked.Extended = NewExtendedAttrs(0, 17)
	line.SetCell(2, linked)
	line.SetCell(7, linked)
	fill := CellDataFromCharData(NewCharData(0, " ", 1, ' '))
	fill.Fg = AttrCMP256 | 42
	line.ReplaceCells(1, 5, fill, false)
	for col := 1; col < 5; col++ {
		var cell CellData
		line.LoadCell(col, &cell)
		if cell.Content != fill.Content || cell.Fg != fill.Fg || cell.Bg != fill.Bg || cell.Extended != nil || cell.CombinedData != "" {
			t.Fatalf("erased column %d: %#v", col, cell)
		}
	}
	if _, ok := line.combined[1]; ok {
		t.Fatal("erased combined metadata retained")
	}
	if _, ok := line.extendedAttrs[2]; ok {
		t.Fatal("erased hyperlink metadata retained")
	}
	if line.combined[6] != "e\u0301" || line.extendedAttrs[7].URLID() != 17 {
		t.Fatal("erase modified metadata outside its range")
	}
}
