package goterminal

import (
	"bytes"
	"os"
	"testing"

	"github.com/SurTeam/Water/internal/goprotocol"
)

func BenchmarkReplayIngestEscapes(b *testing.B) {
	data := bytes.Repeat([]byte("\r\x1b[0K\x1b[1mBold\x1b[0m \x1b[7mInvert\x1b[0m \x1b[4mUnderline\x1b[0m\n"), 2400)
	benchmarkReplayIngest(b, data)
}

func BenchmarkReplayIngestFixture(b *testing.B) {
	path := os.Getenv("WATER_CAT_BENCHMARK_FILE")
	if path == "" {
		b.Skip("set WATER_CAT_BENCHMARK_FILE to the testTermCat fixture")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		b.Fatal(err)
	}
	benchmarkReplayIngest(b, data)
}

func benchmarkReplayIngest(b *testing.B, data []byte) {
	size := goprotocol.TerminalSize{Columns: 80, Lines: 24}
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		term := &Terminal{size: size, scrollbackLines: 2000}
		for pos := 0; pos < len(data); pos += 128 * 1024 {
			term.ingestOutputLocked(data[pos:min(pos+128*1024, len(data))], size, uint64(pos+1))
			term.trimReplayLocked()
		}
	}
}

func TestReplayIngestSpanBoundaries(t *testing.T) {
	size := goprotocol.TerminalSize{Columns: 80, Lines: 24}
	primary := "before\x1b[31mred\x1b[0m\n"
	enter := "\x1b[?1049h"
	alternate := "alt\x1b[32mgreen\x1b[0m\n"
	leave := "\x1b[?1049l"
	tail := "after\x1b]0;title\a\x1b[4munder\x1b[0m"
	input := primary + enter + alternate + leave + tail
	for split := 0; split <= len(input); split++ {
		term := &Terminal{size: size}
		term.ingestOutputLocked([]byte(input[:split]), size, 1)
		term.ingestOutputLocked([]byte(input[split:]), size, 2)
		if got := replayText(term); got != primary+enter+leave+tail {
			t.Fatalf("split %d: got %q", split, got)
		}
		if term.altScreen || len(term.scanHold) != 0 || len(term.altReplay) != 0 {
			t.Fatalf("split %d: unexpected residual state", split)
		}
	}
	active := primary + enter + alternate
	for split := 0; split <= len(active); split++ {
		term := &Terminal{size: size}
		term.ingestOutputLocked([]byte(active[:split]), size, 1)
		term.ingestOutputLocked([]byte(active[split:]), size, 2)
		if !term.altScreen || string(term.altReplay) != alternate || replayText(term) != active {
			t.Fatalf("active split %d: primary/alt mismatch", split)
		}
	}
}

func TestReplayIngestIncompleteEscapeBarrier(t *testing.T) {
	size := goprotocol.TerminalSize{Columns: 80, Lines: 24}
	term := &Terminal{size: size}
	term.ingestOutputLocked([]byte("text\x1b[31mred\x1b[?10"), size, 1)
	if replayText(term) != "text\x1b[31mred" || string(term.scanHold) != "\x1b[?10" {
		t.Fatal("incomplete escape not retained separately")
	}
	term.storeLocked(goprotocol.TerminalEvent{Kind: goprotocol.ResizeEvent, Size: size, Seq: 2})
	if replayText(term) != "text\x1b[31mred\x1b[?10" || len(term.scanHold) != 0 {
		t.Fatal("barrier lost held bytes")
	}
	// Oversized incomplete sequences retain the historical one-byte escape fallback.
	long := []byte("prefix\x1b]" + string(bytes.Repeat([]byte{'x'}, 80)))
	term = &Terminal{size: size}
	term.ingestOutputLocked(long, size, 1)
	if replayText(term) != string(long) || len(term.scanHold) != 0 {
		t.Fatal("oversized incomplete sequence lost bytes")
	}
}
