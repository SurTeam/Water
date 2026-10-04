package gomodel

import (
	"github.com/SurTeam/Water/internal/goagent"
	"github.com/google/uuid"
	"testing"
)

func TestForegroundAgentLifecycle(t *testing.T) {
	m := New()
	workspace := m.CreateWorkspace("agents")
	_, pane, err := m.CreateTab(workspace, "shell", true)
	if err != nil {
		t.Fatal(err)
	}
	terminal := uuid.New()
	if err := m.InstallTerminal(pane, TerminalMeta{TerminalID: terminal, ProcessName: "zsh"}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		kind goagent.Kind
	}{{"claude", goagent.ClaudeCode}, {"pi", goagent.Pi}, {"codex", goagent.Codex}, {"opencode", goagent.OpenCode}} {
		if !m.SetTerminalForeground(terminal, tc.name) {
			t.Fatal("agent not detected", tc.name)
		}
		agents := m.Dump().Agents
		if len(agents) != 1 || agents[0].(map[string]any)["kind"] != tc.kind {
			t.Fatalf("wrong agents: %#v", agents)
		}
		if err := m.RenameAgent(pane, "custom"); err != nil {
			t.Fatal(err)
		}
		if m.SetTerminalForeground(terminal, tc.name) {
			t.Fatal("unchanged process mutated model")
		}
		if !m.SetTerminalForeground(terminal, "zsh") || len(m.Dump().Agents) != 0 {
			t.Fatal("stopped agent remained running")
		}
	}
}

func TestForegroundMetadataIgnoresExitedTerminalsAndKeepsTitles(t *testing.T) {
	m := New()
	workspace := m.CreateWorkspace("test")
	tab, pane, err := m.CreateTab(workspace, "shell", true)
	if err != nil {
		t.Fatal(err)
	}
	terminal := uuid.New()
	if err := m.InstallTerminal(pane, TerminalMeta{TerminalID: terminal, ProcessName: "zsh"}); err != nil {
		t.Fatal(err)
	}
	if err := m.RenameTab(tab, "custom"); err != nil {
		t.Fatal(err)
	}
	revision := m.Revision()
	if !m.SetTerminalForeground(terminal, "sleep") || m.Revision() != revision+1 {
		t.Fatal("foreground change did not increment revision")
	}
	if m.SetTerminalForeground(terminal, "sleep") || m.Revision() != revision+1 {
		t.Fatal("unchanged foreground increments revision")
	}
	dump := m.Dump().Workspaces[0].Tabs[0]
	if dump.TitleOverride == nil || *dump.TitleOverride != "custom" {
		t.Fatal("custom title overwritten")
	}
	m.SetTerminalExit(terminal, nil)
	revision = m.Revision()
	if m.SetTerminalForeground(terminal, "zsh") || m.SetTerminalForeground(uuid.New(), "zsh") || m.Revision() != revision {
		t.Fatal("stale/unknown foreground changed model")
	}
}
