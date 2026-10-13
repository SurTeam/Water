package goserver_test

import (
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SurTeam/Water/internal/goclient"
	"github.com/SurTeam/Water/internal/goconfig"
	"github.com/SurTeam/Water/internal/goprotocol"
	"github.com/SurTeam/Water/internal/goserver"
	"github.com/google/uuid"
)

// Large primary scrollback, a multi-megabyte alternate-screen flood, and
// recovery after the TUI leaves that screen. The flooded PTY stays attached
// on its own pane while control RPCs and a second pane run.
func TestLargeScrollbackTUIFloodAndRecovery(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join("/tmp", "water-pressure-"+uuid.NewString()+".sock")
	cfg := goconfig.Default()
	cfg.Terminal.ScrollbackLines = goconfig.MaxScrollbackLines
	server := goserver.NewWithConfig(socket, cfg)
	served := make(chan error, 1)
	go func() { served <- server.ListenAndServe() }()
	t.Cleanup(func() {
		_ = server.Close()
		select {
		case err := <-served:
			if err != nil {
				t.Errorf("server shutdown: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("server failed to stop")
		}
		_ = os.Remove(socket + ".lock")
	})

	client := goclient.New(socket)
	startupDeadline := time.Now().Add(2 * time.Second)
	for {
		if err := client.CallTimeout("ping", nil, nil, time.Second); err == nil {
			break
		} else if time.Now().After(startupDeadline) {
			t.Fatalf("server did not become ready: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	gui, err := client.OpenSession()
	if err != nil {
		t.Fatal(err)
	}
	defer gui.Close()

	var spawned struct {
		TerminalID uuid.UUID `json:"terminal_id"`
	}
	if err := gui.Dispatch(map[string]any{
		"type":    "terminal.spawn",
		"program": python,
		"args":    []string{"-c", pressureDriver},
		"columns": 80,
		"lines":   24,
	}, &spawned); err != nil {
		t.Fatal(err)
	}
	if spawned.TerminalID == uuid.Nil {
		t.Fatal("missing pressured terminal")
	}
	if err := client.CallTimeout("terminal.contains", map[string]any{
		"terminal_id": spawned.TerminalID,
		"text":        "PRIMARY-TAIL",
		"timeout_ms":  15000,
	}, nil, 20*time.Second); err != nil {
		t.Fatalf("primary scrollback was not retained: %v", err)
	}

	var attached struct {
		TerminalID uuid.UUID                      `json:"terminal_id"`
		Replay     []goprotocol.WireTerminalEvent `json:"replay"`
	}
	if err := gui.Attach(spawned.TerminalID, &attached); err != nil {
		t.Fatal(err)
	}
	defer gui.Detach(spawned.TerminalID)
	primary := replayText(t, attached.Replay)
	if !strings.Contains(primary, "PRIMARY-TAIL") || !strings.Contains(primary, "L14999") {
		t.Fatalf("retained scrollback missing the tail: %s", tail(primary, 200))
	}
	if strings.Contains(primary, "L00000") {
		t.Fatalf("scrollback kept the oldest line past the %d-line ceiling", goconfig.MaxScrollbackLines)
	}

	// Release the driver into the alternate-screen flood only after attach,
	// and do not consume events yet.
	if err := gui.Dispatch(map[string]any{
		"type":        "terminal.send_text",
		"terminal_id": spawned.TerminalID,
		"text":        "\n",
	}, nil); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for len(gui.Events) < cap(gui.Events) {
		if time.Now().After(deadline) {
			t.Fatalf("TUI flood did not saturate the GUI queue: %d/%d", len(gui.Events), cap(gui.Events))
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := gui.CallTimeout("ping", nil, nil, 2*time.Second); err != nil {
		t.Fatalf("TUI flood starved GUI RPC: %v", err)
	}

	var split struct {
		PaneID uuid.UUID `json:"pane_id"`
	}
	if err := gui.Dispatch(map[string]any{"type": "pane.split", "direction": "right"}, &split); err != nil {
		t.Fatalf("TUI flood blocked pane split: %v", err)
	}
	var quiet struct {
		TerminalID uuid.UUID `json:"terminal_id"`
	}
	if err := gui.Dispatch(map[string]any{
		"type":    "terminal.spawn",
		"pane_id": split.PaneID,
		"program": "/bin/sh",
		"args":    []string{"-c", "printf 'quiet-ok\\n'"},
		"columns": 80,
		"lines":   24,
	}, &quiet); err != nil {
		t.Fatalf("TUI flood blocked another pane spawn: %v", err)
	}
	var other struct {
		TerminalID uuid.UUID `json:"terminal_id"`
	}
	if err := gui.Attach(quiet.TerminalID, &other); err != nil {
		t.Fatalf("TUI flood blocked attach: %v", err)
	}
	defer gui.Detach(quiet.TerminalID)

	var last uint64
	for received := 0; received < cap(gui.Events)+32; {
		select {
		case event := <-gui.Events:
			if event.TerminalID != spawned.TerminalID {
				continue
			}
			if last != 0 && event.Event.Seq != last+1 {
				t.Fatalf("TUI output skipped sequence: previous=%d current=%d", last, event.Event.Seq)
			}
			last = event.Event.Seq
			received++
		case <-time.After(8 * time.Second):
			t.Fatalf("TUI flood stalled after %d events", received)
		}
	}

	if err := gui.Dispatch(map[string]any{
		"type":        "terminal.send_text",
		"terminal_id": spawned.TerminalID,
		"text":        "\n",
	}, nil); err != nil {
		t.Fatal(err)
	}
	if err := client.CallTimeout("terminal.contains", map[string]any{
		"terminal_id": spawned.TerminalID,
		"text":        "AFTER-LEAVE",
		"timeout_ms":  10000,
	}, nil, 15*time.Second); err != nil {
		t.Fatalf("TUI did not restore the primary screen: %v", err)
	}
	if err := gui.Detach(spawned.TerminalID); err != nil {
		t.Fatal(err)
	}
	var restored struct {
		Replay []goprotocol.WireTerminalEvent `json:"replay"`
	}
	if err := gui.Attach(spawned.TerminalID, &restored); err != nil {
		t.Fatal(err)
	}
	text := replayText(t, restored.Replay)
	if !strings.Contains(text, "PRIMARY-TAIL") || !strings.Contains(text, "AFTER-LEAVE") {
		t.Fatalf("restored replay lost primary history: %s", tail(text, 300))
	}
	if strings.Contains(text, "FRAME") || strings.Contains(text, "TUIOK") {
		t.Fatal("restored replay kept alternate-screen redraw")
	}
	if strings.Contains(text, "L00000") {
		t.Fatal("restored replay resurrected trimmed scrollback")
	}
	if err := gui.CallTimeout("ping", nil, nil, 2*time.Second); err != nil {
		t.Fatalf("GUI control failed after TUI recovery: %v", err)
	}
}

func replayText(t *testing.T, replay []goprotocol.WireTerminalEvent) string {
	t.Helper()
	var b strings.Builder
	for _, ev := range replay {
		if ev.Type != "output" || ev.Bytes == "" {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(ev.Bytes)
		if err != nil {
			t.Fatalf("decode replay: %v", err)
		}
		b.Write(raw)
	}
	return b.String()
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

const pressureDriver = `
import sys, threading
out = sys.stdout.buffer
for i in range(15000):
    out.write(f"L{i:05d}\n".encode())
out.write(b"PRIMARY-TAIL\n")
out.flush()
sys.stdin.buffer.read(1)
out.write(b"\x1b[?1049h")
stop = threading.Event()
def wait_leave():
    sys.stdin.buffer.read(1)
    stop.set()
threading.Thread(target=wait_leave, daemon=True).start()
chunk = b"\x1b[HFRAME" + (b"F" * (64 * 1024))
while not stop.is_set():
    out.write(chunk)
    out.flush()
out.write(b"TUIOK\x1b[?1049lAFTER-LEAVE\n")
out.flush()
sys.stdin.buffer.read(1)
`
