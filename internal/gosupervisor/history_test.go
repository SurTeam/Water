package gosupervisor

import (
	"bytes"
	"fmt"
	"testing"
)

func TestHistoryRingOffsetsAndWrap(t *testing.T) {
	var term terminal
	var all []byte
	for step, size := range []int{0, 17, historyLimit - 20, 9, 127, historyLimit - 3, 7, historyLimit, historyLimit + 73} {
		data := make([]byte, size)
		for i := range data {
			data[i] = byte((i + step*37) % 251)
		}
		all = append(all, data...)
		term.produced += uint64(len(data))
		term.appendHistoryLocked(data)
		want := all
		if len(want) > historyLimit {
			want = want[len(want)-historyLimit:]
		}
		if cap(term.history) > historyLimit {
			t.Fatalf("history capacity exceeds limit: %d", cap(term.history))
		}
		if term.start != uint64(len(all)-len(want)) || len(term.history) != len(want) {
			t.Fatalf("step %d: start=%d len=%d, want start=%d len=%d", step, term.start, len(term.history), len(all)-len(want), len(want))
		}
		for _, skip := range []int{0, len(want) / 2, len(want)} {
			got := term.copyHistoryLocked(term.start + uint64(skip))
			if !bytes.Equal(got, want[skip:]) {
				t.Fatalf("step %d skip %d: snapshot mismatch", step, skip)
			}
			if len(got) > 0 {
				got[0] ^= 0xff
				if !bytes.Equal(term.copyHistoryLocked(term.start+uint64(skip)), want[skip:]) {
					t.Fatal("snapshot aliases ring storage")
				}
			}
		}
	}
}

func TestAttachRingOffsets(t *testing.T) {
	server := NewServer()
	term := &terminal{id: "ring", subs: make(map[*subscriber]struct{})}
	server.terms[term.id] = term
	data := bytes.Repeat([]byte("0123456789abcdef"), historyLimit/16+11)
	term.produced = uint64(len(data))
	term.appendHistoryLocked(data)
	more := []byte("wrapped-tail")
	term.produced += uint64(len(more))
	term.appendHistoryLocked(more)
	all := append(data, more...)
	for _, offset := range []uint64{0, term.start, term.start + 37, term.produced, term.produced + 1} {
		t.Run(fmt.Sprint(offset), func(t *testing.T) {
			sub := &subscriber{}
			resp, replay := server.attach(attachRequest{ID: term.id, Offset: offset}, sub)
			wantOffset := offset
			if wantOffset < term.start {
				wantOffset = term.start
			}
			if wantOffset > term.produced {
				wantOffset = term.produced
			}
			if resp.Offset != wantOffset || resp.Available != term.produced || resp.Truncated != (offset < term.start) {
				t.Fatalf("unexpected response: %+v", resp)
			}
			if !bytes.Equal(replay, all[wantOffset:]) {
				t.Fatal("attach replay mismatch")
			}
			if _, ok := term.subs[sub]; !ok {
				t.Fatal("subscriber not registered")
			}
		})
	}
}

// Prefill history so measurements exercise the sustained-output path, rather
// than only the initial growth before the retention limit is reached.
func BenchmarkHistoryRingSteadyState(b *testing.B) {
	for _, size := range []int{1024, 4096, 128 * 1024} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			var term terminal
			term.produced = historyLimit
			term.appendHistoryLocked(make([]byte, historyLimit))
			data := make([]byte, size)
			b.SetBytes(int64(size))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				term.produced += uint64(size)
				term.appendHistoryLocked(data)
			}
		})
	}
}
