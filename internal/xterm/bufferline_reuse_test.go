package xterm

import "testing"

func TestCopyFromReusesSparseMaps(t *testing.T) {
	blank := NewBufferLine(80, nil, false)
	dst := NewBufferLine(80, nil, false)

	if blank.combined!=nil || blank.extendedAttrs!=nil {
		t.Fatalf("blank line eagerly allocated sparse maps: combined=%v extended=%v",blank.combined,blank.extendedAttrs)
	}
	dst.ensureCombined()
	dst.ensureExtendedAttrs()
	dst.combined[1] = "stale"
	dst.extendedAttrs[2] = &ExtendedAttrs{}

	if allocs := testing.AllocsPerRun(1000, func() {
		dst.CopyFrom(blank)
	}); allocs != 0 {
		t.Fatalf("blank-line CopyFrom allocated %.2f objects/run, want 0", allocs)
	}
	if len(dst.combined) != 0 || len(dst.extendedAttrs) != 0 {
		t.Fatalf("sparse maps were not cleared: combined=%d extended=%d", len(dst.combined), len(dst.extendedAttrs))
	}

	dst.ensureCombined()
	dst.ensureExtendedAttrs()
	dst.combined[0] = "ok"
	dst.extendedAttrs[0] = &ExtendedAttrs{}

	src := NewBufferLine(4, nil, false)
	src.ensureCombined()
	src.combined[1] = "ab"
	src.data[1*cellSize+cellContent] = ContentIsCombinedMask | (1 << ContentWidthShift)
	attrs := &ExtendedAttrs{}
	src.ensureExtendedAttrs()
	src.extendedAttrs[2] = attrs
	src.data[2*cellSize+cellBg] |= BgFlagHasExtended

	target := NewBufferLine(4, nil, false)
	target.CopyFrom(src)
	if target.combined[1] != "ab" {
		t.Fatalf("combined cell = %q, want ab", target.combined[1])
	}
	if target.extendedAttrs[2] != attrs {
		t.Fatal("extended attributes were not copied")
	}
}
