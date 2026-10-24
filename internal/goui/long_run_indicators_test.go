package goui

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/SurTeam/Water/internal/goconfig"
	"github.com/SurTeam/Water/internal/gomodel"
	"github.com/google/uuid"
)

func TestLongRunIndicatorsTrackCurrentTerminals(t *testing.T) {
	cfg := goconfig.Default()
	cfg.UI.SystemNotifications = false
	cfg.UI.ShellLongRunNotificationSeconds = 2
	cfg.UI.AgentLongRunNotificationSeconds = 3
	view := NewWorkspaceClientWithConnection(nil, nil, cfg, "")
	defer view.Close()
	connection, workspace, tab := uuid.New(), uuid.New(), uuid.New()
	first, second := uuid.New(), uuid.New()
	leaf := func(id uuid.UUID, name string) map[string]any {
		return map[string]any{"type": "leaf", "pane_id": uuid.New(), "terminal": map[string]any{"summary": map[string]any{"terminal_id": id, "process": "running", "process_name": name}}}
	}
	setTree := func(a, b string) {
		tree, err := json.Marshal(map[string]any{"type": "split", "first": leaf(first, a), "second": leaf(second, b)})
		if err != nil {
			t.Fatal(err)
		}
		view.state.Workspaces = []gomodel.WorkspaceDump{{ID: workspace, Tabs: []gomodel.TabDump{{ID: tab, Tree: tree}}}}
	}
	window := &EbitengineWindow{}
	start := time.Unix(100, 0)
	tick := func(seconds int, status string) {
		window.updateLongRunIndicators([]nativeHost{{entry: ConnectionEntry{ID: connection, Status: status}, view: view, state: view.state}}, start.Add(time.Duration(seconds)*time.Second))
	}
	check := func(count int) {
		t.Helper()
		if got := window.longRunTabs[longRunLocation{connection, workspace, tab}]; got != count {
			t.Fatalf("tab count=%d, want %d", got, count)
		}
		if got := window.longRunWorkspaces[longRunLocation{connection: connection, workspace: workspace}]; got != count {
			t.Fatalf("workspace count=%d, want %d", got, count)
		}
	}
	setTree("sleep", "zsh")
	tick(0, "connected")
	tick(1, "connected")
	check(0)
	revision := window.frameRevision.Load()
	tick(2, "connected")
	check(1)
	if window.frameRevision.Load() == revision {
		t.Fatal("threshold crossing did not invalidate frame")
	}
	setTree("sleep", "tail")
	tick(3, "connected")
	check(1)
	tick(5, "connected")
	check(2)
	setTree("zsh", "tail")
	tick(6, "connected")
	check(1)
	// An Agent's idle/error state overrides a foreground CLI process.
	agent := map[string]any{"kind": "codex", "terminal_id": second, "status": "running"}
	view.state.Agents = []any{agent}
	tick(7, "connected")
	check(0)
	tick(10, "connected")
	check(0)
	agent["status"] = map[string]any{"exited": map[string]any{"code": 0}}
	tick(11, "connected")
	check(0)
	view.state.Agents = nil
	tick(12, "connected")
	tick(14, "connected")
	check(1)
	tick(15, "disconnected")
	check(0)
	tick(16, "connected")
	check(0)
	tick(18, "connected")
	check(1)
	// Replacing the command resets its elapsed time.
	setTree("zsh", "sleep")
	tick(19, "connected")
	check(0)
	tick(21, "connected")
	check(1)
	setTree("zsh", "zsh")
	tick(22, "connected")
	check(0)
	if len(window.longRunObservations) != 0 {
		t.Fatal("finished commands retained")
	}
}
