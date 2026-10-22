package goserver_test

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SurTeam/Water/internal/gobuild"
	"github.com/SurTeam/Water/internal/goclient"
	"github.com/SurTeam/Water/internal/goconfig"
	"github.com/SurTeam/Water/internal/goserver"
	"github.com/SurTeam/Water/internal/gosupervisor"
	"github.com/google/uuid"
)

type replayPayload struct {
	TerminalID uuid.UUID `json:"terminal_id"`
	Events     []struct {
		Type  string `json:"type"`
		Bytes string `json:"bytes"`
	} `json:"events"`
}

func waitSocket(t *testing.T, socket string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(socket); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("server socket did not appear")
}

// terminalText reads the retained replay of a terminal, or "" when it is gone.
func terminalText(t *testing.T, socket string, id uuid.UUID) string {
	t.Helper()
	client := goclient.New(socket)
	var out replayPayload
	if err := client.CallTimeout("terminal.replay", map[string]any{"terminal_id": id}, &out, 3*time.Second); err != nil {
		return ""
	}
	var text strings.Builder
	for _, ev := range out.Events {
		if ev.Type != "output" || ev.Bytes == "" {
			continue
		}
		decoded, err := base64.StdEncoding.DecodeString(ev.Bytes)
		if err != nil {
			continue
		}
		text.Write(decoded)
	}
	return text.String()
}

func waitText(t *testing.T, socket string, id uuid.UUID, want string) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var text string
	for time.Now().Before(deadline) {
		text = terminalText(t, socket, id)
		if strings.Contains(text, want) {
			return text
		}
		time.Sleep(50 * time.Millisecond)
	}
	return text
}

// supervisedTerminal returns the only terminal the supervisor owns for this test.
func supervisedTerminal(t *testing.T, supervisorSocket string) uuid.UUID {
	t.Helper()
	client, err := gosupervisor.Dial(supervisorSocket)
	if err != nil {
		t.Fatalf("dial supervisor: %v", err)
	}
	defer client.Close()
	listed, err := client.List()
	if err != nil {
		t.Fatalf("supervisor list: %v", err)
	}
	if len(listed) == 0 {
		t.Fatal("supervisor owns no terminal")
	}
	id, err := uuid.Parse(listed[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func supervisorPID(t *testing.T, socket string, id uuid.UUID) int {
	t.Helper()
	client, err := gosupervisor.Dial(socket)
	if err != nil {
		t.Fatalf("dial supervisor: %v", err)
	}
	defer client.Close()
	listed, err := client.List()
	if err != nil {
		t.Fatalf("supervisor list: %v", err)
	}
	for _, term := range listed {
		if term.ID == id.String() {
			return term.PID
		}
	}
	return 0
}

// TestSupervisedPaneSurvivesServerRestart is the guarantee the supervisor exists
// for: the recovery restart used by an upgrade must resume the running program
// instead of starting a new shell in the pane.
func TestSupervisedPaneSurvivesServerRestart(t *testing.T) {
	socket := filepath.Join("/tmp", "water-go-sup-"+uuid.New().String()+".sock")
	supervisorSocket := filepath.Join("/tmp", "water-go-sup-"+uuid.New().String()+".sock")
	recoveryPath := filepath.Join(t.TempDir(), "recovery.json")

	supervisor := gosupervisor.NewServer()
	if err := supervisor.Listen(supervisorSocket); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = supervisor.Close() }()

	cfg := goconfig.Default()
	cfg.Startup.InitialWorkspace = true
	cfg.Startup.InitialTerminal = true
	cfg.Shell.Program = "/bin/sh"
	cfg.Shell.Args = []string{"-c", "i=0; while :; do echo tick-$i; i=$((i+1)); sleep 0.05; done"}

	first := goserver.NewWithConfig(socket, cfg)
	if err := first.UseSupervisor(supervisorSocket); err != nil {
		t.Fatal(err)
	}
	if err := first.Initialize(true, true); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- first.ListenAndServe() }()
	waitSocket(t, socket)

	terminal := supervisedTerminal(t, supervisorSocket)
	pid := supervisorPID(t, supervisorSocket, terminal)
	if pid <= 0 {
		t.Fatalf("supervisor has no pid for %s", terminal)
	}
	before := waitText(t, socket, terminal, "tick-")
	if !strings.Contains(before, "tick-") {
		t.Fatalf("no output before restart: %q", before)
	}
	beforeTicks := strings.Count(before, "tick-")

	// Export the layout exactly like the GUI restart does, then stop the server.
	session, err := goclient.New(socket).OpenSession()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := goclient.PrepareRecovery(session, recoveryPath, gobuild.Variant, "local:"+socket, true); err != nil {
		t.Fatalf("prepare recovery: %v", err)
	}
	_ = session.Close()
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("first server did not stop")
	}

	// The new server imports the layout; panes must resume, not respawn.
	// A replacement server starts empty and adopts the exported layout.
	second := goserver.NewWithConfig(socket, cfg)
	if err := second.UseSupervisor(supervisorSocket); err != nil {
		t.Fatal(err)
	}
	if err := second.Initialize(false, false); err != nil {
		t.Fatal(err)
	}
	go func() { done <- second.ListenAndServe() }()
	waitSocket(t, socket)
	defer func() { _ = second.Close() }()

	session2, err := goclient.New(socket).OpenSession()
	if err != nil {
		t.Fatal(err)
	}
	defer session2.Close()
	if err := goclient.RestorePending(session2, recoveryPath, gobuild.Variant, "local:"+socket); err != nil {
		t.Fatalf("restore recovery: %v", err)
	}

	resumedPID := supervisorPID(t, supervisorSocket, terminal)
	if resumedPID != pid {
		t.Fatalf("program restarted across upgrade: pid %d -> %d", pid, resumedPID)
	}
	// The counter must keep climbing: a restarted shell would emit a low count
	// again, while the same process continues past where it was.
	text := waitText(t, socket, terminal, "tick-")
	if !strings.Contains(text, "tick-") {
		t.Fatalf("replayed output missing after upgrade: %q", text)
	}
	deadline := time.Now().Add(10 * time.Second)
	for strings.Count(text, "tick-") <= beforeTicks && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		text = terminalText(t, socket, terminal)
	}
	if got := strings.Count(text, "tick-"); got <= beforeTicks {
		t.Fatalf("program did not keep running: %d ticks, previously %d", got, beforeTicks)
	}
}
