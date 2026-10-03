package xterm

import "testing"

func TestPrintableASCIIFastPathPreservesCombiningState(t *testing.T){
	term:=New(WithCols(20),WithRows(2))
	defer term.Dispose()
	_,_ = term.Write([]byte("Ae\xcc\x81B"))

	if got:=term.GetLine(0);got!="AéB"{
		t.Fatalf("line = %q, want %q",got,"AéB")
	}
	line:=term.Buffer().Lines.Get(term.Buffer().YBase)
	if line==nil{t.Fatal("missing first buffer line")}
	var cell CellData
	line.LoadCell(1,&cell)
	if got:=cell.GetChars();got!="é"{
		t.Fatalf("combined cell = %q",got)
	}
	if width:=cell.GetWidth();width!=1{
		t.Fatalf("combined cell width = %d, want 1",width)
	}
}

func TestPrintableASCIIFastPathDoesNotBypassCharsetTranslation(t *testing.T){
	term:=New(WithCols(20),WithRows(2))
	defer term.Dispose()
	_,_ = term.Write([]byte("\x1b(0q\x1b(B"))

	if got:=term.GetLine(0);got!="─"{
		t.Fatalf("DEC special graphics translation = %q, want %q",got,"─")
	}
}

func TestPrintableASCIIFastPathPreservesOSC8LinkAttributes(t *testing.T){
	term:=New(WithCols(20),WithRows(2))
	defer term.Dispose()
	_,_ = term.Write([]byte("\x1b]8;;https://example.com\x1b\\abc\x1b]8;;\x1b\\"))

	line:=term.Buffer().Lines.Get(term.Buffer().YBase)
	if line==nil{t.Fatal("missing first buffer line")}
	var cell CellData
	for col:=0;col<3;col++{
		line.LoadCell(col,&cell)
		if cell.Extended==nil || cell.Extended.URLID()==0{
			t.Fatalf("cell %d lost OSC8 link attributes",col)
		}
	}
}
