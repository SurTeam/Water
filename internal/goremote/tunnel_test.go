package goremote

import "testing"

func TestValidateDestination(t *testing.T){
	for _,valid:=range []string{"build-box","alice@example.com"}{
		got,err:=ValidateDestination(valid);if err!=nil{t.Fatalf("%q: %v",valid,err)}
		if got!=valid{t.Fatalf("got %q want %q",got,valid)}
	}
	for _,invalid:=range []string{"","-oProxyCommand=bad","host name","host\ncommand"}{
		if _,err:=ValidateDestination(invalid);err==nil{t.Fatalf("%q should fail",invalid)}
	}
}

func TestPathsAreStableAndDestinationScoped(t *testing.T){
	c1,r1,err:=DebugPaths("alpha");if err!=nil{t.Fatal(err)}
	c2,r2,err:=DebugPaths("alpha");if err!=nil{t.Fatal(err)}
	if c1!=c2||r1!=r2{t.Fatal("paths are not stable")}
	c3,r3,err:=DebugPaths("beta");if err!=nil{t.Fatal(err)}
	if c1==c3||r1==r3{t.Fatal("destinations share paths")}
}

func TestShellQuote(t *testing.T){
	if got:=shellQuote("a'b");got!="'a'\\''b'"{t.Fatalf("unexpected quote %q",got)}
}
