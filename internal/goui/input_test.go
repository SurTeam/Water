package goui

import (
	"bytes"
	"testing"
	"time"

	"gioui.org/io/key"

	"github.com/SurTeam/Water/internal/govt"
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


func TestHyperlinkClickModifierRules(t *testing.T){
	if !hyperlinkClickAllowed(false,0){
		t.Fatal("plain hyperlink click should be allowed when shortcut is optional")
	}
	if hyperlinkClickAllowed(false,key.ModShift){
		t.Fatal("shift must remain available for terminal selection")
	}
	if hyperlinkClickAllowed(true,0){
		t.Fatal("shortcut-required hyperlink activated without shortcut")
	}
	if !hyperlinkClickAllowed(true,key.ModShortcut){
		t.Fatal("platform shortcut should activate hyperlink")
	}
	if hyperlinkClickAllowed(true,key.ModShortcut|key.ModShift){
		t.Fatal("shift+shortcut should preserve selection override")
	}
}

func TestHyperlinkURIAt(t *testing.T){
	snap:=govt.Snapshot{
		Cols:2,Rows:1,
		RowsData:[]govt.Row{{Cells:[]govt.Cell{
			{Text:"x",Width:1,LinkURI:"https://example.com"},
			{Text:"y",Width:1},
		}}},
	}
	if got:=hyperlinkURIAt(snap,0,0);got!="https://example.com"{
		t.Fatalf("link URI = %q",got)
	}
	for _,point:=range [][2]int{{1,0},{2,0},{0,1},{-1,0}}{
		if got:=hyperlinkURIAt(snap,point[0],point[1]);got!=""{
			t.Fatalf("hyperlinkURIAt(%d,%d) = %q",point[0],point[1],got)
		}
	}
}


func TestTerminalClickCountTracksSameCellWithinWindow(t *testing.T){
	var input TerminalInput
	if got:=input.nextClickCount(100*time.Millisecond,2,3);got!=1{
		t.Fatalf("first click count = %d",got)
	}
	if got:=input.nextClickCount(300*time.Millisecond,2,3);got!=2{
		t.Fatalf("second click count = %d",got)
	}
	if got:=input.nextClickCount(450*time.Millisecond,2,3);got!=3{
		t.Fatalf("third click count = %d",got)
	}
	if got:=input.nextClickCount(500*time.Millisecond,3,3);got!=1{
		t.Fatalf("different cell did not reset click count: %d",got)
	}
	if got:=input.nextClickCount(1200*time.Millisecond,3,3);got!=1{
		t.Fatalf("expired click window did not reset count: %d",got)
	}
}


func TestSelectionAutoScrollDirection(t *testing.T){
	const height=200
	tests:=[]struct{
		y float32
		want int
	}{
		{0,-3},
		{23,-3},
		{24,0},
		{100,0},
		{176,0},
		{177,3},
		{199,3},
		{220,3},
	}
	for _,tc:=range tests{
		if got:=selectionAutoScrollDirection(tc.y,height);got!=tc.want{
			t.Fatalf("direction at y=%.1f = %d, want %d",tc.y,got,tc.want)
		}
	}
	if got:=selectionAutoScrollDirection(0,0);got!=0{
		t.Fatalf("zero-height direction = %d",got)
	}
}
