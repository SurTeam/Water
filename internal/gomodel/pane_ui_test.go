package gomodel

import (
	"math"
	"testing"
)

func TestCloseFinalPaneRemovesFinalTab(t *testing.T) {
	m := New()
	wid := m.CreateWorkspace("test")
	tab, pane, _ := m.CreateTab(wid, "test", false)
	if _, err := m.ClosePane(pane); err != nil {
		t.Fatal(err)
	}
	dump := m.Dump()
	if len(dump.Workspace.Tabs) != 0 || dump.Workspace.ActiveTab != nil || dump.FocusedPane != nil {
		t.Fatal("final tab retained")
	}
	if _, ok := m.tabs[tab]; ok {
		t.Fatal("tab retained")
	}
	if _, ok := m.panes[pane]; ok {
		t.Fatal("pane retained")
	}
}

func TestAdjustSplitAccumulatesAndClamps(t *testing.T) {
	m := New()
	wid := m.CreateWorkspace("test")
	tab, pane, _ := m.CreateTab(wid, "test", false)
	right, _ := m.SplitPane(pane, "right")
	m.SplitPane(right, "down")
	for i := 0; i < 4; i++ {
		if err := m.AdjustSplit(tab, []bool{true}, .02); err != nil {
			t.Fatal(err)
		}
	}
	if math.Abs(float64(m.tabs[tab].Root.Second.Ratio-.58)) > 1e-6 {
		t.Fatal("repeat did not accumulate")
	}
	if m.tabs[tab].Root.Ratio != .5 {
		t.Fatal("wrong split adjusted")
	}
	m.AdjustSplit(tab, []bool{true}, 1)
	if m.tabs[tab].Root.Second.Ratio != .95 {
		t.Fatal("upper clamp")
	}
	m.AdjustSplit(tab, []bool{true}, -1)
	if m.tabs[tab].Root.Second.Ratio != .05 {
		t.Fatal("lower clamp")
	}
	if err := m.AdjustSplit(tab, []bool{false}, .01); err == nil {
		t.Fatal("leaf accepted")
	}
}
