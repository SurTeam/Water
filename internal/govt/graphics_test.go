package govt

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"runtime"
	"testing"
)

func tinyPNG(t *testing.T) string {
	t.Helper()
	img:=image.NewRGBA(image.Rect(0,0,2,1))
	img.SetRGBA(0,0,color.RGBA{R:255,A:255})
	img.SetRGBA(1,0,color.RGBA{G:255,A:255})
	var buf bytes.Buffer
	if err:=png.Encode(&buf,img);err!=nil{t.Fatal(err)}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func TestGraphicsParserSurvivesSplitKittySequence(t *testing.T) {
	var parser graphicsParser
	if events:=parser.feed([]byte("prefix\x1b_Ga=T;"));len(events)!=0{
		t.Fatalf("unexpected early events: %#v",events)
	}
	events:=parser.feed([]byte("data\x1b\\suffix"))
	if len(events)!=1 || events[0].kind!=graphicsKitty || string(events[0].payload)!="a=T;data"{
		t.Fatalf("unexpected events: %#v",events)
	}
	if events[0].endOffset!=len("data\x1b\\"){
		t.Fatalf("end offset=%d",events[0].endOffset)
	}
}

func TestKittyQueryIsLiveOnly(t *testing.T) {
	e:=New(80,24,100)
	defer e.Close()
	query:=[]byte("\x1b_Gi=31,a=q\x1b\\")

	e.WriteReplay(query)
	if got:=e.TakeResponses();len(got)!=0{
		t.Fatalf("replay query produced response %q",bytes.Join(got,nil))
	}
	e.Write(query)
	got:=bytes.Join(e.TakeResponses(),nil)
	if !bytes.Contains(got,[]byte("\x1b_Gi=31;OK\x1b\\")){
		t.Fatalf("missing Kitty query response: %q",got)
	}
}

func TestOversizedEncodedImageIsRejectedBeforeRasterAllocation(t *testing.T) {
	img:=image.NewRGBA(image.Rect(0,0,1,1))
	var buf bytes.Buffer
	if err:=png.Encode(&buf,img);err!=nil{t.Fatal(err)}
	raw:=buf.Bytes()
	if string(raw[12:16])!="IHDR"{t.Fatalf("png layout changed: %q",raw[12:16])}
	binary.BigEndian.PutUint32(raw[16:20],4096)
	binary.BigEndian.PutUint32(raw[20:24],4096)
	binary.BigEndian.PutUint32(raw[29:33],crc32.ChecksumIEEE(raw[12:29]))

	cfg,_,err:=image.DecodeConfig(bytes.NewReader(raw))
	if err!=nil || cfg.Width!=4096 || cfg.Height!=4096{
		t.Fatalf("crafted header was not readable: %v %+v",err,cfg)
	}
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	decoded:=decodeEncodedImage(raw)
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	if decoded!=nil{t.Fatal("oversized PNG was decoded")}
	if grew:=after.TotalAlloc-before.TotalAlloc; grew>8<<20{
		t.Fatalf("decode allocated %d bytes; header check did not run before the raster",grew)
	}

	e:=New(20,6,100)
	defer e.Close()
	e.Write([]byte("\x1b_Ga=T,f=100;"+base64.StdEncoding.EncodeToString(raw)+"\x1b\\"))
	if len(e.Snapshot().Images)!=0{t.Fatal("oversized PNG became a terminal image")}
}

func TestKittyPNGProducesImagePlacement(t *testing.T) {
	e:=New(20,6,100)
	defer e.Close()
	e.SetCellSize(8,16)
	seq:="\x1b_Ga=T,i=7,f=100;"+tinyPNG(t)+"\x1b\\"
	e.Write([]byte(seq))

	s:=e.Snapshot()
	if len(s.Images)!=1{
		t.Fatalf("images=%d snapshot=%#v",len(s.Images),s.Images)
	}
	img:=s.Images[0]
	if img.PixelWidth!=2 || img.PixelHeight!=1 || img.Width!=1 || img.Height!=1{
		t.Fatalf("unexpected image geometry: %#v",img)
	}
	if len(img.RGBA)!=8 || img.RGBA[0]!=255 || img.RGBA[5]!=255{
		t.Fatalf("unexpected rgba: %v",img.RGBA)
	}
}

func TestItermInlinePNGProducesPlacement(t *testing.T) {
	e:=New(20,6,100)
	defer e.Close()
	seq:="\x1b]1337;File=inline=1;width=2;height=1:"+tinyPNG(t)+"\x07"
	e.Write([]byte(seq))
	s:=e.Snapshot()
	if len(s.Images)!=1{
		t.Fatalf("images=%d",len(s.Images))
	}
	if s.Images[0].Width!=2 || s.Images[0].Height!=1{
		t.Fatalf("unexpected placement: %#v",s.Images[0])
	}
}

func TestSixelDecodesPalettePixel(t *testing.T) {
	e:=New(20,6,100)
	defer e.Close()
	e.Write([]byte("\x1bPq#1;2;100;0;0~\x1b\\"))
	s:=e.Snapshot()
	if len(s.Images)!=1{
		t.Fatalf("images=%d",len(s.Images))
	}
	img:=s.Images[0]
	if img.PixelWidth!=1 || img.PixelHeight!=6{
		t.Fatalf("unexpected sixel geometry: %#v",img)
	}
	if len(img.RGBA)<4 || img.RGBA[0]!=255 || img.RGBA[1]!=0 || img.RGBA[2]!=0 || img.RGBA[3]!=255{
		t.Fatalf("unexpected sixel pixel: %v",img.RGBA[:4])
	}
}


func TestKittyPlacementAdvancesCursorBeforeFollowingText(t *testing.T) {
	e:=New(20,6,100)
	defer e.Close()
	e.SetCellSize(8,16)
	seq:="A\x1b_Ga=T,i=9,f=100,c=1,r=2;"+tinyPNG(t)+"\x1b\\B"
	e.Write([]byte(seq))
	s:=e.Snapshot()

	if len(s.Images)!=1 {
		t.Fatalf("images=%d",len(s.Images))
	}
	// A is on row 0. A two-row image placement advances the cursor so the
	// trailing B from the same PTY chunk is parsed after that movement.
	foundB:=false
	for rowIndex,row:=range s.RowsData {
		for col,cell:=range row.Cells {
			if cell.Text=="B" {
				foundB=true
				if rowIndex<2 {
					t.Fatalf("B remained before graphics cursor advance at row=%d col=%d",rowIndex,col)
				}
			}
		}
	}
	if !foundB { t.Fatal("trailing text missing") }
}


func TestCellPixelQueryUsesConfiguredGeometryAndIsLiveOnly(t *testing.T) {
	e:=New(80,24,100)
	defer e.Close()
	e.SetCellSize(11,23)

	e.WriteReplay([]byte("\x1b[16t"))
	if got:=e.TakeResponses();len(got)!=0 {
		t.Fatalf("replay cell query produced response %q",bytes.Join(got,nil))
	}
	e.Write([]byte("\x1b[1"))
	e.Write([]byte("6t"))
	got:=bytes.Join(e.TakeResponses(),nil)
	if !bytes.Contains(got,[]byte("\x1b[6;23;11t")) {
		t.Fatalf("unexpected cell query response %q",got)
	}
}

func TestAltScreenEraseRemovesImagePlacement(t *testing.T) {
	e:=New(20,6,100)
	defer e.Close()
	e.Write([]byte("\x1b[?1049h"))
	e.Write([]byte("\x1b_Ga=T,i=12,f=100;"+tinyPNG(t)+"\x1b\\"))
	if got:=len(e.Snapshot().Images);got!=1 {
		t.Fatalf("image count before erase=%d",got)
	}
	e.Write([]byte("\x1b[2J"))
	if got:=len(e.Snapshot().Images);got!=0 {
		t.Fatalf("image count after alt-screen erase=%d",got)
	}
}


func TestGraphicsParserDropsPlainTextCarry(t *testing.T) {
	var parser graphicsParser
	if events:=parser.feed(bytes.Repeat([]byte("plain-water-output\n"),4096));len(events)!=0{
		t.Fatalf("plain text produced graphics events: %#v",events)
	}
	if len(parser.buffer)!=0{
		t.Fatalf("plain text retained %d graphics carry bytes",len(parser.buffer))
	}
}

func TestGraphicsParserPreservesSplitIntroducersOnly(t *testing.T) {
	var parser graphicsParser
	if events:=parser.feed([]byte("plain\x1b"));len(events)!=0{
		t.Fatalf("split ESC produced early events: %#v",events)
	}
	if got:=string(parser.buffer);got!="\x1b"{
		t.Fatalf("ESC carry = %q",got)
	}
	if events:=parser.feed([]byte("_"));len(events)!=0{
		t.Fatalf("split Kitty prefix produced early events: %#v",events)
	}
	if got:=string(parser.buffer);got!="\x1b_"{
		t.Fatalf("Kitty prefix carry = %q",got)
	}
	events:=parser.feed([]byte("Ga=T;abc\x1b\\"))
	if len(events)!=1 || events[0].kind!=graphicsKitty || string(events[0].payload)!="a=T;abc"{
		t.Fatalf("split Kitty event = %#v",events)
	}
}

func TestGraphicsParserDoesNotTreatSplitUTF8ContinuationAsC1(t *testing.T) {
	var parser graphicsParser
	// U+009F encodes as C2 9F. Split exactly between those bytes, then put a
	// literal G after it; without UTF-8 carry this can be mistaken for raw
	// C1 APC (9F) + Kitty selector G.
	if events:=parser.feed([]byte{0xc2});len(events)!=0{
		t.Fatalf("UTF-8 lead produced graphics event: %#v",events)
	}
	if len(parser.buffer)!=1 || parser.buffer[0]!=0xc2{
		t.Fatalf("UTF-8 lead carry = %x",parser.buffer)
	}
	if events:=parser.feed([]byte{0x9f,'G','x'});len(events)!=0{
		t.Fatalf("UTF-8 continuation misdetected as C1 Kitty: %#v",events)
	}
	if len(parser.buffer)!=0{
		t.Fatalf("completed UTF-8 text retained carry: %x",parser.buffer)
	}
}

func TestGraphicsParserSupportsSplitRawC1Kitty(t *testing.T) {
	var parser graphicsParser
	if events:=parser.feed([]byte{0x9f});len(events)!=0{
		t.Fatalf("raw C1 APC produced early event: %#v",events)
	}
	events:=parser.feed(append([]byte("Ga=T;abc"),0x9c))
	if len(events)!=1 || events[0].kind!=graphicsKitty || string(events[0].payload)!="a=T;abc"{
		t.Fatalf("raw C1 Kitty event = %#v",events)
	}
}
