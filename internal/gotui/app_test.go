package gotui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SurTeam/Water/internal/goclient"
	"github.com/SurTeam/Water/internal/goconfig"
	"github.com/SurTeam/Water/internal/gomodel"
	"github.com/SurTeam/Water/internal/goprotocol"
	"github.com/SurTeam/Water/internal/goserver"
	"github.com/SurTeam/Water/internal/govt"
	"github.com/gdamore/tcell/v2"
	"github.com/google/uuid"
)

func simulation(t *testing.T, w, h int) tcell.SimulationScreen {
	t.Helper()
	s := tcell.NewSimulationScreen("UTF-8")
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	s.SetSize(w, h)
	t.Cleanup(s.Fini)
	return s
}
func queuedBytes(t *testing.T, a *app) string {
	t.Helper()
	select {
	case c := <-a.commands:
		b, ok := c["bytes"].([]int)
		if !ok {
			t.Fatalf("not bytes: %v", c)
		}
		out := make([]byte, len(b))
		for i, v := range b {
			out[i] = byte(v)
		}
		return string(out)
	default:
		t.Fatal("no input dispatched")
		return ""
	}
}
func TestInputModesAndPaste(t *testing.T) {
	for _, tc := range []struct {
		key         tcell.Key
		r           rune
		mod         tcell.ModMask
		application bool
		want        string
	}{
		{tcell.KeyUp, 0, 0, false, "\x1b[A"}, {tcell.KeyUp, 0, 0, true, "\x1bOA"}, {tcell.KeyLeft, 0, tcell.ModCtrl, true, "\x1b[1;5D"}, {tcell.KeyRune, 'é', 0, false, "é"}, {tcell.KeyRune, 'x', tcell.ModAlt, false, "\x1bx"}, {tcell.KeyCtrlC, 0, 0, false, "\x03"}, {tcell.KeyF5, 0, 0, false, "\x1b[15~"}, {tcell.KeyEnter, 0, 0, false, "\r"},
	} {
		if got := string(keyBytes(tcell.NewEventKey(tc.key, tc.r, tc.mod), tc.application)); got != tc.want {
			t.Errorf("key %v: %q != %q", tc.key, got, tc.want)
		}
	}
	a := newApp(nil, simulation(t, 80, 24), 100)
	defer a.close()
	id, pid := uuid.New(), uuid.New()
	e := govt.New(20, 4, 100)
	e.Write([]byte("\x1b[?2004h"))
	a.terms[id] = &terminal{emu: e}
	a.focus = pid
	a.panes = []pane{{id: pid, term: id, box: rect{0, 0, 20, 5}}}
	a.paste = []byte("one\ntwo")
	a.sendPaste()
	if got := queuedBytes(t, a); got != "\x1b[200~one\ntwo\x1b[201~" {
		t.Fatalf("paste=%q", got)
	}
	a.key(tcell.NewEventKey(tcell.KeyCtrlB, 0, 0))
	a.key(tcell.NewEventKey(tcell.KeyCtrlB, 0, 0))
	if got := queuedBytes(t, a); got != "\x02" {
		t.Fatalf("prefix forwarding=%q", got)
	}
}
func TestRecursiveAndNarrowLayout(t *testing.T) {
	a := newApp(nil, simulation(t, 120, 30), 100)
	defer a.close()
	one, two, three := uuid.New(), uuid.New(), uuid.New()
	root := tree{Type: "split", Axis: "horizontal", Ratio: .5, First: &tree{Type: "leaf", Pane: one}, Second: &tree{Type: "split", Axis: "vertical", Ratio: .5, First: &tree{Type: "leaf", Pane: two}, Second: &tree{Type: "leaf", Pane: three}}}
	raw, _ := json.Marshal(root)
	wid, tid := uuid.New(), uuid.New()
	a.workspace = wid
	a.tab = tid
	a.focus = three
	a.state.Workspaces = []gomodel.WorkspaceDump{{ID: wid, Title: "work", Tabs: []gomodel.TabDump{{ID: tid, Tree: raw, ActivePane: one}}}}
	a.draw()
	if len(a.panes) != 3 || len(a.dividers) != 2 {
		t.Fatalf("panes=%v dividers=%v", a.panes, a.dividers)
	}
	for _, p := range a.panes {
		if p.box.w < 2 || p.box.h < 2 {
			t.Fatalf("invalid geometry: %+v", p)
		}
	}
	if len(a.dividers[1].path) != 1 || !a.dividers[1].path[0] {
		t.Fatalf("nested split path=%v", a.dividers[1].path)
	}
	a.screen.(tcell.SimulationScreen).SetSize(60, 20)
	a.draw()
	if len(a.panes) != 1 || a.panes[0].id != three || a.panes[0].box.w != 60 {
		t.Fatalf("narrow view=%+v", a.panes)
	}
	a.cyclePane()
	if a.focus != one {
		t.Fatalf("narrow pane cycle=%v", a.focus)
	}
}
func TestMouseCoordinatesAndRelease(t *testing.T) {
	a := newApp(nil, simulation(t, 120, 30), 100)
	defer a.close()
	id, pid := uuid.New(), uuid.New()
	e := govt.New(40, 10, 100)
	e.Write([]byte("\x1b[?1000h\x1b[?1006h"))
	a.terms[id] = &terminal{emu: e}
	a.focus = pid
	a.panes = []pane{{id: pid, term: id, box: rect{25, 4, 40, 11}}}
	a.mouse(tcell.NewEventMouse(29, 10, tcell.Button1, 0))
	if got := queuedBytes(t, a); got != "\x1b[<0;5;6M" {
		t.Fatalf("press local coordinates=%q", got)
	}
	a.mouse(tcell.NewEventMouse(29, 10, tcell.ButtonNone, 0))
	if got := queuedBytes(t, a); got != "\x1b[<0;5;6m" {
		t.Fatalf("release=%q", got)
	}
	a.mouse(tcell.NewEventMouse(29, 10, tcell.WheelUp, 0))
	if got := queuedBytes(t, a); got != "\x1b[<64;5;6M" {
		t.Fatalf("wheel=%q", got)
	}
}
func TestReplayOverlapAndResponseRouting(t *testing.T) {
	a := newApp(nil, simulation(t, 80, 24), 100)
	defer a.close()
	id := uuid.New()
	target := &terminal{}
	a.terms[id] = target
	a.apply(goclient.TerminalPush{TerminalID: id, Event: goprotocol.TerminalEvent{Kind: goprotocol.OutputEvent, Seq: 10, Data: []byte("live")}})
	e := govt.New(40, 5, 100)
	e.WriteReplay([]byte("base\x1b[6n"))
	if len(e.TakeResponses()) != 0 {
		t.Fatal("replay generated responses")
	}
	a.finishAttach(attachResult{id: id, target: target, response: attached{Last: 9, Size: goprotocol.TerminalSize{Columns: 40, Lines: 5}}, emu: e})
	a.apply(goclient.TerminalPush{TerminalID: id, Event: goprotocol.TerminalEvent{Kind: goprotocol.OutputEvent, Seq: 10, Data: []byte("duplicate")}})
	if text := e.Text(); !strings.Contains(text, "baselive") || strings.Contains(text, "duplicate") {
		t.Fatalf("replay overlap: %q", text)
	}
	a.apply(goclient.TerminalPush{TerminalID: id, Event: goprotocol.TerminalEvent{Kind: goprotocol.OutputEvent, Seq: 11, Data: []byte("\x1b[6n")}})
	if got := queuedBytes(t, a); !strings.HasPrefix(got, "\x1b[") || !strings.HasSuffix(got, "R") {
		t.Fatalf("query response=%q", got)
	}
}
func fixture(t *testing.T) (*goclient.Session, string) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "water-tui-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "server.sock")
	cfg := goconfig.Default()
	cfg.Shell.Program = "/bin/sh"
	cfg.Shell.Args = []string{"-i"}
	srv := goserver.NewWithConfig(socket, cfg)
	if err := srv.Initialize(true, true); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- srv.ListenAndServe() }()
	t.Cleanup(func() {
		srv.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("server did not stop")
		}
	})
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	client := goclient.New(socket)
	for {
		session, err := client.OpenTUISession()
		if err == nil {
			t.Cleanup(func() { session.Close() })
			return session, socket
		}
		select {
		case <-deadline.C:
			t.Fatal(err)
		case <-ticker.C:
		}
	}
}
func TestServerPTYReplayLiveAndDetach(t *testing.T) {
	session, socket := fixture(t)
	var connections any
	if err := goclient.New(socket).CallTimeout("connection.list", nil, &connections, time.Second); err != nil {
		t.Fatal("TUI hijacked GUI control routing:", err)
	}
	if err := goclient.New(socket).CallTimeout("ui.snapshot", nil, nil, time.Second); err == nil {
		t.Fatal("TUI incorrectly advertised GUI automation")
	}

	a := newApp(session, simulation(t, 80, 24), 100)
	defer a.close()
	if err := session.Call("state.dump", nil, &a.state); err != nil {
		t.Fatal(err)
	}
	a.syncState()
	tab := a.activeTab()
	if tab == nil {
		t.Fatal("no shell tab")
	}
	var root tree
	_ = json.Unmarshal(tab.Tree, &root)
	id := root.Terminal.Summary.ID
	a.attach(id)
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()
	ready := false
	for !ready {
		select {
		case r := <-a.attachments:
			a.finishAttach(r)
			if r.err != nil {
				t.Fatal(r.err)
			}
			ready = true
		case p := <-session.Events:
			a.apply(p)
		case <-timeout.C:
			t.Fatal("attach timeout")
		}
	}
	marker := "water_tui_live_" + uuid.NewString()
	if err := session.Dispatch(map[string]any{"type": "terminal.send_text", "terminal_id": id, "text": fmt.Sprintf("printf 'water_tui_live_%%s\\n' '%s'\n", strings.TrimPrefix(marker, "water_tui_live_"))}, nil); err != nil {
		t.Fatal(err)
	}
	for !strings.Contains(a.terms[id].emu.Text(), marker) {
		select {
		case p := <-session.Events:
			a.apply(p)
		case <-timeout.C:
			t.Fatal("live PTY output timeout")
		}
	}
	if err := session.Dispatch(map[string]any{"type": "workspace.new"}, nil); err != nil {
		t.Fatal(err)
	}
	sawSnapshot, sawSelection := false, false
	for !sawSnapshot || !sawSelection {
		select {
		case msg := <-session.Pushes:
			if msg.Method == "push.snapshot" {
				var s gomodel.StateDump
				_ = json.Unmarshal(msg.Params, &s)
				if len(s.Workspaces) == 2 {
					sawSnapshot = true
				}
			}
			if msg.Method == "push.selection" {
				sawSelection = true
			}
		case <-timeout.C:
			t.Fatal("TUI role missing snapshot/selection")
		}
	}
	session.Close()
	reconnected, err := goclient.New(socket).OpenTUISession()
	if err != nil {
		t.Fatal("detached server unavailable:", err)
	}
	defer reconnected.Close()
	var state gomodel.StateDump
	if err := reconnected.Call("state.dump", nil, &state); err != nil {
		t.Fatal(err)
	}
	if len(state.Workspaces) != 2 {
		t.Fatalf("detach lost model: %+v", state)
	}
}

func TestSelectorTouchAndSplitDrag(t *testing.T) {
	a := newApp(nil, simulation(t, 100, 24), 100)
	defer a.close()
	a.tab = uuid.New()
	a.dividers = []divider{{box: rect{40, 1, 1, 20}, parent: rect{22, 1, 78, 20}, axis: "horizontal", path: []bool{true, false}}}
	a.mouse(tcell.NewEventMouse(40, 8, tcell.Button1, 0))
	a.mouse(tcell.NewEventMouse(70, 8, tcell.Button1, 0))
	a.mouse(tcell.NewEventMouse(70, 8, tcell.ButtonNone, 0))
	if a.dragging != nil {
		t.Fatal("drag did not release")
	}
	c := <-a.commands
	if c["type"] != "pane.resize_split" || c["tab_id"] != a.tab {
		t.Fatalf("split routing=%v", c)
	}
	path := c["path"].([]bool)
	if len(path) != 2 || !path[0] || path[1] {
		t.Fatalf("nested drag path=%v", path)
	}
	ratio := c["ratio"].(float64)
	if ratio < .6 || ratio > .7 {
		t.Fatalf("drag ratio=%f", ratio)
	}
	for len(a.commands) > 0 {
		<-a.commands
	}
	id := uuid.New()
	a.menu = true
	a.menuMode = "panes"
	a.hits = []hit{{box: rect{0, 0, 30, 1}, kind: "choose-pane", id: id}}
	a.mouse(tcell.NewEventMouse(2, 0, tcell.Button1, 0))
	a.mouse(tcell.NewEventMouse(2, 0, tcell.ButtonNone, 0))
	if a.focus != id || a.menu {
		t.Fatalf("touch picker focus=%s menu=%v", a.focus, a.menu)
	}
	c = <-a.commands
	if c["type"] != "pane.focus" || c["pane_id"] != id {
		t.Fatalf("touch command=%v", c)
	}
}
func TestSelectionEffectWaitsForSnapshot(t *testing.T) {
	a := newApp(nil, simulation(t, 80, 24), 100)
	defer a.close()
	wid, tid, pid := uuid.New(), uuid.New(), uuid.New()
	raw, _ := json.Marshal(selectionEffect{Pane: pid})
	a.selection(raw)
	if a.pendingSelection == nil {
		t.Fatal("lost selection before model snapshot")
	}
	treeRaw, _ := json.Marshal(tree{Type: "leaf", Pane: pid})
	a.state.Workspaces = []gomodel.WorkspaceDump{{ID: wid, Tabs: []gomodel.TabDump{{ID: tid, Tree: treeRaw}}}}
	a.syncState()
	if a.workspace != wid || a.tab != tid || a.focus != pid || a.pendingSelection != nil {
		t.Fatal("pending pane was not resolved")
	}
}

func TestTopSelectors(t *testing.T) {
	for _, width := range []int{60, 89, 90, 120} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			a := newApp(nil, simulation(t, width, 24), 100)
			defer a.close()
			wid, other, tid := uuid.New(), uuid.New(), uuid.New()
			a.workspace, a.tab = wid, tid
			a.state.Workspaces = []gomodel.WorkspaceDump{
				{ID: wid, Title: "work", Tabs: []gomodel.TabDump{{ID: tid, Title: "shell"}}},
				{ID: other, Title: "other", Tabs: []gomodel.TabDump{{ID: uuid.New(), Title: "hidden"}}},
			}
			if width >= 90 {
				a.draw()
				found := false
				for _, h := range a.hits {
					if h.kind == "menu:s" || h.kind == "menu:T" {
						t.Fatal("wide layout has selector buttons")
					}
					if h.kind == "tab" && h.id == tid && h.box.y == 0 {
						found = true
					}
				}
				if !found {
					t.Fatal("wide layout missing tab strip")
				}
				return
			}
			for _, tc := range []struct {
				kind, mode string
				count      int
			}{{"menu:s", "workspaces", 2}, {"menu:T", "tabs", 1}} {
				a.draw()
				var button *hit
				for i := range a.hits {
					h := &a.hits[i]
					if h.kind == "tab" && h.box.y == 0 {
						t.Fatal("top bar still directly selects tabs")
					}
					if h.kind == tc.kind && h.box.y == 0 {
						button = h
					}
				}
				if button == nil {
					t.Fatalf("missing %s button", tc.kind)
				}
				a.mouse(tcell.NewEventMouse(button.box.x, 0, tcell.Button1, 0))
				a.mouse(tcell.NewEventMouse(button.box.x, 0, tcell.ButtonNone, 0))
				a.draw()
				if !a.menu || a.menuMode != tc.mode || len(a.menuItems) != tc.count {
					t.Fatalf("selector: open=%v mode=%s items=%v", a.menu, a.menuMode, a.menuItems)
				}
				a.key(tcell.NewEventKey(tcell.KeyEscape, 0, 0))
			}
		})
	}
}

func TestTabTitle(t *testing.T) {
	active := uuid.New()
	custom := "My task"
	for _, tc := range []struct {
		name, process, agent string
		override             *string
		invalid              bool
		want                 string
	}{
		{name: "custom", process: "zsh", agent: "Codex", override: &custom, want: custom},
		{name: "agent", process: "zsh", agent: "Codex", want: "Codex"},
		{name: "process", process: "zsh", want: "zsh"},
		{name: "default", want: "Terminal 123"},
		{name: "invalid tree", invalid: true, want: "Terminal 123"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			leaf := func(id uuid.UUID, process, agent string) map[string]any {
				return map[string]any{"type": "leaf", "pane_id": id, "terminal": map[string]any{"summary": map[string]any{"process_name": process, "agent_name": agent}}}
			}
			raw, _ := json.Marshal(map[string]any{"type": "split", "First": leaf(uuid.New(), "wrong", "wrong agent"), "Second": leaf(active, tc.process, tc.agent)})
			if tc.invalid {
				raw = []byte("invalid")
			}
			tab := gomodel.TabDump{Title: "Terminal 123", TitleOverride: tc.override, ActivePane: active, Tree: raw}
			if got := tabTitle(tab); got != tc.want {
				t.Fatalf("title=%q want=%q", got, tc.want)
			}
			a := newApp(nil, simulation(t, 60, 24), 100)
			defer a.close()
			a.workspace = uuid.New()
			a.state.Workspaces = []gomodel.WorkspaceDump{{ID: a.workspace, Tabs: []gomodel.TabDump{tab}}}
			a.menuMode = "tabs"
			a.drawMenu(60, 24)
			if len(a.menuItems) != 1 || a.menuItems[0].label != tc.want {
				t.Fatalf("tabs popup=%v", a.menuItems)
			}
		})
	}
}

func TestNarrowCommandShortcutAndBottomPrompt(t *testing.T) {
	for _, width := range []int{40, 60, 89, 90} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			a := newApp(nil, simulation(t, width, 24), 100)
			defer a.close()
			wid, tid, pid := uuid.New(), uuid.New(), uuid.New()
			raw, _ := json.Marshal(tree{Type: "leaf", Pane: pid})
			a.workspace, a.tab = wid, tid
			a.state.Workspaces = []gomodel.WorkspaceDump{{ID: wid, Tabs: []gomodel.TabDump{{ID: tid, ActivePane: pid, Tree: raw}}}}
			a.draw()
			wantHeight := 23
			if width >= 90 {
				wantHeight = 22
			}
			if len(a.panes) != 1 || a.panes[0].box.h != wantHeight {
				t.Fatalf("pane geometry=%v", a.panes)
			}
			var shortcut *hit
			for i := range a.hits {
				h := &a.hits[i]
				if h.kind == "menu:?" {
					shortcut = h
				}
			}
			if shortcut == nil {
				t.Fatal("missing command shortcut")
			}
			if width < 90 && (shortcut.box.y != 0 || shortcut.box.x+shortcut.box.w != width) {
				t.Fatalf("shortcut not at top right: %v", shortcut)
			}
			a.mouse(tcell.NewEventMouse(shortcut.box.x, shortcut.box.y, tcell.Button1, 0))
			a.mouse(tcell.NewEventMouse(shortcut.box.x, shortcut.box.y, tcell.ButtonNone, 0))
			a.draw()
			if !a.menu || a.menuMode != "" || len(a.menuItems) < 20 {
				t.Fatalf("full command menu missing: %v", a.menuItems)
			}
			a.action('x')
			a.draw()
			if a.panes[0].box.h != 22 {
				t.Fatalf("confirmation did not reserve bottom row: %v", a.panes)
			}
			var line strings.Builder
			for x := 0; x < width; x++ {
				r, _, _, _ := a.screen.GetContent(x, 23)
				line.WriteRune(r)
			}
			if !strings.Contains(line.String(), "Close pane? yes>") {
				t.Fatalf("confirmation missing from bottom: %q", line.String())
			}
			a.key(tcell.NewEventKey(tcell.KeyEscape, 0, 0))
			a.draw()
			if a.panes[0].box.h != wantHeight {
				t.Fatal("bottom row not reclaimed after dismissing confirmation")
			}
		})
	}
}

func TestNarrowBarTitleDoesNotCoverCommands(t *testing.T) {
	for _, width := range []int{10, 12, 24, 40, 60, 89} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			a := newApp(nil, simulation(t, width, 24), 100)
			defer a.close()
			wid, tid, pid := uuid.New(), uuid.New(), uuid.New()
			title := strings.Repeat("中文tab", 30)
			raw, _ := json.Marshal(tree{Type: "leaf", Pane: pid})
			a.workspace, a.tab = wid, tid
			a.state.Workspaces = []gomodel.WorkspaceDump{{ID: wid, Tabs: []gomodel.TabDump{{ID: tid, Title: title, Tree: raw, ActivePane: pid}}}}
			a.draw()
			var buttons []hit
			for _, h := range a.hits {
				if h.box.y == 0 {
					buttons = append(buttons, h)
				}
			}
			if len(buttons) != 3 {
				t.Fatalf("buttons=%v", buttons)
			}
			for i, button := range buttons {
				if button.box.w <= 0 || button.box.x+button.box.w > width {
					t.Fatalf("invalid button=%v", button)
				}
				if i > 0 && buttons[i-1].box.x+buttons[i-1].box.w > button.box.x {
					t.Fatalf("overlap=%v", buttons)
				}
			}
			r, _, _, _ := a.screen.GetContent(buttons[2].box.x, 0)
			if r != '[' {
				t.Fatalf("commands bracket overwritten: %q", r)
			}
			if !a.panes[0].headerless || a.panes[0].contentBox() != a.panes[0].box {
				t.Fatal("narrow terminal still reserves a title row")
			}
			if width == 10 {
				var line strings.Builder
				for x := 0; x < width; x++ {
					r, _, _, _ := a.screen.GetContent(x, 0)
					line.WriteRune(r)
				}
				if line.String() != "[W][T] [C]" {
					t.Fatalf("compact bar=%q", line.String())
				}
			}
		})
	}
}

func TestCommandButtonPrefixHighlight(t *testing.T) {
	for _, width := range []int{10, 60} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			a := newApp(nil, simulation(t, width, 24), 100)
			defer a.close()
			check := func(want tcell.Style) {
				t.Helper()
				a.draw()
				for _, h := range a.hits {
					if h.kind != "menu:?" || h.box.y != 0 {
						continue
					}
					for x := h.box.x; x < h.box.x+h.box.w; x++ {
						_, _, style, _ := a.screen.GetContent(x, 0)
						if style != want {
							t.Fatalf("button style=%v want=%v", style, want)
						}
					}
					return
				}
				t.Fatal("missing Commands button")
			}
			check(chrome)
			a.key(tcell.NewEventKey(tcell.KeyCtrlB, 0, 0))
			check(selected)
			a.key(tcell.NewEventKey(tcell.KeyEscape, 0, 0))
			check(chrome)
			a.key(tcell.NewEventKey(tcell.KeyCtrlB, 0, 0))
			check(selected)
			a.key(tcell.NewEventKey(tcell.KeyRune, '?', 0))
			check(chrome)
		})
	}
}

func TestNewTabFromEmptyState(t *testing.T) {
	for _, closeWorkspace := range []bool{false, true} {
		t.Run(fmt.Sprint("closeWorkspace=", closeWorkspace), func(t *testing.T) {
			session, _ := fixture(t)
			a := newApp(session, simulation(t, 60, 24), 100)
			defer a.close()
			refresh := func() {
				t.Helper()
				if err := session.Call("state.dump", nil, &a.state); err != nil {
					t.Fatal(err)
				}
				a.syncState()
			}
			refresh()
			wid := a.workspace
			cmd := map[string]any{"type": "tab.close", "tab_id": a.tab}
			if closeWorkspace {
				cmd = map[string]any{"type": "workspace.close", "workspace_id": wid}
			}
			if err := a.executeCommand(cmd); err != nil {
				t.Fatal(err)
			}
			refresh()
			if a.activeTab() != nil {
				t.Fatal("expected empty state")
			}
			a.key(tcell.NewEventKey(tcell.KeyCtrlB, 0, 0))
			a.key(tcell.NewEventKey(tcell.KeyRune, 'n', 0))
			select {
			case cmd := <-a.commands:
				if err := a.executeCommand(cmd); err != nil {
					t.Fatal(err)
				}
			default:
				t.Fatal("Ctrl-B n did not dispatch")
			}
			refresh()
			if a.activeTab() == nil {
				t.Fatal("no tab created")
			}
			if !closeWorkspace && a.workspace != wid {
				t.Fatal("new tab moved out of empty workspace")
			}
			var root tree
			if err := json.Unmarshal(a.activeTab().Tree, &root); err != nil {
				t.Fatal(err)
			}
			if root.Terminal == nil || root.Terminal.Summary.ID == uuid.Nil {
				t.Fatal("new tab has no real terminal")
			}
		})
	}
}
