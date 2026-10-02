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


func TestMultiClickSelectionExpandsLikeRustTerminal(t *testing.T) {
	e:=govt.New(32,2,100)
	defer e.Close()
	e.Write([]byte("alpha-beta gamma"))
	snap:=e.Snapshot()

	word:=MultiClickSelection(snap,1,0,2)
	if got:=SelectedText(snap,word);got!="alpha" {
		t.Fatalf("double-click selected %q",got)
	}
	sameLevel:=MultiClickSelection(snap,1,0,3)
	if got:=SelectedText(snap,sameLevel);got!="alpha" {
		t.Fatalf("third click selected %q",got)
	}
	expanded:=MultiClickSelection(snap,1,0,4)
	if got:=SelectedText(snap,expanded);got!="alpha-beta" {
		t.Fatalf("fourth click selected %q",got)
	}
	punctuation:=MultiClickSelection(snap,5,0,4)
	if got:=SelectedText(snap,punctuation);got!="alpha-beta" {
		t.Fatalf("expanded punctuation selected %q",got)
	}
}

func TestMultiClickSelectionKeepsCJKAsItsOwnSegment(t *testing.T) {
	e:=govt.New(24,2,100)
	defer e.Close()
	e.Write([]byte("abc中文def"))
	snap:=e.Snapshot()

	selection:=MultiClickSelection(snap,3,0,2)
	if got:=SelectedText(snap,selection);got!="中文" {
		t.Fatalf("CJK segment selected %q",got)
	}
}

func TestWrappedRowsSelectAndCopyAsOneLogicalLine(t *testing.T) {
	e:=govt.New(5,3,100)
	defer e.Close()
	e.Write([]byte("abcdefgh"))
	snap:=e.Snapshot()
	if len(snap.RowsData)<2 || !snap.RowsData[1].Wrapped {
		t.Fatalf("expected second row to be wrapped: %#v",snap.RowsData)
	}

	selection:=MultiClickSelection(snap,1,1,2)
	if got:=SelectedText(snap,selection);got!="abcdefgh" {
		t.Fatalf("wrapped word selected %q",got)
	}

	full:=Selection{
		AnchorCol:0,AnchorRow:0,
		FocusCol:2,FocusRow:1,
		Active:true,
	}
	if got:=SelectedText(snap,full);got!="abcdefgh" {
		t.Fatalf("wrapped copy inserted a newline: %q",got)
	}
}
