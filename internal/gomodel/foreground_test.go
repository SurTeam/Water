package gomodel

import (
	"github.com/google/uuid"
	"testing"
)

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
