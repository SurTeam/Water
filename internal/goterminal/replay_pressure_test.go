package goterminal

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/SurTeam/Water/internal/goprotocol"
)

// Dozens of live terminals each dump shell scrollback and a megabyte-scale
// redraw. Alternate-screen TUIs must not evict that scrollback or keep the
// redraw after they exit. Primary-buffer redraws must leave the surrounding
// lines in place. Retained replay stays far below one full copy of the floods.
func TestDozensOfTerminalsKeepScrollbackUnderTUIPressure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires raw Unix PTY VT passthrough; ConPTY synthesizes its output stream")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	const (
		terminals  = 40
		scrollback = 100
		floodUnits = 120_000
	)
	r := NewRegistryWithScrollback(scrollback)
	defer r.CloseAll()

	baseline := replayMemorySample(t, r, "pty-baseline")

	size := goprotocol.TerminalSize{Columns: 80, Lines: 24}
	type live struct {
		id   int
		mode string
		term *Terminal
	}
	modes := make([]live, 0, terminals)
	for i := 0; i < terminals; i++ {
		mode := "leave"
		switch {
		case i >= 30:
			mode = "primary"
		case i >= 20:
			mode = "stay"
		}
		term, err := r.Spawn(python, []string{"-c", pressureScript, fmt.Sprint(i), mode}, size)
		if err != nil {
			t.Fatal(err)
		}
		modes = append(modes, live{id: i, mode: mode, term: term})
	}

	for _, item := range modes {
		wait := "after-" + fmt.Sprint(item.id)
		switch item.mode {
		case "stay":
			wait = "TUIOK-" + fmt.Sprint(item.id)
		case "primary":
			wait = "tail-" + fmt.Sprint(item.id)
		}
		text := waitForReplay(t, item.term, wait)
		// PTYs translate newline to CR LF. Match the line marker either way.
		shell := fmt.Sprintf("shell-%d-0", item.id)
		last := fmt.Sprintf("shell-%d-79", item.id)
		if !strings.Contains(text, shell+"\n") && !strings.Contains(text, shell+"\r\n") ||
			!strings.Contains(text, last+"\n") && !strings.Contains(text, last+"\r\n") {
			t.Fatalf("terminal %d (%s) lost primary scrollback", item.id, item.mode)
		}
		switch item.mode {
		case "leave":
			if strings.Contains(text, "FRAME") || strings.Contains(text, "TUIOK-") {
				t.Fatalf("terminal %d kept alternate-screen redraw after the TUI exited", item.id)
			}
			item.term.mu.RLock()
			alt := item.term.altScreen
			item.term.mu.RUnlock()
			if alt {
				t.Fatalf("terminal %d still on the alternate screen after leave", item.id)
			}
		case "stay":
			item.term.mu.RLock()
			alt := item.term.altScreen
			altBytes := len(item.term.altReplay)
			item.term.mu.RUnlock()
			if !alt {
				t.Fatalf("terminal %d TUI was not active", item.id)
			}
			if altBytes > screenReplayLimit(size.Columns, size.Lines) {
				t.Fatalf("terminal %d alt replay %d bytes", item.id, altBytes)
			}
		case "primary":
			item.term.mu.RLock()
			alt := item.term.altScreen
			item.term.mu.RUnlock()
			if alt {
				t.Fatalf("terminal %d primary redraw entered the alternate screen", item.id)
			}
		}
	}

	retained := r.RetainedReplayBytes()
	const retainedCeiling = 4 * 1024 * 1024
	floodBytes := terminals * floodUnits * len("\x1b[H\x1b[2JFRAME")
	if retained > retainedCeiling {
		t.Fatalf("40 live terminals retained %d replay bytes, ceiling %d; flood was about %d bytes", retained, retainedCeiling, floodBytes)
	}
	used := replayMemorySample(t, r, "pty-live") - baseline
	const heapCeiling = 24 * 1024 * 1024
	if used > heapCeiling {
		t.Fatalf("heap grew %d bytes after GC, ceiling %d (retained replay %d)", used, heapCeiling, retained)
	}
	t.Logf("40 live terminals: retained replay %d bytes, heap growth after GC %d bytes, raw flood about %d bytes", retained, used, floodBytes)

	// Registry ownership alone is insufficient to prove cleanup: wait for every
	// reader to finish, then release the fixture's own Terminal pointers too.
	r.CloseAll()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for i := range modes {
		select {
		case <-modes[i].term.readerDone:
		case <-deadline.C:
			t.Fatal("closed terminal reader did not finish")
		}
		modes[i].term = nil
	}
	modes = nil
	r.CloseAll()
	cleared := replayMemorySample(t, r, "pty-cleared")
	if r.Count() != 0 || r.RetainedReplayBytes() != 0 {
		t.Fatal("CloseAll retained terminal owners or replay")
	}
	if cleared > baseline+4*1024*1024 {
		t.Fatalf("closed PTYs retained heap: baseline=%d cleared=%d", baseline, cleared)
	}
}

const pressureScript = `
import sys
ident, mode = sys.argv[1], sys.argv[2]
out = sys.stdout.buffer
for i in range(80):
    out.write(f"shell-{ident}-{i}\n".encode())
frame = b"\x1b[H\x1b[2JFRAME" * 120000
if mode == "primary":
    out.write(frame)
    out.write(f"tail-{ident}\n".encode())
else:
    out.write(b"\x1b[?1049h")
    out.write(frame)
    out.write(f"TUIOK-{ident}".encode())
    if mode == "leave":
        out.write(b"\x1b[?1049l")
        out.write(f"after-{ident}\n".encode())
out.flush()
sys.stdin.buffer.read(1)
`

func waitForReplay(t *testing.T, term *Terminal, needle string) string {
	t.Helper()
	deadline := time.Now().Add(45 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		last = replayText(term)
		if strings.Contains(last, needle) {
			return last
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("terminal output %q did not appear; replay length %d", needle, len(last))
	return ""
}
