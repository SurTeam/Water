package goui

import (
	"github.com/SurTeam/Water/internal/gomodel"
	"github.com/google/uuid"
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

func TestClosedPaneRestoresWindowMRU(t *testing.T) {
	m := gomodel.New()
	wid := m.CreateWorkspace("test")
	tab, first, _ := m.CreateTab(wid, "test", false)
	second, _ := m.SplitPane(first, "right")
	third, _ := m.SplitPane(second, "down")
	var a, b windowSelection
	for _, pane := range []uuid.UUID{first, second, third} {
		a.pending = &selectionEffect{Pane: pane}
		a.project(m.Dump())
	}
	for _, pane := range []uuid.UUID{second, first, third} {
		b.pending = &selectionEffect{Pane: pane}
		b.project(m.Dump())
	}
	if _, err := m.ClosePane(third); err != nil {
		t.Fatal(err)
	}
	if got := a.project(m.Dump()); got.FocusedPane == nil || *got.FocusedPane != second {
		t.Fatal("first window did not restore second pane")
	}
	if got := b.project(m.Dump()); got.FocusedPane == nil || *got.FocusedPane != first {
		t.Fatal("second window did not restore first pane")
	}
	if len(a.history[tab]) != 2 {
		t.Fatal("deleted pane remained in focus history")
	}
	if _, err := m.ClosePane(second); err != nil {
		t.Fatal(err)
	}
	if got := a.project(m.Dump()); got.FocusedPane == nil || *got.FocusedPane != first {
		t.Fatal("deleted MRU pane did not fall back")
	}
	if _, err := m.ClosePane(first); err != nil {
		t.Fatal(err)
	}
	got := a.project(m.Dump())
	if got.FocusedPane != nil || got.Workspace.ActiveTab != nil || len(a.history) != 0 {
		t.Fatal("final pane retained stale selection")
	}
}
