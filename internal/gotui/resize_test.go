package gotui

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/SurTeam/Water/internal/goclient"
	"github.com/SurTeam/Water/internal/gomodel"
	"github.com/SurTeam/Water/internal/goprotocol"
	"github.com/SurTeam/Water/internal/govt"
	"github.com/google/uuid"
)

func TestNarrowSelectsEveryLeafBeforeSplitLayout(t *testing.T) {
	a := newApp(nil, simulation(t, 12, 4), 100)
	defer a.close()
	deepest := &tree{Type: "leaf", Pane: uuid.New()}
	root := deepest
	for depth := 0; depth < 18; depth++ {
		axis := "horizontal"
		if depth%2 != 0 {
			axis = "vertical"
		}
		root = &tree{Type: "split", Axis: axis, Ratio: .95, First: &tree{Type: "leaf", Pane: uuid.New()}, Second: root}
	}
	raw, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	wid, tid := uuid.New(), uuid.New()
	a.workspace, a.tab = wid, tid
	a.state.Workspaces = []gomodel.WorkspaceDump{{ID: wid, Tabs: []gomodel.TabDump{{ID: tid, Tree: raw, ActivePane: deepest.Pane}}}}
	// The old narrow implementation first ran this pruned wide layout and could
	// never select its deepest leaves, even though narrow mode has a full view.
	a.layout(root, rect{0, 1, 12, 2}, nil)
	for _, p := range a.panes {
		if p.id == deepest.Pane {
			t.Fatal("fixture did not prune the deepest leaf")
		}
	}
	walk(root, func(leaf *tree) {
		a.focus = leaf.Pane
		a.draw()
		if len(a.panes) != 1 || a.panes[0].id != leaf.Pane || a.focus != leaf.Pane {
			t.Fatalf("narrow selection lost leaf %s: focus=%s panes=%+v", leaf.Pane, a.focus, a.panes)
		}
		if got, want := a.panes[0].box, (rect{0, 1, 12, 2}); got != want {
			t.Fatalf("leaf viewport=%+v want=%+v", got, want)
		}
		if len(a.dividers) != 0 {
			t.Fatal("narrow mode retained split dividers")
		}
	})
	a.focus = uuid.New() // An expired client selection falls back to the tab leaf.
	a.draw()
	if a.focus != deepest.Pane || a.panes[0].id != deepest.Pane {
		t.Fatal("full-tree fallback did not select the active pane")
	}
}

func TestRemoteResizeUpdatesGeometryWithoutFeedback(t *testing.T) {
	a := newApp(nil, simulation(t, 80, 24), 100)
	defer a.close()
	id := uuid.New()
	terminal := &terminal{emu: govt.New(80, 24, 100), seq: 1, cols: 80, rows: 24}
	a.terms[id] = terminal
	a.setFocus(true)
	a.resizeTerminal(id, terminal, 60, 15)
	if len(a.commands) != 1 {
		t.Fatalf("initial resize count=%d", len(a.commands))
	}
	<-a.commands
	a.apply(goclient.TerminalPush{TerminalID: id, Event: goprotocol.TerminalEvent{Kind: goprotocol.ResizeEvent, Seq: 2, Size: goprotocol.TerminalSize{Columns: 100, Lines: 32}}})
	snapshot := terminal.emu.FrameSnapshot()
	if terminal.cols != 100 || terminal.rows != 32 || snapshot.Cols != 100 || snapshot.Rows != 32 {
		t.Fatalf("remote geometry stale: cached=%dx%d emulator=%dx%d", terminal.cols, terminal.rows, snapshot.Cols, snapshot.Rows)
	}
	for i := 0; i < 100; i++ {
		a.resizeTerminal(id, terminal, 60, 15)
	}
	if len(a.commands) != 0 {
		t.Fatalf("remote resize caused feedback: %d queued commands", len(a.commands))
	}
	a.setFocus(false)
	a.resizeTerminal(id, terminal, 50, 12)
	if len(a.commands) != 0 {
		t.Fatal("background TUI tried to change the PTY grid")
	}
	a.setFocus(true)
	for i := 0; i < 100; i++ {
		a.resizeTerminal(id, terminal, 50, 12)
	}
	if len(a.commands) != 1 {
		t.Fatalf("focus regain should request exactly once: %d", len(a.commands))
	}
	<-a.commands
	a.apply(goclient.TerminalPush{TerminalID: id, Event: goprotocol.TerminalEvent{Kind: goprotocol.ResizeEvent, Seq: 3, Size: goprotocol.TerminalSize{Columns: 50, Lines: 12}}})
	if terminal.remoteGeometry {
		t.Fatal("own resize acknowledgement treated as a competing resize")
	}
	a.resizeTerminal(id, terminal, 50, 12)
	if len(a.commands) != 0 {
		t.Fatal("own resize acknowledgement caused another request")
	}
	a.apply(goclient.TerminalPush{TerminalID: id, Event: goprotocol.TerminalEvent{Kind: goprotocol.ResizeEvent, Seq: 4, Size: goprotocol.TerminalSize{Columns: 90, Lines: 30}}})
	a.focusActivity()
	a.resizeTerminal(id, terminal, 50, 12)
	for i := 0; i < 100; i++ {
		a.focusActivity()
		a.resizeTerminal(id, terminal, 50, 12)
	}
	if len(a.commands) != 1 {
		t.Fatalf("input should reclaim geometry only once: %d", len(a.commands))
	}
	<-a.commands
	for i := 0; i < 100; i++ {
		a.resizeTerminal(id, terminal, 1000, 1000)
	}
	if len(a.commands) != 1 {
		t.Fatalf("normalized geometry retriggered: %d", len(a.commands))
	}
	request := <-a.commands
	if request["columns"] != 512 || request["lines"] != 256 {
		t.Fatalf("request was not normalized: %v", request)
	}

	// Without outer-terminal focus reports, a background layout request can be
	// ignored by the server. Real input must still reclaim once, even after that
	// layout request recorded the desired grid locally.
	a.apply(goclient.TerminalPush{TerminalID: id, Event: goprotocol.TerminalEvent{Kind: goprotocol.ResizeEvent, Seq: 5, Size: goprotocol.TerminalSize{Columns: 70, Lines: 20}}})
	a.resizeTerminal(id, terminal, 32, 8)
	if len(a.commands) != 1 {
		t.Fatal("layout change did not request geometry")
	}
	<-a.commands
	a.focusActivity()
	a.resizeTerminal(id, terminal, 32, 8)
	if len(a.commands) != 1 {
		t.Fatal("actual input did not reassert an ignored layout request")
	}
	<-a.commands
	for i := 0; i < 100; i++ {
		a.focusActivity()
		a.resizeTerminal(id, terminal, 32, 8)
	}
	if len(a.commands) != 0 {
		t.Fatal("unacknowledged focus claim retriggered on every input")
	}
}

func TestTUIResizeRespectsServerFocusOwner(t *testing.T) {
	session, socket := fixture(t)
	var state gomodel.StateDump
	if err := session.Call("state.dump", nil, &state); err != nil {
		t.Fatal(err)
	}
	var root tree
	if err := json.Unmarshal(state.Workspaces[0].Tabs[0].Tree, &root); err != nil {
		t.Fatal(err)
	}
	id := root.Terminal.Summary.ID
	var initial attached
	if err := session.Attach(id, &initial); err != nil {
		t.Fatal(err)
	}
	waitSize := func(cols, rows int) {
		t.Helper()
		timer := time.NewTimer(3 * time.Second)
		defer timer.Stop()
		for {
			select {
			case p := <-session.Events:
				if p.TerminalID == id && p.Event.Kind == goprotocol.ResizeEvent && p.Event.Size.Columns == cols && p.Event.Size.Lines == rows {
					return
				}
			case <-timer.C:
				t.Fatalf("timed out waiting for ordered resize %dx%d", cols, rows)
			}
		}
	}
	other, err := goclient.New(socket).OpenSession()
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if err := other.Call("session.focus", map[string]any{"focused": true}, nil); err != nil {
		t.Fatal(err)
	}
	if err := other.Dispatch(map[string]any{"type": "terminal.resize", "terminal_id": id, "columns": 90, "lines": 30}, nil); err != nil {
		t.Fatal(err)
	}
	waitSize(90, 30)
	var ignored struct {
		Ignored bool `json:"ignored"`
	}
	if err := session.Dispatch(map[string]any{"type": "terminal.resize", "terminal_id": id, "columns": 60, "lines": 15}, &ignored); err != nil {
		t.Fatal(err)
	}
	if !ignored.Ignored {
		t.Fatal("background TUI resize bypassed the focused owner")
	}
	var replay attached
	if err := session.Attach(id, &replay); err != nil {
		t.Fatal(err)
	}
	if replay.Size.Columns != 90 || replay.Size.Lines != 30 {
		t.Fatalf("background TUI changed shared geometry: %+v", replay.Size)
	}
	if err := session.Call("session.focus", map[string]any{"focused": true}, nil); err != nil {
		t.Fatal(err)
	}
	if err := session.Dispatch(map[string]any{"type": "terminal.resize", "terminal_id": id, "columns": 60, "lines": 15}, nil); err != nil {
		t.Fatal(err)
	}
	waitSize(60, 15)
	if err := session.Attach(id, &replay); err != nil {
		t.Fatal(err)
	}
	if replay.Size.Columns != 60 || replay.Size.Lines != 15 {
		t.Fatalf("focused TUI could not claim shared geometry: %+v", replay.Size)
	}
}
