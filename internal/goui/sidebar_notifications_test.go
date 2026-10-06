package goui

import (
	"encoding/json"
	"image"
	"image/color"
	"testing"
	"time"

	"github.com/SurTeam/Water/internal/goconfig"
	"github.com/SurTeam/Water/internal/gomodel"
	"github.com/SurTeam/Water/internal/govt"
	"github.com/google/uuid"
)

type recordingNotifications struct {
	nativePlatform
	messages []string
}

func (p *recordingNotifications) Notify(title, body string) {
	p.messages = append(p.messages, title+": "+body)
}

func TestSidebarModesKeepAllAgentPaneAndConnectionTargets(t *testing.T) {
	for _, mode := range []string{"workspace", "split"} {
		t.Run(mode, func(t *testing.T) {
			cfg := goconfig.Default()
			cfg.UI.SidebarAgentMode = mode
			cfg.UI.SidebarAgentRowWidth = .3
			manager := NewMultiWorkspaceClient(nil)
			defer manager.Close()
			view := NewWorkspaceClientWithConnection(nil, nil, cfg, "")
			model := gomodel.New()
			workspace := model.CreateWorkspace("workspace")
			_, pane, err := model.CreateTab(workspace, "agent", true)
			if err != nil {
				t.Fatal(err)
			}
			terminal := uuid.New()
			if err := model.InstallTerminal(pane, gomodel.TerminalMeta{TerminalID: terminal, ProcessName: "zsh"}); err != nil {
				t.Fatal(err)
			}
			model.SetTerminalForeground(terminal, "codex")
			view.state = model.Dump()
			connection := uuid.New()
			if err := manager.AddConnection(ConnectionEntry{ID: connection, Kind: "local"}, view, nil, true); err != nil {
				t.Fatal(err)
			}
			window := &EbitengineWindow{multi: manager, scale: 1, views: map[*WorkspaceClient]*nativeView{}}
			window.drawSidebar(view, nil, image.Rect(0, 0, 240, 800), color.NRGBA{})
			agents := 0
			for _, hit := range view.hitRegions {
				if hit.Kind == hitAgent {
					agents++
					if hit.ID != pane || hit.Rect.Empty() {
						t.Fatalf("invalid pane hit: %#v", hit)
					}
					if mode == "split" && hit.Rect.Min.Y < window.view(view).agentShelfRect.Min.Y {
						t.Fatal("agent shelf not in lower half")
					}
					if mode == "split" && hit.Rect.Dx() != window.view(view).agentShelfRect.Dx() {
						t.Fatal("concentrated agents must use the entire shelf width")
					}
				}
			}
			if agents != 1 {
				t.Fatalf("agent hits=%d", agents)
			}
		})
	}
}

func TestSidebarAgentOSCTitlesAndStatuses(t *testing.T) {
	c := NewWorkspaceClientWithConnection(nil, nil, goconfig.Default(), "")
	id, pane, workspace := uuid.New(), uuid.New(), uuid.New()
	emu := govt.New(80, 24, 10)
	defer emu.Close()
	c.terminals[id] = &terminalClient{id: id, emu: emu}
	raw := map[string]any{"kind": "codex", "label": "Codex", "terminal_id": id, "pane_id": pane, "workspace_id": workspace, "status": "running"}
	state := gomodel.StateDump{Workspaces: []gomodel.WorkspaceDump{{ID: workspace, Title: "Remote project"}}, Agents: []any{nil, raw}}
	check := func(title, source, status string) {
		t.Helper()
		a := c.sidebarAgents(state)[0]
		if a.label != title || a.titleSource != source || a.status != status || a.workspaceName != "Remote project" {
			t.Fatalf("unexpected sidebar agent: %#v", a)
		}
	}
	check("Codex", "kind", "Running")
	emu.Write([]byte("\x1b]2;修复侧栏"))
	check("Codex", "kind", "Running")
	emu.Write([]byte("\x1b\\"))
	check("修复侧栏", "osc", "Running")
	emu.WriteReplay([]byte("\x1b]0;Restored session\x07"))
	check("Restored session", "osc", "Running")
	raw["custom_label"] = "Pinned name"
	check("Pinned name", "custom", "Running")
	emu.Write([]byte("\x1b]9;4;4\x07"))
	check("Pinned name", "custom", "Paused")
	emu.Write([]byte("\x1b]9;4;2;50\x07"))
	check("Pinned name", "custom", "Error")
	emu.Write([]byte("\x1b]9;4;0\x07"))
	check("Pinned name", "custom", "Idle")
	emu.Write([]byte("\x1b]9;4;3\x07"))
	check("Pinned name", "custom", "Running")
	delete(raw, "custom_label")
	raw["status"] = map[string]any{"exited": map[string]any{"code": 0}}
	check("Restored session", "osc", "Completed")
	emu.Write([]byte("\x1b]2;\x07"))
	check("Codex", "kind", "Completed")
}

func TestAgentStatusGlyphsUseConsistentCircleFamily(t *testing.T) {
	for status, want := range map[string]string{"Running": "◔", "Paused": "◑", "Error": "◕", "Idle": "○", "Completed": "◌", "Offline": "◍"} {
		if got := agentStatusGlyph(status); got != want {
			t.Fatalf("%s glyph=%q, want %q", status, got, want)
		}
	}
}

func TestAgentProgressNotificationsDistinguishAttentionCompletionAndErrors(t *testing.T) {
	cfg := goconfig.Default()
	cfg.UI.SystemNotifications = true
	manager := NewMultiWorkspaceClient(nil)
	defer manager.Close()
	view := NewWorkspaceClientWithConnection(nil, nil, cfg, "")
	connection, pane, terminal := uuid.New(), uuid.New(), uuid.New()
	emu := govt.New(80, 24, 10)
	defer func() {
		delete(view.terminals, terminal)
		emu.Close()
	}()
	view.terminals[terminal] = &terminalClient{id: terminal, emu: emu}
	view.state.Agents = []any{map[string]any{"kind": "codex", "label": "Codex", "pane_id": pane, "terminal_id": terminal, "status": "running"}}
	if err := manager.AddConnection(ConnectionEntry{ID: connection, Kind: "remote", Status: "connected"}, view, nil, true); err != nil {
		t.Fatal(err)
	}
	platform := &recordingNotifications{}
	window := &EbitengineWindow{multi: manager, platform: platform}
	emu.WriteReplay([]byte("\x1b]9;4;4\x07"))
	window.processNotifications(view)
	if len(platform.messages) != 0 {
		t.Fatal("initial replay notified")
	}
	transitions := []struct{ osc, message string }{
		{"3", ""},
		{"0", "Water: Agent completed: Codex"},
		{"4", "Water: Agent paused; it may need your attention: Codex"},
		{"2", "Water: Agent reported an error: Codex"},
	}
	for _, transition := range transitions {
		emu.Write([]byte("\x1b]9;4;" + transition.osc + "\x07"))
		window.processNotifications(view)
		window.processNotifications(view)
		if transition.message == "" {
			if len(platform.messages) != 0 {
				t.Fatalf("running transition notified: %v", platform.messages)
			}
			continue
		}
		if len(platform.messages) == 0 || platform.messages[len(platform.messages)-1] != transition.message {
			t.Fatalf("transition %s notifications=%v", transition.osc, platform.messages)
		}
	}
	manager.markDisconnected(connection)
	window.processNotifications(view)
	before := len(platform.messages)
	emu.WriteReplay([]byte("\x1b]9;4;0\x07"))
	window.processNotifications(view)
	if len(platform.messages) != before {
		t.Fatal("offline replay emitted agent notification")
	}
	manager.mu.Lock()
	manager.connections[connection].entry.Status = "connected"
	manager.mu.Unlock()
	window.processNotifications(view)
	if len(platform.messages) != before+1 || platform.messages[before] != "Water: Remote connection restored." {
		t.Fatalf("reconnect emitted false agent transition: %v", platform.messages)
	}
}

func TestSidebarFooterIndependentHeights(t *testing.T) {
	cfg := goconfig.Default()
	cfg.UI.SidebarRemoteButtonHeight, cfg.UI.SidebarWorkspaceButtonHeight = 40, 22
	cfg.UI.SidebarRemoteButtonFontSize, cfg.UI.SidebarWorkspaceButtonFontSize = 18, 9
	cfg.UI.AgentLongRunNotificationSeconds, cfg.UI.ShellLongRunNotificationSeconds = 1, 3
	manager := NewMultiWorkspaceClient(nil)
	defer manager.Close()
	view := NewWorkspaceClientWithConnection(nil, nil, cfg, "")
	window := &EbitengineWindow{multi: manager, scale: 2, views: map[*WorkspaceClient]*nativeView{}}
	window.drawSidebar(view, nil, image.Rect(0, 0, 480, 1000), color.NRGBA{})
	var remote, workspace image.Rectangle
	for _, h := range view.hitRegions {
		if h.Kind == hitNewRemote {
			remote = h.Rect
		}
		if h.Kind == hitNewWorkspace {
			workspace = h.Rect
		}
	}
	if remote.Dy() != 80 || workspace.Dy() != 44 || workspace.Min.Y-remote.Max.Y != 16 || window.view(view).sidebarRect.Max.Y >= remote.Min.Y {
		t.Fatalf("footer dimensions or list overlap: %v %v", remote, workspace)
	}
	view.openSettings()
	parsed, err := view.settings.parse()
	if err != nil || parsed.UI.SidebarRemoteButtonFontSize != 18 || parsed.UI.SidebarWorkspaceButtonFontSize != 9 || parsed.UI.AgentLongRunNotificationSeconds != 1 || parsed.UI.ShellLongRunNotificationSeconds != 3 || !parsed.UI.SystemNotifications {
		t.Fatalf("settings lost values: %v", err)
	}
}

func TestAgentStartNotificationIsEmittedOnceAfterBaseline(t *testing.T) {
	manager := NewMultiWorkspaceClient(nil)
	defer manager.Close()
	view := NewWorkspaceClientWithConnection(nil, nil, goconfig.Default(), "")
	connection := uuid.New()
	if err := manager.AddConnection(ConnectionEntry{ID: connection, Kind: "local", Status: "connected"}, view, nil, true); err != nil {
		t.Fatal(err)
	}
	platform := &recordingNotifications{}
	window := &EbitengineWindow{multi: manager, platform: platform}
	window.processNotificationsAt(view, false, time.Unix(300, 0))
	if len(platform.messages) != 0 {
		t.Fatalf("initial baseline notified: %v", platform.messages)
	}
	view.state.Agents = []any{map[string]any{"kind": "codex", "label": "Codex", "pane_id": uuid.New(), "status": "running"}}
	window.processNotificationsAt(view, false, time.Unix(301, 0))
	window.processNotificationsAt(view, false, time.Unix(302, 0))
	if len(platform.messages) != 1 || platform.messages[0] != "Water: Agent started: Codex" {
		t.Fatalf("Agent start notifications=%v", platform.messages)
	}
}

func TestAgentLongRunNotificationUsesConfiguredThreshold(t *testing.T) {
	cfg := goconfig.Default()
	cfg.UI.AgentLongRunNotificationSeconds = 3
	manager := NewMultiWorkspaceClient(nil)
	defer manager.Close()
	view := NewWorkspaceClientWithConnection(nil, nil, cfg, "")
	connection, pane := uuid.New(), uuid.New()
	otherPane := uuid.New()
	view.state.Agents = []any{map[string]any{"kind": "codex", "label": "Codex", "pane_id": pane, "status": "running"}}
	view.state.FocusedPane = &pane
	if err := manager.AddConnection(ConnectionEntry{ID: connection, Kind: "local", Status: "connected"}, view, nil, true); err != nil {
		t.Fatal(err)
	}
	platform := &recordingNotifications{}
	window := &EbitengineWindow{multi: manager, platform: platform}
	start := time.Unix(100, 0)
	window.processNotificationsAt(view, false, start)
	window.processNotificationsAt(view, false, start.Add(3*time.Second-time.Nanosecond))
	if len(platform.messages) != 0 {
		t.Fatalf("Agent notified before threshold: %v", platform.messages)
	}
	window.processNotificationsAt(view, true, start.Add(4*time.Second))
	if len(platform.messages) != 0 {
		t.Fatalf("focused window notified for Agent: %v", platform.messages)
	}
	window.processNotificationsAt(view, false, start.Add(5*time.Second))
	if len(platform.messages) != 0 {
		t.Fatalf("focused Agent pane notified: %v", platform.messages)
	}
	view.state.FocusedPane = &otherPane
	window.processNotificationsAt(view, false, start.Add(6*time.Second))
	window.processNotificationsAt(view, false, start.Add(7*time.Second))
	if len(platform.messages) != 1 || platform.messages[0] != "Water: Agent is still running: Codex" {
		t.Fatalf("Agent long-run notifications=%v", platform.messages)
	}
}

func TestBackgroundShellCommandNotificationRequiresUnfocusedPaneAndWindow(t *testing.T) {
	cfg := goconfig.Default()
	cfg.UI.ShellLongRunNotificationSeconds = 1
	manager := NewMultiWorkspaceClient(nil)
	defer manager.Close()
	view := NewWorkspaceClientWithConnection(nil, nil, cfg, "")
	connection, workspace, pane, terminal := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	focused := uuid.New()
	tree, err := json.Marshal(map[string]any{
		"type": "leaf", "pane_id": pane,
		"terminal": map[string]any{"summary": map[string]any{"terminal_id": terminal, "process": "running", "process_name": "sleep"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	view.state = gomodel.StateDump{
		Workspaces:  []gomodel.WorkspaceDump{{ID: workspace, Title: "test", Tabs: []gomodel.TabDump{{ID: uuid.New(), Tree: tree}}}},
		FocusedPane: &focused,
	}
	if err := manager.AddConnection(ConnectionEntry{ID: connection, Kind: "local", Status: "connected"}, view, nil, true); err != nil {
		t.Fatal(err)
	}
	platform := &recordingNotifications{}
	window := &EbitengineWindow{multi: manager, platform: platform}
	start := time.Unix(200, 0)
	window.processNotificationsAt(view, false, start)
	window.processNotificationsAt(view, true, start.Add(2*time.Second))
	if len(platform.messages) != 0 {
		t.Fatalf("focused window notified for shell command: %v", platform.messages)
	}
	view.state.FocusedPane = &pane
	window.processNotificationsAt(view, false, start.Add(3*time.Second))
	if len(platform.messages) != 0 {
		t.Fatalf("focused pane notified for shell command: %v", platform.messages)
	}
	view.state.FocusedPane = &focused
	window.processNotificationsAt(view, false, start.Add(4*time.Second))
	window.processNotificationsAt(view, false, start.Add(5*time.Second))
	if len(platform.messages) != 1 || platform.messages[0] != "Water: Long-running command: sleep" {
		t.Fatalf("background command notifications=%v", platform.messages)
	}
}

func TestNotificationsBaselineDeduplicateAndHonorSetting(t *testing.T) {
	cfg := goconfig.Default()
	cfg.UI.SystemNotifications = true
	manager := NewMultiWorkspaceClient(nil)
	defer manager.Close()
	view := NewWorkspaceClientWithConnection(nil, nil, cfg, "")
	connection, pane := uuid.New(), uuid.New()
	view.state.Agents = []any{map[string]any{"kind": "codex", "label": "Codex", "pane_id": pane, "status": "running"}}
	if err := manager.AddConnection(ConnectionEntry{ID: connection, Kind: "remote", Status: "connected"}, view, nil, true); err != nil {
		t.Fatal(err)
	}
	platform := &recordingNotifications{}
	window := &EbitengineWindow{multi: manager, platform: platform}
	window.processNotifications(view)
	if len(platform.messages) != 0 {
		t.Fatal("initial snapshot notified")
	}
	view.state.Agents = nil
	window.processNotifications(view)
	window.processNotifications(view)
	if len(platform.messages) != 1 || platform.messages[0] != "Water: Agent completed: Codex" {
		t.Fatalf("agent completion notifications=%v", platform.messages)
	}
	manager.markDisconnected(connection)
	window.processNotifications(view)
	window.processNotifications(view)
	if len(platform.messages) != 2 {
		t.Fatalf("connection notifications=%v", platform.messages)
	}
	view.config.UI.SystemNotifications = false
	manager.mu.Lock()
	manager.connections[connection].entry.Status = "connected"
	manager.mu.Unlock()
	window.processNotifications(view)
	if len(platform.messages) != 2 {
		t.Fatal("disabled notification emitted")
	}
}

func TestBellNotificationCoalescesPerTerminal(t *testing.T) {
	cfg := goconfig.Default()
	cfg.UI.SystemNotifications = true
	manager := NewMultiWorkspaceClient(nil)
	defer manager.Close()
	view := NewWorkspaceClientWithConnection(nil, nil, cfg, "")
	platform := &recordingNotifications{}
	window := &EbitengineWindow{multi: manager, platform: platform, bellNotifications: make(chan uuid.UUID, 32)}
	terminal := uuid.New()
	window.bellNotifications <- terminal
	window.bellNotifications <- terminal
	window.processNotifications(view)
	if len(platform.messages) != 1 {
		t.Fatalf("bell burst emitted %d notifications", len(platform.messages))
	}
}
