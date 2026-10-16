package goterminal

import (
	"bytes"
	"strings"
	"testing"

	"github.com/SurTeam/Water/internal/goprotocol"
	"github.com/SurTeam/Water/internal/govt"
)

func TestCappedReplayPreservesMouseModes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		modes  string
		report string
	}{
		{"first_rule", "\x1b[?1000h\x1b[?1002h\x1b[?1003h\x1b[?1006h", "\x1b[<64;5;3M"},
		{"combined_params", "\x1b[?1003;1006h", "\x1b[<64;5;3M"},
		{"protocol_changed", "\x1b[?1003h\x1b[?1000h\x1b[?1006h", "\x1b[<64;5;3M"},
		{"reporting_disabled", "\x1b[?1003;1006h\x1b[?1000l", ""},
		{"encoding_reset", "\x1b[?1003;1006h\x1b[?1006l", "\x1b[M`%#"},
		{"pixel_encoding", "\x1b[?1003;1016h", "\x1b[<64;40;60M"},
		{"terminal_reset", "\x1b[?1003;1006h\x1bc", ""},
		{"osc_is_not_a_mode", "\x1b]0;\x1b[?1003;1006h\x07", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := []byte(tc.modes + strings.Repeat("\x1b[Hframe", 200))
			for cycle := range 3 {
				data = capEscapeBytes(data, 128)
				if len(data) > 128 {
					t.Fatalf("cycle %d: capped replay has %d bytes", cycle, len(data))
				}
				emu := govt.New(80, 24, 100)
				emu.WriteReplay(data)
				accepted := emu.Mouse(govt.MouseEvent{Col: 5, Row: 3, X: 40, Y: 60, Button: govt.MouseWheel, Action: govt.MouseUp})
				report := string(bytes.Join(emu.TakeResponses(), nil))
				emu.Close()
				if accepted != (tc.report != "") || report != tc.report {
					t.Fatalf("cycle %d: accepted=%v report=%q, want %q", cycle, accepted, report, tc.report)
				}
				data = append(data, []byte(strings.Repeat("\x1b[Hnext", 200))...)
			}
		})
	}
}

func TestAlternateReplayRestoresMouseAfterRedrawFlood(t *testing.T) {
	term := &Terminal{
		size:            goprotocol.TerminalSize{Columns: 80, Lines: 24},
		scrollbackLines: 100,
	}
	publishReplay(term, "shell history\r\n\x1b[?1049h\x1b[?1000h\x1b[?1002h\x1b[?1003h\x1b[?1006h")
	publishReplay(term, strings.Repeat("\x1b[Hframe", 100000))
	if len(term.altReplay) > screenReplayLimit(80, 24) {
		t.Fatalf("alternate replay grew beyond its byte limit: %d", len(term.altReplay))
	}
	restore := func() *govt.Emulator {
		emu := govt.New(80, 24, 100)
		for _, event := range term.Replay() {
			emu.WriteReplay(event.Data)
		}
		return emu
	}
	emu := restore()
	if !emu.Snapshot().AltScreen || !emu.Mouse(govt.MouseEvent{Col: 5, Row: 3, Button: govt.MouseWheel, Action: govt.MouseUp}) {
		t.Fatal("restored fullscreen TUI rejected wheel input")
	}
	if got := string(bytes.Join(emu.TakeResponses(), nil)); got != "\x1b[<64;5;3M" {
		t.Fatalf("restored mouse report = %q", got)
	}
	emu.Close()

	// A reset retained in the suffix must override the preserved prefix too.
	publishReplay(term, "\x1b[?1000l")
	emu = restore()
	defer emu.Close()
	if emu.Mouse(govt.MouseEvent{Col: 5, Row: 3, Button: govt.MouseWheel, Action: govt.MouseUp}) {
		t.Fatal("retained mouse reset was overridden by the replay prefix")
	}
}
