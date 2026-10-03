package goui

import (
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/SurTeam/Water/internal/goconfig"
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
	panes                                    map[uuid.UUID]nativePane
	splits                                   map[string]nativeSplit
	settingsScroll, sidebarScroll, tabScroll int
	remoteFocused                            bool
	lastActiveTab                            uuid.UUID
	sidebarWidth                             float32
	lastUI                                   goconfig.UIConfig
	sidebarRect, tabRect                     image.Rectangle
	sidebarContentHeight                     int
	rename                                   *nativeRename
}
type nativeDrag struct {
	kind                            automationHitKind
	hit                             automationHit
	split                           nativeSplit
	pane                            nativePane
	start                           image.Point
	windowX, windowY, width, height int
	edges                           int
	hyperlink                       string
}

// EbitengineWindow owns rendering, input and window chrome. Transport requests
// enter the bounded queue; GPU and window APIs run only in Update/Draw.
type EbitengineWindow struct {
	multi            *MultiWorkspaceClient
	cfg              goconfig.AppConfig
	queue            chan nativeRequest
	done             chan struct{}
	once             sync.Once
	views            map[*WorkspaceClient]*nativeView
	fonts            *nativeFonts
	textures         map[uuid.UUID]*nativeTerminalTexture
	cornerMask       *ebiten.Image
	cornerRadius     int
	scale            float64
	size             image.Point
	mouse            image.Point
	lastMouse        image.Point
	drag             *nativeDrag
	pressed          *automationHit
	lastClick        time.Time
	lastClickPoint   image.Point
	clickCount       int
	closing          bool
	windowConfigured bool
	shots            []nativeRequest
	composer         textinput.Composer
	composition      string
	inputTarget      string
	clipboardReady   bool
	platform         nativePlatform
	menuEvents       chan string
	quitServer       func() error
	newWindow        func() error
	windowResult     chan error
	quitResult       chan error
	quitError        string
}

func NewEbitengineWindow(multi *MultiWorkspaceClient, cfg goconfig.AppConfig) *EbitengineWindow {
	w := &EbitengineWindow{multi: multi, cfg: cfg, queue: make(chan nativeRequest, 128), done: make(chan struct{}), menuEvents: make(chan string, 16), views: map[*WorkspaceClient]*nativeView{}, textures: map[uuid.UUID]*nativeTerminalTexture{}, scale: 1}
	w.fonts = newNativeFonts(cfg)
	w.clipboardReady = clipboard.Init() == nil
	w.initComposer()
	return w
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
	w.scale = ebiten.Monitor().DeviceScaleFactor()
	if w.scale < 1 {
		w.scale = 1
	}
	w.size = image.Pt(max(1, w.dp(float64(outsideWidth))), max(1, w.dp(float64(outsideHeight))))
	return w.size.X, w.size.Y
}

func (w *EbitengineWindow) request(c *WorkspaceClient, method string, params json.RawMessage) (any, error) {
	r := nativeRequest{view: c, method: method, params: params, reply: make(chan nativeReply, 1), expires: time.Now().Add(5 * time.Second)}
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case w.queue <- r:
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
			default:
			}
		}, w.quitServer != nil)
		if err != nil {
			return err
		}
		w.platform = platform
		w.windowConfigured = true
	}
	if w.quitResult != nil {
		select {
		case err := <-w.quitResult:
			w.quitResult = nil
			if err == nil {
				w.closing = true
			} else {
				w.quitError = "Server shutdown failed: " + err.Error()
			}
		default:
		}
	}
	if w.windowResult != nil {
		select {
		case err := <-w.windowResult:
			w.windowResult = nil
			if err != nil {
				w.quitError = "New window failed: " + err.Error()
			}
		default:
		}
	}
	for n := 0; n < 16; n++ {
		select {
		case action := <-w.menuEvents:
			w.menuAction(action)
		default:
			n = 16
		}
	}
	if w.closing || ebiten.IsWindowBeingClosed() {
		return ebiten.Termination
	}
	x, y := ebiten.CursorPosition()
	w.mouse = image.Pt(x, y)
	c := w.active()
	if c == nil {
		return nil
	}
	platformConfig := c.currentConfig()
	if ebiten.IsWindowMaximized() || ebiten.IsFullscreen() {
		platformConfig.UI.WindowCornerRadius = 0
	}
	w.platform.Update(platformConfig)
	w.multi.mu.RLock()
	liveViews := make(map[*WorkspaceClient]bool, len(w.multi.connections))
	for _, connection := range w.multi.connections {
		liveViews[connection.view] = true
	}
	w.multi.mu.RUnlock()
	for view := range w.views {
		if !liveViews[view] {
			delete(w.views, view)
		}
	}
	w.layout(c, nil)
	for n := 0; n < 128; n++ {
		select {
		case r := <-w.queue:
			w.handleRequest(r)
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
	w.layout(c, nil)
	w.nativePointer(c)
	w.updateCursor(c)
	if !ebiten.IsFocused() {
		w.composer.Cancel()
		return nil
	}
	// Command chords must not be swallowed or turned into text by the IME.
	// Active composition remains owned by the input method until it commits.
	if w.composition == "" && (ebiten.IsKeyPressed(ebiten.KeyControl) || ebiten.IsKeyPressed(ebiten.KeyMeta) || ebiten.IsKeyPressed(ebiten.KeyAlt)) {
		w.composer.Cancel()
		w.nativeKeys(c)
		return nil
	}
	handled, err := w.updateComposer(c)
	if err != nil {
		return err
	}
	if !handled {
		w.nativeKeys(c)
	}
	return nil
}

func (w *EbitengineWindow) Draw(screen *ebiten.Image) {
	if c := w.active(); c != nil {
		w.layout(c, screen)
		visible := map[uuid.UUID]bool{}
		for _, pane := range w.view(c).panes {
			visible[pane.term.id] = true
		}
		for id, cache := range w.textures {
			if !visible[id] {
				for _, row := range cache.rows {
					row.image.Deallocate()
				}
				for _, tex := range cache.images {
					tex.Deallocate()
				}
				delete(w.textures, id)
			}
		}
	}
	w.maskWindow(screen)
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
		ClickCount             int `json:"click_count"`
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
		state["window_decorated"] = ebiten.IsWindowDecorated()
		state["window_position"] = []int{wx, wy}
		state["window_size"] = []int{ww, wh}
		state["window_maximized"] = ebiten.IsWindowMaximized()
		state["window_minimized"] = ebiten.IsWindowMinimized()
		state["custom_titlebar"] = true
		state["native_menu"] = w.platform.Snapshot()
		state["application_hidden"] = w.platform.Snapshot()["hidden"]
		state["quit_error"] = w.quitError
		state["quitting_server"] = w.quitResult != nil
		state["titlebar_height"] = w.titleHeight(c)
		state["ui_config"] = c.currentConfig().UI
		state["sidebar_width"] = w.dp(float64(w.view(c).sidebarWidth))
		state["rename_visible"] = w.view(c).rename != nil
		if c.settings.visible {
			fields := make([]map[string]any, 0, len(c.settings.fields))
			for _, f := range c.settings.fields {
				fields = append(fields, map[string]any{"name": f.group + "." + f.name, "apply": settingsApplyKind(f)})
			}
			state["settings_fields"] = fields
		}
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
		handled := w.pointerDown(c, point, max(1, p.ClickCount))
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
