// Package gotui implements a server-backed terminal frontend.
package gotui

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/SurTeam/Water/internal/goagent"
	"github.com/SurTeam/Water/internal/goclient"
	"github.com/SurTeam/Water/internal/goconfig"
	"github.com/SurTeam/Water/internal/gomodel"
	"github.com/SurTeam/Water/internal/goprotocol"
	"github.com/SurTeam/Water/internal/goremote"
	"github.com/SurTeam/Water/internal/goserver"
	"github.com/SurTeam/Water/internal/govt"
	"github.com/gdamore/tcell/v2"
	"github.com/google/uuid"
)

type Options struct {
	Socket, Config, SSH string
	Serve               bool
}

// Run connects to an existing server or starts an independent server process.
// Quitting the frontend detaches and preserves the shells.
func Run(o Options) error {
	cfg, err := goconfig.Load(o.Config)
	if err != nil {
		return err
	}
	if o.Serve {
		signal.Ignore(syscall.SIGHUP)
		srv := goserver.NewWithConfig(o.Socket, cfg)
		srv.ConfigPath = o.Config
		if err := srv.Initialize(cfg.Startup.InitialWorkspace, cfg.Startup.InitialWorkspace && cfg.Startup.InitialTerminal); err != nil {
			return err
		}
		return srv.ListenAndServe()
	}
	if o.SSH != "" {
		tunnel, err := goremote.Connect(o.SSH)
		if err != nil {
			return err
		}
		defer tunnel.Close()
		o.Socket = tunnel.LocalSocket()
	}
	client := goclient.New(o.Socket)
	session, err := client.OpenTUISession()
	if err != nil && o.SSH == "" {
		probe, probeErr := net.DialTimeout("unix", o.Socket, time.Second)
		if probeErr == nil {
			probe.Close()
			return fmt.Errorf("existing server handshake: %w", err)
		}
		if !cfg.Server.AutoStart {
			return fmt.Errorf("server unavailable (auto_start disabled): %w", err)
		}
		// Only an absent/refused listener is eligible for startup. Never unlink an
		// active or inaccessible socket after a failed handshake.
		if !errors.Is(probeErr, os.ErrNotExist) {
			if op, ok := probeErr.(*net.OpError); !ok || !isRefused(op) {
				return err
			}
		}
		exe, e := os.Executable()
		if e != nil {
			return e
		}
		log, e := os.CreateTemp(os.TempDir(), "water-"+client.Build+"-tui-server-*.log")
		if e != nil {
			return e
		}
		defer log.Close()
		cmd := exec.Command(exe, "tui", "--serve", "--socket", o.Socket, "--config", o.Config)
		cmd.Stdout = log
		cmd.Stderr = log
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if e = cmd.Start(); e != nil {
			return e
		}
		go cmd.Wait()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			session, err = client.OpenTUISession()
			if err == nil {
				break
			}
			time.Sleep(25 * time.Millisecond)
		}
		if err != nil {
			return fmt.Errorf("start server (diagnostics: %s): %w", log.Name(), err)
		}
	}
	if err != nil {
		return err
	}
	defer session.Close()
	if session.Diagnostic {
		return fmt.Errorf("incompatible server: %v", session.Compatibility)
	}
	screen, err := tcell.NewScreen()
	if err != nil {
		return err
	}
	if err = screen.Init(); err != nil {
		return err
	}
	defer screen.Fini()
	screen.EnableMouse(tcell.MouseMotionEvents)
	screen.EnablePaste()
	screen.EnableFocus()
	app := newApp(session, screen, cfg.Terminal.ScrollbackLines)
	app.closeWorkspaceShortcut = cfg.Shortcuts.CloseWorkspace
	defer app.close()
	return app.run()
}

type tree struct {
	Type     string    `json:"type"`
	Pane     uuid.UUID `json:"pane_id"`
	Terminal *struct {
		Summary struct {
			ID          uuid.UUID               `json:"terminal_id"`
			Size        goprotocol.TerminalSize `json:"size"`
			ProcessName string                  `json:"process_name"`
			AgentName   string                  `json:"agent_name"`
		} `json:"summary"`
	} `json:"terminal"`
	Axis          string  `json:"axis"`
	Ratio         float64 `json:"ratio"`
	First, Second *tree
}
type attached struct {
	Last   uint64                         `json:"last_seq"`
	Size   goprotocol.TerminalSize        `json:"size"`
	Replay []goprotocol.WireTerminalEvent `json:"replay"`
}
type attachResult struct {
	id       uuid.UUID
	target   *terminal
	response attached
	emu      *govt.Emulator
	err      error
}

type terminal struct {
	// cols/rows describe the last observed server geometry. Requests track the
	// local viewport separately so remote resizes cannot retrigger every draw.
	requestCols, requestRows int
	requestGeneration        uint64
	remoteGeometry           bool

	pending  []goclient.TerminalPush
	overflow bool

	emu        *govt.Emulator
	seq        uint64
	cols, rows int
	exited     bool
}
type rect struct{ x, y, w, h int }

func (r rect) contains(x, y int) bool { return x >= r.x && y >= r.y && x < r.x+r.w && y < r.y+r.h }

type pane struct {
	id, term   uuid.UUID
	box        rect
	title      string
	headerless bool
}

func (p pane) contentBox() rect {
	r := p.box
	if !p.headerless {
		r.y++
		r.h--
	}
	return r
}

type divider struct {
	box, parent rect
	axis        string
	path        []bool
}
type hit struct {
	box  rect
	kind string
	id   uuid.UUID
}
type selectionEffect struct {
	Workspace uuid.UUID `json:"workspace_id"`
	Tab       uuid.UUID `json:"tab_id"`
	Pane      uuid.UUID `json:"pane_id"`
}
type localSelection struct {
	term                               uuid.UUID
	startRow, startCol, endRow, endCol int
	drag                               bool
}
type menuItem struct {
	label, kind string
	id          uuid.UUID
}
type app struct {
	focused, focusKnown bool
	focusGeneration     uint64

	mouseCapture *pane

	menuMode  string
	menuIndex int
	menuItems []menuItem

	pendingSelection *selectionEffect
	selectionRange   *localSelection

	attachments     chan attachResult
	quit            bool
	shutdownPending bool

	session                *goclient.Session
	screen                 tcell.Screen
	scrollback             int
	state                  gomodel.StateDump
	workspace, tab, focus  uuid.UUID
	terms                  map[uuid.UUID]*terminal
	panes                  []pane
	dividers               []divider
	hits                   []hit
	prefix, menu, pasting  bool
	paste                  []byte
	prompt, promptKind     string
	confirmWorkspace       uuid.UUID
	closeWorkspaceShortcut string
	flash                  string
	flashUntil             time.Time
	status                 string
	dragging               *divider
	buttons                tcell.ButtonMask
	commands               chan map[string]any
	results                chan error
	stop                   chan struct{}
}

func newApp(s *goclient.Session, screen tcell.Screen, scrollback int) *app {
	return &app{focused: true, attachments: make(chan attachResult), session: s, screen: screen, scrollback: scrollback, terms: map[uuid.UUID]*terminal{}, commands: make(chan map[string]any, 128), results: make(chan error, 128), stop: make(chan struct{})}
}
func (a *app) close() {
	close(a.stop)
	for _, t := range a.terms {
		if t.emu != nil {
			t.emu.Close()
		}
	}
}
func (a *app) command(c map[string]any) {
	select {
	case a.commands <- c:
	default:
		a.status = "Command queue full; retry"
	}
}

var errServerShutdown = errors.New("server shutdown requested")
var errServerShutdownFailed = errors.New("server shutdown failed")

func (a *app) run() error {
	terminalEvents, pushes := a.session.Events, a.session.Pushes
	go func() {
		for {
			select {
			case c := <-a.commands:
				err := a.executeCommand(c)
				select {
				case a.results <- err:
				case <-a.stop:
					return
				}
			case <-a.stop:
				return
			}
		}
	}()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(signals)
	events := make(chan tcell.Event, 32)
	go a.screen.ChannelEvents(events, a.stop)
	if err := a.session.Call("state.dump", nil, &a.state); err != nil {
		return err
	}
	a.syncState()
	a.setFocus(true)
	tick := time.NewTicker(time.Second / 30)
	defer tick.Stop()
	a.draw()
	for {
		select {
		case <-signals:
			return nil
		case result := <-a.attachments:
			a.finishAttach(result)
		case ev := <-events:
			if ev == nil {
				return nil
			}
			switch e := ev.(type) {
			case *tcell.EventResize:
				a.screen.Sync()
			case *tcell.EventFocus:
				a.setFocus(e.Focused)
			case *tcell.EventKey:
				if !a.pasting {
					a.focusActivity()
				}
				if a.key(e) {
					return nil
				}
			case *tcell.EventPaste:
				a.pasting = e.Start()
				if e.Start() {
					a.focusActivity()
				}
				if !e.Start() {
					if a.promptKind != "" {
						a.prompt += string(a.paste)
					} else {
						a.sendPaste()
					}
					a.paste = nil
				}
			case *tcell.EventMouse:
				if e.Buttons() != tcell.ButtonNone && a.buttons == tcell.ButtonNone {
					a.focusActivity()
				}
				a.mouse(e)
				if a.quit {
					return nil
				}
			}
		case push, ok := <-terminalEvents:
			if !ok {
				if a.shutdownPending {
					terminalEvents = nil
					continue
				}
				return fmt.Errorf("server disconnected: %v", a.session.Err())
			}
			a.apply(push)
		case msg, ok := <-pushes:
			if !ok {
				if a.shutdownPending {
					pushes = nil
					continue
				}
				return fmt.Errorf("server disconnected: %v", a.session.Err())
			}
			if msg.Method == "push.snapshot" {
				if json.Unmarshal(msg.Params, &a.state) == nil {
					a.syncState()
				}
			} else if msg.Method == "push.selection" {
				a.selection(msg.Params)
			} else if msg.Method == "push.ui" {
				a.session.ReplyFailure(msg.RequestID, "UNSUPPORTED", "TUI does not implement GUI automation")
			}
		case err := <-a.results:
			if errors.Is(err, errServerShutdown) {
				return nil
			}
			if errors.Is(err, errServerShutdownFailed) {
				a.shutdownPending = false
				if terminalEvents == nil || pushes == nil {
					return fmt.Errorf("server shutdown: %w", err)
				}
			}
			if err != nil {
				a.status = err.Error()
			}
		case <-tick.C:
			a.draw()
		}
	}
}
func (a *app) syncState() {
	if p := a.pendingSelection; p != nil {
		matched := false
		for _, w := range a.state.Workspaces {
			if p.Workspace == w.ID {
				a.workspace = w.ID
				a.tab = uuid.Nil
				a.focus = uuid.Nil
				matched = true
			}
			for _, tab := range w.Tabs {
				var root tree
				_ = json.Unmarshal(tab.Tree, &root)
				hasPane := false
				walk(&root, func(n *tree) {
					if n.Pane == p.Pane && p.Pane != uuid.Nil {
						hasPane = true
					}
				})
				if p.Tab == tab.ID || hasPane {
					a.workspace = w.ID
					a.tab = tab.ID
					a.focus = tab.ActivePane
					if hasPane {
						a.focus = p.Pane
					}
					matched = true
				}
			}
		}
		if matched {
			a.pendingSelection = nil
		}
	}

	var ws *gomodel.WorkspaceDump
	for i := range a.state.Workspaces {
		if a.state.Workspaces[i].ID == a.workspace {
			ws = &a.state.Workspaces[i]
		}
	}
	if ws == nil && len(a.state.Workspaces) > 0 {
		ws = &a.state.Workspaces[0]
		if a.state.ActiveWorkspace != nil {
			for i := range a.state.Workspaces {
				if a.state.Workspaces[i].ID == *a.state.ActiveWorkspace {
					ws = &a.state.Workspaces[i]
				}
			}
		}
		a.workspace = ws.ID
		a.tab = uuid.Nil
	}
	if ws == nil {
		a.workspace, a.tab, a.focus = uuid.Nil, uuid.Nil, uuid.Nil
	}
	if ws != nil {
		found := false
		for _, t := range ws.Tabs {
			if t.ID == a.tab {
				found = true
			}
		}
		if !found {
			a.tab = uuid.Nil
			if ws.ActiveTab != nil {
				a.tab = *ws.ActiveTab
			} else if len(ws.Tabs) > 0 {
				a.tab = ws.Tabs[0].ID
			}
			a.focus = uuid.Nil
		}
	}
	live := map[uuid.UUID]bool{}
	for _, w := range a.state.Workspaces {
		for _, tab := range w.Tabs {
			var root tree
			if json.Unmarshal(tab.Tree, &root) != nil {
				continue
			}
			walk(&root, func(n *tree) {
				if n.Terminal != nil {
					live[n.Terminal.Summary.ID] = true
				}
			})
		}
	}
	for id, t := range a.terms {
		if !live[id] {
			a.session.Detach(id)
			if t.emu != nil {
				t.emu.Close()
			}
			delete(a.terms, id)
		}
	}
}
func walk(n *tree, f func(*tree)) {
	if n == nil {
		return
	}
	if n.Type == "leaf" {
		f(n)
		return
	}
	walk(n.First, f)
	walk(n.Second, f)
}
func (a *app) selection(raw json.RawMessage) {
	var s selectionEffect
	if json.Unmarshal(raw, &s) != nil {
		return
	}
	a.pendingSelection = &s
	a.syncState()
}
func (a *app) attach(id uuid.UUID) *terminal {
	if t := a.terms[id]; t != nil {
		if t.emu == nil {
			return nil
		}
		return t
	}
	t := &terminal{}
	a.terms[id] = t
	go func() {
		var r attached
		err := a.session.Attach(id, &r)
		var emu *govt.Emulator
		if err == nil {
			emu = govt.New(r.Size.Columns, r.Size.Lines, a.scrollback)
			for _, e := range r.Replay {
				switch e.Type {
				case "output":
					if b, err := base64.StdEncoding.DecodeString(e.Bytes); err == nil {
						emu.WriteReplay(b)
					}
				case "resize":
					emu.Resize(e.Columns, e.Lines)
				}
			}
		}
		result := attachResult{id, t, r, emu, err}
		select {
		case a.attachments <- result:
		case <-a.stop:
			if emu != nil {
				emu.Close()
			}
		}
	}()
	return nil
}
func (a *app) finishAttach(r attachResult) {
	t := a.terms[r.id]
	if t != r.target {
		if r.emu != nil {
			r.emu.Close()
		}
		return
	}
	if r.err != nil {
		a.status = r.err.Error()
		delete(a.terms, r.id)
		return
	}
	t.emu = r.emu
	t.seq = r.response.Last
	t.cols = r.response.Size.Columns
	t.rows = r.response.Size.Lines
	for _, ev := range r.response.Replay {
		if ev.Type == "exit" {
			t.exited = true
		}
	}
	queued := t.pending
	t.pending = nil
	if t.overflow {
		t.emu.Close()
		delete(a.terms, r.id)
		a.attach(r.id)
		return
	}
	for _, push := range queued {
		a.apply(push)
	}
}
func (a *app) apply(p goclient.TerminalPush) {
	t := a.terms[p.TerminalID]
	if t == nil {
		return
	}
	if t.emu == nil {
		if len(t.pending) < 128 {
			t.pending = append(t.pending, p)
		} else {
			t.overflow = true
		}
		return
	}
	if p.Event.Seq <= t.seq {
		return
	}
	if t.seq != 0 && p.Event.Seq != t.seq+1 {
		if t.emu != nil {
			t.emu.Close()
		}
		delete(a.terms, p.TerminalID)
		a.attach(p.TerminalID)
		return
	}
	t.seq = p.Event.Seq
	switch p.Event.Kind {
	case goprotocol.OutputEvent:
		before, alt := t.emu.HistoryState()
		t.emu.Write(p.Event.Data)
		after, nextAlt := t.emu.HistoryState()
		if sel := a.selectionRange; sel != nil && sel.term == p.TerminalID {
			if alt != nextAlt {
				a.selectionRange = nil
			} else if after > before {
				sel.startRow -= int(after - before)
				sel.endRow -= int(after - before)
				if sel.startRow < 0 || sel.endRow < 0 {
					a.selectionRange = nil
				}
			}
		}
	case goprotocol.ResizeEvent:
		if a.selectionRange != nil && a.selectionRange.term == p.TerminalID {
			a.selectionRange = nil
		}
		t.emu.Resize(p.Event.Size.Columns, p.Event.Size.Lines)
		t.cols, t.rows = p.Event.Size.Columns, p.Event.Size.Lines
		t.remoteGeometry = t.requestCols != 0 && (t.cols != t.requestCols || t.rows != t.requestRows)
	case goprotocol.ExitEvent:
		t.exited = true
	}
	a.responses(p.TerminalID, t)
}
func (a *app) responses(id uuid.UUID, t *terminal) {
	for _, b := range t.emu.TakeResponses() {
		a.bytes(id, b)
	}
}
func (a *app) bytes(id uuid.UUID, b []byte) {
	if len(b) == 0 {
		return
	}
	ints := make([]int, len(b))
	for i, v := range b {
		ints[i] = int(v)
	}
	a.command(map[string]any{"type": "terminal.send_bytes", "terminal_id": id, "bytes": ints})
}

func isRefused(err error) bool { return errors.Is(err, syscall.ECONNREFUSED) }

func (a *app) executeCommand(c map[string]any) error {
	if c["type"] == "tui.shutdown" {
		var result any
		if err := a.session.Call("server.shutdown", map[string]any{}, &result); err != nil {
			return fmt.Errorf("%w: %v", errServerShutdownFailed, err)
		}
		return errServerShutdown
	}
	if c["type"] == "tui.agent_control" {
		var diagnostic goagent.Diagnostic
		sample := map[string]any{"type": "agents.test", "pane_id": c["pane_id"]}
		if err := a.session.Dispatch(sample, &diagnostic); err != nil {
			return err
		}
		if diagnostic.Selected == nil {
			return fmt.Errorf("foreground process is not a recognized Agent")
		}
		c = map[string]any{"type": "agent.control", "pane_id": c["pane_id"], "action": c["action"], "expected": diagnostic.Process, "expected_agent_id": diagnostic.Selected.ID}
	}
	show, _ := c["tui_show_result"].(bool)
	delete(c, "tui_show_result")
	var result any
	if err := a.session.Dispatch(c, &result); err != nil {
		return err
	}
	if show {
		raw, _ := json.Marshal(result)
		return fmt.Errorf("%s", raw)
	}
	return nil
}

func (a *app) setFocus(focused bool) {
	if a.focusKnown && a.focused == focused {
		return
	}
	if a.session != nil {
		if err := a.session.Notify("session.focus", map[string]any{"focused": focused}); err != nil {
			a.status = err.Error()
			return
		}
	}
	a.focusKnown, a.focused = true, focused
	if focused {
		a.focusGeneration++
		for _, t := range a.terms {
			t.remoteGeometry = false
		}
	}
}
func (a *app) focusActivity() {
	if !a.focusKnown || !a.focused {
		a.setFocus(true)
	} else if a.session != nil {
		// Terminals without focus reports can still claim ownership on real input.
		if err := a.session.Notify("session.focus", map[string]any{"focused": true}); err != nil {
			a.status = err.Error()
			return
		}
	}
	reclaim := false
	for _, t := range a.terms {
		if t.remoteGeometry {
			reclaim = true
			t.remoteGeometry = false
		}
	}
	if reclaim {
		a.focusGeneration++
	}
}
