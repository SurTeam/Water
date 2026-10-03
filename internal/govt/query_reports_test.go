package govt

import (
	"bytes"
	"strings"
	"sync"
	"testing"
)

func TestWindowQueriesUseNativeMetricsAtEverySplit(t *testing.T) {
	input := "\x1b[11t\x1b[13t\x1b[13;2t\x1b[14t\x1b[14;2t\x1b[15t\x1b[16t\x1b[18t\x1b[19t"
	want := "\x1b[1t\x1b[3;65526;20t\x1b[3;30;60t\x1b[4;552;880t\x1b[4;700;1000t\x1b[5;1200;1920t\x1b[6;23;11t\x1b[8;24;80t\x1b[9;52;174t"
	for split := 0; split <= len(input); split++ {
		e := New(80, 24, 100)
		e.SetCellSize(11, 23)
		e.SetWindowMetrics(WindowMetrics{Width: 1000, Height: 700, ScreenWidth: 1920, ScreenHeight: 1200, X: -10, Y: 20, TextX: 30, TextY: 60})
		e.Write([]byte(input[:split]))
		e.Write([]byte(input[split:]))
		if got := string(bytes.Join(e.TakeResponses(), nil)); got != want {
			t.Fatalf("split %d: %q", split, got)
		}
		e.Resize(93, 31)
		e.Write([]byte("\x1b[18t"))
		if got := string(bytes.Join(e.TakeResponses(), nil)); got != "\x1b[8;31;93t" {
			t.Fatal("stale rows/columns", got)
		}
		e.WriteReplay([]byte(input))
		if len(e.TakeResponses()) != 0 {
			t.Fatal("replay replied")
		}
		e.Close()
	}
}

func TestThemeChangesConcurrentWithTerminalParsing(t *testing.T) {
	e := New(20, 3, 10)
	defer e.Close()
	e.SetDefaultColors([259]uint32{})
	e.Write([]byte("\x1b[?2031h"))
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		for i := 0; i < 100; i++ {
			var colors [259]uint32
			colors[257] = uint32(i%2) * 0xffffff
			e.SetDefaultColors(colors)
		}
	}()
	go func() {
		defer workers.Done()
		for i := 0; i < 100; i++ {
			e.Write([]byte("\x1b]11;?\x07text\r\n"))
			e.WriteReplay([]byte("\x1b[?996n"))
		}
	}()
	workers.Wait()
	if len(e.TakeResponses()) == 0 {
		t.Fatal("missing replies")
	}
}

func TestColorSchemeQueriesAndUpdates(t *testing.T) {
	e := New(20, 3, 10)
	defer e.Close()
	var colors [259]uint32
	colors[257] = 0xffffff
	e.SetDefaultColors(colors)
	e.Write([]byte("\x1b[?996n"))
	if got := string(bytes.Join(e.TakeResponses(), nil)); got != "\x1b[?997;2n" {
		t.Fatal(got)
	}
	e.Write([]byte("\x1b[?2031h"))
	colors[257] = 0
	e.SetDefaultColors(colors)
	if got := string(bytes.Join(e.TakeResponses(), nil)); got != "\x1b[?997;1n" {
		t.Fatal(got)
	}
	e.Write([]byte("\x1b[?2031l"))
	colors[257] = 0xffffff
	e.SetDefaultColors(colors)
	if len(e.TakeResponses()) != 0 {
		t.Fatal("disabled mode sent update")
	}
}

func TestOSCDirectoryClipboardAndTitleQueries(t *testing.T) {
	e := New(20, 3, 10)
	defer e.Close()
	input := "\x1b]7;file://localhost/tmp/space%20dir\x07\x1b]52;c;aGVsbG8=\x1b\\\x1b]0;测试标题\x07\x1b[20t\x1b[21t"
	for _, b := range []byte(input) {
		e.Write([]byte{b})
	}
	if e.Snapshot().WorkingDirectoryURI != "file://localhost/tmp/space%20dir" {
		t.Fatal("directory not retained")
	}
	if data, ok := e.TakeClipboardWrite(); !ok || string(data) != "hello" {
		t.Fatal("clipboard write", string(data), ok)
	}
	if got := string(bytes.Join(e.TakeResponses(), nil)); got != "\x1b]L测试标题\x1b\\\x1b]l测试标题\x1b\\" {
		t.Fatal(got)
	}
	e.WriteReplay([]byte("\x1b]52;c;aGVsbG8=\x07"))
	if _, ok := e.TakeClipboardWrite(); ok {
		t.Fatal("replay changed clipboard")
	}
	e.Write([]byte("\x1b]52;c;!invalid!\x07"))
	if _, ok := e.TakeClipboardWrite(); ok {
		t.Fatal("invalid base64 changed clipboard")
	}
	e.Write([]byte("\x1b]52;c;\x07"))
	if data, ok := e.TakeClipboardWrite(); !ok || len(data) != 0 {
		t.Fatal("clipboard clear")
	}
	e.Write([]byte("\x1b]52;c;?\x07"))
	if got := string(bytes.Join(e.TakeResponses(), nil)); got != "\x1b]52;c;\x1b\\" {
		t.Fatal("clipboard query", got)
	}
}

func TestCursorStyleAndCapabilityReports(t *testing.T) {
	e := New(20, 10, 10)
	defer e.Close()
	e.Write([]byte("\x1b[3;8r\x1b[?6h\x1b[2;4H\x1b[6n\x1b[?6n"))
	if got := string(bytes.Join(e.TakeResponses(), nil)); got != "\x1b[2;4R\x1b[?2;4;1R" {
		t.Fatal("origin-relative cursor", got)
	}
	e.Write([]byte("\x1b[1;3;38;2;1;2;3;48;5;42m\x1bP$qm\x1b\\\x1bP+q544e;436f;524742\x1b\\"))
	got := string(bytes.Join(e.TakeResponses(), nil))
	for _, want := range []string{"\x1bP1$r0;1;3;38;2;1;2;3;48;5;42m\x1b\\", "544e=787465726d", "436f=323536", "524742=38"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
	e.Write([]byte(strings.Repeat("\x1b[5n", 2048)))
	if len(e.TakeResponses()) > 1024 {
		t.Fatal("reply queue unbounded")
	}
	e.Write([]byte("\x1b[5 q\x1bP$q q\x1b\\"))
	if got := string(bytes.Join(e.TakeResponses(), nil)); got != "\x1bP1$r5 q\x1b\\" {
		t.Fatal("cursor style not current", got)
	}
}
