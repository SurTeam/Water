package goui

import (
	"testing"

	"github.com/SurTeam/Water/internal/govt"
)

func TestSelectedTextAcrossRowsAndWideCells(t *testing.T) {
	e:=govt.New(8,3,100)
	defer e.Close()
	e.Write([]byte("abc界x\r\ndef ghi\r\nlast"))
	snap:=e.Snapshot()

	text:=SelectedText(snap,Selection{
		AnchorCol:1,AnchorRow:0,
		FocusCol:4,FocusRow:1,
		Active:true,
	})
	if text!="bc界x\ndef g" {
		t.Fatalf("selected text = %q",text)
	}
}

func TestSelectionColumnsNormalizesReverseDrag(t *testing.T) {
	sel:=Selection{
		AnchorCol:5,AnchorRow:2,
		FocusCol:2,FocusRow:1,
		Active:true,
	}
	left,right,ok:=selectionColumns(sel,1,10)
	if !ok || left!=2 || right!=9 {
		t.Fatalf("row 1 = %d..%d ok=%v",left,right,ok)
	}
	left,right,ok=selectionColumns(sel,2,10)
	if !ok || left!=0 || right!=5 {
		t.Fatalf("row 2 = %d..%d ok=%v",left,right,ok)
	}
}

func TestInactiveSelectionCopiesNothing(t *testing.T) {
	snap:=govt.Snapshot{Cols:2,Rows:1,RowsData:[]govt.Row{{Cells:[]govt.Cell{{Text:"a",Width:1},{Text:"b",Width:1}}}}}
	if got:=SelectedText(snap,Selection{});got!="" {
		t.Fatalf("inactive selection copied %q",got)
	}
}
