package goui

import (
	"bytes"
	"testing"

	"gioui.org/io/key"
)

func TestEncodeKeyApplicationCursorAndModifiers(t *testing.T){
	tests:=[]struct{
		name string
		ev key.Event
		app bool
		want []byte
	}{
		{"up-normal",key.Event{Name:key.NameUpArrow,State:key.Press},false,[]byte("\x1b[A")},
		{"up-app",key.Event{Name:key.NameUpArrow,State:key.Press},true,[]byte("\x1bOA")},
		{"ctrl-up",key.Event{Name:key.NameUpArrow,Modifiers:key.ModCtrl,State:key.Press},true,[]byte("\x1b[1;5A")},
		{"shift-tab",key.Event{Name:key.NameTab,Modifiers:key.ModShift,State:key.Press},false,[]byte("\x1b[Z")},
		{"f5",key.Event{Name:key.NameF5,State:key.Press},false,[]byte("\x1b[15~")},
		{"ctrl-c",key.Event{Name:key.Name("C"),Modifiers:key.ModCtrl,State:key.Press},false,[]byte{3}},
		{"alt-x",key.Event{Name:key.Name("X"),Modifiers:key.ModAlt,State:key.Press},false,[]byte{0x1b,'X'}},
	}
	for _,tc:=range tests{
		t.Run(tc.name,func(t *testing.T){
			got:=EncodeKey(tc.ev,tc.app)
			if !bytes.Equal(got,tc.want){t.Fatalf("got %q want %q",got,tc.want)}
		})
	}
}
