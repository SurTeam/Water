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


func TestPlainASCIIBulkPathMatchesParserPath(t *testing.T){
	fast:=New(WithCols(8),WithRows(3),WithScrollback(16))
	slow:=New(WithCols(8),WithRows(3),WithScrollback(16),WithScreenReaderMode(true))
	defer fast.Dispose()
	defer slow.Dispose()

	payload:=[]byte("12345678AB\r\ncdEFGH\nijklmnopQRST\r\nuv")
	_,_=fast.Write(payload)
	_,_=slow.Write(payload)

	if got,want:=fast.String(),slow.String();got!=want{
		t.Fatalf("viewport mismatch\nfast=%q\nslow=%q",got,want)
	}
	if fast.CursorX()!=slow.CursorX() || fast.CursorY()!=slow.CursorY(){
		t.Fatalf("cursor fast=(%d,%d) slow=(%d,%d)",
			fast.CursorX(),fast.CursorY(),slow.CursorX(),slow.CursorY())
	}
	fb,sb:=fast.Buffer(),slow.Buffer()
	if fb.YBase!=sb.YBase || fb.YDisp!=sb.YDisp || fb.Lines.Length()!=sb.Lines.Length(){
		t.Fatalf("buffer geometry fast=(base=%d disp=%d lines=%d) slow=(base=%d disp=%d lines=%d)",
			fb.YBase,fb.YDisp,fb.Lines.Length(),sb.YBase,sb.YDisp,sb.Lines.Length())
	}
	for row:=0;row<fb.Lines.Length();row++{
		fl,sl:=fb.Lines.Get(row),sb.Lines.Get(row)
		if fl==nil || sl==nil{continue}
		if fl.TranslateToString(false,0,-1)!=sl.TranslateToString(false,0,-1) || fl.IsWrapped!=sl.IsWrapped{
			t.Fatalf("row %d differs: fast=%q wrapped=%v slow=%q wrapped=%v",
				row,fl.TranslateToString(false,0,-1),fl.IsWrapped,
				sl.TranslateToString(false,0,-1),sl.IsWrapped)
		}
	}
}

func TestPlainASCIIBulkPathPreservesBasicSGRAttributes(t *testing.T){
	fast:=New(WithCols(20),WithRows(2))
	slow:=New(WithCols(20),WithRows(2),WithScreenReaderMode(true))
	defer fast.Dispose()
	defer slow.Dispose()

	for _,term:=range []*Terminal{fast,slow}{
		_,_=term.Write([]byte("\x1b[1;31m"))
	}
	payload:=[]byte("styled-ascii")
	_,_=fast.Write(payload)
	_,_=slow.Write(payload)

	fl:=fast.Buffer().Lines.Get(fast.Buffer().YBase)
	sl:=slow.Buffer().Lines.Get(slow.Buffer().YBase)
	if fl==nil || sl==nil{t.Fatal("missing styled row")}
	var fc,sc CellData
	for col:=range payload{
		fl.LoadCell(col,&fc)
		sl.LoadCell(col,&sc)
		if fc.Content!=sc.Content || fc.Fg!=sc.Fg || fc.Bg!=sc.Bg{
			t.Fatalf("cell %d attrs differ: fast=%#v slow=%#v",col,fc,sc)
		}
	}
}

func TestPlainASCIIBulkPathFallsBackForExtendedAttrs(t *testing.T){
	term:=New(WithCols(20),WithRows(2))
	defer term.Dispose()
	_,_=term.Write([]byte("\x1b]8;;https://example.com\x1b\\"))
	if term.inputHandler.tryParsePlainASCII([]byte("linked")){
		t.Fatal("OSC8 extended attributes incorrectly entered bulk ASCII path")
	}
}
