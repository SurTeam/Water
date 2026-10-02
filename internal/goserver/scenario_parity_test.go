package goserver_test

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/SurTeam/Water/internal/goclient"
	"github.com/SurTeam/Water/internal/gomodel"
	"github.com/SurTeam/Water/internal/goserver"
	"github.com/google/uuid"
)

func withServer(t *testing.T, fn func(*goclient.Client)) {
	t.Helper()
	socket := filepath.Join("/tmp", "water-go-scenario-"+uuid.New().String()+".sock")
	srv := goserver.New(socket)
	done := make(chan error, 1)
	go func() { done <- srv.ListenAndServe() }()
	defer func() {
		_ = srv.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("server did not stop")
		}
	}()

	client := goclient.New(socket)
	deadline := time.Now().Add(2 * time.Second)
	for {
		var pong map[string]any
		if err := client.Call("ping", map[string]any{}, &pong); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("server never became ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	fn(client)
}

func dispatchOK(t *testing.T, client *goclient.Client, command map[string]any) map[string]any {
	t.Helper()
	var out map[string]any
	if err := client.Dispatch(command, &out); err != nil {
		t.Fatalf("dispatch %v: %v", command["type"], err)
	}
	return out
}

func stateDump(t *testing.T, client *goclient.Client) gomodel.StateDump {
	t.Helper()
	var state gomodel.StateDump
	if err := client.Call("state.dump", map[string]any{}, &state); err != nil {
		t.Fatal(err)
	}
	return state
}

func activePaneCount(t *testing.T, state gomodel.StateDump) int {
	t.Helper()
	ws := activeWorkspaceDump(state)
	if ws == nil || ws.ActiveTab == nil {
		return 0
	}
	for _, tab := range ws.Tabs {
		if tab.ID != *ws.ActiveTab {
			continue
		}
		var node struct {
			Type   string          `json:"type"`
			First  json.RawMessage `json:"first"`
			Second json.RawMessage `json:"second"`
		}
		if err := json.Unmarshal(tab.Tree, &node); err != nil {
			t.Fatal(err)
		}
		var count func(json.RawMessage) int
		count = func(raw json.RawMessage) int {
			var n struct {
				Type   string          `json:"type"`
				First  json.RawMessage `json:"first"`
				Second json.RawMessage `json:"second"`
			}
			if json.Unmarshal(raw, &n) != nil {
				return 0
			}
			if n.Type == "leaf" {
				return 1
			}
			return count(n.First) + count(n.Second)
		}
		return count(tab.Tree)
	}
	return 0
}

func activeWorkspaceDump(state gomodel.StateDump) *gomodel.WorkspaceDump {
	if state.ActiveWorkspace != nil {
		for i := range state.Workspaces {
			if state.Workspaces[i].ID == *state.ActiveWorkspace {
				return &state.Workspaces[i]
			}
		}
	}
	return state.Workspace
}

func TestWorkspaceBasicScenarioParity(t *testing.T) {
	withServer(t, func(client *goclient.Client) {
		dispatchOK(t, client, map[string]any{"type": "workspace.create"})
		dispatchOK(t, client, map[string]any{"type": "tab.new", "title": "Main"})
		dispatchOK(t, client, map[string]any{"type": "pane.split", "direction": "right"})
		dispatchOK(t, client, map[string]any{"type": "pane.focus", "direction": "left"})
		if got := activePaneCount(t, stateDump(t, client)); got != 2 {
			t.Fatalf("pane count after first split = %d, want 2", got)
		}

		dispatchOK(t, client, map[string]any{"type": "pane.split", "direction": "down"})
		dispatchOK(t, client, map[string]any{"type": "pane.resize", "ratio": 0.6})
		dispatchOK(t, client, map[string]any{"type": "pane.close"})
		if got := activePaneCount(t, stateDump(t, client)); got != 2 {
			t.Fatalf("pane count after close = %d, want 2", got)
		}

		dispatchOK(t, client, map[string]any{"type": "tab.new", "title": "Second"})
		state := stateDump(t, client)
		ws := activeWorkspaceDump(state)
		if ws == nil || len(ws.Tabs) != 2 {
			t.Fatalf("tab count = %d, want 2", len(ws.Tabs))
		}

		dispatchOK(t, client, map[string]any{"type": "tab.activate", "index": 0})
		if got := activePaneCount(t, stateDump(t, client)); got != 2 {
			t.Fatalf("reactivated pane count = %d, want 2", got)
		}
		dispatchOK(t, client, map[string]any{"type": "tab.close"})
		ws = activeWorkspaceDump(stateDump(t, client))
		if ws == nil || len(ws.Tabs) != 1 {
			t.Fatalf("tab count after close = %d, want 1", len(ws.Tabs))
		}
	})
}

func TestTerminalBasicScenarioParity(t *testing.T) {
	withServer(t, func(client *goclient.Client) {
		dispatchOK(t, client, map[string]any{"type": "workspace.create"})
		dispatchOK(t, client, map[string]any{"type": "tab.new", "title": "Terminal"})
		dispatchOK(t, client, map[string]any{"type": "pane.split", "direction": "right"})

		out := dispatchOK(t, client, map[string]any{
			"type": "terminal.spawn",
			"program": "/bin/sh",
			"args": []string{"-c", "printf '\033[31m__READY__\033[0m\\n'"},
			"columns": 80,
			"lines": 24,
		})
		terminalID, err := uuid.Parse(out["terminal_id"].(string))
		if err != nil {
			t.Fatal(err)
		}
		var contains any
		if err := client.Call("terminal.contains", map[string]any{
			"terminal_id": terminalID, "text": "__READY__", "timeout_ms": 5000,
		}, &contains); err != nil {
			t.Fatal(err)
		}
		if err := client.Call("terminal.wait_exit", map[string]any{
			"terminal_id": terminalID, "timeout_ms": 5000,
		}, &contains); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(time.Second)
		for activePaneCount(t, stateDump(t, client)) != 1 {
			if time.Now().After(deadline) {
				t.Fatal("short-lived terminal pane was not auto-closed")
			}
			time.Sleep(10 * time.Millisecond)
		}

		out = dispatchOK(t, client, map[string]any{
			"type": "terminal.spawn",
			"program": "/bin/sh",
			"args": []string{"-c", "read line; printf 'INPUT:%s\\n' \"$line\""},
			"columns": 80,
			"lines": 24,
		})
		terminalID, err = uuid.Parse(out["terminal_id"].(string))
		if err != nil {
			t.Fatal(err)
		}
		dispatchOK(t, client, map[string]any{
			"type": "terminal.send_text",
			"text": "hello\n",
		})
		if err := client.Call("terminal.contains", map[string]any{
			"terminal_id": terminalID, "text": "INPUT:hello", "timeout_ms": 5000,
		}, &contains); err != nil {
			t.Fatal(err)
		}
		if err := client.Call("terminal.wait_exit", map[string]any{
			"terminal_id": terminalID, "timeout_ms": 5000,
		}, &contains); err != nil {
			t.Fatal(err)
		}
	})
}
