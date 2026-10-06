package gomodel

import (
	"github.com/google/uuid"
	"testing"
)

func TestRecoveryPreservesTopologyAndRejectsCorruption(t *testing.T) {
	m := New()
	w := m.CreateWorkspace("work")
	tab, pane, err := m.CreateTab(w, "one", false)
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.SplitPane(pane, "right")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.SplitPane(second, "down"); err != nil {
		t.Fatal(err)
	}
	d := m.ExportLayout(uuid.NewString())
	d.Workspaces[0].Tabs[0].Tree.Ratio = .05
	restored, panes, err := RecoveryModel(d)
	if err != nil {
		t.Fatal(err)
	}
	if len(panes) != 3 {
		t.Fatalf("panes=%d", len(panes))
	}
	dump := restored.Dump()
	if dump.Workspaces[0].ID != w || dump.Workspaces[0].Tabs[0].ID != tab || dump.Workspaces[0].Title != "work" {
		t.Fatalf("lost identity/order/title: %+v", dump)
	}
	tree := d.Workspaces[0].Tabs[0].Tree
	tree.Second.First.PaneID = tree.First.PaneID
	if err := d.Validate(); err == nil {
		t.Fatal("duplicate pane accepted")
	}
	if err := m.AdoptRecovery(restored); err == nil {
		t.Fatal("existing workspace overwritten")
	}
}
