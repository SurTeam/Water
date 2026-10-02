package goclient

import (
	"testing"

	"github.com/SurTeam/Water/internal/gobuild"
)

func TestMarshalParamsOmitsRustUnitVariantPayload(t *testing.T) {
	for _,value:=range []any{nil,map[string]any{},struct{}{}} {
		raw,err:=marshalParams(value)
		if err!=nil{t.Fatal(err)}
		if len(raw)!=0{t.Fatalf("marshalParams(%#v) = %q, want omitted params",value,raw)}
	}
	raw,err:=marshalParams(map[string]any{"terminal_id":"abc"})
	if err!=nil{t.Fatal(err)}
	if len(raw)==0{t.Fatal("non-empty params were omitted")}
}


func TestNewClientUsesSharedBuildVariant(t *testing.T) {
	old:=gobuild.Variant
	gobuild.Variant="release"
	defer func(){gobuild.Variant=old}()
	if got:=New("/tmp/water.sock").Build;got!="release"{
		t.Fatalf("client build variant = %q",got)
	}
}
