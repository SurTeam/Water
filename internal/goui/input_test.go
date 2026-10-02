package goui

import (
	"bytes"
	"image"
	"testing"
	"time"

	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"

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


func TestTerminalIMEPreeditDoesNotLeakIntoPTY(t *testing.T){
	var inputState TerminalInput
	var emitted [][]byte
	inputState.OnInput=func(data []byte){
		emitted=append(emitted,append([]byte(nil),data...))
	}

	if compositionActive(key.Range{Start:-1,End:-1}) {
		t.Fatal("(-1,-1) must mean no active composition")
	}
	if !compositionActive(key.Range{Start:0,End:0}) {
		t.Fatal("zero-length preedit range is still an active composition")
	}

	inputState.handleComposition(key.CompositionEvent{Start:0,End:0})
	inputState.handleEdit(key.EditEvent{Text:"拼"})
	if len(emitted)!=0 {
		t.Fatalf("preedit leaked into PTY: %q",emitted)
	}
	if inputState.pendingComposition!="拼" || !inputState.composing {
		t.Fatalf("unexpected preedit state: composing=%v pending=%q",inputState.composing,inputState.pendingComposition)
	}

	inputState.handleComposition(key.CompositionEvent{Start:-1,End:-1})
	if len(emitted)!=0 {
		t.Fatalf("ending composition emitted stale preedit: %q",emitted)
	}
	if inputState.pendingComposition!="" || inputState.composing {
		t.Fatalf("composition did not reset: composing=%v pending=%q",inputState.composing,inputState.pendingComposition)
	}

	inputState.handleEdit(key.EditEvent{Text:"中"})
	if len(emitted)!=1 || string(emitted[0])!="中" {
		t.Fatalf("commit = %q, want exactly one 中",emitted)
	}
}

func TestTerminalIMEPublishesCaretAndEmptySnippet(t *testing.T){
	var terminalInput TerminalInput
	var router input.Router
	var ops op.Ops
	size:=image.Pt(320,200)

	snap:=govt.Snapshot{Cols:40,Rows:10,CursorX:3,CursorY:2}
	gtx:=layout.Context{
		Ops:&ops,
		Source:router.Source(),
		Constraints:layout.Exact(size),
	}
	// Register the same FocusFilter and input ops as a real terminal frame.
	terminalInput.Process(gtx,snap,8,20)
	terminalInput.Add(gtx,size)
	router.Frame(&ops)

	// Focus an already-registered terminal tag, then run the next real frame.
	router.Source().Execute(key.FocusCmd{Tag:&terminalInput.tag})
	if !router.Source().Focused(&terminalInput.tag){
		t.Fatal("terminal input did not become focused")
	}
	ops.Reset()
	gtx.Ops=&ops
	gtx.Source=router.Source()
	terminalInput.Process(gtx,snap,8,20)
	terminalInput.Add(gtx,size)
	router.Frame(&ops)

	state:=router.EditorState()
	if state.Snippet.Text!="" || state.Snippet.Range!=(key.Range{Start:0,End:0}) {
		t.Fatalf("terminal IME snippet = %#v, want empty zero-range snippet",state.Snippet)
	}
	if state.Selection.Range!=(key.Range{Start:0,End:0}) {
		t.Fatalf("terminal IME selection = %#v",state.Selection.Range)
	}
	if state.Selection.Caret.Pos.X!=24 || state.Selection.Caret.Pos.Y!=58 {
		t.Fatalf("terminal IME caret = %#v, want (24,58)",state.Selection.Caret)
	}

	terminalInput.handleComposition(key.CompositionEvent{Start:0,End:1})
	terminalInput.handleEdit(key.EditEvent{Text:"拼音"})
	ops.Reset()
	gtx.Ops=&ops
	gtx.Source=router.Source()
	terminalInput.syncIME(gtx,snap,8,20)
	terminalInput.Add(gtx,size)
	router.Frame(&ops)
	if bounds:=router.EditorState().Selection.CompositionBounds; bounds.Empty() {
		t.Fatal("active terminal composition did not expose caret-local composition bounds")
	}
}
