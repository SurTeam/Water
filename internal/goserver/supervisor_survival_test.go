package goserver_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
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
	"github.com/SurTeam/Water/internal/govt"
	"github.com/google/uuid"
)

type replayPayload struct {
	TerminalID uuid.UUID `json:"terminal_id"`
	Events     []struct {
		Type  string `json:"type"`
		Bytes string `json:"bytes"`
	} `json:"events"`
}

// readState returns the raw state dump for structural assertions.
func readState(t *testing.T, socket string) string {
	t.Helper()
	var raw map[string]any
	if err := goclient.New(socket).CallTimeout("state.dump", map[string]any{}, &raw, 3*time.Second); err != nil {
		t.Fatalf("state.dump: %v", err)
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
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

// serverTerminal returns the terminal id the model points at for the first pane.
func serverTerminal(t *testing.T, socket string) uuid.UUID {
	t.Helper()
	var state struct {
		Workspaces []struct {
			Tabs []struct {
				ActivePane string `json:"active_pane"`
				Tree       struct {
					SurfaceState struct {
						Terminal struct {
							TerminalID string `json:"terminal_id"`
						} `json:"Terminal"`
					} `json:"surface_state"`
				} `json:"tree"`
			} `json:"tabs"`
		} `json:"workspaces"`
	}
	if err := goclient.New(socket).CallTimeout("state.dump", map[string]any{}, &state, 3*time.Second); err != nil {
		t.Fatalf("state.dump: %v", err)
	}
	for _, ws := range state.Workspaces {
		for _, tab := range ws.Tabs {
			if id, err := uuid.Parse(tab.Tree.SurfaceState.Terminal.TerminalID); err == nil {
				return id
			}
		}
	}
	t.Fatal("no pane terminal in state")
	return uuid.Nil
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

	key := supervisedTerminal(t, supervisorSocket)
	terminal := serverTerminal(t, socket)
	pid := supervisorPID(t, supervisorSocket, key)
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
	prepared, err := goclient.PrepareRecovery(session, recoveryPath, gobuild.Variant, "local:"+socket, true)
	if err != nil {
		t.Fatalf("prepare recovery: %v", err)
	}
	if err := session.CallTimeout("recovery.shutdown", map[string]any{"token": prepared.Token}, nil, 3*time.Second); err != nil {
		t.Fatal(err)
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

	resumedPID := supervisorPID(t, supervisorSocket, key)
	if resumedPID != pid {
		t.Fatalf("program restarted across upgrade: pid %d -> %d", pid, resumedPID)
	}
	// The counter must keep climbing: a restarted shell would emit a low count
	// again, while the same process continues past where it was.
	// The resumed pane is a new terminal instance; the program behind it is the
	// same supervisor terminal.
	resumed := serverTerminal(t, socket)
	text := waitText(t, socket, resumed, "tick-")
	if !strings.Contains(text, "tick-") {
		t.Fatalf("replayed output missing after upgrade: %q", text)
	}
	deadline := time.Now().Add(10 * time.Second)
	for strings.Count(text, "tick-") <= beforeTicks && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		text = terminalText(t, socket, resumed)
	}
	if got := strings.Count(text, "tick-"); got <= beforeTicks {
		t.Fatalf("program did not keep running: %d ticks, previously %d", got, beforeTicks)
	}
}

func TestSupervisedMouseSurvivesServerRestart(t *testing.T) {
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
	cfg.Shell.Args = []string{"-c", `stty raw -echo; printf '\033[?1049h\033[?1003;1006h'; i=0; while [ "$i" -lt 40000 ]; do printf '\033[HFRAME                                                            '; i=$((i+1)); done; printf '\033[2J\033[HMOUSE_READY'; dd bs=1 count=10 2>/dev/null | od -An -tx1; printf 'MOUSE_RECEIVED'; sleep 30`}

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

	key := supervisedTerminal(t, supervisorSocket)
	terminal := serverTerminal(t, socket)
	pid := supervisorPID(t, supervisorSocket, key)
	if pid <= 0 {
		t.Fatalf("supervisor has no pid for %s", terminal)
	}
	if text := waitText(t, socket, terminal, "MOUSE_READY"); !strings.Contains(text, "MOUSE_READY") {
		t.Fatalf("TUI not ready: %q", text)
	}

	// Export the layout exactly like the GUI restart does, then stop the server.
	session, err := goclient.New(socket).OpenSession()
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := goclient.PrepareRecovery(session, recoveryPath, gobuild.Variant, "local:"+socket, true)
	if err != nil {
		t.Fatalf("prepare recovery: %v", err)
	}
	if err := session.CallTimeout("recovery.shutdown", map[string]any{"token": prepared.Token}, nil, 3*time.Second); err != nil {
		t.Fatal(err)
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

	resumedPID := supervisorPID(t, supervisorSocket, key)
	if resumedPID != pid {
		t.Fatalf("program restarted across upgrade: pid %d -> %d", pid, resumedPID)
	}
	// The counter must keep climbing: a restarted shell would emit a low count
	// again, while the same process continues past where it was.
	// The resumed pane is a new terminal instance; the program behind it is the
	// same supervisor terminal.
	resumed := serverTerminal(t, socket)
	text := waitText(t, socket, resumed, "MOUSE_READY")
	emu := govt.New(80, 24, 100)
	defer emu.Close()
	emu.WriteReplay([]byte(text))
	if !emu.Snapshot().AltScreen {
		t.Fatal("alternate screen lost after server restart")
	}
	if !emu.Mouse(govt.MouseEvent{Col: 5, Row: 3, Button: govt.MouseWheel, Action: govt.MouseUp}) {
		t.Fatal("resumed TUI rejects mouse")
	}
	report := bytes.Join(emu.TakeResponses(), nil)
	if string(report) != "\x1b[<64;5;3M" {
		t.Fatalf("wheel report=%q", report)
	}
	values := make([]int, len(report))
	for i, b := range report {
		values[i] = int(b)
	}
	var result map[string]any
	if err := goclient.New(socket).Dispatch(map[string]any{"type": "terminal.send_bytes", "terminal_id": resumed, "bytes": values}, &result); err != nil {
		t.Fatal(err)
	}
	text = waitText(t, socket, resumed, "MOUSE_RECEIVED")
	// od pads bytes differently across host platforms; compare the byte tokens.
	if !strings.Contains(strings.Join(strings.Fields(text), " "), "1b 5b 3c 36 34 3b 35 3b 33 4d") {
		t.Fatalf("original PTY did not receive wheel: %q", text)
	}
}

// TestReplacingSupervisedTerminalKeepsThePane guards the replacement ordering: the
// supervisor keeps the PTY under the pane id, so the retired program's Exit must
// not close the pane that its successor now owns.
func TestReplacingSupervisedTerminalKeepsThePane(t *testing.T) {
	socket := filepath.Join("/tmp", "water-go-sup-"+uuid.New().String()+".sock")
	supervisorSocket := filepath.Join("/tmp", "water-go-sup-"+uuid.New().String()+".sock")
	supervisor := gosupervisor.NewServer()
	if err := supervisor.Listen(supervisorSocket); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = supervisor.Close() }()

	cfg := goconfig.Default()
	cfg.Startup.InitialWorkspace = true
	cfg.Startup.InitialTerminal = true
	cfg.Shell.Program = "/bin/sh"
	cfg.Shell.Args = []string{"-c", "sleep 30"}

	server := goserver.NewWithConfig(socket, cfg)
	if err := server.UseSupervisor(supervisorSocket); err != nil {
		t.Fatal(err)
	}
	if err := server.Initialize(true, true); err != nil {
		t.Fatal(err)
	}
	go func() { _ = server.ListenAndServe() }()
	waitSocket(t, socket)
	defer func() { _ = server.Close() }()

	terminal := supervisedTerminal(t, supervisorSocket)
	pane := terminal.String()

	var out map[string]any
	err := goclient.New(socket).Dispatch(map[string]any{
		"type": "terminal.spawn", "pane_id": pane, "columns": 80, "lines": 24,
		"program": "/bin/sh", "args": []string{"-c", "echo REPLACED; sleep 30"},
	}, &out)
	if err != nil {
		t.Fatalf("replace failed: %v", err)
	}
	replacement, _ := out["terminal_id"].(string)
	if replacement == "" {
		t.Fatalf("replacement has no terminal id: %v", out)
	}
	// The supervisor keeps the PTY under the pane id, but the terminal instance is
	// new: clients must be able to tell the replacement from the retired stream.
	if replacement == pane {
		t.Fatal("replacement reused the retired terminal id")
	}
	// The pane must still exist and show the replacement program's output.
	id, err := uuid.Parse(replacement)
	if err != nil {
		t.Fatal(err)
	}
	text := waitText(t, socket, id, "REPLACED")
	if !strings.Contains(text, "REPLACED") {
		t.Fatalf("replacement output missing: %q", text)
	}
	// And the pane still points at the replacement, not at a closed pane.
	state := readState(t, socket)
	if !strings.Contains(state, replacement) {
		t.Fatalf("pane does not reference the replacement terminal: %s", state)
	}
}

// Ordinary shutdown must not retain terminals that a fresh startup cannot adopt.
func TestOrdinaryCloseStopsSupervisor(t *testing.T) {
	socket := filepath.Join("/tmp", "water-go-sup-"+uuid.New().String()+".sock")
	supervisor := gosupervisor.NewServer()
	if err := supervisor.Listen(socket); err != nil {
		t.Fatal(err)
	}
	defer supervisor.Close()
	cfg := goconfig.Default()
	cfg.Shell.Program = "/bin/sh"
	cfg.Shell.Args = []string{"-c", "sleep 30"}
	server := goserver.NewWithConfig(filepath.Join(t.TempDir(), "server.sock"), cfg)
	if err := server.UseSupervisor(socket); err != nil {
		t.Fatal(err)
	}
	if err := server.Initialize(true, true); err != nil {
		t.Fatal(err)
	}
	client, err := gosupervisor.Dial(socket)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	terms, err := client.List()
	if err != nil || len(terms) != 1 {
		t.Fatalf("terminals: %v, %v", terms, err)
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-supervisor.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("supervisor survived ordinary close")
	}
	// Close waits for the entire cleanup, including terminal process groups.
	if err := supervisor.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := gosupervisor.Dial(socket); err == nil {
		t.Fatal("supervisor still accepting connections")
	}
}
