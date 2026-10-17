package goui

import (
	"image"
	"image/color"
	"testing"
	"time"

	"github.com/SurTeam/Water/internal/goclient"
	"github.com/SurTeam/Water/internal/goconfig"
	"github.com/SurTeam/Water/internal/gomodel"
	"github.com/google/uuid"
)

func TestWorkspaceCloseConfirmationPinsTarget(t *testing.T) {
	socket, stop := startWorkspaceTestServer(t)
	defer stop()
	rpc := goclient.New(socket)
	session, err := rpc.OpenSession()
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	var state gomodel.StateDump
	if err := rpc.Call("state.dump", nil, &state); err != nil {
		t.Fatal(err)
	}
	target := state.Workspaces[0].ID
	// A tab with no terminal must still require confirmation.
	state.Workspaces[0].Tabs = []gomodel.TabDump{{ID: uuid.New()}}
	c := NewWorkspaceClientWithConfig(session, nil, goconfig.Default())
	c.state = state
	if !c.dispatchShortcut("ctrl-alt-w") || c.workspaceClose == nil {
		t.Fatal("close shortcut did not prompt for a nonterminal tab")
	}
	other := uuid.New()
	c.state.ActiveWorkspace = &other
	c.requestWorkspaceClose(other)
	if c.workspaceClose.ID != target {
		t.Fatal("active workspace change retargeted confirmation")
	}
	c.dispatchShortcut("cmd-shift-n")
	if c.workspaceClose.ID != target {
		t.Fatal("modal shortcut retargeted confirmation")
	}
	c.handleWorkspaceCloseKey("escape")
	if c.workspaceClose != nil {
		t.Fatal("escape did not cancel")
	}
	if err := rpc.Call("state.dump", nil, &state); err != nil {
		t.Fatal(err)
	}
	if len(state.Workspaces) != 1 {
		t.Fatal("cancel mutated server state")
	}
	c.state = state
	c.state.Workspaces[0].Tabs = []gomodel.TabDump{{ID: uuid.New()}}
	c.requestWorkspaceClose(target)
	c.state.ActiveWorkspace = &other
	c.handleWorkspaceCloseKey("return")
	deadline := time.Now().Add(time.Second)
	for {
		if err := rpc.Call("state.dump", nil, &state); err != nil {
			t.Fatal(err)
		}
		if len(state.Workspaces) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("confirm did not close pinned workspace")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestWorkspaceCloseEmptyAndMissing(t *testing.T) {
	socket, stop := startWorkspaceTestServer(t)
	defer stop()
	rpc := goclient.New(socket)
	session, err := rpc.OpenSession()
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	c := NewWorkspaceClientWithConfig(session, nil, goconfig.Default())
	if err := rpc.Call("state.dump", nil, &c.state); err != nil {
		t.Fatal(err)
	}
	c.requestWorkspaceClose(uuid.New())
	if c.workspaceClose != nil {
		t.Fatal("missing workspace prompted")
	}
	c.requestWorkspaceClose(c.state.Workspaces[0].ID)
	if c.workspaceClose != nil {
		t.Fatal("empty workspace requires no confirmation")
	}
	deadline := time.Now().Add(time.Second)
	for {
		var state gomodel.StateDump
		if err := rpc.Call("state.dump", nil, &state); err != nil {
			t.Fatal(err)
		}
		if len(state.Workspaces) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("empty workspace was not closed")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestWorkspaceCloseSidebarAndSettings(t *testing.T) {
	cfg := goconfig.Default()
	cfg.Shortcuts.CloseWorkspace = "ctrl-alt-x"
	c := NewWorkspaceClientWithConfig(nil, nil, cfg)
	model := gomodel.New()
	target := model.CreateWorkspace("Project")
	if _, _, err := model.CreateTab(target, "Tab", false); err != nil {
		t.Fatal(err)
	}
	c.state = model.Dump()
	manager := NewMultiWorkspaceClient(nil)
	defer manager.Close()
	connection := uuid.New()
	if err := manager.AddConnection(ConnectionEntry{ID: connection, Kind: "local"}, c, nil, true); err != nil {
		t.Fatal(err)
	}
	w := &EbitengineWindow{multi: manager, scale: 1, views: map[*WorkspaceClient]*nativeView{}}
	w.drawSidebar(c, nil, image.Rect(0, 0, 240, 800), color.NRGBA{})
	found := false
	for _, hit := range c.hitRegions {
		if hit.Kind == hitCloseWorkspace {
			found = hit.ID == target && hit.Label == connection.String() && !hit.Rect.Empty()
		}
	}
	if !found {
		t.Fatal("sidebar lacks owning-connection close target")
	}
	if !w.key(c, "ctrl-alt-x") || c.workspaceClose == nil {
		t.Fatal("configured native shortcut did not prompt")
	}
	w.key(c, "escape")
	if c.workspaceClose != nil {
		t.Fatal("native escape did not cancel")
	}
	c.openSettingsConfig(cfg)
	found = false
	for _, field := range c.settings.fields {
		if field.name == "CloseWorkspace" {
			found = true
		}
	}
	if !found {
		t.Fatal("settings lacks close workspace binding")
	}
}
