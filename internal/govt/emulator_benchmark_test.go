package govt

import (
	"bytes"
	"testing"
)

func ansiFixture(size int)[]byte{
	unit:=[]byte("\x1b[38;2;120;200;255mwater\x1b[0m \x1b[1mbenchmark\x1b[0m 界\r\n")
	if size<=len(unit){return append([]byte(nil),unit[:size]...)}
	out:=make([]byte,0,size)
	for len(out)<size{
		n:=size-len(out)
		if n>len(unit){n=len(unit)}
		out=append(out,unit[:n]...)
	}
	return out
}

func BenchmarkWriteANSI64K(b *testing.B){
	data:=ansiFixture(64*1024)
	e:=New(200,60,10000);defer e.Close()
	b.ReportAllocs();b.SetBytes(int64(len(data)))
	for i:=0;i<b.N;i++{e.Write(data)}
}

func BenchmarkSnapshot80x24(b *testing.B){
	e:=New(80,24,10000);defer e.Close()
	e.Write(ansiFixture(256*1024))
	b.ReportAllocs()
	for i:=0;i<b.N;i++{
		s:=e.Snapshot()
		if len(s.RowsData)!=24{b.Fatal("bad snapshot")}
	}
}

func BenchmarkSnapshot200x60(b *testing.B){
	e:=New(200,60,10000);defer e.Close()
	e.Write(ansiFixture(1024*1024))
	b.ReportAllocs()
	for i:=0;i<b.N;i++{
		s:=e.Snapshot()
		if len(s.RowsData)!=60{b.Fatal("bad snapshot")}
	}
}

func BenchmarkWriteAndSnapshotVisibleCadence(b *testing.B){
	data:=bytes.Repeat([]byte("water benchmark output\r\n"),128)
	e:=New(120,40,10000);defer e.Close()
	b.ReportAllocs();b.SetBytes(int64(len(data)))
	for i:=0;i<b.N;i++{
		e.Write(data)
		_ = e.Snapshot()
	}
}
