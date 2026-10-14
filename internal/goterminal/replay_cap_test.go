package goterminal

import (
	"bytes"
	"fmt"
	"math/rand"
	"testing"

	"github.com/SurTeam/Water/internal/goprotocol"
)

// Frozen pre-optimization selector; intentionally retains the span table so
// differential tests and paired benchmarks can measure the original behavior.
func referenceCapEscapeBytes(data []byte, limit int) []byte {
	if len(data) == 0 || limit < 1 {
		return nil
	}
	if len(data) <= limit {
		spans := referenceCompleteSpans(data)
		if len(spans) > 0 && spans[len(spans)-1].end == len(data) {
			return data
		}
	}
	spans := referenceCompleteSpans(data)
	sum, start := 0, len(spans)
	for i := len(spans) - 1; i >= 0; i-- {
		n := spans[i].end - spans[i].start
		if n > limit || sum+n > limit {
			break
		}
		sum += n
		start = i
	}
	if start >= len(spans) {
		return nil
	}
	return append([]byte(nil), data[spans[start].start:spans[len(spans)-1].end]...)
}

type referenceSpan struct{ start, end int }

func referenceCompleteSpans(data []byte) []referenceSpan {
	var spans []referenceSpan
	i := 0
	for i < len(data) {
		start := i
		if data[i] != 0x1b {
			j := i + 1
			for j < len(data) && data[j] != 0x1b {
				j++
			}
			spans = append(spans, referenceSpan{start, j})
			i = j
			continue
		}
		end, complete := skipEscape(data, i)
		if !complete {
			break
		}
		spans = append(spans, referenceSpan{start, end})
		i = end
	}
	return spans
}

func TestCapEscapeBytesEquivalent(t *testing.T) {
	cases := [][]byte{nil, {}, []byte("plain"), []byte("\x1b"), []byte("\x1b["), []byte("\x1b[2J"), []byte("a\x1b[2Jtail\x1b["), []byte("\x1b]0;title\a"), []byte("\x1b]x\x1b\\"), []byte("\x1b]unterminated"), bytes.Repeat([]byte("\x1b[HFRAME"), 200), append([]byte("\x1b["), bytes.Repeat([]byte("0"), 80)...)}
	rng := rand.New(rand.NewSource(42))
	alphabet := []byte("abc012;[?]HJ\\\a\x1b\r\n")
	for i := 0; i < 3000; i++ {
		data := make([]byte, rng.Intn(512))
		for j := range data {
			data[j] = alphabet[rng.Intn(len(alphabet))]
		}
		cases = append(cases, data)
	}
	for i, data := range cases {
		original := bytes.Clone(data)
		for _, limit := range []int{-1, 0, 1, 2, 3, 8, 64, 256, len(data) - 1, len(data), len(data) + 1} {
			want := referenceCapEscapeBytes(data, limit)
			got := capEscapeBytes(data, limit)
			if !bytes.Equal(got, want) || (got == nil) != (want == nil) {
				t.Fatalf("case %d limit %d: got %q want %q", i, limit, got, want)
			}
			if !bytes.Equal(data, original) {
				t.Fatal("mutated input")
			}
		}
	}
}

func TestCapEscapeBytesOwnership(t *testing.T) {
	data := make([]byte, 16*1024, 4*1024*1024)
	for i := range data {
		data[i] = 'x'
	}
	got := capEscapeBytes(data, len(data))
	if &got[0] != &data[0] {
		t.Fatal("complete in-limit input must preserve alias contract")
	}
	data = bytes.Repeat([]byte("\x1b[Hframe"), 32768)
	got = capEscapeBytes(data, 32768)
	if len(got) == 0 || len(got) > 32768 {
		t.Fatalf("bad capped length %d", len(got))
	}
	saved := bytes.Clone(got)
	for i := range data {
		data[i] = '!'
	}
	if !bytes.Equal(got, saved) {
		t.Fatal("oversized output retains input backing storage")
	}
}

func TestCapEscapeBytesSpanExpectations(t *testing.T) {
	cases := []struct {
		data  string
		limit int
		want  string
	}{
		{"oversized", 3, ""},
		{"oversized\x1b[Hok", 5, "\x1b[Hok"},
		{"ok\x1b[", 32, "ok"},
		{"ok\x1b]title", 32, "ok"},
		{"\x1b]title\a", 32, "\x1b]title\a"},
		{"\x1b]title\x1b\\", 32, "\x1b]title\x1b\\"},
	}
	for _, c := range cases {
		if got := capEscapeBytes([]byte(c.data), c.limit); string(got) != c.want {
			t.Fatalf("%q got %q want %q", c.data, got, c.want)
		}
	}
}

func TestReplayEscapeReadBoundaries(t *testing.T) {
	size := goprotocol.TerminalSize{Columns: 80, Lines: 24}
	for _, data := range []string{"head\x1b[2Jtail", "head\x1b]title\atail", "head\x1b]title\x1b\\tail"} {
		for split := 0; split <= len(data); split++ {
			term := &Terminal{size: size, scrollbackLines: 100}
			term.publish(goprotocol.TerminalEvent{Kind: goprotocol.OutputEvent, Size: size, Seq: 1, Data: []byte(data[:split])})
			term.publish(goprotocol.TerminalEvent{Kind: goprotocol.OutputEvent, Size: size, Seq: 2, Data: []byte(data[split:])})
			if got := replayText(term); got != data {
				t.Fatalf("split %d got %q want %q", split, got, data)
			}
		}
	}
	// On an active alternate screen the cap deliberately discards an unfinished
	// OSC; its later terminator cannot reconstruct that discarded prefix.
	term := &Terminal{size: size, scrollbackLines: 100}
	prefix := bytes.Repeat([]byte("\x1b[H"), 22000)
	data := append([]byte("\x1b[?1049h"), prefix...)
	data = append(data, []byte("\x1b]unfinished")...)
	term.publish(goprotocol.TerminalEvent{Kind: goprotocol.OutputEvent, Size: size, Seq: 1, Data: data})
	want := bytes.Repeat([]byte("\x1b[H"), screenReplayLimit(80, 24)/3)
	if !bytes.Equal(term.altReplay, want) {
		t.Fatalf("first cap length %d want %d", len(term.altReplay), len(want))
	}
	term.publish(goprotocol.TerminalEvent{Kind: goprotocol.OutputEvent, Size: size, Seq: 2, Data: []byte("\aend")})
	want = append(want[6:], []byte("\aend")...)
	if !bytes.Equal(term.altReplay, want) {
		t.Fatalf("later terminator length %d want %d", len(term.altReplay), len(want))
	}
}

var capBenchmarkResult []byte

func BenchmarkCapEscapeBytes(b *testing.B) {
	for _, n := range []int{4096, 32768, 262144, 1048576} {
		for _, pattern := range []struct{ name, unit string }{{"plain", "x"}, {"dense", "\x1b[H"}, {"mixed", "FRAME\x1b[2J\x1b]title\a"}} {
			data := bytes.Repeat([]byte(pattern.unit), (n+len(pattern.unit)-1)/len(pattern.unit))[:n]
			for _, limit := range []int{n / 2, n, n * 2} {
				for _, impl := range []struct {
					name string
					fn   func([]byte, int) []byte
				}{{"reference", referenceCapEscapeBytes}, {"constant", capEscapeBytes}} {
					b.Run(fmt.Sprintf("%s/%d/limit%d/%s", pattern.name, n, limit, impl.name), func(b *testing.B) {
						b.ReportAllocs()
						b.SetBytes(int64(n))
						b.ResetTimer()
						for i := 0; i < b.N; i++ {
							capBenchmarkResult = impl.fn(data, limit)
						}
					})
				}
			}
		}
	}
}
