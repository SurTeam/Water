package goprotocol

import (
	"bytes"
	"io"
	"testing"

	"github.com/google/uuid"
)

func BenchmarkWriteTerminal64K(b *testing.B){
	id:=uuid.MustParse("12345678-1234-4234-8234-123456789abc")
	data:=bytes.Repeat([]byte("0123456789abcdef"),4096)
	ev:=TerminalEvent{Kind:OutputEvent,Seq:1,Size:TerminalSize{Columns:200,Lines:60},Data:data}
	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	for i:=0;i<b.N;i++{
		var dst bytes.Buffer
		dst.Grow(len(data)+64)
		if err:=WriteTerminal(&dst,id,ev);err!=nil{b.Fatal(err)}
	}
}

func BenchmarkReadTerminal64K(b *testing.B){
	id:=uuid.MustParse("12345678-1234-4234-8234-123456789abc")
	data:=bytes.Repeat([]byte("0123456789abcdef"),4096)
	var encoded bytes.Buffer
	if err:=WriteTerminal(&encoded,id,TerminalEvent{Kind:OutputEvent,Seq:1,Size:TerminalSize{Columns:200,Lines:60},Data:data});err!=nil{b.Fatal(err)}
	frame:=append([]byte(nil),encoded.Bytes()...)
	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	for i:=0;i<b.N;i++{
		got,err:=ReadFrame(bytes.NewReader(frame));if err!=nil{b.Fatal(err)}
		if got.Terminal==nil||len(got.Terminal.Data)!=len(data){b.Fatal("bad frame")}
	}
}

func BenchmarkReadTerminalStream64K(b *testing.B){
	id:=uuid.MustParse("12345678-1234-4234-8234-123456789abc")
	data:=bytes.Repeat([]byte("0123456789abcdef"),4096)
	var encoded bytes.Buffer
	if err:=WriteTerminal(&encoded,id,TerminalEvent{Kind:OutputEvent,Seq:1,Size:TerminalSize{Columns:200,Lines:60},Data:data});err!=nil{b.Fatal(err)}
	frame:=append([]byte(nil),encoded.Bytes()...)
	stream:=bytes.Repeat(frame,b.N)
	reader:=bytes.NewReader(stream)
	b.ReportAllocs();b.SetBytes(int64(len(data)));b.ResetTimer()
	for i:=0;i<b.N;i++{
		if _,err:=ReadFrame(reader);err!=nil&&err!=io.EOF{b.Fatal(err)}
	}
}
