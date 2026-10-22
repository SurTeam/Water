package goserver

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SurTeam/Water/internal/goclient"
	"github.com/SurTeam/Water/internal/goconfig"
	"github.com/SurTeam/Water/internal/goterminal"
	"github.com/google/uuid"
)

func TestPaneExecuteRPC(t *testing.T) {
	socket := filepath.Join("/tmp", "water-execute-"+uuid.NewString()+".sock")
	s := NewWithConfig(socket, goconfig.Default())
	served := make(chan error, 1)
	go func() { served <- s.ListenAndServe() }()
	t.Cleanup(func() { _ = s.Close(); <-served; _ = os.Remove(socket + ".lock") })
	client := goclient.New(socket)
	deadline := time.Now().Add(3 * time.Second)
	for client.CallTimeout("ping", nil, nil, time.Second) != nil {
		if time.Now().After(deadline) {
			t.Fatal("server startup timed out")
		}
		time.Sleep(10 * time.Millisecond)
	}
	raw := json.RawMessage(`{"type":"terminal.spawn","program":"/bin/sh","args":["-i"],"columns":80,"lines":24}`)
	if _, err := s.executeCommand("terminal.spawn", raw); err != nil {
		t.Fatal(err)
	}
	_, pane, err := s.model.ResolvePane(nil)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		goterminal.ExecuteResult
		Status string    `json:"status"`
		Error  string    `json:"error"`
		PaneID uuid.UUID `json:"pane_id"`
	}
	call := func(command string, timeout int) {
		t.Helper()
		if err := client.CallTimeout("pane.execute", map[string]any{"pane_id": pane, "command": command, "timeout_ms": timeout}, &result, 5*time.Second); err != nil {
			t.Fatal(err)
		}
	}
	call("printf 'old-screen\\n'", 1000)
	call("printf 'first\\rfinal\\n'; exit 7", 1000)
	if result.Status != "completed" || result.ExitCode == nil || *result.ExitCode != 7 || !strings.Contains(result.Text, "final") || strings.Contains(result.Text, "old-screen") || result.PaneID != pane {
		t.Fatalf("unexpected result: %#v", result)
	}
	// Concurrent requests must progress while execute waits for its own deadline.
	finished := make(chan error, 1)
	go func() {
		var timeoutResult struct {
			goterminal.ExecuteResult
			Status string `json:"status"`
		}
		err := client.CallTimeout("pane.execute", map[string]any{"pane_id": pane, "command": "trap '' TERM; printf 'timeout-output\\n'; while :; do sleep 1; done", "timeout_ms": 500}, &timeoutResult, 5*time.Second)
		if err == nil && (!timeoutResult.TimedOut || len(timeoutResult.Signals) < 2 || timeoutResult.Signals[1] != "KILL" || !strings.Contains(timeoutResult.Text, "timeout-output")) {
			t.Errorf("timeout result %#v", timeoutResult)
		}
		finished <- err
	}()
	terminalID, _ := s.model.TerminalForPane(pane)
	if err := client.CallTimeout("terminal.contains", map[string]any{"terminal_id": terminalID, "text": "timeout-output", "timeout_ms": 1000}, nil, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := client.CallTimeout("state.dump", nil, nil, time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := s.executeCommand("workspace.new", json.RawMessage(`{"type":"workspace.new"}`)); err != nil {
		t.Fatal(err)
	}
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	call("printf 'shell-alive\\n'", 1000)
	if result.Status != "completed" || !strings.Contains(result.Text, "shell-alive") {
		t.Fatalf("shell survival %#v", result)
	}
	if err := client.CallTimeout("pane.execute", map[string]any{"command": "true"}, nil, time.Second); err == nil {
		t.Fatal("missing pane accepted")
	}
}
