package govt

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"fmt"
	xterm "github.com/SurTeam/Water/internal/xterm"
	"testing"
)

func TestAllPlaceholderDiacriticsJoinCells(t *testing.T) {
	u := xterm.NewUnicodeService()
	for _, r := range imageDiacritics {
		if u.Wcwidth(r) != 0 {
			t.Fatalf("U+%04X consumes a placeholder column", r)
		}
	}
}

func TestKittyUnpaddedCompressedRGBChunks(t *testing.T) {
	e := New(20, 4, 100)
	defer e.Close()
	var compressed bytes.Buffer
	w := zlib.NewWriter(&compressed)
	_, _ = w.Write([]byte{255, 0, 0, 0, 255, 0})
	_ = w.Close()
	encoded := base64.RawStdEncoding.EncodeToString(compressed.Bytes())
	e.Write([]byte("\x1b_Ga=T,q=2,f=24,o=z,m=1,s=2,v=1;" + encoded[:8] + "\x1b\\"))
	e.Write([]byte("\x1b_Ga=T,q=2;" + encoded[8:] + "\x1b\\"))
	if len(e.Snapshot().Images) != 1 {
		t.Fatal("unpadded stream was rejected")
	}
}

func TestGraphicsRemainInHistoryAfterScreenDeleteAndBufferSwitch(t *testing.T) {
	e := New(20, 4, 100)
	defer e.Close()
	e.Write([]byte("\x1b_Ga=T,q=2,i=42,f=100,c=2,r=3;" + tinyPNG(t) + "\x1b\\"))
	e.Write(bytes.Repeat([]byte("history\r\n"), 12))
	e.Write([]byte("\x1b_Ga=d,d=A\x1b\\"))
	e.Scroll(-100)
	s := e.Snapshot()
	if len(s.Images) != 1 || s.Images[0].Row != 0 || s.Images[0].Height != 3 {
		t.Fatalf("history image lost: %+v", s.Images)
	}
	e.Write([]byte("\x1b[?1049h"))
	if len(e.Snapshot().Images) != 0 {
		t.Fatal("normal buffer image leaked into alternate screen")
	}
	e.Write([]byte("\x1b[?1049l"))
	e.Scroll(-100)
	if len(e.Snapshot().Images) != 1 {
		t.Fatal("buffer switch lost history image")
	}
	e.Write([]byte("\x1b_Ga=d,d=R,y=4294967295\x1b\\"))
	if len(e.Snapshot().Images) != 0 || e.ImageBytes() != 0 {
		t.Fatal("clear-all retained history image")
	}
}

func TestTallImageSurvivesPartialHistoryTrim(t *testing.T) {
	e := New(20, 4, 4)
	defer e.Close()
	e.Write([]byte("\x1b_Ga=T,q=2,i=5,f=100,c=2,r=6;" + tinyPNG(t) + "\x1b\\"))
	e.Write([]byte("\r\n\r\n\r\n\r\n"))
	e.Scroll(-100)
	s := e.Snapshot()
	if len(s.Images) != 1 || s.Images[0].Row >= 0 {
		t.Fatalf("partially trimmed image lost: %+v", s.Images)
	}
	e.Write(bytes.Repeat([]byte("\r\n"), 20))
	e.Snapshot()
	if e.ImageBytes() != 0 {
		t.Fatal("expired image data retained")
	}
}

func TestHistoryMouseIsLocalEvenWhenApplicationTracksMouse(t *testing.T) {
	e := New(20, 4, 100)
	defer e.Close()
	e.Write(bytes.Repeat([]byte("history\r\n"), 10))
	e.Write([]byte("\x1b[?1000h\x1b[?1006h"))
	e.TakeResponses()
	e.Scroll(-2)
	if e.Mouse(MouseEvent{Col: 1, Row: 1, Button: MouseLeft, Action: MouseDown}) || len(e.TakeResponses()) != 0 {
		t.Fatal("history click sent to application")
	}
	e.ScrollToBottom()
	if !e.Mouse(MouseEvent{Col: 1, Row: 1, Button: MouseLeft, Action: MouseDown}) {
		t.Fatal("live application mouse tracking broken")
	}
}

func TestPlaceholderClipsWithoutRescalingInHistory(t *testing.T) {
	e := New(20, 2, 100)
	defer e.Close()
	id := uint32(42 | 2<<24)
	e.Write([]byte(fmt.Sprintf("\x1b_Ga=T,q=2,U=1,i=%d,f=100,c=2,r=3;", id) + tinyPNG(t) + "\x1b\\"))
	for row := 0; row < 3; row++ {
		e.Write([]byte(fmt.Sprintf("\x1b[38:2:0:0:42m%c%c%c%c%c%c%c%c\x1b[0m\r\n", terminalImagePlaceholder, imageDiacritics[row], imageDiacritics[0], imageDiacritics[2], terminalImagePlaceholder, imageDiacritics[row], imageDiacritics[1], imageDiacritics[2])))
	}
	e.Scroll(-1)
	s := e.Snapshot()
	if len(s.Images) != 2 {
		t.Fatalf("placeholder runs: %+v", s.Images)
	}
	for _, img := range s.Images {
		if img.Width != 2 || img.Height != 3 || img.Row != -1 || img.ClipHeight != 1 || img.ClipWidth != 2 {
			t.Fatalf("cropped image rescaled: %+v", img)
		}
	}
	if !s.RowsData[0].Cells[0].Invisible {
		t.Fatal("placeholder glyph should not draw")
	}
}

func TestKittyUploadRepliesAndQuietChunks(t *testing.T) {
	e := New(20, 4, 100)
	defer e.Close()
	data := tinyPNG(t)
	e.Write([]byte("\x1b_Ga=t,i=8,f=100,m=1;" + data[:8] + "\x1b\\"))
	if len(e.TakeResponses()) != 0 {
		t.Fatal("intermediate chunk replied")
	}
	e.Write([]byte("\x1b_Gm=0;" + data[8:] + "\x1b\\"))
	if got := string(bytes.Join(e.TakeResponses(), nil)); got != "\x1b_Gi=8;OK\x1b\\" {
		t.Fatalf("upload response %q", got)
	}
	e.Write([]byte("\x1b_Ga=p,i=8,q=1,C=1,c=2,r=2\x1b\\"))
	if len(e.TakeResponses()) != 0 || len(e.Snapshot().Images) != 1 {
		t.Fatal("quiet redisplay failed")
	}
	e.Write([]byte("\x1b_Ga=p,i=99\x1b\\"))
	if !bytes.Contains(bytes.Join(e.TakeResponses(), nil), []byte("ENOENT")) {
		t.Fatal("missing image error reply")
	}
}
