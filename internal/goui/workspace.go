package goui

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gioui.org/f32"
	"gioui.org/font"
	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/SurTeam/Water/internal/goclient"
	"github.com/SurTeam/Water/internal/goconfig"
	"github.com/SurTeam/Water/internal/gohyperlink"
	"github.com/SurTeam/Water/internal/gomodel"
	"github.com/SurTeam/Water/internal/goprotocol"
	"github.com/SurTeam/Water/internal/govt"
	"github.com/google/uuid"
)

type paneTree struct {
	Type        string              `json:"type"`
	PaneID      uuid.UUID           `json:"pane_id"`
	SurfaceID   uuid.UUID           `json:"surface_id"`
	SurfaceKind string              `json:"surface_kind"`
	Terminal    *terminalProjection `json:"terminal"`
	Axis        string              `json:"axis"`
	Ratio       float32             `json:"ratio"`
	First       *paneTree           `json:"first"`
	Second      *paneTree           `json:"second"`
}

type terminalProjection struct {
	Summary terminalSummary `json:"summary"`
}

type terminalSummary struct {
	TerminalID  uuid.UUID               `json:"terminal_id"`
	Size        goprotocol.TerminalSize `json:"size"`
	Process     json.RawMessage         `json:"process"`
	ProcessName string                  `json:"process_name"`
	CWD         string                  `json:"cwd"`
}

type terminalAttach struct {
	TerminalID uuid.UUID                      `json:"terminal_id"`
	FirstSeq   *uint64                        `json:"first_seq"`
	LastSeq    uint64                         `json:"last_seq"`
	Size       goprotocol.TerminalSize        `json:"size"`
	Replay     []goprotocol.WireTerminalEvent `json:"replay"`
}

type automationHitKind uint8

const (
	hitWorkspace automationHitKind = iota + 1
	hitTab
	hitNewWorkspace
	hitNewTab
	hitPane
	hitConnection
	hitNewRemote
	hitRemoteCancel
	hitDisconnectConnection
	hitSettings
	hitSettingsControl
	hitSplitRight
	hitSplitDown
	hitDivider
	hitWindowClose
	hitWindowMinimize
	hitWindowMaximize
	hitTitlebar
	hitRemoteSubmit
	hitRemoteField
	hitHyperlinkConfirm
	hitHyperlinkCancel
	hitSidebarResize
	hitAgent
	hitRenameField
	hitRenameSave
	hitRenameCancel
)

type automationHit struct {
	Rect  image.Rectangle
	Kind  automationHitKind
	ID    uuid.UUID
	Label string
}

type hyperlinkPrompt struct {
	URI         string
	Destination string
	Working     bool
	Error       string
}

type ConnectionEntry struct {
	ID               uuid.UUID
	Name             string
	Kind             string
	Status           string
	SocketPath       string
	RemoteSocketPath string
	Destination      string
}

type terminalClient struct {
	id                      uuid.UUID
	mu                      sync.RWMutex
	parseMu                 sync.Mutex
	emu                     *govt.Emulator
	snapshot                govt.Snapshot
	lastSeq                 uint64
	cols                    int
	rows                    int
	cellWidth               int
	resizeGeneration        uint64
	cellHeight              int
	view                    *TerminalView
	input                   *TerminalInput
	selection               Selection
	scrollbackOnClearScreen bool
	imePreedit              string
	imeComposing            bool
	defaultColors           [259]uint32
	cursorStyle             string
	cursorBlink             bool
	inputPending            atomic.Bool
}

func (t *terminalClient) close() {
	t.parseMu.Lock()
	defer t.parseMu.Unlock()
	t.mu.Lock()
	if t.emu != nil {
		t.emu.Close()
		t.emu = nil
	}
	t.mu.Unlock()
}

func (t *terminalClient) setScrollbackOnClearScreen(enabled bool) {
	t.parseMu.Lock()
	defer t.parseMu.Unlock()
	t.mu.Lock()
	t.scrollbackOnClearScreen = enabled
	if t.emu != nil {
		t.emu.SetScrollbackOnClearScreen(enabled)
	}
	t.mu.Unlock()
}

type WorkspaceClient struct {
	oscClipboardMu       sync.Mutex
	oscClipboardWrite    []byte
	native               atomic.Pointer[EbitengineWindow]
	session              *goclient.Session
	invalidate           func()
	snapshotWake         chan struct{}
	config               goconfig.AppConfig
	runtimeConfig        *goconfig.AppConfig // Owned by layoutMu; server settings change on restart.
	settingsStore        *SettingsStore
	settings             settingsPanel
	server               serverPanel
	recoveryBlocked      bool
	configPath           string
	settingsButton       widget.Clickable
	splitRightButton     widget.Clickable
	splitDownButton      widget.Clickable
	sidebarHidden        bool
	dividers             map[string]*splitDivider
	layoutFrame          uint64
	restoreTerminalFocus bool
	remoteDestination    string

	mu              sync.RWMutex
	state           gomodel.StateDump
	selection       windowSelection
	nativeFocused   bool // Owned by the native frame loop.
	focusKnown      bool
	focusGeneration uint64
	connectionError string
	terminals       map[uuid.UUID]*terminalClient

	layoutMu             sync.Mutex
	frameSize            image.Point
	frameMetric          unit.Metric
	frameStateRevision   uint64
	frameActiveWorkspace uuid.UUID
	frameFocusedPane     uuid.UUID
	frameActiveTerminal  uuid.UUID
	hitRegions           []automationHit

	workspaceClicks map[uuid.UUID]*widget.Clickable
	tabClicks       map[uuid.UUID]*widget.Clickable
	newWorkspace    widget.Clickable
	newTab          widget.Clickable

	connectionMu               sync.RWMutex
	connectionID               uuid.UUID
	connectionEntries          []ConnectionEntry
	connectionClicks           map[uuid.UUID]*widget.Clickable
	connectionDisconnectClicks map[uuid.UUID]*widget.Clickable
	onActivateConnection       func(uuid.UUID)
	onConnectRemote            func(string) error
	onRemoveConnection         func(uuid.UUID) bool
	onDisconnected             func(error)
	newRemote                  widget.Clickable
	remoteConnect              widget.Clickable
	remoteCancel               widget.Clickable
	remoteEditor               widget.Editor
	remoteFormVisible          bool
	remoteConnecting           bool
	remoteError                string
	remoteClearEditor          bool

	hyperlinkMu      sync.Mutex
	hyperlinkPrompt  *hyperlinkPrompt
	hyperlinkConfirm widget.Clickable
	hyperlinkCancel  widget.Clickable
	hyperlinkScrim   widget.Clickable
}

func NewWorkspaceClient(session *goclient.Session, invalidate func()) *WorkspaceClient {
	return NewWorkspaceClientWithConfig(session, invalidate, goconfig.Default())
}

func NewWorkspaceClientWithConfig(session *goclient.Session, invalidate func(), config goconfig.AppConfig) *WorkspaceClient {
	return NewWorkspaceClientWithConnection(session, invalidate, config, "")
}

func NewWorkspaceClientWithConnection(session *goclient.Session, invalidate func(), config goconfig.AppConfig, remoteDestination string) *WorkspaceClient {
	c := &WorkspaceClient{
		session:                    session,
		invalidate:                 invalidate,
		snapshotWake:               make(chan struct{}, 1),
		config:                     config.Normalized(),
		sidebarHidden:              !config.UI.SidebarVisible,
		remoteDestination:          strings.TrimSpace(remoteDestination),
		terminals:                  make(map[uuid.UUID]*terminalClient),
		workspaceClicks:            make(map[uuid.UUID]*widget.Clickable),
		tabClicks:                  make(map[uuid.UUID]*widget.Clickable),
		connectionClicks:           make(map[uuid.UUID]*widget.Clickable),
		connectionDisconnectClicks: make(map[uuid.UUID]*widget.Clickable),
	}
	c.remoteEditor.SingleLine = true
	if session != nil {
		c.server.info = session.Server
		c.server.compatibility = session.Compatibility
		if session.Server.ProtocolVersion != 0 && (session.Diagnostic || session.Compatibility.RestartRecommended) {
			c.openSettingsConfig(c.config)
			c.settings.group = 5
			c.settings.focus = -1
		}
	}
	return c
}

func (c *WorkspaceClient) SetConnectionSwitcher(id uuid.UUID, entries []ConnectionEntry, activate func(uuid.UUID)) {
	c.connectionMu.Lock()
	c.connectionID = id
	c.connectionEntries = append(c.connectionEntries[:0], entries...)
	c.onActivateConnection = activate
	for key := range c.connectionClicks {
		found := false
		for _, entry := range entries {
			if entry.ID == key {
				found = true
				break
			}
		}
		if !found {
			delete(c.connectionClicks, key)
			delete(c.connectionDisconnectClicks, key)
		}
	}
	c.connectionMu.Unlock()
	if c.invalidate != nil {
		c.invalidate()
	}
}

func (c *WorkspaceClient) SetConnectionActions(connect func(string) error, remove func(uuid.UUID) bool) {
	c.connectionMu.Lock()
	c.onConnectRemote = connect
	c.onRemoveConnection = remove
	c.connectionMu.Unlock()
	if c.invalidate != nil {
		c.invalidate()
	}
}

func (c *WorkspaceClient) connectionActions() (func(string) error, func(uuid.UUID) bool) {
	c.connectionMu.RLock()
	connect := c.onConnectRemote
	remove := c.onRemoveConnection
	c.connectionMu.RUnlock()
	return connect, remove
}

func (c *WorkspaceClient) connectionSnapshot() (uuid.UUID, []ConnectionEntry, func(uuid.UUID)) {
	c.connectionMu.RLock()
	id := c.connectionID
	entries := append([]ConnectionEntry(nil), c.connectionEntries...)
	activate := c.onActivateConnection
	c.connectionMu.RUnlock()
	return id, entries, activate
}

func (c *WorkspaceClient) connectionListResponse() map[string]any {
	active, entries, _ := c.connectionSnapshot()
	out := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		item := map[string]any{
			"id":     entry.ID,
			"name":   entry.Name,
			"kind":   entry.Kind,
			"status": entry.Status,
			"active": entry.ID == active,
		}
		if entry.SocketPath != "" {
			item["socket_path"] = entry.SocketPath
		}
		if entry.RemoteSocketPath != "" {
			item["remote_socket_path"] = entry.RemoteSocketPath
		}
		if entry.Destination != "" {
			item["destination"] = entry.Destination
		}
		out = append(out, item)
	}
	return map[string]any{"connections": out}
}

func (c *WorkspaceClient) Close() {
	c.mu.Lock()
	terms := make([]*terminalClient, 0, len(c.terminals))
	for _, term := range c.terminals {
		terms = append(terms, term)
	}
	c.terminals = make(map[uuid.UUID]*terminalClient)
	c.mu.Unlock()
	for _, term := range terms {
		_ = c.session.Detach(term.id)
		term.close()
	}
}

func (c *WorkspaceClient) Run() {
	pushes := c.session.Pushes
	events := c.session.Events
	// The renderer consumes immutable views and asks for the next latest view.
	// Publish the first change immediately, then retain only the latest parsed
	// view at a bounded output cadence. Input wakes the GUI independently. ANSI
	// parsing never waits for the renderer, including when its window is hidden.
	pending := make(map[*terminalClient]bool)
	outstanding := false
	var published time.Time
	publication := time.NewTimer(time.Hour)
	publication.Stop()
	defer publication.Stop()
	var publicationDue <-chan time.Time
	publish := func() {
		if outstanding && c.native.Load() != nil {
			return
		}
		if len(pending) > 0 && c.native.Load() != nil {
			if delay := outputPublicationDelay(published, time.Now()); delay > 0 {
				if publicationDue == nil {
					publication.Reset(delay)
					publicationDue = publication.C
				}
				return
			}
		}
		outstanding = c.publishTerminalSnapshots(pending)
		if outstanding {
			published = time.Now()
		}
	}
	for pushes != nil || events != nil {
		select {
		case <-publicationDue:
			publicationDue = nil
			publish()
		case <-c.snapshotWake:
			outstanding = false
			publish()
		case msg, ok := <-pushes:
			if !ok {
				pushes = nil
				continue
			}
			switch msg.Method {
			case "push.selection":
				c.applySelection(msg.Params)
			case "push.snapshot":
				var state gomodel.StateDump
				if json.Unmarshal(msg.Params, &state) == nil {
					c.applyState(state)
				}
			case "push.ui":
				c.handleUIPush(msg)
			}
		case push, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			if term := c.applyTerminalEventDeferred(push); term != nil {
				pending[term] = true
			}
			// Prioritize a frame request after each bounded ordered event, rather
			// than letting a permanently full output channel starve publication.
			select {
			case <-c.snapshotWake:
				outstanding = false
			default:
			}
			if !outstanding || c.native.Load() == nil {
				publish()
			}
		}
	}
	c.publishTerminalSnapshots(pending)
	if err := c.session.Err(); err != nil {
		c.mu.Lock()
		c.connectionError = err.Error()
		c.mu.Unlock()
		c.connectionMu.RLock()
		notify := c.onDisconnected
		c.connectionMu.RUnlock()
		if notify != nil {
			notify(err)
		}
		if c.invalidate != nil {
			c.invalidate()
		}
	}
}

func (c *WorkspaceClient) handleUIPush(msg goprotocol.WireMessage) {
	var request struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(msg.Params, &request); err != nil {
		_ = c.session.ReplyFailure(msg.RequestID, "UI_AUTOMATION_FAILED", err.Error())
		return
	}
	result, err := c.handleUIRequest(request.Method, request.Params)
	if err != nil {
		_ = c.session.ReplyFailure(msg.RequestID, "UI_AUTOMATION_FAILED", err.Error())
		return
	}
	_ = c.session.ReplySuccess(msg.RequestID, result)
}

func (c *WorkspaceClient) handleUIRequest(method string, params json.RawMessage) (any, error) {
	if method == "ui.content" {
		return c.readTerminalContent(params)
	}
	if window := c.native.Load(); window != nil && strings.HasPrefix(method, "ui.") {
		return window.request(c, method, params)
	}
	switch method {
	case "ui.snapshot":
		return c.uiSnapshot(), nil
	case "ui.keystroke":
		var p struct {
			Keystroke string `json:"keystroke"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		handled := c.dispatchAutomationKeystroke(p.Keystroke)
		return map[string]any{
			"keystroke":    p.Keystroke,
			"handled":      handled,
			"window_count": 1,
		}, nil
	case "ui.click":
		var p struct {
			X          float32 `json:"x"`
			Y          float32 `json:"y"`
			ClickCount int     `json:"click_count"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		handled := c.automationClick(p.X, p.Y, p.ClickCount)
		return map[string]any{
			"window_count":      1,
			"has_active_window": true,
			"handled":           handled,
		}, nil
	case "ui.drag":
		var p struct {
			X   float32 `json:"x"`
			Y   float32 `json:"y"`
			ToX float32 `json:"to_x"`
			ToY float32 `json:"to_y"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		return map[string]any{"handled": c.automationDrag(p.X, p.Y, p.ToX, p.ToY), "window_count": 1}, nil
	case "ui.wheel":
		var p struct {
			X  float32 `json:"x"`
			Y  float32 `json:"y"`
			DX float32 `json:"dx"`
			DY float32 `json:"dy"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		c.layoutMu.Lock()
		settingsVisible := c.settings.visible
		c.layoutMu.Unlock()
		scrolled := false
		if settingsVisible {
			scrolled = c.automationSettingsWheel(p.X, p.Y, p.DX, p.DY)
		} else {
			scrolled = c.scrollActiveTerminal(p.DY)
		}
		return map[string]any{
			"position":          []float32{p.X, p.Y},
			"delta":             []float32{p.DX, p.DY},
			"propagate":         !scrolled,
			"default_prevented": scrolled,
		}, nil
	case "ui.screenshot":
		var p struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		return c.Screenshot(p.Path)
	case "connection.list":
		return c.connectionListResponse(), nil
	default:
		return nil, fmt.Errorf("unsupported UI method %q", method)
	}
}

func (c *WorkspaceClient) uiSnapshot() map[string]any {
	c.mu.RLock()
	connectionError := c.connectionError
	terms := make([]*terminalClient, 0, len(c.terminals))
	for _, term := range c.terminals {
		terms = append(terms, term)
	}
	c.mu.RUnlock()

	c.layoutMu.Lock()
	hits := make([]map[string]any, 0, len(c.hitRegions))
	for _, hit := range c.hitRegions {
		hits = append(hits, map[string]any{
			"kind":  automationHitKindName(hit.Kind),
			"id":    hit.ID,
			"rect":  []int{hit.Rect.Min.X, hit.Rect.Min.Y, hit.Rect.Max.X, hit.Rect.Max.Y},
			"label": hit.Label,
		})
	}
	frameSize := c.frameSize
	frameStateRevision := c.frameStateRevision
	frameActiveWorkspace := c.frameActiveWorkspace
	frameFocusedPane := c.frameFocusedPane
	frameActiveTerminal := c.frameActiveTerminal
	settingsVisible := c.settings.visible
	settingsSaving := c.settings.saving
	settingsMessage := c.settings.message
	effectiveConfig := c.currentConfig()
	c.layoutMu.Unlock()

	visibleCells := 0
	preparedRows := 0
	imageTextures := 0
	graphicsBytes := 0
	activeIMEPreedit := ""
	activeIMEComposing := false
	activeTerminalLastSeq := uint64(0)
	for _, term := range terms {
		term.mu.RLock()
		snapshot := term.snapshot
		emu := term.emu
		view := term.view
		imePreedit := term.imePreedit
		imeComposing := term.imeComposing
		termID := term.id
		lastSeq := term.lastSeq
		term.mu.RUnlock()
		if termID == frameActiveTerminal {
			activeTerminalLastSeq = lastSeq
			activeIMEPreedit = imePreedit
			activeIMEComposing = imeComposing
		}
		visibleCells += snapshot.Cols * snapshot.Rows
		if emu != nil {
			graphicsBytes += emu.ImageBytes()
		}
		if view != nil {
			stats := view.CacheStats()
			preparedRows += stats.PreparedRows
			imageTextures += stats.ImageTextures
		}
	}
	c.connectionMu.RLock()
	remoteFormVisible := c.remoteFormVisible
	remoteConnecting := c.remoteConnecting
	remoteError := c.remoteError
	c.connectionMu.RUnlock()

	return map[string]any{
		"connection_error": connectionError,
		"settings_visible": settingsVisible, "settings_saving": settingsSaving, "settings_message": settingsMessage,
		"effective_config":            effectiveConfig,
		"window_count":                1,
		"has_active_window":           true,
		"attached_terminal_count":     len(terms),
		"visible_cells":               visibleCells,
		"prepared_row_cache_entries":  preparedRows,
		"image_texture_cache_entries": imageTextures,
		"terminal_graphics_bytes":     graphicsBytes,
		"frame_size":                  []int{frameSize.X, frameSize.Y},
		"frame_state_revision":        frameStateRevision,
		"frame_active_workspace":      frameActiveWorkspace,
		"frame_focused_pane":          frameFocusedPane,
		"frame_active_terminal":       frameActiveTerminal,
		"active_terminal_last_seq":    activeTerminalLastSeq,
		"ime_preedit":                 activeIMEPreedit,
		"ime_composing":               activeIMEComposing,
		"automation_hits":             hits,
		"connections":                 c.connectionListResponse()["connections"],
		"remote_form_visible":         remoteFormVisible,
		"remote_connecting":           remoteConnecting,
		"remote_error":                remoteError,
	}
}

func automationHitKindName(kind automationHitKind) string {
	switch kind {
	case hitWorkspace:
		return "workspace"
	case hitTab:
		return "tab"
	case hitNewWorkspace:
		return "new_workspace"
	case hitNewTab:
		return "new_tab"
	case hitPane:
		return "pane"
	case hitConnection:
		return "connection"
	case hitNewRemote:
		return "new_remote"
	case hitRemoteCancel:
		return "remote_cancel"
	case hitDisconnectConnection:
		return "disconnect_connection"
	case hitSettings:
		return "settings"
	case hitSettingsControl:
		return "settings_control"
	case hitSplitRight:
		return "split_right"
	case hitSplitDown:
		return "split_down"
	case hitDivider:
		return "divider"
	case hitWindowClose:
		return "window_close"
	case hitWindowMinimize:
		return "window_minimize"
	case hitWindowMaximize:
		return "window_maximize"
	case hitTitlebar:
		return "titlebar"
	case hitRemoteSubmit:
		return "remote_submit"
	case hitRemoteField:
		return "remote_field"
	case hitHyperlinkConfirm:
		return "hyperlink_confirm"
	case hitHyperlinkCancel:
		return "hyperlink_cancel"
	case hitSidebarResize:
		return "sidebar_resize"
	case hitAgent:
		return "agent"
	case hitRenameField:
		return "rename_field"
	case hitRenameSave:
		return "rename_save"
	case hitRenameCancel:
		return "rename_cancel"
	default:
		return "unknown"
	}
}

func (c *WorkspaceClient) dispatchAutomationKeystroke(spec string) bool {
	c.layoutMu.Lock()
	defer c.layoutMu.Unlock()
	if c.dispatchShortcut(spec) {
		return true
	}
	if c.settings.visible {
		return c.routeSettingsInput(spec)
	}
	id, ok := c.activeTerminalID()
	if !ok {
		return false
	}
	c.mu.RLock()
	term := c.terminals[id]
	c.mu.RUnlock()
	if term == nil || term.input == nil {
		return false
	}
	term.mu.RLock()
	snapshot := term.snapshot
	term.mu.RUnlock()
	return routeAutomationInput(term.input, snapshot, spec)
}

func (c *WorkspaceClient) dispatchShortcut(spec string) bool {
	ev, ok := automationInputEvent(spec)
	if !ok {
		return false
	}
	pressed, ok := ev.(key.Event)
	if !ok {
		return false
	}
	shortcuts := c.currentConfig().Shortcuts
	if shortcutMatches(pressed, shortcuts.OpenSettings) {
		if c.settings.visible {
			if !c.settings.saving {
				c.settings.cancelChanges()
			}
		} else {
			c.openSettings()
		}
		return true
	}
	if c.settings.visible {
		return false
	}
	for _, action := range []struct{ binding, command, direction string }{
		{shortcuts.SplitRight, "pane.split", "right"}, {shortcuts.SplitDown, "pane.split", "down"},
		{shortcuts.NewTerminalTab, "tab.new", ""}, {shortcuts.NewWorkspace, "workspace.new", ""},
		{shortcuts.ClosePane, "pane.close", ""}, {shortcuts.PromotePaneToTab, "pane.promote_to_tab", ""},
		{shortcuts.FocusLeft, "pane.focus", "left"}, {shortcuts.FocusRight, "pane.focus", "right"},
		{shortcuts.FocusUp, "pane.focus", "up"}, {shortcuts.FocusDown, "pane.focus", "down"},
	} {
		if !shortcutMatches(pressed, action.binding) {
			continue
		}
		if action.command == "pane.focus" {
			return c.focusDirection(action.direction)
		}
		command := c.creationCommand(action.command)
		if action.direction != "" {
			command["direction"] = action.direction
		}
		_ = c.session.DispatchAsync(command)
		return true
	}
	if shortcutMatches(pressed, shortcuts.NextTab) {
		return c.cycleTab(1)
	}
	if shortcutMatches(pressed, shortcuts.PreviousTab) {
		return c.cycleTab(-1)
	}
	if shortcutMatches(pressed, shortcuts.NextWorkspace) {
		return c.cycleWorkspace(1)
	}
	if shortcutMatches(pressed, shortcuts.PreviousWorkspace) {
		return c.cycleWorkspace(-1)
	}
	for index, binding := range numberedTabBindings(shortcuts.SwitchTab) {
		if shortcutMatches(pressed, binding) {
			return c.activateTabAt(index)
		}
	}
	if shortcutMatches(pressed, shortcuts.ToggleSidebar) {
		c.sidebarHidden = !c.sidebarHidden
		if c.invalidate != nil {
			c.invalidate()
		}
		return true
	}
	return false
}

func (c *WorkspaceClient) dispatchWindowShortcut(ev key.Event) bool {
	for _, spec := range c.windowShortcutBindings() {
		parsed, ok := automationInputEvent(spec)
		if !ok {
			continue
		}
		want, ok := parsed.(key.Event)
		if ok && ev.Name == want.Name && ev.Modifiers == want.Modifiers {
			return c.dispatchShortcut(spec)
		}
	}
	return false
}

func (c *WorkspaceClient) focusDirection(direction string) bool {
	c.mu.RLock()
	pane := c.state.FocusedPane
	c.mu.RUnlock()
	if pane == nil {
		return false
	}
	_ = c.session.DispatchAsync(map[string]any{
		"type": "pane.focus", "pane_id": *pane, "direction": direction,
	})
	return true
}

func (c *WorkspaceClient) activeTerminalID() (uuid.UUID, bool) {
	c.mu.RLock()
	state := c.state
	c.mu.RUnlock()
	return activeTerminalIDFromState(state)
}

func activeTerminalIDFromState(state gomodel.StateDump) (uuid.UUID, bool) {
	workspace := activeWorkspace(state)
	if workspace == nil {
		return uuid.Nil, false
	}
	tab := activeTab(*workspace)
	if tab == nil {
		return uuid.Nil, false
	}
	var root paneTree
	if json.Unmarshal(tab.Tree, &root) != nil {
		return uuid.Nil, false
	}
	return terminalForPane(&root, tab.ActivePane)
}

func terminalForPane(node *paneTree, paneID uuid.UUID) (uuid.UUID, bool) {
	if node == nil {
		return uuid.Nil, false
	}
	if node.Type == "leaf" {
		if node.PaneID == paneID && node.Terminal != nil && node.Terminal.Summary.TerminalID != uuid.Nil {
			return node.Terminal.Summary.TerminalID, true
		}
		return uuid.Nil, false
	}
	if id, ok := terminalForPane(node.First, paneID); ok {
		return id, true
	}
	return terminalForPane(node.Second, paneID)
}

func (c *WorkspaceClient) scrollActiveTerminal(deltaY float32) bool {
	id, ok := c.activeTerminalID()
	if !ok {
		return false
	}
	c.mu.RLock()
	term := c.terminals[id]
	c.mu.RUnlock()
	if term == nil {
		return false
	}
	term.inputPending.Store(false)
	lines := int(deltaY / 20)
	if lines == 0 {
		if deltaY < 0 {
			lines = -1
		} else if deltaY > 0 {
			lines = 1
		}
	}
	if lines == 0 {
		return false
	}
	term.mu.Lock()
	if term.emu == nil {
		term.mu.Unlock()
		return false
	}
	term.emu.Scroll(lines)
	term.snapshot = term.emu.FrameSnapshot()
	term.mu.Unlock()
	c.invalidateScroll()
	return true
}

// History movement is a discrete visual change, not a request to keep the
// display-rate input loop active after the wheel event has been handled.
func (c *WorkspaceClient) invalidateScroll() {
	if window := c.native.Load(); window != nil {
		window.invalidateOutput()
	} else if c.invalidate != nil {
		c.invalidate()
	}
}

func (c *WorkspaceClient) Bootstrap() error {
	if c.session.Diagnostic || c.recoveryBlocked {
		return nil
	}
	var state gomodel.StateDump
	if err := c.session.Call("state.dump", map[string]any{}, &state); err != nil {
		return err
	}
	c.applyState(state)
	if len(state.Workspaces) == 0 {
		return c.session.DispatchAsync(map[string]any{"type": "workspace.new"})
	}
	return nil
}

func (c *WorkspaceClient) applyState(state gomodel.StateDump) {
	if c.session != nil && c.session.Diagnostic {
		return
	}
	wanted := collectTerminalIDs(state)
	scrollbackOnClearScreen := make(map[uuid.UUID]bool, len(wanted))
	for id := range wanted {
		scrollbackOnClearScreen[id] = true
	}
	for _, raw := range state.Agents {
		if agent, ok := raw.(map[string]any); ok && agent["status"] == "running" {
			id := nativeUUID(agent["terminal_id"])
			if enabled, ok := agent["scrollback_on_clear_screen"].(bool); ok && id != uuid.Nil {
				scrollbackOnClearScreen[id] = enabled
			}
		}
	}
	c.mu.Lock()
	if state.StateRevision < c.state.StateRevision {
		c.mu.Unlock()
		return
	}
	c.state = c.selection.project(state)
	var remove []*terminalClient
	for id, term := range c.terminals {
		if _, ok := wanted[id]; !ok {
			delete(c.terminals, id)
			remove = append(remove, term)
		}
	}
	var add []terminalSummary
	var update []*terminalClient
	for id, summary := range wanted {
		if term := c.terminals[id]; term != nil {
			update = append(update, term)
		} else {
			add = append(add, summary)
		}
	}
	c.mu.Unlock()

	for _, term := range remove {
		_ = c.session.Detach(term.id)
		term.close()
	}
	for _, term := range update {
		term.setScrollbackOnClearScreen(scrollbackOnClearScreen[term.id])
	}
	for _, summary := range add {
		c.attachTerminal(summary, scrollbackOnClearScreen[summary.TerminalID])
	}
	if c.invalidate != nil {
		c.invalidate()
	}
}

func collectTerminalIDs(state gomodel.StateDump) map[uuid.UUID]terminalSummary {
	out := make(map[uuid.UUID]terminalSummary)
	for _, workspace := range state.Workspaces {
		for _, tab := range workspace.Tabs {
			var root paneTree
			if json.Unmarshal(tab.Tree, &root) == nil {
				collectTreeTerminals(&root, out)
			}
		}
	}
	if state.Workspace != nil {
		for _, tab := range state.Workspace.Tabs {
			var root paneTree
			if json.Unmarshal(tab.Tree, &root) == nil {
				collectTreeTerminals(&root, out)
			}
		}
	}
	return out
}

func collectTreeTerminals(node *paneTree, out map[uuid.UUID]terminalSummary) {
	if node == nil {
		return
	}
	if node.Type == "leaf" {
		if node.Terminal != nil && node.Terminal.Summary.TerminalID != uuid.Nil {
			out[node.Terminal.Summary.TerminalID] = node.Terminal.Summary
		}
		return
	}
	collectTreeTerminals(node.First, out)
	collectTreeTerminals(node.Second, out)
}

func (c *WorkspaceClient) attachTerminal(summary terminalSummary, scrollbackOnClearScreen bool) {
	var attached terminalAttach
	if err := c.session.Attach(summary.TerminalID, &attached); err != nil {
		return
	}
	emu := govt.New(attached.Size.Columns, attached.Size.Lines, c.config.Terminal.ScrollbackLines)
	emu.SetScrollbackOnClearScreen(scrollbackOnClearScreen)
	emu.SetCursorDefaults(c.currentConfig().Terminal.CursorStyle, c.currentConfig().Terminal.CursorBlink)
	emu.SetDefaultColors(terminalDefaultColors(c.config))
	emu.SetCellSize(
		max(1, int(c.config.Terminal.FontSize*0.6)),
		max(1, int(c.config.Terminal.LineHeight)),
	)
	last := uint64(0)
	for _, ev := range attached.Replay {
		if ev.Seq <= last {
			continue
		}
		applyWireEvent(emu, ev)
		last = ev.Seq
	}
	if attached.LastSeq > last {
		last = attached.LastSeq
	}
	view := NewTerminalView()
	view.FontSize = unit.Sp(c.config.Terminal.FontSize)
	view.LineHeight = unit.Dp(c.config.Terminal.LineHeight)
	view.CellWidth = unit.Dp(c.config.Terminal.FontSize * 0.6)
	view.FontFamily = c.config.Terminal.FontFamily
	view.Hyperlinks = c.config.Terminal.Hyperlinks
	view.Theme.Foreground = configColor(c.config.Theme.TerminalForeground, 0xe4e4e4)
	view.Theme.Background = configColor(c.config.Theme.TerminalBackground, 0x2c2c2c)
	view.Theme.Cursor = configColor(c.config.Theme.CursorBackground, 0xe4e4e4)
	view.Theme.Selection = configColor(c.config.Theme.Accent, 0x5e81ac)
	view.Theme.Selection.A = 0x88
	term := &terminalClient{
		id: summary.TerminalID, emu: emu, lastSeq: last, scrollbackOnClearScreen: scrollbackOnClearScreen,
		cols: attached.Size.Columns, rows: attached.Size.Lines,
		view: view,
	}
	term.input = &TerminalInput{
		OnShortcut:            c.dispatchWindowShortcut,
		BracketedPaste:        c.config.Features.BracketedPaste,
		Hyperlinks:            c.config.Terminal.Hyperlinks,
		HyperlinkCommandClick: c.config.Terminal.HyperlinkCommandClick,
		OnInput: func(data []byte) {
			// Input must not wait for output parsing or allocate a screen copy on
			// the frame thread. The terminal worker applies viewport follow-up.
			term.inputPending.Store(true)
			select {
			case c.snapshotWake <- struct{}{}:
			default:
			}
			if c.invalidate != nil {
				c.invalidate()
			}
			_ = c.session.DispatchAsync(map[string]any{
				"type":        "terminal.send_bytes",
				"terminal_id": summary.TerminalID,
				"bytes":       bytesAsInts(data),
			})
		},
		OnMouse: func(ev govt.MouseEvent) bool {
			if !c.hyperlinkConfig().Features.MouseReporting {
				return false
			}
			term.mu.Lock()
			if term.emu == nil {
				term.mu.Unlock()
				return false
			}
			accepted := term.emu.Mouse(ev)
			selectionCleared := accepted && term.selection.Active
			if accepted {
				// Mouse reports only send input; output workers publish screen changes.
				term.selection = Selection{}
			}
			term.mu.Unlock()
			if accepted {
				c.flushVTResponses(term)
			}
			if selectionCleared && c.invalidate != nil {
				c.invalidate()
			}
			return accepted
		},
		OnScroll: func(lines int) {
			term.inputPending.Store(false)
			term.mu.Lock()
			if term.emu == nil {
				term.mu.Unlock()
				return
			}
			term.emu.Scroll(lines)
			term.snapshot = term.emu.FrameSnapshot()
			term.mu.Unlock()
			c.invalidateScroll()
		},
		OnSelectionBoundaryStart: func(col, row int, extend bool) {
			term.inputPending.Store(false)
			if !c.hyperlinkConfig().Features.Selection {
				return
			}
			term.mu.Lock()
			if term.emu != nil {
				term.emu.HoldViewport()
			}
			absoluteRow := term.snapshot.YDisp + row
			if extend && !term.selection.empty() {
				if !term.selection.Boundaries {
					// Convert inclusive endpoints without changing the selected text.
					if term.selection.AnchorRow > term.selection.FocusRow || (term.selection.AnchorRow == term.selection.FocusRow && term.selection.AnchorCol > term.selection.FocusCol) {
						term.selection.AnchorCol++
					} else {
						term.selection.FocusCol++
					}
					term.selection.Boundaries = true
				}
				term.selection.FocusCol = col
				term.selection.FocusRow = absoluteRow
			} else {
				term.selection = Selection{AnchorCol: col, AnchorRow: absoluteRow, FocusCol: col, FocusRow: absoluteRow, Active: true, Absolute: true, Boundaries: true}
			}
			term.mu.Unlock()
			if c.invalidate != nil {
				c.invalidate()
			}
		},
		OnSelectionStart: func(col, row, clickCount int) {
			term.inputPending.Store(false)
			if !c.hyperlinkConfig().Features.Selection {
				return
			}
			term.mu.Lock()
			if term.emu != nil {
				term.emu.HoldViewport()
			}
			if clickCount >= 2 {
				term.selection = MultiClickSelection(term.snapshot, col, row, clickCount)
			} else {
				absoluteRow := term.snapshot.YDisp + row
				term.selection = Selection{
					AnchorCol: col, AnchorRow: absoluteRow,
					FocusCol: col, FocusRow: absoluteRow,
					Active: true, Absolute: true,
				}
			}
			term.mu.Unlock()
			if c.invalidate != nil {
				c.invalidate()
			}
		},
		OnSelectionMove: func(col, row int) {
			if !c.hyperlinkConfig().Features.Selection {
				return
			}
			term.mu.Lock()
			if term.selection.Active {
				term.selection.FocusCol = col
				if term.selection.Absolute {
					term.selection.FocusRow = term.snapshot.YDisp + row
				} else {
					term.selection.FocusRow = row
				}
			}
			term.mu.Unlock()
			if c.invalidate != nil {
				c.invalidate()
			}
		},
		OnSelectionEnd: func(col, row int) {
			if !c.hyperlinkConfig().Features.Selection {
				return
			}
			term.mu.Lock()
			if term.selection.Active {
				term.selection.FocusCol = col
				focusRow := row
				if term.selection.Absolute {
					focusRow = term.snapshot.YDisp + row
				}
				term.selection.FocusRow = focusRow
				if term.selection.AnchorCol == col && term.selection.AnchorRow == focusRow {
					term.selection = Selection{}
				}
			}
			term.mu.Unlock()
			if c.invalidate != nil {
				c.invalidate()
			}
		},
		OnSelectionAutoScroll: func(col, row, lines int) {
			if !c.hyperlinkConfig().Features.Selection {
				return
			}
			term.mu.Lock()
			if term.emu == nil || !term.selection.Active {
				term.mu.Unlock()
				return
			}
			before := term.snapshot.YDisp
			term.emu.Scroll(lines)
			term.snapshot = term.emu.FrameSnapshot()
			term.selection.FocusCol = col
			if term.selection.Absolute {
				term.selection.FocusRow = term.snapshot.YDisp + row
			} else {
				term.selection.FocusRow = row
			}
			changed := before != term.snapshot.YDisp
			term.mu.Unlock()
			if changed {
				c.invalidateScroll()
			}
		},
		OnCopy: func() string {
			if !c.hyperlinkConfig().Features.Selection {
				return ""
			}
			term.mu.RLock()
			selection := term.selection
			snapshot := term.snapshot
			emu := term.emu
			term.mu.RUnlock()
			if selection.empty() {
				return ""
			}
			if selection.Absolute && emu != nil {
				startCol, startRow, endCol, endRow := selection.normalized()
				return emu.SelectionText(startRow, startCol, endRow, endCol)
			}
			return SelectedText(snapshot, selection)
		},
		OnHyperlink: func(uri string) {
			c.activateHyperlink(uri)
		},
	}
	term.snapshot = emu.Snapshot()
	c.flushVTResponses(term)

	c.mu.Lock()
	if old := c.terminals[summary.TerminalID]; old != nil {
		c.mu.Unlock()
		term.close()
		return
	}
	c.terminals[summary.TerminalID] = term
	c.mu.Unlock()
}

func (c *WorkspaceClient) activateHyperlink(uri string) {
	cfg := c.hyperlinkConfig()
	uri = strings.TrimSpace(uri)
	if uri == "" || !cfg.Terminal.Hyperlinks {
		return
	}
	destination := ""
	if c.remoteDestination != "" && strings.HasPrefix(uri, "file://") {
		destination = c.remoteDestination
	}
	if destination != "" && !cfg.Terminal.RemoteHyperlinkAutoDownload {
		c.hyperlinkMu.Lock()
		c.hyperlinkPrompt = &hyperlinkPrompt{URI: uri, Destination: destination}
		c.hyperlinkMu.Unlock()
		if c.invalidate != nil {
			c.invalidate()
		}
		return
	}
	c.runHyperlink(uri, destination)
}

func (c *WorkspaceClient) hyperlinkConfig() goconfig.AppConfig {
	if c.settingsStore != nil {
		return *c.settingsStore.value.Load()
	}
	return c.config
}

func (c *WorkspaceClient) confirmHyperlink() {
	c.hyperlinkMu.Lock()
	prompt := c.hyperlinkPrompt
	if prompt == nil || prompt.Working {
		c.hyperlinkMu.Unlock()
		return
	}
	prompt.Working = true
	prompt.Error = ""
	uri, destination := prompt.URI, prompt.Destination
	c.hyperlinkMu.Unlock()
	if c.invalidate != nil {
		c.invalidate()
	}
	c.runHyperlink(uri, destination)
}

func (c *WorkspaceClient) cancelHyperlink() {
	c.hyperlinkMu.Lock()
	c.hyperlinkPrompt = nil
	c.hyperlinkMu.Unlock()
	if c.invalidate != nil {
		c.invalidate()
	}
}

func (c *WorkspaceClient) runHyperlink(uri, destination string) {
	cfg := c.hyperlinkConfig()
	go func() {
		var err error
		if destination != "" {
			var path string
			path, err = gohyperlink.DownloadRemote(
				destination,
				uri,
				cfg.Terminal.HyperlinkDownloadDirectory,
			)
			if err == nil {
				err = gohyperlink.OpenPath(path)
			}
		} else {
			err = gohyperlink.OpenTarget(uri)
		}

		c.hyperlinkMu.Lock()
		prompt := c.hyperlinkPrompt
		same := prompt != nil && prompt.URI == uri && prompt.Destination == destination
		if err == nil {
			if same {
				c.hyperlinkPrompt = nil
			}
		} else if prompt == nil || same {
			if prompt == nil {
				prompt = &hyperlinkPrompt{URI: uri, Destination: destination}
				c.hyperlinkPrompt = prompt
			}
			prompt.Working = false
			prompt.Error = err.Error()
		}
		c.hyperlinkMu.Unlock()
		if c.invalidate != nil {
			c.invalidate()
		}
	}()
}

func applyWireEvent(emu *govt.Emulator, ev goprotocol.WireTerminalEvent) {
	switch ev.Type {
	case "output":
		if data, err := base64.StdEncoding.DecodeString(ev.Bytes); err == nil {
			emu.WriteReplay(data)
		}
	case "resize":
		emu.Resize(ev.Columns, ev.Lines)
	}
}

func (c *WorkspaceClient) applyTerminalEvent(push goclient.TerminalPush) {
	c.applyTerminalEvents([]goclient.TerminalPush{push})
}

func (c *WorkspaceClient) applyTerminalEvents(batch []goclient.TerminalPush) {
	changed := make(map[*terminalClient]bool)
	for _, push := range batch {
		if term := c.applyTerminalEventDeferred(push); term != nil {
			changed[term] = true
		}
	}
	c.publishTerminalSnapshots(changed)
}

func (c *WorkspaceClient) publishTerminalSnapshots(changed map[*terminalClient]bool) bool {
	c.mu.RLock()
	for _, term := range c.terminals {
		if term.inputPending.Load() {
			term.mu.Lock()
			if term.snapshot.YDisp != term.snapshot.YBase || term.selection.Active {
				changed[term] = true
			}
			// If the published view is already at the bottom, keep the request
			// until PTY output arrives. Input can race ahead of echo; a selection
			// or held viewport may still keep later output from following the tail.
			term.mu.Unlock()
		}
	}
	c.mu.RUnlock()
	if len(changed) == 0 {
		return false
	}
	if end := traceNativeWork("terminal.snapshot"); end != nil {
		defer end()
	}
	for term := range changed {
		term.mu.Lock()
		if term.emu != nil {
			if term.inputPending.Swap(false) {
				term.selection = Selection{}
				term.emu.ScrollToBottom()
			}
			term.snapshot = term.emu.FrameSnapshot()
		}
		term.mu.Unlock()
		delete(changed, term)
	}
	if window := c.native.Load(); window != nil {
		window.invalidateOutput()
	} else if c.invalidate != nil {
		c.invalidate()
	}
	return true
}

func (c *WorkspaceClient) applyTerminalEventDeferred(push goclient.TerminalPush) *terminalClient {
	if end := traceNativeWork("terminal.event"); end != nil {
		defer end()
	}
	c.mu.RLock()
	term := c.terminals[push.TerminalID]
	c.mu.RUnlock()
	if term == nil {
		return nil
	}
	term.parseMu.Lock()
	defer term.parseMu.Unlock()

	term.mu.Lock()
	if term.emu == nil {
		term.mu.Unlock()
		return nil
	}
	if push.Event.Seq <= term.lastSeq {
		term.mu.Unlock()
		return nil
	}
	if term.lastSeq != 0 && push.Event.Seq != term.lastSeq+1 {
		term.mu.Unlock()
		c.resyncTerminal(push.TerminalID)
		return nil
	}
	term.lastSeq = push.Event.Seq
	emu := term.emu
	term.mu.Unlock()
	switch push.Event.Kind {
	case goprotocol.OutputEvent:
		beforeTrim, beforeAlt := emu.HistoryState()
		bells := emu.BellCount()
		end := traceNativeWork("terminal.parse")
		emu.Write(push.Event.Data)
		if end != nil {
			end()
		}
		countNativeWork("count.output_bytes", len(push.Event.Data))
		if emu.BellCount() > bells {
			if window := c.native.Load(); window != nil {
				select {
				case window.bellNotifications <- term.id:
				default:
				}
			}
		}
		term.mu.Lock()
		afterTrim, afterAlt := emu.HistoryState()
		if beforeAlt != afterAlt {
			term.selection = Selection{}
		} else if term.selection.Active && term.selection.Absolute && afterTrim > beforeTrim {
			trim := int(afterTrim - beforeTrim)
			term.selection.AnchorRow -= trim
			term.selection.FocusRow -= trim
			// Do not silently copy a different line when selected history expires.
			if term.selection.AnchorRow < 0 || term.selection.FocusRow < 0 {
				term.selection = Selection{}
			}
		}
		term.mu.Unlock()
	case goprotocol.ResizeEvent:
		emu.Resize(push.Event.Size.Columns, push.Event.Size.Lines)
		term.mu.Lock()
		term.cols = push.Event.Size.Columns
		term.rows = push.Event.Size.Lines
		term.selection = Selection{}
		term.mu.Unlock()
	}
	c.flushVTResponses(term)
	return term
}

func (c *WorkspaceClient) resyncTerminal(id uuid.UUID) {
	c.mu.RLock()
	old := c.terminals[id]
	c.mu.RUnlock()
	if old == nil {
		return
	}
	var attached terminalAttach
	if err := c.session.Attach(id, &attached); err != nil {
		return
	}
	old.mu.RLock()
	scrollbackOnClearScreen := old.scrollbackOnClearScreen
	old.mu.RUnlock()
	next := govt.New(attached.Size.Columns, attached.Size.Lines, c.config.Terminal.ScrollbackLines)
	next.SetScrollbackOnClearScreen(scrollbackOnClearScreen)
	next.SetCursorDefaults(c.currentConfig().Terminal.CursorStyle, c.currentConfig().Terminal.CursorBlink)
	next.SetDefaultColors(terminalDefaultColors(c.config))
	next.SetCellSize(
		max(1, int(c.config.Terminal.FontSize*0.6)),
		max(1, int(c.config.Terminal.LineHeight)),
	)
	var last uint64
	for _, ev := range attached.Replay {
		if ev.Seq <= last {
			continue
		}
		applyWireEvent(next, ev)
		last = ev.Seq
	}
	if attached.LastSeq > last {
		last = attached.LastSeq
	}
	old.mu.Lock()
	prev := old.emu
	old.emu = next
	old.snapshot = next.Snapshot()
	old.selection = Selection{}
	old.lastSeq = last
	old.cols = attached.Size.Columns
	old.rows = attached.Size.Lines
	old.mu.Unlock()
	if prev != nil {
		prev.Close()
	}
	c.flushVTResponses(old)
	if c.invalidate != nil {
		c.invalidate()
	}
}

func (c *WorkspaceClient) flushVTResponses(term *terminalClient) {
	term.mu.RLock()
	if term.emu == nil {
		term.mu.RUnlock()
		return
	}
	responses := term.emu.TakeResponses()
	if data, ok := term.emu.TakeClipboardWrite(); ok {
		c.oscClipboardMu.Lock()
		c.oscClipboardWrite = data
		c.oscClipboardMu.Unlock()
	}
	term.mu.RUnlock()
	for _, data := range responses {
		_ = c.session.DispatchAsync(map[string]any{
			"type":        "terminal.send_bytes",
			"terminal_id": term.id,
			"bytes":       bytesAsInts(data),
		})
	}
}

func bytesAsInts(data []byte) []int {
	out := make([]int, len(data))
	for i, b := range data {
		out[i] = int(b)
	}
	return out
}

func (c *WorkspaceClient) Layout(gtx layout.Context, th *material.Theme) layout.Dimensions {
	c.layoutMu.Lock()
	defer c.layoutMu.Unlock()
	c.frameSize = gtx.Constraints.Max
	c.frameMetric = gtx.Metric
	c.hitRegions = c.hitRegions[:0]
	return c.layoutUnlocked(gtx, th)
}

func (c *WorkspaceClient) layoutUnlocked(gtx layout.Context, th *material.Theme) layout.Dimensions {
	c.layoutFrame++
	defer func() {
		for id, divider := range c.dividers {
			if divider.frame != c.layoutFrame {
				delete(c.dividers, id)
			}
		}
	}()
	c.pollSettingsSave()
	c.processWindowShortcuts(gtx)
	cfg := c.currentConfig()
	th.Palette.Bg = configColor(cfg.Theme.ChromeBackground, 0x121416)
	th.Palette.Fg = configColor(cfg.Theme.UIForeground, 0xe6eaea)
	th.Palette.ContrastBg = configColor(cfg.Theme.Accent, 0x72d6ab)
	th.Palette.ContrastFg = configColor(cfg.Theme.AccentForeground, 0x111714)
	th.TextSize = unit.Sp(cfg.UI.UIFontSize)
	th.Face = font.Typeface(cfg.UI.UIFontFamily)
	paint.FillShape(gtx.Ops, th.Palette.Bg, clip.Rect{Max: gtx.Constraints.Max}.Op())
	for c.settingsButton.Clicked(gtx) {
		c.openSettings()
	}
	for c.splitRightButton.Clicked(gtx) {
		c.splitPane("right")
	}
	for c.splitDownButton.Clicked(gtx) {
		c.splitPane("down")
	}
	c.mu.RLock()
	state := c.state
	connectionError := c.connectionError
	c.mu.RUnlock()
	c.frameStateRevision = state.StateRevision
	c.frameActiveWorkspace = uuid.Nil
	c.frameFocusedPane = uuid.Nil
	c.frameActiveTerminal = uuid.Nil
	if state.ActiveWorkspace != nil {
		c.frameActiveWorkspace = *state.ActiveWorkspace
	}
	if state.FocusedPane != nil {
		c.frameFocusedPane = *state.FocusedPane
	}
	if id, ok := activeTerminalIDFromState(state); ok {
		c.frameActiveTerminal = id
	}

	for c.newWorkspace.Clicked(gtx) {
		_ = c.session.DispatchAsync(map[string]any{"type": "workspace.new"})
	}
	for c.newTab.Clicked(gtx) {
		_ = c.session.DispatchAsync(c.creationCommand("tab.new"))
	}

	return layout.Stack{Alignment: layout.Center}.Layout(gtx,
		layout.Expanded(func(gtx layout.Context) layout.Dimensions {
			if c.settings.visible {
				gtx = gtx.Disabled()
			}
			return c.layoutMain(gtx, th, state)
		}),
		layout.Expanded(func(gtx layout.Context) layout.Dimensions {
			return c.layoutHyperlinkOverlay(gtx, th)
		}),
		layout.Expanded(func(gtx layout.Context) layout.Dimensions { return c.layoutSettings(gtx, th) }),
		layout.Stacked(func(gtx layout.Context) layout.Dimensions {
			if connectionError == "" {
				return layout.Dimensions{}
			}
			return material.Body2(th, "Connection closed: "+connectionError+". Reopen the window to reconnect.").Layout(gtx)
		}),
	)
}

func (c *WorkspaceClient) layoutMain(gtx layout.Context, th *material.Theme, state gomodel.StateDump) layout.Dimensions {
	sidebarWidth := gtx.Dp(unit.Dp(c.currentConfig().UI.SidebarWidth))
	if c.sidebarHidden {
		return c.layoutWorkspace(gtx, th, state, image.Point{})
	}
	return layout.Flex{Axis: layout.Horizontal}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.X = sidebarWidth
			gtx.Constraints.Max.X = sidebarWidth
			paint.FillShape(gtx.Ops, configColor(c.currentConfig().Theme.SidebarBackground, 0x171a1c), clip.Rect{Max: gtx.Constraints.Max}.Op())
			return c.layoutSidebar(gtx, th, state)
		}),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			return c.layoutWorkspace(gtx, th, state, image.Pt(sidebarWidth, 0))
		}),
	)
}

func (c *WorkspaceClient) layoutHyperlinkOverlay(gtx layout.Context, th *material.Theme) layout.Dimensions {
	c.hyperlinkMu.Lock()
	prompt := c.hyperlinkPrompt
	if prompt != nil {
		copy := *prompt
		prompt = &copy
	}
	c.hyperlinkMu.Unlock()
	if prompt == nil {
		return layout.Dimensions{}
	}

	for c.hyperlinkCancel.Clicked(gtx) {
		c.cancelHyperlink()
		return layout.Dimensions{}
	}
	for c.hyperlinkConfirm.Clicked(gtx) {
		c.confirmHyperlink()
	}
	for c.hyperlinkScrim.Clicked(gtx) {
	}

	c.hyperlinkMu.Lock()
	current := c.hyperlinkPrompt
	if current != nil {
		copy := *current
		prompt = &copy
	} else {
		prompt = nil
	}
	c.hyperlinkMu.Unlock()
	if prompt == nil {
		return layout.Dimensions{}
	}

	return layout.Stack{Alignment: layout.Center}.Layout(gtx,
		layout.Expanded(func(gtx layout.Context) layout.Dimensions {
			return c.hyperlinkScrim.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				size := gtx.Constraints.Max
				paint.FillShape(gtx.Ops, color.NRGBA{A: 0xb0}, clip.Rect{Max: size}.Op())
				return layout.Dimensions{Size: size}
			})
		}),
		layout.Stacked(func(gtx layout.Context) layout.Dimensions {
			if gtx.Constraints.Max.X > 520 {
				gtx.Constraints.Max.X = 520
			}
			if gtx.Constraints.Max.Y > 280 {
				gtx.Constraints.Max.Y = 280
			}
			record := op.Record(gtx.Ops)
			dims := layout.UniformInset(unit.Dp(18)).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				title := "Open hyperlink"
				action := "Open"
				if prompt.Destination != "" {
					title = "Download remote file and open?"
					action = "Download & Open"
				}
				if prompt.Working {
					title = "Processing hyperlink…"
				}
				children := []layout.FlexChild{
					layout.Rigid(material.H6(th, title).Layout),
					layout.Rigid(layout.Spacer{Height: unit.Dp(10)}.Layout),
					layout.Rigid(material.Body2(th, prompt.URI).Layout),
				}
				if prompt.Destination != "" {
					children = append(children,
						layout.Rigid(layout.Spacer{Height: unit.Dp(8)}.Layout),
						layout.Rigid(material.Caption(th, "Download directory: "+c.config.Terminal.HyperlinkDownloadDirectory).Layout),
					)
				}
				if prompt.Error != "" {
					children = append(children,
						layout.Rigid(layout.Spacer{Height: unit.Dp(8)}.Layout),
						layout.Rigid(material.Body2(th, prompt.Error).Layout),
					)
				}
				if !prompt.Working {
					children = append(children,
						layout.Rigid(layout.Spacer{Height: unit.Dp(14)}.Layout),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							return layout.Flex{Axis: layout.Horizontal, Spacing: layout.SpaceEnd}.Layout(gtx,
								layout.Rigid(material.Button(th, &c.hyperlinkCancel, "Cancel").Layout),
								layout.Rigid(layout.Spacer{Width: unit.Dp(10)}.Layout),
								layout.Rigid(material.Button(th, &c.hyperlinkConfirm, action).Layout),
							)
						}),
					)
				}
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
			})
			call := record.Stop()
			paint.FillShape(gtx.Ops, configColor(c.config.Theme.ChromeBackground, 0x121416), clip.Rect{Max: dims.Size}.Op())
			call.Add(gtx.Ops)
			return dims
		}),
	)
}

func (c *WorkspaceClient) layoutSidebar(gtx layout.Context, th *material.Theme, state gomodel.StateDump) layout.Dimensions {
	copyTheme := *th
	th = &copyTheme
	th.Palette.ContrastFg = th.Palette.Fg
	items := make([]layout.FlexChild, 0, len(state.Workspaces)+8)
	y := 0
	header := material.Label(th, unit.Sp(18), "water")
	header.Font.Weight = 600
	items = append(items, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
		dims := layout.Inset{Left: 20, Right: 16, Top: 22, Bottom: 8}.Layout(gtx, header.Layout)
		y += dims.Size.Y
		return dims
	}))
	items = append(items, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
		dims := sectionLabel(gtx, th, "CONNECTIONS")
		y += dims.Size.Y
		return dims
	}))

	activeConnection, connections, activateConnection := c.connectionSnapshot()
	connectRemote, removeConnection := c.connectionActions()

	if len(connections) > 0 {
		for _, connection := range connections {
			entry := connection
			c.connectionMu.Lock()
			click := c.connectionClicks[entry.ID]
			if click == nil {
				click = new(widget.Clickable)
				c.connectionClicks[entry.ID] = click
			}
			disconnect := c.connectionDisconnectClicks[entry.ID]
			if disconnect == nil {
				disconnect = new(widget.Clickable)
				c.connectionDisconnectClicks[entry.ID] = disconnect
			}
			c.connectionMu.Unlock()

			for click.Clicked(gtx) {
				if activateConnection != nil {
					activateConnection(entry.ID)
				}
			}
			for disconnect.Clicked(gtx) {
				if entry.Kind == "remote" && removeConnection != nil {
					id := entry.ID
					go func() { _ = removeConnection(id) }()
				}
			}

			items = append(items, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				top := y
				label := entry.Name
				if entry.ID == activeConnection {
					label = "● " + label
				} else {
					label = "○ " + label
				}
				if entry.Status != "" && entry.Status != "connected" {
					label += " (" + entry.Status + ")"
				}

				dims := layout.Inset{Left: unit.Dp(8), Right: unit.Dp(8), Bottom: unit.Dp(4)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Axis: layout.Horizontal}.Layout(gtx,
						layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
							button := chromeButton(th, click, label)
							button.FillWidth = true
							button.Background = configColor(c.currentConfig().Theme.SidebarConnectionBackground, 0x202427)
							if entry.ID == activeConnection {
								button.Background = configColor(c.currentConfig().Theme.SidebarConnectionActiveBackground, 0x222927)
								button.Selected = true
							}
							return button.Layout(gtx)
						}),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							if entry.Kind != "remote" {
								return layout.Dimensions{}
							}
							return layout.Inset{Left: unit.Dp(4)}.Layout(gtx, chromeButton(th, disconnect, "×").Layout)
						}),
					)
				})
				c.hitRegions = append(c.hitRegions, automationHit{
					Rect: image.Rect(0, top, gtx.Constraints.Max.X, top+dims.Size.Y),
					Kind: hitConnection, ID: entry.ID,
				})
				y += dims.Size.Y
				return dims
			}))
		}
	}

	if connectRemote != nil {
		for c.newRemote.Clicked(gtx) {
			c.connectionMu.Lock()
			c.remoteFormVisible = true
			c.remoteError = ""
			c.connectionMu.Unlock()
		}
		for c.remoteCancel.Clicked(gtx) {
			c.connectionMu.Lock()
			if !c.remoteConnecting {
				c.remoteFormVisible = false
				c.remoteError = ""
				c.remoteClearEditor = true
			}
			c.connectionMu.Unlock()
		}
		for c.remoteConnect.Clicked(gtx) {
			destination := strings.TrimSpace(c.remoteEditor.Text())
			if destination == "" {
				c.connectionMu.Lock()
				c.remoteError = "SSH destination is required"
				c.connectionMu.Unlock()
				continue
			}
			c.connectionMu.Lock()
			if c.remoteConnecting {
				c.connectionMu.Unlock()
				continue
			}
			c.remoteConnecting = true
			c.remoteError = ""
			c.connectionMu.Unlock()
			go func(destination string) {
				err := connectRemote(destination)
				c.connectionMu.Lock()
				c.remoteConnecting = false
				if err != nil {
					c.remoteError = err.Error()
					c.remoteFormVisible = true
				} else {
					c.remoteError = ""
					c.remoteFormVisible = false
					c.remoteClearEditor = true
				}
				c.connectionMu.Unlock()
				if c.invalidate != nil {
					c.invalidate()
				}
			}(destination)
		}

		c.connectionMu.Lock()
		if c.remoteClearEditor {
			c.remoteEditor.SetText("")
			c.remoteClearEditor = false
		}
		showRemote := c.remoteFormVisible
		connecting := c.remoteConnecting
		remoteError := c.remoteError
		c.connectionMu.Unlock()

		if !showRemote {
			items = append(items, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				top := y
				dims := layout.Inset{Left: unit.Dp(8), Right: unit.Dp(8), Bottom: unit.Dp(8)}.Layout(gtx, chromeButton(th, &c.newRemote, "+ Connect remote").Layout)
				c.hitRegions = append(c.hitRegions, automationHit{
					Rect: image.Rect(0, top, gtx.Constraints.Max.X, top+dims.Size.Y),
					Kind: hitNewRemote,
				})
				y += dims.Size.Y
				return dims
			}))
		} else {
			items = append(items, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				dims := layout.Inset{Left: unit.Dp(8), Right: unit.Dp(8), Bottom: unit.Dp(8)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					children := []layout.FlexChild{
						layout.Rigid(chromeEditor(th, &c.remoteEditor, "user@host")),
						layout.Rigid(layout.Spacer{Height: unit.Dp(6)}.Layout),
					}
					if remoteError != "" {
						children = append(children,
							layout.Rigid(material.Caption(th, remoteError).Layout),
							layout.Rigid(layout.Spacer{Height: unit.Dp(6)}.Layout),
						)
					}
					if connecting {
						children = append(children, layout.Rigid(material.Caption(th, "Connecting…").Layout))
					} else {
						children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							return layout.Flex{Axis: layout.Horizontal}.Layout(gtx,
								layout.Flexed(1, chromeButton(th, &c.remoteConnect, "Connect").Layout),
								layout.Rigid(layout.Spacer{Width: unit.Dp(6)}.Layout),
								layout.Rigid(chromeButton(th, &c.remoteCancel, "Cancel").Layout),
							)
						}))
					}
					return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
				})
				y += dims.Size.Y
				return dims
			}))
		}
	}

	items = append(items, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
		dims := sectionLabel(gtx, th, "WORKSPACES")
		y += dims.Size.Y
		return dims
	}))
	for _, workspace := range state.Workspaces {
		w := workspace
		click := c.workspaceClicks[w.ID]
		if click == nil {
			click = new(widget.Clickable)
			c.workspaceClicks[w.ID] = click
		}
		for click.Clicked(gtx) {
			_ = c.session.DispatchAsync(map[string]any{"type": "workspace.activate", "workspace_id": w.ID})
		}
		items = append(items, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			top := y
			button := chromeButton(th, click, w.Title)
			button.FillWidth = true
			button.Background = configColor(c.currentConfig().Theme.SidebarWorkspaceBackground, 0x1d2124)
			if state.ActiveWorkspace != nil && w.ID == *state.ActiveWorkspace {
				button.Background = configColor(c.currentConfig().Theme.SidebarWorkspaceActiveBackground, 0x29332f)
				button.Selected = true
			}
			dims := layout.Inset{Left: unit.Dp(8), Right: unit.Dp(8), Bottom: unit.Dp(4)}.Layout(gtx, button.Layout)
			c.hitRegions = append(c.hitRegions, automationHit{
				Rect: image.Rect(0, top, gtx.Constraints.Max.X, top+dims.Size.Y),
				Kind: hitWorkspace, ID: w.ID,
			})
			y += dims.Size.Y
			return dims
		}))
	}
	items = append(items, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
		top := y
		button := chromeButton(th, &c.newWorkspace, "+ New workspace")
		dims := layout.UniformInset(unit.Dp(8)).Layout(gtx, button.Layout)
		c.hitRegions = append(c.hitRegions, automationHit{
			Rect: image.Rect(0, top, gtx.Constraints.Max.X, top+dims.Size.Y),
			Kind: hitNewWorkspace,
		})
		y += dims.Size.Y
		return dims
	}))
	items = append(items, layout.Flexed(1, layout.Spacer{}.Layout), layout.Rigid(func(gtx layout.Context) layout.Dimensions {
		chromeRule(gtx, th)
		return layout.Inset{Left: 20, Right: 12, Top: 14, Bottom: 16}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			label := material.Label(th, unit.Sp(10), "WATER  /  TERMINAL")
			label.Color = mixColor(th.Palette.Bg, th.Palette.Fg, .4)
			return label.Layout(gtx)
		})
	}))
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, items...)
}

func (c *WorkspaceClient) layoutWorkspace(gtx layout.Context, th *material.Theme, state gomodel.StateDump, origin image.Point) layout.Dimensions {
	workspace := activeWorkspace(state)
	if workspace == nil {
		label := material.Label(th, unit.Sp(14), "Creating workspace…")
		return layout.Center.Layout(gtx, label.Layout)
	}
	tabHeight := 0
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			dims := c.layoutTabs(gtx, th, *workspace, origin)
			tabHeight = dims.Size.Y
			return dims
		}),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			tab := activeTab(*workspace)
			if tab == nil {
				label := material.Label(th, unit.Sp(14), "No active tab")
				return layout.Center.Layout(gtx, label.Layout)
			}
			var root paneTree
			if err := json.Unmarshal(tab.Tree, &root); err != nil {
				label := material.Label(th, unit.Sp(14), "Invalid pane tree")
				return layout.Center.Layout(gtx, label.Layout)
			}
			return c.layoutPane(gtx, th, &root, *tab, origin.Add(image.Pt(0, tabHeight)))
		}),
	)
}

func activeWorkspace(state gomodel.StateDump) *gomodel.WorkspaceDump {
	if state.ActiveWorkspace != nil {
		for idx := range state.Workspaces {
			if state.Workspaces[idx].ID == *state.ActiveWorkspace {
				return &state.Workspaces[idx]
			}
		}
	}
	if state.Workspace != nil {
		return state.Workspace
	}
	if len(state.Workspaces) > 0 {
		return &state.Workspaces[0]
	}
	return nil
}

func activeTab(workspace gomodel.WorkspaceDump) *gomodel.TabDump {
	if workspace.ActiveTab != nil {
		for idx := range workspace.Tabs {
			if workspace.Tabs[idx].ID == *workspace.ActiveTab {
				return &workspace.Tabs[idx]
			}
		}
	}
	if len(workspace.Tabs) > 0 {
		return &workspace.Tabs[0]
	}
	return nil
}

func (c *WorkspaceClient) layoutTabs(gtx layout.Context, th *material.Theme, workspace gomodel.WorkspaceDump, origin image.Point) layout.Dimensions {
	copyTheme := *th
	th = &copyTheme
	th.Palette.ContrastFg = th.Palette.Fg
	children := make([]layout.FlexChild, 0, len(workspace.Tabs)+1)
	x := 0
	for _, tab := range workspace.Tabs {
		t := tab
		click := c.tabClicks[t.ID]
		if click == nil {
			click = new(widget.Clickable)
			c.tabClicks[t.ID] = click
		}
		for click.Clicked(gtx) {
			_ = c.session.DispatchAsync(map[string]any{"type": "tab.activate", "tab_id": t.ID})
		}
		children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			left := x
			u := c.currentConfig().UI
			button := chromeButton(th, click, boundedTabTitle(tabCommandTitle(t), u.TabMaxTitleLength))
			button.TextSize = unit.Sp(u.TabFontSize)
			button.Background = configColor(c.currentConfig().Theme.TabInactiveBackground, 0x191c1e)
			if workspace.ActiveTab != nil && t.ID == *workspace.ActiveTab {
				button.Background = configColor(c.currentConfig().Theme.TabActiveBackground, 0x252b2a)
				button.Selected = true
			}
			dims := layout.Inset{Left: unit.Dp(4), Top: unit.Dp(4), Bottom: unit.Dp(4)}.Layout(gtx, button.Layout)
			c.hitRegions = append(c.hitRegions, automationHit{
				Rect: image.Rect(origin.X+left, origin.Y, origin.X+left+dims.Size.X, origin.Y+dims.Size.Y),
				Kind: hitTab, ID: t.ID,
			})
			x += dims.Size.X
			return dims
		}))
	}
	children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
		left := x
		button := chromeButton(th, &c.newTab, "+")
		button.Background = configColor(c.currentConfig().Theme.TabAddBackground, 0x2b3032)
		dims := layout.UniformInset(unit.Dp(4)).Layout(gtx, button.Layout)
		c.hitRegions = append(c.hitRegions, automationHit{
			Rect: image.Rect(origin.X+left, origin.Y, origin.X+left+dims.Size.X, origin.Y+dims.Size.Y),
			Kind: hitNewTab,
		})
		x += dims.Size.X
		return dims
	}))
	children = append(children, layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
		return layout.Dimensions{Size: gtx.Constraints.Min}
	}))
	dims := layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx, children...)
	chromeOffset(gtx, image.Pt(0, dims.Size.Y-1), func() { chromeRule(gtx, th) })
	return dims
}

func (c *WorkspaceClient) layoutPane(gtx layout.Context, th *material.Theme, node *paneTree, tab gomodel.TabDump, origin image.Point) layout.Dimensions {
	return c.layoutPaneAt(gtx, th, node, tab, origin, nil)
}

func (c *WorkspaceClient) layoutPaneAt(gtx layout.Context, th *material.Theme, node *paneTree, tab gomodel.TabDump, origin image.Point, path []bool) layout.Dimensions {
	if node == nil {
		return layout.Dimensions{}
	}
	if node.Type == "split" {
		return c.layoutSplit(gtx, th, node, tab, origin, path)
	}
	cfg := c.currentConfig()
	size := gtx.Constraints.Max
	area := clip.Rect{Max: size}.Push(gtx.Ops)
	defer area.Pop()
	margin := gtx.Dp(unit.Dp(cfg.UI.PaneMargin))
	radius := gtx.Dp(unit.Dp(cfg.UI.PaneCornerRadius))
	bounds := image.Rect(margin, margin, max(margin, size.X-margin), max(margin, size.Y-margin))
	border := configColor(cfg.Theme.InactivePaneBorder, 0x42484a)
	if tab.ActivePane == node.PaneID {
		border = configColor(cfg.Theme.ActivePaneBorder, 0x72d6ab)
	}
	paint.FillShape(gtx.Ops, border, clip.RRect{Rect: bounds, NE: radius, NW: radius, SE: radius, SW: radius}.Op(gtx.Ops))
	bounds = bounds.Inset(1)
	paint.FillShape(gtx.Ops, configColor(cfg.Theme.PaneBackground, 0x2c2c2c), clip.RRect{Rect: bounds, NE: radius, NW: radius, SE: radius, SW: radius}.Op(gtx.Ops))
	inset := cfg.UI.PaneMargin + cfg.UI.PanePadding
	return layout.UniformInset(unit.Dp(inset)).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return c.layoutLeaf(gtx, th, node, tab, origin.Add(image.Pt(gtx.Dp(unit.Dp(inset)), gtx.Dp(unit.Dp(inset)))))
	})
}

func (c *WorkspaceClient) layoutLeaf(gtx layout.Context, th *material.Theme, node *paneTree, tab gomodel.TabDump, origin image.Point) layout.Dimensions {
	c.hitRegions = append(c.hitRegions, automationHit{
		Rect: image.Rectangle{Min: origin, Max: origin.Add(gtx.Constraints.Max)},
		Kind: hitPane, ID: node.PaneID,
	})
	if node.Terminal == nil {
		label := material.Label(th, unit.Sp(14), "Empty pane")
		return layout.Center.Layout(gtx, label.Layout)
	}
	id := node.Terminal.Summary.TerminalID
	c.mu.RLock()
	term := c.terminals[id]
	c.mu.RUnlock()
	if term == nil {
		label := material.Label(th, unit.Sp(14), "Attaching terminal…")
		return layout.Center.Layout(gtx, label.Layout)
	}
	applyViewConfig(term.view, c.currentConfig())
	syncTerminalCursor(term, c.currentConfig())
	if syncTerminalColors(term) {
		go c.flushVTResponses(term)
	}
	term.view.Focused = tab.ActivePane == node.PaneID
	bindings := c.currentConfig().Shortcuts
	term.input.Shortcuts = &bindings
	term.input.Hyperlinks = c.currentConfig().Terminal.Hyperlinks
	term.input.HyperlinkCommandClick = c.currentConfig().Terminal.HyperlinkCommandClick
	c.ensureTerminalSize(gtx, term)
	term.mu.RLock()
	snapshot := term.snapshot
	selection := term.selection
	term.mu.RUnlock()
	cellWidth := gtx.Dp(term.view.CellWidth)
	lineHeight := gtx.Dp(term.view.LineHeight)
	if c.restoreTerminalFocus && !c.settings.visible && tab.ActivePane == node.PaneID {
		term.input.Focus(gtx)
		c.restoreTerminalFocus = false
	}
	if !c.settings.visible {
		term.input.Process(gtx, snapshot, cellWidth, lineHeight)
	}
	composition, composing := term.input.CompositionState()
	term.mu.Lock()
	term.imePreedit = composition
	term.imeComposing = composing
	term.mu.Unlock()
	dims := term.view.Layout(gtx, th, snapshot, selection, composition)
	term.input.Add(gtx, dims.Size)
	return dims
}

func (c *WorkspaceClient) ensureTerminalSize(gtx layout.Context, term *terminalClient) {
	cellWidth := gtx.Dp(term.view.CellWidth)
	lineHeight := gtx.Dp(term.view.LineHeight)
	if cellWidth < 1 || lineHeight < 1 {
		return
	}
	cols := gtx.Constraints.Max.X / cellWidth
	rows := gtx.Constraints.Max.Y / lineHeight
	if cols < 2 {
		cols = 2
	}
	if cols > 512 {
		cols = 512
	}
	if rows < 1 {
		rows = 1
	}
	if rows > 256 {
		rows = 256
	}

	term.mu.Lock()
	term.emu.SetCellSize(cellWidth, lineHeight)
	if cols == term.cols && rows == term.rows {
		term.mu.Unlock()
		return
	}
	term.cols, term.rows = cols, rows
	term.emu.Resize(cols, rows)
	term.snapshot = term.emu.Snapshot()
	term.mu.Unlock()
	_ = c.session.DispatchAsync(map[string]any{
		"type": "terminal.resize", "terminal_id": term.id, "columns": cols, "lines": rows,
		"cell_width": cellWidth, "cell_height": lineHeight,
	})
}

func (c *WorkspaceClient) Background() color.NRGBA {
	c.layoutMu.Lock()
	defer c.layoutMu.Unlock()
	return configColor(c.currentConfig().Theme.ChromeBackground, 0x121416)
}

func exact(gtx layout.Context, size image.Point) layout.Context {
	gtx.Constraints = layout.Exact(size)
	return gtx
}

func configColor(value string, fallback uint32) color.NRGBA {
	rgb := goconfig.ParseColor(value, fallback)
	return color.NRGBA{R: uint8(rgb >> 16), G: uint8(rgb >> 8), B: uint8(rgb), A: 0xff}
}

func (c *WorkspaceClient) automationClick(x, y float32, count int) bool {
	c.layoutMu.Lock()
	defer c.layoutMu.Unlock()
	if count < 1 {
		count = 1
	}

	size := c.frameSize
	if size.X <= 0 || size.Y <= 0 {
		size = fallbackWindowSize(c.config)
	}
	if size.X < 1 {
		size.X = 1
	}
	if size.Y < 1 {
		size.Y = 1
	}
	metric := c.frameMetric
	point := image.Pt(int(x), int(y))

	var router input.Router
	th := material.NewTheme()
	now := time.Now()
	frame := func(frameNow time.Time) {
		var ops op.Ops
		gtx := layout.Context{
			Constraints: layout.Exact(size),
			Metric:      metric,
			Now:         frameNow,
			Source:      router.Source(),
			Ops:         &ops,
		}
		c.hitRegions = c.hitRegions[:0]
		c.layoutUnlocked(gtx, th)
		router.Frame(&ops)
	}

	// First frame registers the same widget tags and clip regions used by the
	// real window. The synthetic pointer events below are then routed by Gio
	// itself instead of directly invoking widget callbacks.
	frame(now)
	handled := false
	for _, hit := range c.hitRegions {
		if point.In(hit.Rect) {
			handled = true
			break
		}
	}
	if !handled {
		return false
	}

	position := f32.Pt(x, y)
	for n := 0; n < count; n++ {
		eventOffset := time.Duration(n+1) * 50 * time.Millisecond
		router.Queue(
			pointer.Event{
				Kind:      pointer.Press,
				Source:    pointer.Mouse,
				PointerID: 1,
				Buttons:   pointer.ButtonPrimary,
				Position:  position,
				Time:      eventOffset,
			},
			pointer.Event{
				Kind:      pointer.Release,
				Source:    pointer.Mouse,
				PointerID: 1,
				Position:  position,
				Time:      eventOffset + 10*time.Millisecond,
			},
		)
		frame(now.Add(eventOffset + 10*time.Millisecond))
	}
	if c.settings.visible {
		// Carry the routed editor focus into the native window's input router.
		c.settings.requestFocus = true
	}
	if c.invalidate != nil {
		c.invalidate()
	}
	return true
}
