package xterm

import "testing"

func TestDefaultAttributeDataKeepsExtendedAttrsLazy(t *testing.T) {
	attr:=DefaultAttrData()
	if attr.Extended!=nil {
		t.Fatalf("default extended attrs were eagerly allocated: %#v",attr.Extended)
	}
	if got:=attr.GetUnderlineStyle();got!=UnderlineStyleNone {
		t.Fatalf("default underline style = %v",got)
	}
	if got:=attr.GetUnderlineColorMode();got!=0 {
		t.Fatalf("default underline color mode = %d",got)
	}
	attr.UpdateExtended()
	if attr.Extended!=nil {
		t.Fatal("read-only extended attr queries materialized heap state")
	}
}

func TestSGRResetDropsExtendedAttrsWithoutAllocation(t *testing.T) {
	var handler InputHandler
	attr:=AttributeData{
		Fg:FgFlagBold|FgFlagUnderline|AttrCMP16|3,
		Bg:BgFlagItalic|BgFlagHasExtended,
		Extended:NewExtendedAttrs(uint32(UnderlineStyleDouble)<<26,17),
	}
	handler.processSGR0(&attr)
	if attr.Fg!=0 || attr.Bg!=0 || attr.Extended!=nil {
		t.Fatalf("SGR reset = %#v",attr)
	}

	allocs:=testing.AllocsPerRun(1000,func(){
		attr.Fg=FgFlagBold|AttrCMP16|1
		attr.Bg=BgFlagDim
		attr.Extended=nil
		handler.processSGR0(&attr)
	})
	if allocs!=0 {
		t.Fatalf("SGR reset allocations = %.2f, want 0",allocs)
	}
}

func TestAttributeClonePreservesLazyAndOwnedExtendedState(t *testing.T) {
	plain:=DefaultAttrData().Clone()
	if plain.Extended!=nil {
		t.Fatal("cloning default attrs materialized extended state")
	}

	source:=AttributeData{Bg:BgFlagHasExtended,Extended:NewExtendedAttrs(123,9)}
	clone:=source.Clone()
	if clone.Extended==nil || clone.Extended==source.Extended {
		t.Fatal("non-empty extended attrs were not deeply cloned")
	}
	clone.Extended.SetURLID(99)
	if source.Extended.URLID()!=9 {
		t.Fatalf("clone mutation aliased source: source URL=%d",source.Extended.URLID())
	}
}
