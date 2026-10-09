package goui

import (
	"encoding/json"
	"errors"
	"fmt"
	"gioui.org/widget"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/SurTeam/Water/internal/goconfig"
	"github.com/SurTeam/Water/internal/goupdate"
	"github.com/SurTeam/Water/internal/govt"
	"github.com/google/uuid"
	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/exp/textinput"
	"github.com/hajimehoshi/ebiten/v2/inpututil"
	"golang.design/x/clipboard"
)

type nativeRequest struct {
	view    *WorkspaceClient
	method  string
	params  json.RawMessage
	reply   chan nativeReply
	expires time.Time
}
type nativeReply struct {
	value any
	err   error
}
type nativeSplit struct {
	rect     image.Rectangle
	vertical bool
	tab      uuid.UUID
	path     []bool
	ratio    float32
}
type nativePane struct {
	rect   image.Rectangle
	term   *terminalClient
	id     uuid.UUID
	cw, lh int
}
type nativeView struct {
	tabTitles                                map[uuid.UUID]string
	tabTitleRevision                         uint64
	panes                                    map[uuid.UUID]nativePane
	splits                                   map[string]nativeSplit
	settingsScroll, sidebarScroll, tabScroll int
	agentShelfScroll                         int
	agentShelfRect                           image.Rectangle
	remoteFocused                            bool
	lastActiveTab                            uuid.UUID
	sidebarWidth                             float32
	lastUI                                   goconfig.UIConfig
	sidebarRect, tabRect                     image.Rectangle
	sidebarContentHeight                     int
	rename                                   *nativeRename
}
type nativeDrag struct {
	editor                          *widget.Editor
	sidebarWidth                    float32
	kind                            automationHitKind
	hit                             automationHit
	split                           nativeSplit
	pane                            nativePane
	start                           image.Point
	windowX, windowY, width, height int
	edges                           int
	hyperlink                       string
	selecting                       bool
	moved                           bool
}

// EbitengineWindow owns rendering, input and window chrome. Transport requests
// enter the bounded queue; GPU and window APIs run only in Update/Draw.
type EbitengineWindow struct {
	multi                   *MultiWorkspaceClient
	cfg                     goconfig.AppConfig
	testInstance            string
	queue                   chan nativeRequest
	done                    chan struct{}
	once                    sync.Once
	views                   map[*WorkspaceClient]*nativeView
	fonts                   *nativeFonts
	textures                map[uuid.UUID]*nativeTerminalTexture
	cornerShader            *ebiten.Shader
	scale                   float64
	size                    image.Point
	mouse                   image.Point
	lastMouse               image.Point
	drag                    *nativeDrag
	pressed                 *automationHit
	lastClick               time.Time
	lastClickPoint          image.Point
	clickCount              int
	closing                 bool
	windowConfigured        bool
	shots                   []nativeRequest
	composer                textinput.Composer
	composition             string
	inputTarget             string
	inputCaret              image.Rectangle
	clipboardReady          bool
	fieldEditor             *widget.Editor
	fieldScroll             float64
	platform                nativePlatform
	notificationAgents      map[string]nativeAgent
	notificationConnections map[uuid.UUID]string
	notificationAgentRuns   map[string]notificationRun
	notificationShellRuns   map[string]notificationRun
	bellNotifications       chan uuid.UUID
	lastBellNotification    map[uuid.UUID]time.Time
	menuEvents              chan string
	quitServer              func() error
	newWindow               func() error
	windowResult            chan error
	quitResult              chan error
	quitError               string
	updates                 *goupdate.Manager
	updateVisible           bool
	updateRestartAllowed    atomic.Bool
	updateResult            chan error
	updating                atomic.Bool
	startupAcknowledged     bool
	startupFrameDrawn       bool
	frameRevision           atomic.Uint64
	presentedRevision       atomic.Uint64
	continuousInput         atomic.Bool
	frameWake               chan struct{}
	frameActivity           atomic.Int64
	keyRepeat               nativeKeyRepeater
	layoutRevision          uint64
	drawnRevision           uint64
	lastLayout              time.Time
	lastFrameSize           image.Point
	lastFrameScale          float64
	lastFocus               bool
	lastBlink               bool
	lastFontGeneration      uint64
	wasInvisible            bool
	fileDropText            string // pending file-drop text to insert into the terminal
}

// Invalidate coalesces worker updates without doing graphics work off-thread.
func (w *EbitengineWindow) Invalidate() {
	countNativeInvalidation()
	w.frameActivity.Store(time.Now().UnixNano())
	w.invalidateOutput()
}

// Output publication has its own bounded cadence. It must not turn on the
// display-rate input loop merely because a PTY keeps producing bytes.
func (w *EbitengineWindow) invalidateOutput() {
	w.frameRevision.Add(1)
	select {
	case w.frameWake <- struct{}{}:
	default:
	}
}

func NewEbitengineWindow(multi *MultiWorkspaceClient, cfg goconfig.AppConfig) *EbitengineWindow {
	w := &EbitengineWindow{multi: multi, cfg: cfg, queue: make(chan nativeRequest, 128), done: make(chan struct{}), menuEvents: make(chan string, 16), views: map[*WorkspaceClient]*nativeView{}, textures: map[uuid.UUID]*nativeTerminalTexture{}, scale: 1}
	w.fonts = newNativeFonts(cfg)
	w.frameWake = make(chan struct{}, 1)
	w.bellNotifications = make(chan uuid.UUID, 32)
	w.clipboardReady = clipboard.Init() == nil
	w.initComposer()
	w.Invalidate()
	go w.scheduleFrames()
	return w
}

// Minimum FPS mode wakes for native input. Workers coalesce their invalidations
// here at display cadence; periodic maintenance covers native menus, visibility,
// settings completion and cursor blink without a permanent 60 Hz frame loop.
func (w *EbitengineWindow) scheduleFrames() {
	ticker := time.NewTicker(time.Second / 60)
	defer ticker.Stop()
	ticks := 0
	blink := time.Now().UnixMilli()%1000 < 600
	for {
		select {
		case <-w.done:
			return
		case <-w.frameWake:
			ebiten.ScheduleFrame()
		case <-ticker.C:
			ticks++
			phase := time.Now().UnixMilli()%1000 < 600
			// Draw can run before the next simulation tick consumes a queued key.
			// Its presented revision must not put still-pending input to sleep
			// until the 250ms maintenance wakeup.
			if ticks%15 == 0 || phase != blink || len(w.queue) > 0 || w.continuousInput.Load() || w.frameRevision.Load() != w.presentedRevision.Load() {
				ebiten.ScheduleFrame()
			}
			blink = phase
		}
	}
}
func (w *EbitengineWindow) SetTestInstance(instance string) { w.testInstance = instance }
func (w *EbitengineWindow) windowTitle() string {
	if w.testInstance != "" {
		return "Water Test"
	}
	return "Water"
}
func (w *EbitengineWindow) Attach(c *WorkspaceClient) { c.native.Store(w) }
func (w *EbitengineWindow) Close()                    { w.once.Do(func() { close(w.done); w.fonts.close() }) }
func (w *EbitengineWindow) active() *WorkspaceClient {
	w.multi.mu.RLock()
	defer w.multi.mu.RUnlock()
	if c := w.multi.connections[w.multi.active]; c != nil {
		return c.view
	}
	return nil
}
func (w *EbitengineWindow) view(c *WorkspaceClient) *nativeView {
	v := w.views[c]
	if v == nil {
		v = &nativeView{panes: map[uuid.UUID]nativePane{}, splits: map[string]nativeSplit{}}
		w.views[c] = v
	}
	return v
}
func (w *EbitengineWindow) dp(n float64) int { return int(n*w.scale + .5) }
func (w *EbitengineWindow) Layout(outsideWidth, outsideHeight int) (int, int) {
	w.refreshDisplayScale()
	w.size = image.Pt(max(1, w.dp(float64(outsideWidth))), max(1, w.dp(float64(outsideHeight))))
	return w.size.X, w.size.Y
}

func (w *EbitengineWindow) request(c *WorkspaceClient, method string, params json.RawMessage) (any, error) {
	r := nativeRequest{view: c, method: method, params: params, reply: make(chan nativeReply, 1), expires: time.Now().Add(5 * time.Second)}
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case w.queue <- r:
		if method == "ui.wheel" {
			w.invalidateOutput()
		} else {
			w.Invalidate()
		}
		// Control input is latency-sensitive; wake the native event loop now
		// rather than waiting for the next output coalescing tick.
		ebiten.ScheduleFrame()
	case <-w.done:
		return nil, errors.New("window closed")
	case <-timer.C:
		return nil, errors.New("window input queue busy")
	}
	select {
	case result := <-r.reply:
		return result.value, result.err
	case <-w.done:
		return nil, errors.New("window closed")
	case <-timer.C:
		return nil, errors.New("window request timed out")
	}
}

func (w *EbitengineWindow) Update() error {
	defer w.updateFramePacing()
	if end := traceNativeWork("native.update"); end != nil {
		defer end()
	}
	defer func() {
		w.continuousInput.Store(len(inpututil.AppendPressedKeys(nil)) > 0 || w.drag != nil || w.composition != "")
	}()
	if !w.windowConfigured {
		// Apply decoration to the live native window too: platform startup can
		// alter its initial style while setting up resizability and focus.
		ebiten.SetWindowDecorated(false)
		platform, err := newNativePlatform(func(action string) {
			select {
			case <-w.done:
				return
			default:
			}
			select {
			case w.menuEvents <- action:
				w.Invalidate()
			default:
			}
		}, w.quitServer != nil)
		if err != nil {
			return err
		}
		w.platform = platform
		w.windowConfigured = true
		// Register the window as a file drop target. Dropped file paths are
		// delivered to the active terminal as shell-quoted absolute paths.
		w.platform.RegisterFileDrop(func(paths []string) {
			text := fileDropToText(paths)
			select {
			case <-w.done:
				return
			default:
			}
			w.fileDropText = text
			w.Invalidate()
		})
	}
	c := w.active()
	if c == nil {
		return nil
	}
	if w.startupFrameDrawn && !w.startupAcknowledged {
		w.startupAcknowledged = true
		go goupdate.AcknowledgeStartup()
	}
	if w.quitResult != nil {
		select {
		case err := <-w.quitResult:
			w.quitResult = nil
			if err == nil {
				w.closing = true
			} else {
				w.quitError = c.trf("Server shutdown failed: %s", err.Error())
			}
		default:
		}
	}
	if w.windowResult != nil {
		select {
		case err := <-w.windowResult:
			w.windowResult = nil
			if err != nil {
				w.quitError = c.trf("New window failed: %s", err.Error())
			}
		default:
		}
	}
	for n := 0; n < 16; n++ {
		select {
		case action := <-w.menuEvents:
			w.menuAction(action)
			w.Invalidate()
		default:
			n = 16
		}
	}
	if w.updateResult != nil {
		select {
		case err := <-w.updateResult:
			w.updateResult = nil
			if err == nil {
				w.updating.Store(true)
				w.closing = true
			} else {
				w.updating.Store(false)
			}
			w.Invalidate()
		default:
		}
	}
	if w.closing || ebiten.IsWindowBeingClosed() {
		return ebiten.Termination
	}
	x, y := ebiten.CursorPosition()
	w.mouse = image.Pt(x, y)
	focused := ebiten.IsFocused()
	blink := time.Now().UnixMilli()%1000 < 600
	fontGeneration := w.fonts.generation.Load()
	if nativeHoverChanged(c.hitRegions, w.lastMouse, w.mouse) || w.size != w.lastFrameSize || w.scale != w.lastFrameScale || focused != w.lastFocus || fontGeneration != w.lastFontGeneration || w.drag != nil {
		w.Invalidate()
	}
	if blink != w.lastBlink {
		w.frameRevision.Add(1)
	}
	w.lastFrameSize, w.lastFrameScale, w.lastFocus, w.lastBlink, w.lastFontGeneration = w.size, w.scale, focused, blink, fontGeneration
	// Keep the existing 60 Hz input/repeat clock, but don't rebuild or submit
	// an unchanged frame. Keyboard and pointer edits can be purely local.
	if len(inpututil.AppendPressedKeys(nil)) > 0 || inpututil.IsMouseButtonJustPressed(ebiten.MouseButtonLeft) || inpututil.IsMouseButtonJustReleased(ebiten.MouseButtonLeft) {
		w.Invalidate()
	}
	if dx, dy := ebiten.Wheel(); dx != 0 || dy != 0 {
		w.invalidateOutput()
	}
	platformConfig := c.currentConfig()
	if ebiten.IsWindowMaximized() || ebiten.IsFullscreen() {
		platformConfig.UI.WindowCornerRadius = 0
	}
	w.platform.Update(platformConfig)
	w.processNotificationsAt(c, focused, time.Now())
	w.processTerminalReports()
	w.multi.mu.RLock()
	liveViews := make(map[*WorkspaceClient]bool, len(w.multi.connections))
	for _, connection := range w.multi.connections {
		liveViews[connection.view] = true
	}
	w.multi.mu.RUnlock()
	for view := range w.views {
		if !liveViews[view] {
			delete(w.views, view)
			continue
		}
		view.updateWindowFocus(focused && view == c)
	}
	revision := w.frameRevision.Load()
	if revision != w.layoutRevision || time.Since(w.lastLayout) >= 250*time.Millisecond {
		// Settings save completion is polled by layout even without input.
		w.layout(c, nil)
		w.layoutRevision, w.lastLayout = revision, time.Now()
	}
	processed := false
	for n := 0; n < 128; n++ {
		select {
		case r := <-w.queue:
			w.handleRequest(r)
			processed = true
		default:
			n = 128
		}
	}
	if w.closing {
		return nil
	}
	// A connection switch from either input path takes effect immediately.
	c = w.active()
	if c == nil {
		return nil
	}
	if processed || w.frameRevision.Load() != w.layoutRevision {
		w.layout(c, nil)
		w.layoutRevision = w.frameRevision.Load()
	}
	w.nativePointer(c)
	w.updateCursor(c)
	w.keyRepeat.observe(ebiten.IsKeyPressed, false)
	if !ebiten.IsFocused() {
		w.keyRepeat = nativeKeyRepeater{}
		w.composer.Cancel()
		return nil
	}
	// Deliver pending file-drop text to the active terminal.
	if w.fileDropText != "" {
		text := w.fileDropText
		w.fileDropText = ""
		if id, ok := c.activeTerminalID(); ok {
			c.mu.RLock()
			term := c.terminals[id]
			c.mu.RUnlock()
			if term != nil {
				term.mu.RLock()
				snap := term.snapshot
				term.mu.RUnlock()
				data := []byte(text)
				if snap.BracketedPaste && c.currentConfig().Features.BracketedPaste {
					data = append(append([]byte("\x1b[200~"), data...), []byte("\x1b[201~")...)
				}
				term.input.emit(data)
			}
		}
	}
	// Command chords must not be swallowed or turned into text by the IME.
	// Active composition remains owned by the input method until it commits.
	if w.composition == "" && (ebiten.IsKeyPressed(ebiten.KeyControl) || ebiten.IsKeyPressed(ebiten.KeyMeta) || ebiten.IsKeyPressed(ebiten.KeyAlt)) {
		w.composer.Cancel()
		w.nativeKeys(c)
		return nil
	}
	previousComposition := w.composition
	handled, err := w.updateComposer(c)
	if handled || previousComposition != w.composition {
		w.Invalidate()
	}
	if err != nil {
		return err
	}
	if handled || previousComposition != "" {
		w.keyRepeat.observe(ebiten.IsKeyPressed, true)
	}
	if !handled {
		w.nativeKeys(c)
	}
	return nil
}

func (w *EbitengineWindow) Draw(screen *ebiten.Image) {
	if end := traceNativeWork("native.draw"); end != nil {
		defer end()
	}
	revision := w.frameRevision.Load()
	w.presentedRevision.Store(revision)
	// Ebitengine does not swap buffers for hidden or fully occluded windows.
	// Continuing to draw accumulates graphics commands until restore, including automatic
	// screen clears. Leave the retained frame untouched while Update and the
	// terminal workers keep running. Explicit screenshots still render once.
	invisible := false
	if w.platform != nil {
		state := w.platform.Snapshot()
		invisible = state["hidden"] == true || state["occluded"] == true
	}
	if (invisible || ebiten.IsWindowMinimized()) && len(w.shots) == 0 {
		w.wasInvisible = true
		return
	}
	// Acknowledge consumption after rendering: the worker may publish the next
	// view immediately, without another timer or a request on the next Update.
	defer func() {
		if c := w.active(); c != nil {
			select {
			case c.snapshotWake <- struct{}{}:
			default:
			}
		}
	}()
	if revision == w.drawnRevision && !w.wasInvisible && len(w.shots) == 0 {
		return
	}
	w.wasInvisible = false
	w.drawnRevision = revision
	if end := traceNativeWork("native.present"); end != nil {
		defer end()
	}
	screen.Clear()
	if c := w.active(); c != nil {
		w.layout(c, screen)
		visible := map[uuid.UUID]bool{}
		for _, pane := range w.view(c).panes {
			visible[pane.term.id] = true
		}
		for id, cache := range w.textures {
			if !visible[id] {
				if cache.surface != nil {
					cache.surface.Deallocate()
				}
				for _, tex := range cache.images {
					tex.Deallocate()
				}
				delete(w.textures, id)
			}
		}
	}
	w.maskWindow(screen)
	w.startupFrameDrawn = true
	if len(w.shots) > 0 {
		img := image.NewRGBA(screen.Bounds())
		screen.ReadPixels(img.Pix)
		shots := w.shots
		w.shots = nil
		// PNG encoding and filesystem I/O must not block the frame loop.
		go func() {
			for _, r := range shots {
				var p struct {
					Path string `json:"path"`
				}
				_ = json.Unmarshal(r.params, &p)
				value, err := saveNativeScreenshot(img, p.Path)
				r.reply <- nativeReply{value, err}
			}
		}()
	}
}

func saveNativeScreenshot(img *image.RGBA, path string) (any, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		path = "target/water-screenshot.png"
	}
	if filepath.Ext(path) == "" {
		path += ".png"
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, err
	}
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	err = png.Encode(f, img)
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{"path": path, "width": img.Bounds().Dx(), "height": img.Bounds().Dy(), "renderer": "ebitengine"}, nil
}

func (w *EbitengineWindow) handleRequest(r nativeRequest) {
	if time.Now().After(r.expires) {
		r.reply <- nativeReply{err: errors.New("window request expired")}
		return
	}
	var p struct {
		Action                 string `json:"action"`
		Keystroke              string `json:"keystroke"`
		X, Y, ToX, ToY, DX, DY float32
		ClickCount             int  `json:"click_count"`
		Shift                  bool `json:"shift"`
	}
	// JSON's snake-case coordinates are separate from Go field names.
	var coords struct {
		ToX float32 `json:"to_x"`
		ToY float32 `json:"to_y"`
	}
	if err := json.Unmarshal(r.params, &p); err != nil {
		r.reply <- nativeReply{err: err}
		return
	}
	_ = json.Unmarshal(r.params, &coords)
	p.ToX, p.ToY = coords.ToX, coords.ToY
	c := r.view
	if active := w.active(); active != nil {
		c = active
	}
	var value any
	var err error
	switch r.method {
	case "ui.snapshot":
		value = c.uiSnapshot()
		state := value.(map[string]any)
		wx, wy := ebiten.WindowPosition()
		ww, wh := ebiten.WindowSize()
		state["renderer"] = "ebitengine"
		state["test_instance"] = w.testInstance
		state["window_id"] = c.session.WindowID
		state["update_visible"] = w.updateVisible
		state["update_restart_allowed"] = w.updateRestartAllowed.Load()
		if w.updates != nil {
			state["update"] = w.updates.Snapshot()
		}
		state["window_decorated"] = ebiten.IsWindowDecorated()
		state["window_position"] = []int{wx, wy}
		state["window_size"] = []int{ww, wh}
		state["display_scale"] = w.scale
		state["ime_caret_bounds"] = []int{w.inputCaret.Min.X, w.inputCaret.Min.Y, w.inputCaret.Max.X, w.inputCaret.Max.Y}
		if monitor := ebiten.Monitor(); monitor != nil {
			sw, sh := monitor.Size()
			state["screen_size_pixels"] = []int{w.dp(float64(sw)), w.dp(float64(sh))}
		}
		state["window_maximized"] = ebiten.IsWindowMaximized()
		state["window_minimized"] = ebiten.IsWindowMinimized()
		state["window_focused"] = ebiten.IsFocused()
		state["custom_titlebar"] = true
		state["native_menu"] = w.platform.Snapshot()
		state["application_hidden"] = w.platform.Snapshot()["hidden"]
		state["window_occluded"] = w.platform.Snapshot()["occluded"]
		state["quit_error"] = w.quitError
		state["quitting_server"] = w.quitResult != nil
		state["titlebar_height"] = w.titleHeight(c)
		state["ui_language"] = c.language()
		state["ui_strings"] = map[string]string{"settings": c.tr("Settings"), "save": c.tr("Save"), "connect_remote": c.tr("+  Connect remote")}
		grids := []map[string]any{}
		for id, pane := range w.view(c).panes {
			pane.term.mu.RLock()
			directory := pane.term.snapshot.WorkingDirectoryURI
			yBase, yDisp := pane.term.snapshot.YBase, pane.term.snapshot.YDisp
			cursorX, cursorY := pane.term.snapshot.CursorX, pane.term.snapshot.CursorY
			cursorStyle, cursorBlink := pane.term.snapshot.CursorStyle, pane.term.snapshot.CursorBlink
			selection := pane.term.selection
			images := []map[string]any{}
			for _, img := range pane.term.snapshot.Images {
				images = append(images, map[string]any{"id": img.ID, "row": img.Row, "column": img.Column, "width": img.Width, "height": img.Height, "pixel_width": img.PixelWidth, "pixel_height": img.PixelHeight})
			}
			pane.term.mu.RUnlock()
			pane.term.mu.RLock()
			columns, rows := pane.term.cols, pane.term.rows
			pane.term.mu.RUnlock()
			grids = append(grids, map[string]any{"pane_id": id, "rect": []int{pane.rect.Min.X, pane.rect.Min.Y, pane.rect.Max.X, pane.rect.Max.Y}, "columns": pane.rect.Dx() / pane.cw, "rows": pane.rect.Dy() / pane.lh, "terminal_columns": columns, "terminal_rows": rows, "cell_width": pane.cw, "cell_height": pane.lh, "working_directory_uri": directory, "y_base": yBase, "y_disp": yDisp, "cursor_x": cursorX, "cursor_y": cursorY, "cursor_style": cursorStyle, "cursor_blink": cursorBlink})
			grids[len(grids)-1]["selection"] = selection
			grids[len(grids)-1]["images"] = images
		}
		state["terminal_grids"] = grids
		state["ui_config"] = c.currentConfig().UI
		sidebarAgents := []map[string]any{}
		for _, host := range w.hosts() {
			for _, a := range host.view.sidebarAgents(host.state) {
				status := a.status
				if host.entry.Status != "" && host.entry.Status != "connected" && host.entry.Status != "online" {
					status = "Offline"
				}
				sidebarAgents = append(sidebarAgents, map[string]any{"pane_id": a.pane, "connection_id": host.entry.ID, "workspace_id": a.workspace, "workspace_name": a.workspaceName, "title": a.label, "title_source": a.titleSource, "status": status, "status_label": c.tr(status)})
			}
		}
		state["sidebar_agents"] = sidebarAgents
		state["sidebar_width"] = w.dp(float64(w.view(c).sidebarWidth))
		state["sidebar_visible"] = !c.sidebarHidden
		state["rename_visible"] = w.view(c).rename != nil
		state["server_status"] = c.ServerPanelState()
		if c.settings.visible {
			state["settings_category"] = settingsGroups[c.settings.group]
			fields := make([]map[string]any, 0, len(c.settings.fields))
			for group := 0; group < len(settingsGroups); group++ {
				panel := settingsPanel{fields: c.settings.fields}
				panel.group = group
				for _, row := range settingsRows(&panel) {
					if row.field < 0 {
						continue
					}
					f := panel.fields[row.field]
					fields = append(fields, map[string]any{"name": f.group + "." + f.name, "label": localizedField(c.language(), f), "apply": c.tr(settingsApplyKind(f)), "section": c.tr(row.section)})
				}
			}
			state["settings_fields"] = fields
			if i := c.settings.focus; i >= 0 && i < len(c.settings.fields) {
				f := &c.settings.fields[i]
				caret, anchor := f.editor.Selection()
				state["settings_editor"] = map[string]any{"name": f.group + "." + f.name, "text": f.editor.Text(), "caret": caret, "anchor": anchor}
			}
			sections := []string{}
			for _, row := range settingsRows(&c.settings) {
				if row.field < 0 {
					sections = append(sections, c.tr(row.section))
				}
			}
			state["settings_sections"] = sections
		}
		state["tab_font_size"] = c.currentConfig().UI.TabFontSize
		state["gui_pid"] = os.Getpid()
		cfg := c.currentConfig()
		state["terminal_font"] = w.fonts.resolution(fontKey{family: cfg.Terminal.FontFamily})
		state["terminal_font_styles"] = map[string]any{
			"bold":        w.fonts.resolution(fontKey{family: cfg.Terminal.FontFamily, bold: true}),
			"italic":      w.fonts.resolution(fontKey{family: cfg.Terminal.FontFamily, italic: true}),
			"bold_italic": w.fonts.resolution(fontKey{family: cfg.Terminal.FontFamily, bold: true, italic: true}),
		}
	case "ui.keystroke":
		value = map[string]any{"handled": w.key(c, p.Keystroke), "window_count": 1, "keystroke": p.Keystroke}
	case "ui.menu":
		err = w.platform.Invoke(p.Action)
		value = map[string]any{"handled": err == nil, "action": p.Action}
	case "ui.click":
		point := image.Pt(int(p.X), int(p.Y))
		handled := w.pointerDown(c, point, max(1, p.ClickCount), p.Shift)
		w.pointerUp(c, point)
		value = map[string]any{"handled": handled, "window_count": 1, "has_active_window": true}
	case "ui.drag":
		from, to := image.Pt(int(p.X), int(p.Y)), image.Pt(int(p.ToX), int(p.ToY))
		handled := w.pointerDown(c, from, 1)
		if w.drag == nil {
			handled = false
		} else {
			w.pointerMove(c, to)
			w.pointerUp(c, to)
		}
		value = map[string]any{"handled": handled, "window_count": 1}
	case "ui.wheel":
		handled := w.wheel(c, image.Pt(int(p.X), int(p.Y)), float64(p.DX), float64(p.DY))
		value = map[string]any{"default_prevented": handled, "propagate": !handled}
	case "ui.screenshot":
		w.shots = append(w.shots, r)
		return
	default:
		err = fmt.Errorf("unsupported Ebitengine UI method %q", r.method)
	}
	r.reply <- nativeReply{value, err}
}

func (w *EbitengineWindow) minimize() { ebiten.MinimizeWindow() }

func (w *EbitengineWindow) nativePointer(c *WorkspaceClient) {
	if inpututil.IsMouseButtonJustPressed(ebiten.MouseButtonLeft) {
		now := time.Now()
		if now.Sub(w.lastClick) < 400*time.Millisecond && w.mouse.Sub(w.lastClickPoint).X*w.mouse.Sub(w.lastClickPoint).X+w.mouse.Sub(w.lastClickPoint).Y*w.mouse.Sub(w.lastClickPoint).Y < w.dp(5)*w.dp(5) {
			w.clickCount = w.clickCount%3 + 1
		} else {
			w.clickCount = 1
		}
		w.lastClick, w.lastClickPoint = now, w.mouse
		w.pointerDown(c, w.mouse, w.clickCount)
	}
	if ebiten.IsMouseButtonPressed(ebiten.MouseButtonLeft) {
		w.pointerMove(c, w.mouse)
	}
	if inpututil.IsMouseButtonJustReleased(ebiten.MouseButtonLeft) {
		w.pointerUp(c, w.mouse)
	}
	for _, button := range []struct {
		native   ebiten.MouseButton
		terminal govt.MouseButton
	}{{ebiten.MouseButtonMiddle, govt.MouseMiddle}, {ebiten.MouseButtonRight, govt.MouseRight}} {
		if inpututil.IsMouseButtonJustPressed(button.native) {
			w.reportPointer(c, w.mouse, govt.MouseDown, button.terminal)
		}
		if inpututil.IsMouseButtonJustReleased(button.native) {
			w.reportPointer(c, w.mouse, govt.MouseUp, button.terminal)
		}
	}
	if w.mouse != w.lastMouse && w.drag == nil {
		w.reportPointer(c, w.mouse, govt.MouseMove, govt.MouseNone)
	}
	w.lastMouse = w.mouse
	dx, dy := ebiten.Wheel()
	if dx != 0 || dy != 0 {
		w.wheel(c, w.mouse, -dx*40, -dy*40)
	}
}
