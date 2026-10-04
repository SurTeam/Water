package goui

import (
	"gioui.org/io/key"
	"github.com/SurTeam/Water/internal/goconfig"
	"github.com/SurTeam/Water/internal/gomodel"
	"github.com/SurTeam/Water/internal/govt"
	"github.com/google/uuid"
	"testing"
)

func TestCreationUsesOSC7FromFocusedPane(t *testing.T) {
	m := gomodel.New()
	w := m.CreateWorkspace("test")
	_, pane, _ := m.CreateTab(w, "", false)
	id := uuid.New()
	if err := m.InstallTerminal(pane, gomodel.TerminalMeta{TerminalID: id}); err != nil {
		t.Fatal(err)
	}
	c := NewWorkspaceClientWithConnection(nil, nil, goconfig.Default(), "")
	c.state = m.Dump()
	c.terminals[id] = &terminalClient{snapshot: govt.Snapshot{WorkingDirectoryURI: "file://localhost/tmp/project%20%E4%B8%AD%E6%96%87"}}
	for _, kind := range []string{"tab.new", "pane.split"} {
		if got := c.creationCommand(kind)["cwd"]; got != "/tmp/project 中文" {
			t.Fatalf("%s directory=%v", kind, got)
		}
	}
	c.terminals[id].snapshot.WorkingDirectoryURI = "https://example.com/tmp"
	if _, exists := c.creationCommand("tab.new")["cwd"]; exists {
		t.Fatal("non-file directory accepted")
	}
}

func TestCommandNeverEncodesControlBytes(t *testing.T) {
	for _, name := range []key.Name{"C", "D", "L", key.NameUpArrow} {
		for _, modifier := range []key.Modifiers{key.ModCommand, key.ModSuper, key.ModCommand | key.ModCtrl} {
			if got := EncodeKey(key.Event{Name: name, Modifiers: modifier}, false); len(got) != 0 {
				t.Fatalf("Command key encoded as terminal input: %q", got)
			}
		}
	}
	if got := EncodeKey(key.Event{Name: "C", Modifiers: key.ModCtrl}, false); string(got) != "\x03" {
		t.Fatalf("Control+C=%q", got)
	}
}

func TestWheelKeepsFineStepsAndBulkMagnitude(t *testing.T) {
	for _, tc := range []struct {
		delta float64
		steps int
	}{{.1, 1}, {1, 1}, {40, 1}, {120, 3}, {1000, 25}, {1e9, 65536}} {
		if terminalWheelStep(tc.delta) != tc.steps || terminalWheelStep(-tc.delta) != -tc.steps {
			t.Fatalf("delta=%v", tc.delta)
		}
	}
	if terminalWheelSteps(240, 80) != 3 {
		t.Fatal("DPI changed wheel magnitude")
	}
	if terminalWheelStep(0) != 0 {
		t.Fatal("zero delta moved terminal")
	}
}
