package goui

import (
	"github.com/SurTeam/Water/internal/gomodel"
	"testing"
)

func TestWindowsKeepIndependentWorkspaceTabAndPane(t *testing.T) {
	m := gomodel.New()
	w1 := m.CreateWorkspace("one")
	t1, p1, _ := m.CreateTab(w1, "one", false)
	p2, _ := m.SplitPane(p1, "right")
	w2 := m.CreateWorkspace("two")
	t2, _, _ := m.CreateTab(w2, "two", false)
	var a, b windowSelection
	a.pending = &selectionEffect{Pane: p1}
	old := a.project(m.Dump())
	b.pending = &selectionEffect{Tab: t2}
	b.project(m.Dump())
	m.FocusPane(p2)
	afterA, afterB := a.project(m.Dump()), b.project(m.Dump())
	if *afterA.ActiveWorkspace != w1 || *afterA.Workspace.ActiveTab != t1 || *afterA.FocusedPane != p1 {
		t.Fatal("other window changed first selection")
	}
	if *afterB.ActiveWorkspace != w2 || *afterB.Workspace.ActiveTab != t2 {
		t.Fatal("other window changed second selection")
	}
	a.pending = &selectionEffect{Pane: p2}
	a.project(m.Dump())
	if *old.FocusedPane != p1 || old.Workspace.Tabs[0].ActivePane != p1 {
		t.Fatal("projection mutated retained frame")
	}
	m.CloseWorkspace(w1)
	afterA = a.project(m.Dump())
	if *afterA.ActiveWorkspace != w2 {
		t.Fatal("closed workspace did not fall back")
	}
}
