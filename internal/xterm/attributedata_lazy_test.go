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

func TestSGRResetPreservesOSC8AndKeepsPlainPathAllocationFree(t *testing.T) {
	var handler InputHandler
	original:=NewExtendedAttrs(uint32(UnderlineStyleDouble)<<26,17)
	attr:=AttributeData{
		Fg:FgFlagBold|FgFlagUnderline|AttrCMP16|3,
		Bg:BgFlagItalic|BgFlagHasExtended,
		Extended:original,
	}
	handler.processSGR0(&attr)
	if attr.Fg!=0 {
		t.Fatalf("SGR reset foreground = %#x",attr.Fg)
	}
	if attr.Extended==nil || attr.Extended==original {
		t.Fatalf("SGR reset did not preserve owned OSC8 state safely: %#v",attr.Extended)
	}
	if got:=attr.Extended.URLID();got!=17 {
		t.Fatalf("SGR reset URL ID = %d, want 17",got)
	}
	if got:=attr.Extended.UnderlineStyle();got!=UnderlineStyleDashed {
		t.Fatalf("OSC8 effective underline style = %v, want dashed",got)
	}
	if attr.Bg&BgFlagHasExtended==0 {
		t.Fatalf("SGR reset dropped HAS_EXTENDED while OSC8 link is active: %#x",attr.Bg)
	}
	if original.URLID()!=17 || original.UnderlineStyle()!=UnderlineStyleDashed {
		t.Fatal("SGR reset mutated the previously owned extended attrs")
	}

	plain:=AttributeData{}
	allocs:=testing.AllocsPerRun(1000,func(){
		plain.Fg=FgFlagBold|AttrCMP16|1
		plain.Bg=BgFlagDim
		plain.Extended=nil
		handler.processSGR0(&plain)
	})
	if allocs!=0 {
		t.Fatalf("plain SGR reset allocations = %.2f, want 0",allocs)
	}
	if plain.Fg!=0 || plain.Bg!=0 || plain.Extended!=nil {
		t.Fatalf("plain SGR reset = %#v",plain)
	}
}

func TestAttributeClonePreservesLazyAndOwnedExtendedState(t *testing.T) {
	defaults:=DefaultAttrData()
	plain:=defaults.Clone()
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
