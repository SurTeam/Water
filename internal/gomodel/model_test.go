package gomodel

import (
	"encoding/json"
	"testing"

	"github.com/SurTeam/Water/internal/goprotocol"
	"github.com/google/uuid"
)

func TestWorkspaceTabPaneDump(t *testing.T) {
	m := New()
	wid := m.CreateWorkspace("Workspace 1")
	tab, pane, err := m.CreateTab(wid, "Terminal 1", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.ActivateTab(tab); err != nil {
		t.Fatal(err)
	}
	second, err := m.SplitPane(pane, "right")
	if err != nil {
		t.Fatal(err)
	}
	meta := TerminalMeta{
		TerminalID: uuid.New(),
		SessionID: uuid.New(),
		Program: "/bin/sh",
		Args: []string{"-l"},
		Size: goprotocol.TerminalSize{Columns: 80, Lines: 24},
	}
	if err := m.InstallTerminal(second, meta); err != nil {
		t.Fatal(err)
	}

	dump := m.Dump()
	if dump.Workspace == nil || len(dump.Workspace.Tabs) != 1 {
		t.Fatalf("unexpected dump: %#v", dump)
	}
	if dump.FocusedPane == nil || *dump.FocusedPane != second {
		t.Fatalf("focused pane mismatch: %#v", dump.FocusedPane)
	}
	raw, err := json.Marshal(dump)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["state_revision"] == nil {
		t.Fatal("state revision missing")
	}
}

func TestClosePaneCollapsesSplit(t *testing.T) {
	m := New()
	wid := m.CreateWorkspace("")
	_, first, err := m.CreateTab(wid, "", false)
	if err != nil { t.Fatal(err) }
	second, err := m.SplitPane(first, "down")
	if err != nil { t.Fatal(err) }
	if _, err := m.ClosePane(second); err != nil { t.Fatal(err) }
	dump := m.Dump()
	if dump.Workspace == nil || len(dump.Workspace.Tabs) != 1 {
		t.Fatal("workspace/tab missing")
	}
}


func TestExitedTerminalClosesPaneAndFinalTab(t *testing.T) {
	m:=New()
	wid:=m.CreateWorkspace("")
	_,pane,err:=m.CreateTab(wid,"",false)
	if err!=nil{t.Fatal(err)}
	term:=uuid.New()
	if err:=m.InstallTerminal(pane,TerminalMeta{
		TerminalID:term,SessionID:uuid.New(),Program:"/bin/sh",
		Size:goprotocol.TerminalSize{Columns:80,Lines:24},
	});err!=nil{t.Fatal(err)}
	m.SetTerminalExit(term,nil)
	if !m.AutoCloseExitedTerminal(term){t.Fatal("terminal pane was not closed")}
	state:=m.Dump()
	if len(state.Workspaces)!=1{t.Fatalf("workspace removed: %#v",state)}
	if len(state.Workspaces[0].Tabs)!=0{t.Fatalf("final tab remains: %#v",state.Workspaces[0])}
}
