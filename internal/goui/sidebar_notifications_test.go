package goui

import (
	"github.com/SurTeam/Water/internal/goconfig"
	"github.com/SurTeam/Water/internal/gomodel"
	"github.com/SurTeam/Water/internal/govt"
	"github.com/google/uuid"
	"image"
	"image/color"
	"testing"
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
	delete(raw, "custom_label")
	raw["status"] = map[string]any{"exited": map[string]any{"code": 0}}
	check("Restored session", "osc", "Exited")
	emu.Write([]byte("\x1b]2;\x07"))
	check("Codex", "kind", "Exited")
}

func TestSidebarFooterIndependentHeights(t *testing.T) {
	cfg := goconfig.Default()
	cfg.UI.SidebarRemoteButtonHeight, cfg.UI.SidebarWorkspaceButtonHeight = 40, 22
	cfg.UI.SidebarRemoteButtonFontSize, cfg.UI.SidebarWorkspaceButtonFontSize = 18, 9
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
	if err != nil || parsed.UI.SidebarRemoteButtonFontSize != 18 || parsed.UI.SidebarWorkspaceButtonFontSize != 9 {
		t.Fatalf("footer settings lost: %v", err)
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
	if len(platform.messages) != 1 {
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
