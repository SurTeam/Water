package govt

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
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
