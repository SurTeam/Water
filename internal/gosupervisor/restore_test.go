package gosupervisor

import (
	"bytes"
	"testing"

	"github.com/SurTeam/Water/internal/govt"
)

func TestTruncatedHistoryRestoresTUIMouse(t *testing.T) {
	for _, split := range []string{"", "\x1b[?100", "\x1b]title", "\x1bPpayload\a"} {
		t.Run(split, func(t *testing.T) {
			server := NewServer()
			term := &terminal{id: "restore", subs: make(map[*subscriber]struct{})}
			server.terms[term.id] = term
			initial := []byte("\x1b[?1049h\x1b[?1003;1006;2004h" + split)
			suffix := ""
			switch split {
			case "\x1b[?100":
				suffix = "3h"
			case "\x1b]title", "\x1bPpayload\a":
				suffix = "remaining\x1b\\"
			}
			// Place the retained boundary exactly after initial, including inside a
			// control sequence/string. Each publish can itself split the commands.
			data := append(initial, []byte(suffix)...)
			data = append(data, bytes.Repeat([]byte("x"), historyLimit-len(suffix))...)
			for _, chunk := range [][]byte{data[:7], data[7:]} {
				term.produced += uint64(len(chunk))
				term.appendHistoryLocked(chunk)
			}
			resp, replay := server.attach(attachRequest{ID: term.id}, &subscriber{})
			if !resp.Truncated || resp.Offset != uint64(len(initial)) || len(replay) != historyLimit {
				t.Fatalf("bad offsets: %+v replay=%d", resp, len(replay))
			}
			emu := govt.New(80, 24, 100)
			defer emu.Close()
			emu.WriteReplay(resp.Restore)
			emu.WriteReplay(replay)
			if !emu.Snapshot().AltScreen {
				t.Fatal("alternate screen lost")
			}
			if !emu.Mouse(govt.MouseEvent{Col: 5, Row: 3, Button: govt.MouseWheel, Action: govt.MouseUp}) {
				t.Fatal("wheel rejected")
			}
			if got := string(bytes.Join(emu.TakeResponses(), nil)); got != "\x1b[<64;5;3M" {
				t.Fatalf("wheel=%q", got)
			}
			again, _ := server.attach(attachRequest{ID: term.id, Offset: resp.Offset}, &subscriber{})
			if len(again.Restore) != 0 {
				t.Fatal("nontruncated incremental attach received restore")
			}
		})
	}
}

func TestRestoreModesResetAndBoundedParser(t *testing.T) {
	var r historyRestore
	for _, b := range []byte("\x1b[?1049;1003;1016;2004h\x1b[?1000l\x1b[?1016l\x1b[?1049l") {
		r.observe([]byte{b})
	}
	if got := string(r.bytes()); got != "\x1b[?2004h" {
		t.Fatalf("reset modes=%q", got)
	}
	r.observe([]byte("\x1bc"))
	if len(r.bytes()) != 0 {
		t.Fatal("RIS did not clear checkpoint")
	}
	r.observe([]byte("\x1bPqpayload\a[?1003h\x1b\\"))
	if len(r.bytes()) != 0 {
		t.Fatal("DCS payload enabled mouse")
	}
	r.observe([]byte("\x1b["))
	r.observe(bytes.Repeat([]byte("1"), 10000))
	if len(r.pending) > 128 || len(r.bytes()) > 256 {
		t.Fatal("unbounded parser")
	}
}

// Compare the checkpoint's input behavior with the actual frontend VT,
// including boundaries on every byte of an aborted or incomplete command.
func TestRestoreControlTransitionsMatchVT(t *testing.T) {
	for _, stream := range []string{
		"\x1b]0;title\x1b[?1003;1006h",
		"\x1bPqpayload\x1b[?1003;1006h",
		"\x1b_apayload\x1b[?1003;1006h",
		"\x1bXignored\x1b[?1003;1006h",
		"\x1b]0;title\a\x1b[?1003;1006h",
		"\x1bPqpayload\x18\x1b[?1003;1006h",
		"\x1b[?1003;1006h\x1b]0;abort\x1bc",
		"\x1b[?10\a03;1006h",
	} {
		reference := govt.New(80, 24, 100)
		reference.WriteReplay([]byte(stream))
		ev := govt.MouseEvent{Col: 5, Row: 3, Button: govt.MouseWheel, Action: govt.MouseUp}
		wantAccepted := reference.Mouse(ev)
		want := bytes.Join(reference.TakeResponses(), nil)
		reference.Close()
		for split := 0; split <= len(stream); split++ {
			var checkpoint historyRestore
			checkpoint.observe([]byte(stream[:split]))
			restored := govt.New(80, 24, 100)
			restored.WriteReplay(checkpoint.bytes())
			restored.WriteReplay([]byte(stream[split:]))
			accepted := restored.Mouse(ev)
			got := bytes.Join(restored.TakeResponses(), nil)
			restored.Close()
			if accepted != wantAccepted || !bytes.Equal(got, want) {
				t.Fatalf("stream %q split %d: mouse=%v %q want %v %q restore=%q", stream, split, accepted, got, wantAccepted, want, checkpoint.bytes())
			}
		}
	}
}
