package goui

import (
	"fmt"
	"image"
	"reflect"
	"strings"

	"gioui.org/io/key"
	"gioui.org/widget"
	"github.com/SurTeam/Water/internal/goconfig"
	"github.com/SurTeam/Water/internal/govt"
	"github.com/google/uuid"
	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/exp/textinput"
	"github.com/hajimehoshi/ebiten/v2/inpututil"
	"github.com/hajimehoshi/ebiten/v2/text/v2"
	"golang.design/x/clipboard"
)

func (w *EbitengineWindow) pointerDown(c *WorkspaceClient, p image.Point, count int) bool {
	w.composer.Confirm()
	c.layoutMu.Lock()
	defer c.layoutMu.Unlock()
	w.pressed = nil
	w.drag = nil
	// Border hit testing is independent of content and native decorations.
	edges := 0
	border := w.dp(5)
	if p.X < border {
		edges |= 1
	}
	if p.X >= w.size.X-border {
		edges |= 2
	}
	if p.Y < border {
		edges |= 4
	}
	if p.Y >= w.size.Y-border {
		edges |= 8
	}
	if edges != 0 && !ebiten.IsWindowMaximized() {
		x, y := ebiten.WindowPosition()
		width, height := ebiten.WindowSize()
		w.drag = &nativeDrag{start: p, windowX: x, windowY: y, width: width, height: height, edges: edges}
		return true
	}
	for i := len(c.hitRegions) - 1; i >= 0; i-- {
		h := c.hitRegions[i]
		if !p.In(h.Rect) {
			continue
		}
		w.pressed = &h
		switch h.Kind {
		case hitTitlebar:
			if count == 2 {
				w.toggleMaximize()
				w.pressed = nil
				return true
			}
			if ebiten.IsWindowMaximized() {
				return true
			}
			x, y := ebiten.WindowPosition()
			w.drag = &nativeDrag{kind: hitTitlebar, start: p, windowX: x, windowY: y}
		case hitDivider:
			if s, ok := w.view(c).splits[h.Label]; ok {
				w.drag = &nativeDrag{kind: hitDivider, hit: h, split: s, start: p}
			}
		case hitSidebarResize:
			w.drag = &nativeDrag{kind: hitSidebarResize, hit: h, start: p, sidebarWidth: w.view(c).sidebarWidth}
		case hitWorkspace:
			if count == 2 {
				if view := w.connectionView(nativeUUID(h.Label)); view != nil {
					w.multi.ActivateConnection(nativeUUID(h.Label))
					w.openRename(view, "workspace", h.ID)
				}
				w.pressed = nil
				return true
			}
			w.drag = &nativeDrag{kind: hitWorkspace, hit: h, start: p}
		case hitTab:
			if count == 2 {
				w.openRename(c, "tab", h.ID)
				w.pressed = nil
				return true
			}
		case hitPane:
			pane, ok := w.view(c).panes[h.ID]
			if !ok {
				return false
			}
			_ = c.session.DispatchAsync(map[string]any{"type": "pane.focus", "pane_id": h.ID})
			w.view(c).remoteFocused = false
			col, row := (p.X-pane.rect.Min.X)/pane.cw, (p.Y-pane.rect.Min.Y)/pane.lh
			pane.term.mu.RLock()
			uri := hyperlinkURIAt(pane.term.snapshot, col, row)
			pane.term.mu.RUnlock()
			cfg := c.currentConfig()
			if count == 1 && cfg.Terminal.Hyperlinks && uri != "" && (!cfg.Terminal.HyperlinkCommandClick || ebiten.IsKeyPressed(ebiten.KeyMeta) || ebiten.IsKeyPressed(ebiten.KeyControl)) {
				w.drag = &nativeDrag{kind: hitPane, pane: pane, start: p, hyperlink: uri}
				return true
			}
			if !w.terminalMouse(pane, p, govt.MouseDown, govt.MouseLeft) && cfg.Features.Selection {
				if pane.term.input.OnSelectionStart != nil {
					pane.term.input.OnSelectionStart(col, row, count)
				}
			}
			w.drag = &nativeDrag{kind: hitPane, pane: pane, start: p}
		case hitRemoteField:
			w.view(c).remoteFocused = true
		case hitSettingsControl:
			if strings.HasPrefix(h.Label, "field:") {
				for i := range c.settings.fields {
					f := &c.settings.fields[i]
					if h.Label == "field:"+f.group+"."+f.name {
						c.settings.focus = i
						c.settings.requestFocus = false
						if f.kind != reflect.Bool {
							n := len([]rune(f.editor.Text()))
							f.editor.SetCaret(n, n)
						}
						break
					}
				}
			}
		}
		return true
	}
	return false
}

func (w *EbitengineWindow) pointerMove(c *WorkspaceClient, p image.Point) {
	d := w.drag
	if d == nil {
		return
	}
	if d.edges != 0 {
		currentX, currentY := ebiten.WindowPosition()
		dx, dy := currentX-d.windowX+int(float64(p.X-d.start.X)/w.scale), currentY-d.windowY+int(float64(p.Y-d.start.Y)/w.scale)
		x, y, width, height := d.windowX, d.windowY, d.width, d.height
		if d.edges&1 != 0 {
			width -= dx
			x += dx
		}
		if d.edges&2 != 0 {
			width += dx
		}
		if d.edges&4 != 0 {
			height -= dy
			y += dy
		}
		if d.edges&8 != 0 {
			height += dy
		}
		minW, minH := int(w.cfg.Startup.WindowMinWidth), int(w.cfg.Startup.WindowMinHeight)
		if width < minW {
			if d.edges&1 != 0 {
				x -= minW - width
			}
			width = minW
		}
		if height < minH {
			if d.edges&4 != 0 {
				y -= minH - height
			}
			height = minH
		}
		ebiten.SetWindowPosition(x, y)
		ebiten.SetWindowSize(width, height)
		return
	}
	switch d.kind {
	case hitSidebarResize:
		cfg := c.currentConfig()
		width := d.sidebarWidth + float32(float64(p.X-d.start.X)/w.scale)
		w.view(c).sidebarWidth = min(cfg.UI.SidebarMaxWidth, max(cfg.UI.SidebarMinWidth, width))
	case hitTitlebar:
		// Cursor coordinates are relative to the moving window. Convert back
		// to desktop coordinates each tick so a stationary cursor stays stable.
		x, y := ebiten.WindowPosition()
		ebiten.SetWindowPosition(x+int(float64(p.X-d.start.X)/w.scale), y+int(float64(p.Y-d.start.Y)/w.scale))
	case hitDivider:
		gap := w.dp(float64(c.currentConfig().UI.PaneMargin))
		span, pos := d.split.rect.Dx()-gap, p.X-d.split.rect.Min.X
		if d.split.vertical {
			span = d.split.rect.Dy() - gap
			pos = p.Y - d.split.rect.Min.Y
		}
		if span > 0 {
			d.split.ratio = min(.95, max(.05, float32(pos-gap/2)/float32(span)))
		}
	case hitPane:
		if !c.currentConfig().Features.Selection && d.hyperlink == "" {
			w.terminalMouse(d.pane, p, govt.MouseMove, govt.MouseLeft)
			return
		}
		if d.hyperlink != "" {
			return
		}
		if w.terminalMouse(d.pane, p, govt.MouseMove, govt.MouseLeft) {
			return
		}
		col := min(max(0, (p.X-d.pane.rect.Min.X)/d.pane.cw), max(0, d.pane.rect.Dx()/d.pane.cw-1))
		row := (p.Y - d.pane.rect.Min.Y) / d.pane.lh
		if p.Y < d.pane.rect.Min.Y || p.Y >= d.pane.rect.Max.Y {
			if d.pane.term.input.OnSelectionAutoScroll != nil {
				lines := 1
				if p.Y < d.pane.rect.Min.Y {
					lines = -1
				}
				d.pane.term.input.OnSelectionAutoScroll(col, min(max(0, row), max(0, d.pane.rect.Dy()/d.pane.lh-1)), lines)
			}
		} else if d.pane.term.input.OnSelectionMove != nil {
			d.pane.term.input.OnSelectionMove(col, row)
		}
	}
}

func (w *EbitengineWindow) pointerUp(c *WorkspaceClient, p image.Point) {
	d, h := w.drag, w.pressed
	w.drag = nil
	w.pressed = nil
	if d != nil {
		switch d.kind {
		case hitWorkspace:
			view := w.connectionView(nativeUUID(d.hit.Label))
			if view == nil {
				return
			}
			if p.Y-d.start.Y > w.dp(5) || d.start.Y-p.Y > w.dp(5) {
				for _, target := range c.hitRegions {
					if target.Kind != hitWorkspace || target.Label != d.hit.Label || !p.In(target.Rect) {
						continue
					}
					view.mu.RLock()
					state := view.state
					view.mu.RUnlock()
					for index, ws := range state.Workspaces {
						if ws.ID == target.ID {
							_ = view.session.DispatchAsync(map[string]any{"type": "workspace.reorder", "workspace_id": d.hit.ID, "index": index})
							return
						}
					}
				}
			} else if p.In(d.hit.Rect) {
				w.multi.ActivateConnection(nativeUUID(d.hit.Label))
				_ = view.session.DispatchAsync(map[string]any{"type": "workspace.activate", "workspace_id": d.hit.ID})
			}
			return
		case hitSidebarResize:
			return
		case hitDivider:
			_ = c.session.DispatchAsync(map[string]any{"type": "pane.resize_split", "tab_id": d.split.tab, "path": d.split.path, "ratio": d.split.ratio})
			return
		case hitPane:
			if d.hyperlink != "" {
				col, row := (p.X-d.pane.rect.Min.X)/d.pane.cw, (p.Y-d.pane.rect.Min.Y)/d.pane.lh
				d.pane.term.mu.RLock()
				uri := hyperlinkURIAt(d.pane.term.snapshot, col, row)
				d.pane.term.mu.RUnlock()
				if uri == d.hyperlink {
					c.activateHyperlink(uri)
				}
				return
			}
			if !w.terminalMouse(d.pane, p, govt.MouseUp, govt.MouseLeft) && d.pane.term.input.OnSelectionEnd != nil {
				d.pane.term.input.OnSelectionEnd((p.X-d.pane.rect.Min.X)/d.pane.cw, (p.Y-d.pane.rect.Min.Y)/d.pane.lh)
			}
			return
		default:
			return
		}
	}
	if h == nil || !p.In(h.Rect) {
		return
	}
	switch h.Kind {
	case hitWindowClose:
		w.closing = true
	case hitWindowMinimize:
		ebiten.MinimizeWindow()
	case hitWindowMaximize:
		w.toggleMaximize()
	case hitConnection:
		_, _, activate := c.connectionSnapshot()
		if activate != nil {
			activate(h.ID)
		}
	case hitDisconnectConnection:
		_, remove := c.connectionActions()
		if remove != nil {
			go remove(h.ID)
		}
	case hitWorkspace:
		view := w.connectionView(nativeUUID(h.Label))
		if view != nil {
			w.multi.ActivateConnection(nativeUUID(h.Label))
			_ = view.session.DispatchAsync(map[string]any{"type": "workspace.activate", "workspace_id": h.ID})
		}
	case hitAgent:
		w.activateAgent(c, *h)
	case hitRenameSave:
		w.submitRename(c)
	case hitRenameCancel:
		w.view(c).rename = nil
	case hitTab:
		_ = c.session.DispatchAsync(map[string]any{"type": "tab.activate", "tab_id": h.ID})
	case hitNewWorkspace:
		_ = c.session.DispatchAsync(map[string]any{"type": "workspace.new"})
	case hitNewTab:
		_ = c.session.DispatchAsync(map[string]any{"type": "tab.new"})
	case hitSplitRight:
		c.splitPane("right")
	case hitSplitDown:
		c.splitPane("down")
	case hitSettings:
		c.layoutMu.Lock()
		c.openSettings()
		w.view(c).settingsScroll = 0
		c.layoutMu.Unlock()
	case hitNewRemote:
		c.connectionMu.Lock()
		c.remoteFormVisible = true
		c.remoteError = ""
		c.connectionMu.Unlock()
		w.view(c).remoteFocused = true
	case hitRemoteCancel:
		c.connectionMu.Lock()
		if !c.remoteConnecting {
			c.remoteFormVisible = false
			c.remoteError = ""
		}
		c.connectionMu.Unlock()
		w.view(c).remoteFocused = false
	case hitRemoteSubmit:
		w.connectRemote(c)
	case hitSettingsControl:
		w.settingsAction(c, h.Label)
	case hitHyperlinkConfirm:
		c.confirmHyperlink()
	case hitHyperlinkCancel:
		c.hyperlinkMu.Lock()
		if c.hyperlinkPrompt != nil && !c.hyperlinkPrompt.Working {
			c.hyperlinkPrompt = nil
		}
		c.hyperlinkMu.Unlock()
	}
}
func (w *EbitengineWindow) toggleMaximize() {
	if ebiten.IsWindowMaximized() || ebiten.IsWindowMinimized() {
		ebiten.RestoreWindow()
	} else {
		ebiten.MaximizeWindow()
	}
}
func (w *EbitengineWindow) terminalMouse(pane nativePane, p image.Point, action govt.MouseAction, button govt.MouseButton) bool {
	if c := w.active(); c != nil && !c.currentConfig().Features.MouseReporting {
		return false
	}
	input := pane.term.input
	if input.OnMouse == nil || ebiten.IsKeyPressed(ebiten.KeyShift) {
		return false
	}
	event := nativeMouseEvent(pane, p, action, button)
	event.Ctrl = ebiten.IsKeyPressed(ebiten.KeyControl)
	event.Alt = ebiten.IsKeyPressed(ebiten.KeyAlt)
	return input.OnMouse(event)
}

func nativeMouseEvent(pane nativePane, p image.Point, action govt.MouseAction, button govt.MouseButton) govt.MouseEvent {
	return govt.MouseEvent{Col: min(max(0, (p.X-pane.rect.Min.X)/pane.cw), max(0, pane.rect.Dx()/pane.cw-1)) + 1, Row: min(max(0, (p.Y-pane.rect.Min.Y)/pane.lh), max(0, pane.rect.Dy()/pane.lh-1)) + 1, X: max(0, p.X-pane.rect.Min.X) + 1, Y: max(0, p.Y-pane.rect.Min.Y) + 1, Action: action, Button: button}
}
func (w *EbitengineWindow) reportPointer(c *WorkspaceClient, p image.Point, action govt.MouseAction, button govt.MouseButton) {
	c.layoutMu.Lock()
	defer c.layoutMu.Unlock()
	c.connectionMu.RLock()
	remoteVisible := c.remoteFormVisible
	c.connectionMu.RUnlock()
	if c.settings.visible || remoteVisible || w.view(c).rename != nil {
		return
	}
	for _, pane := range w.view(c).panes {
		if p.In(pane.rect) {
			w.terminalMouse(pane, p, action, button)
			return
		}
	}
}
func (w *EbitengineWindow) updateCursor(c *WorkspaceClient) {
	c.layoutMu.Lock()
	defer c.layoutMu.Unlock()
	shape := ebiten.CursorShapeDefault
	for i := len(c.hitRegions) - 1; i >= 0; i-- {
		h := c.hitRegions[i]
		if !w.mouse.In(h.Rect) {
			continue
		}
		switch h.Kind {
		case hitSidebarResize:
			shape = ebiten.CursorShapeEWResize
		case hitDivider:
			if w.view(c).splits[h.Label].vertical {
				shape = ebiten.CursorShapeNSResize
			} else {
				shape = ebiten.CursorShapeEWResize
			}
		case hitPane, hitRemoteField, hitRenameField:
			shape = ebiten.CursorShapeText
		case hitSettingsControl:
			if strings.HasPrefix(h.Label, "field:") {
				shape = ebiten.CursorShapeText
			}
		case hitTitlebar:
			shape = ebiten.CursorShapeDefault
		default:
			shape = ebiten.CursorShapePointer
		}
		break
	}
	border := w.dp(5)
	horizontal := w.mouse.X < border || w.mouse.X >= w.size.X-border
	vertical := w.mouse.Y < border || w.mouse.Y >= w.size.Y-border
	if horizontal {
		shape = ebiten.CursorShapeEWResize
	}
	if vertical {
		shape = ebiten.CursorShapeNSResize
	}
	if horizontal && vertical {
		shape = ebiten.CursorShapeNWSEResize
	}
	ebiten.SetCursorShape(shape)
}

func (w *EbitengineWindow) wheel(c *WorkspaceClient, p image.Point, dx, dy float64) bool {
	c.layoutMu.Lock()
	defer c.layoutMu.Unlock()
	v := w.view(c)
	if c.settings.visible {
		delta := int(dy / 40)
		if delta == 0 {
			if dy > 0 {
				delta = 1
			} else if dy < 0 {
				delta = -1
			}
		}
		v.settingsScroll = max(0, v.settingsScroll+delta)
		return true
	}
	c.connectionMu.RLock()
	remoteVisible := c.remoteFormVisible
	c.connectionMu.RUnlock()
	if remoteVisible {
		return true
	}
	if p.In(v.tabRect) {
		delta := dx
		if c.currentConfig().UI.TabBarVerticalWheelScroll {
			delta += dy
		}
		v.tabScroll = max(0, v.tabScroll+int(delta))
		return true
	}
	if !c.sidebarHidden && p.In(v.sidebarRect) {
		v.sidebarScroll = max(0, v.sidebarScroll+int(dy))
		return true
	}
	for _, pane := range v.panes {
		if p.In(pane.rect) {
			steps := int(dy / float64(pane.lh))
			if steps == 0 {
				if dy > 0 {
					steps = 1
				} else if dy < 0 {
					steps = -1
				}
			}
			action := govt.MouseDown
			if steps < 0 {
				action = govt.MouseUp
				steps = -steps
			}
			accepted := false
			for i := 0; i < min(steps, 64); i++ {
				if w.terminalMouse(pane, p, action, govt.MouseWheel) {
					accepted = true
				}
			}
			if accepted {
				return true
			}
			if pane.term.input.OnScroll != nil {
				lines := int(dy / float64(pane.lh))
				if lines == 0 {
					if dy > 0 {
						lines = 1
					} else if dy < 0 {
						lines = -1
					}
				}
				pane.term.input.OnScroll(lines)
				return true
			}
		}
	}
	return false
}

func (w *EbitengineWindow) settingsAction(c *WorkspaceClient, label string) {
	c.layoutMu.Lock()
	defer c.layoutMu.Unlock()
	s := &c.settings
	if s.saving {
		return
	}
	switch label {
	case "cancel", "scrim":
		s.visible = false
		s.focus = -1
		if label == "scrim" {
			c.connectionMu.Lock()
			if !c.remoteConnecting {
				c.remoteFormVisible = false
			}
			c.connectionMu.Unlock()
			w.view(c).remoteFocused = false
		}
	case "defaults":
		c.openSettingsConfig(goconfig.Default())
		w.view(c).settingsScroll = 0
	case "save":
		w.saveSettings(c)
	default:
		if strings.HasPrefix(label, "category:") {
			for i, name := range []string{"Terminal", "UI", "Shortcuts", "Theme", "Startup"} {
				if label == "category:"+name {
					s.group = i
					s.focus = -1
					w.view(c).settingsScroll = 0
				}
			}
		}
		if strings.HasPrefix(label, "field:") {
			for i := range s.fields {
				f := &s.fields[i]
				if label == "field:"+f.group+"."+f.name {
					s.focus = i
					if f.kind == reflect.Bool {
						f.toggle.Value = !f.toggle.Value
					}
					break
				}
			}
		}
	}
}
func (w *EbitengineWindow) saveSettings(c *WorkspaceClient) {
	s := &c.settings
	cfg, err := s.parse()
	if err != nil {
		s.message = err.Error()
		return
	}
	if c.configPath == "" {
		s.message = "Settings path is unavailable"
		return
	}
	if c.settingsStore != nil {
		if !c.settingsStore.saving.CompareAndSwap(false, true) {
			s.message = "Another connection is saving settings"
			return
		}
		if s.base != c.settingsStore.value.Load() {
			c.settingsStore.saving.Store(false)
			s.message = "Settings changed in another connection. Reopen to reload them."
			return
		}
	}
	s.saving = true
	result := make(chan settingsSave, 1)
	s.result = result
	path := c.configPath
	go func() {
		err := goconfig.Save(path, cfg)
		if c.settingsStore != nil {
			if err == nil {
				c.settingsStore.value.Store(&cfg)
			}
			c.settingsStore.saving.Store(false)
		}
		result <- settingsSave{cfg, err}
	}()
}
func (w *EbitengineWindow) connectRemote(c *WorkspaceClient) {
	c.layoutMu.Lock()
	destination := strings.TrimSpace(c.remoteEditor.Text())
	c.layoutMu.Unlock()
	connect, _ := c.connectionActions()
	if connect == nil {
		return
	}
	c.connectionMu.Lock()
	if c.remoteConnecting {
		c.connectionMu.Unlock()
		return
	}
	if destination == "" {
		c.remoteError = "Enter an SSH destination"
		c.connectionMu.Unlock()
		return
	}
	c.remoteConnecting = true
	c.remoteError = ""
	c.connectionMu.Unlock()
	go func() {
		err := connect(destination)
		c.connectionMu.Lock()
		c.remoteConnecting = false
		if err != nil {
			c.remoteError = err.Error()
		} else {
			c.remoteFormVisible = false
			c.remoteClearEditor = true
		}
		c.connectionMu.Unlock()
	}()
}

// Native and control keys share this handler. No Gio router or rendering is
// involved: the existing VT encoder is reused only for terminal escape codes.
func (w *EbitengineWindow) key(c *WorkspaceClient, spec string) bool {
	c.layoutMu.Lock()
	defer c.layoutMu.Unlock()
	v := w.view(c)
	if ev, ok := automationInputEvent(spec); ok {
		if pressed, isKey := ev.(key.Event); isKey {
			bindings := c.currentConfig().Shortcuts
			if shortcutMatches(pressed, bindings.IgnoreQuit) {
				return true
			}
			if shortcutMatches(pressed, bindings.HideWindow) {
				w.menuAction("hide-window")
				return true
			}
			if shortcutMatches(pressed, bindings.MinimizeWindow) {
				w.menuAction("minimize-window")
				return true
			}
			if shortcutMatches(pressed, bindings.NewWindow) {
				w.startNewWindow()
				return true
			}
			if !c.settings.visible && v.rename == nil {
				if shortcutMatches(pressed, bindings.ConnectRemote) {
					c.connectionMu.Lock()
					c.remoteFormVisible = true
					c.remoteError = ""
					c.connectionMu.Unlock()
					v.remoteFocused = true
					return true
				}
				if shortcutMatches(pressed, bindings.RenameWorkspace) {
					c.mu.RLock()
					id := c.state.ActiveWorkspace
					c.mu.RUnlock()
					if id != nil {
						w.openRename(c, "workspace", *id)
					}
					return true
				}
				if shortcutMatches(pressed, bindings.RenameTab) {
					c.mu.RLock()
					ws := activeWorkspace(c.state)
					c.mu.RUnlock()
					if ws != nil && ws.ActiveTab != nil {
						w.openRename(c, "tab", *ws.ActiveTab)
					}
					return true
				}
				if c.currentConfig().UI.WorkspaceNavigationAcrossHosts {
					if shortcutMatches(pressed, bindings.NextWorkspace) {
						return w.cycleWorkspaceAcrossHosts(1)
					}
					if shortcutMatches(pressed, bindings.PreviousWorkspace) {
						return w.cycleWorkspaceAcrossHosts(-1)
					}
				}
			}
		}
	}
	if v.rename != nil {
		if strings.EqualFold(spec, "escape") {
			v.rename = nil
			return true
		}
		if strings.EqualFold(spec, "enter") || strings.EqualFold(spec, "return") {
			w.submitRename(c)
			return true
		}
		return w.editEditor(&v.rename.editor, spec)
	}
	c.connectionMu.RLock()
	remote := c.remoteFormVisible
	c.connectionMu.RUnlock()
	if remote && v.remoteFocused {
		if strings.EqualFold(spec, "escape") {
			c.connectionMu.Lock()
			if !c.remoteConnecting {
				c.remoteFormVisible = false
			}
			c.connectionMu.Unlock()
			return true
		}
		if strings.EqualFold(spec, "Return") || strings.EqualFold(spec, "Enter") {
			go w.connectRemote(c)
			return true
		}
		return w.editEditor(&c.remoteEditor, spec)
	}
	if c.dispatchShortcut(spec) {
		return true
	}
	if c.settings.visible {
		if strings.EqualFold(spec, "escape") {
			if !c.settings.saving {
				c.settings.visible = false
			}
			return true
		}
		if c.settings.saving {
			return true
		}
		if strings.EqualFold(spec, "Tab") || strings.EqualFold(spec, "shift-Tab") {
			groups := []string{"Terminal", "UI", "Shortcuts", "Theme", "Startup"}
			indices := []int{}
			for i, f := range c.settings.fields {
				if f.group == groups[c.settings.group] || c.settings.group == 4 && (f.group == "Shell" || f.group == "Server" || f.group == "Features") {
					indices = append(indices, i)
				}
			}
			if len(indices) > 0 {
				at := 0
				for i, index := range indices {
					if index == c.settings.focus {
						at = i
						break
					}
				}
				delta := 1
				if strings.EqualFold(spec, "shift-Tab") {
					delta = -1
				}
				at = (at + delta + len(indices)) % len(indices)
				c.settings.focus = indices[at]
				v.settingsScroll = at
			}
			return true
		}
		if c.settings.focus < 0 || c.settings.focus >= len(c.settings.fields) {
			return false
		}
		f := &c.settings.fields[c.settings.focus]
		if f.kind == reflect.Bool {
			if spec == "Space" || spec == "text: " {
				f.toggle.Value = !f.toggle.Value
				return true
			}
			return false
		}
		return w.editEditor(&f.editor, spec)
	}
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
	term.mu.RLock()
	snap := term.snapshot
	term.mu.RUnlock()
	bindings := c.currentConfig().Shortcuts
	ev, ok := automationInputEvent(spec)
	if !ok {
		return false
	}
	if edit, ok := ev.(key.EditEvent); ok {
		if edit.Text != "" {
			term.input.emit([]byte(edit.Text))
		}
		return true
	}
	pressed, ok := ev.(key.Event)
	if !ok {
		return false
	}
	if shortcutMatches(pressed, bindings.Paste) {
		if w.clipboardReady {
			data := clipboard.Read(clipboard.FmtText)
			if snap.BracketedPaste && c.currentConfig().Features.BracketedPaste {
				data = append(append([]byte("\x1b[200~"), data...), []byte("\x1b[201~")...)
			}
			term.input.emit(data)
		}
		return true
	}
	if shortcutMatches(pressed, bindings.CopyOrInterrupt) {
		if c.currentConfig().Features.Selection && w.clipboardReady && term.input.OnCopy != nil {
			if selected := term.input.OnCopy(); selected != "" {
				clipboard.Write(clipboard.FmtText, []byte(selected))
				return true
			}
		}
		term.input.emit([]byte{3})
		return true
	}
	if shortcutMatches(pressed, bindings.EOF) {
		term.input.emit([]byte{4})
		return true
	}
	if shortcutMatches(pressed, bindings.ScrollPageUp) || shortcutMatches(pressed, bindings.ScrollPageDown) {
		lines := snap.Rows
		if shortcutMatches(pressed, bindings.ScrollPageUp) {
			lines = -lines
		}
		if term.input.OnScroll != nil {
			term.input.OnScroll(lines)
		}
		return true
	}
	if pressed.Modifiers.Contain(key.ModCommand) || pressed.Modifiers.Contain(key.ModSuper) {
		return false
	}
	if seq := EncodeKey(pressed, snap.ApplicationCursor); len(seq) > 0 {
		term.input.emit(seq)
		return true
	}
	return false
}

func (w *EbitengineWindow) editEditor(editor *widget.Editor, spec string) bool {
	ev, ok := automationInputEvent(spec)
	if e, yes := ev.(key.Event); ok && yes && (e.Modifiers.Contain(key.ModCommand) || e.Modifiers.Contain(key.ModCtrl)) {
		start, end := editor.Selection()
		if start > end {
			start, end = end, start
		}
		value := []rune(editor.Text())
		start = min(max(0, start), len(value))
		end = min(max(0, end), len(value))
		if w.clipboardReady {
			switch e.Name {
			case "V":
				return editNativeEditor(editor, "text:"+string(clipboard.Read(clipboard.FmtText)))
			case "C", "X":
				clipboard.Write(clipboard.FmtText, []byte(string(value[start:end])))
				if e.Name == "X" {
					return editNativeEditor(editor, "text:")
				}
				return true
			}
		}
	}
	return editNativeEditor(editor, spec)
}

func editNativeEditor(editor *widget.Editor, spec string) bool {
	value := []rune(editor.Text())
	start, end := editor.Selection()
	start = min(max(0, start), len(value))
	end = min(max(0, end), len(value))
	if start > end {
		start, end = end, start
	}
	ev, ok := automationInputEvent(spec)
	if !ok {
		return false
	}
	if edit, ok := ev.(key.EditEvent); ok {
		insert := []rune(edit.Text)
		text := append(append(append([]rune{}, value[:start]...), insert...), value[end:]...)
		editor.SetText(string(text))
		at := start + len(insert)
		editor.SetCaret(at, at)
		return true
	}
	e, ok := ev.(key.Event)
	if !ok {
		return false
	}
	if e.Name == "A" && (e.Modifiers.Contain(key.ModCommand) || e.Modifiers.Contain(key.ModCtrl)) {
		editor.SetCaret(0, len(value))
		return true
	}
	if e.Name == key.NameDeleteBackward || e.Name == key.NameDeleteForward {
		if start == end {
			if e.Name == key.NameDeleteBackward && start > 0 {
				start--
			}
			if e.Name == key.NameDeleteForward && end < len(value) {
				end++
			}
		}
		editor.SetText(string(append(value[:start], value[end:]...)))
		editor.SetCaret(start, start)
		return true
	}
	anchor, caret := editor.Selection()
	pos := caret
	switch e.Name {
	case key.NameLeftArrow:
		if start != end && !e.Modifiers.Contain(key.ModShift) {
			pos = start
		} else {
			pos = max(0, caret-1)
		}
	case key.NameRightArrow:
		if start != end && !e.Modifiers.Contain(key.ModShift) {
			pos = end
		} else {
			pos = min(len(value), caret+1)
		}
	case key.NameHome:
		pos = 0
	case key.NameEnd:
		pos = len(value)
	default:
		return false
	}
	if e.Modifiers.Contain(key.ModShift) {
		editor.SetCaret(anchor, pos)
	} else {
		editor.SetCaret(pos, pos)
	}
	return true
}

func (w *EbitengineWindow) nativeKeys(c *WorkspaceClient) {
	mods := ""
	if ebiten.IsKeyPressed(ebiten.KeyControl) {
		mods += "ctrl-"
	}
	if ebiten.IsKeyPressed(ebiten.KeyAlt) {
		mods += "alt-"
	}
	if ebiten.IsKeyPressed(ebiten.KeyShift) {
		mods += "shift-"
	}
	if ebiten.IsKeyPressed(ebiten.KeyMeta) {
		mods += "cmd-"
	}
	for _, spec := range nativeKeySpecs(mods, w.composition != "", inpututil.KeyPressDuration) {
		w.composer.Confirm()
		w.key(c, spec)
	}
}

// Enumerate physical keys from zero: KeyDigit0 is after letters, arrows and
// Backspace in Ebitengine's enum. Printable text still comes through the IME.
func nativeKeySpecs(mods string, composing bool, durationOf func(ebiten.Key) int) []string {
	var specs []string
	for k := ebiten.Key(0); k <= ebiten.KeyMax; k++ {
		duration := durationOf(k)
		if duration != 1 && (duration < 22 || (duration-22)%3 != 0) {
			continue
		}
		name, printable := nativeKeyName(k, mods)
		if name == "" {
			continue
		}
		if printable && !strings.Contains(mods, "cmd-") && !strings.Contains(mods, "ctrl-") && !strings.Contains(mods, "alt-") {
			continue
		}
		if composing && name != "Escape" {
			continue
		}
		specs = append(specs, mods+name)
	}
	return specs
}

// Physical names cover terminal controls and shortcuts. Unmodified printable
// text (including non-US layouts, dead keys and keypad text) belongs to the OS
// text/IME path, so it is never also emitted from the physical key path.
func nativeKeyName(k ebiten.Key, mods string) (string, bool) {
	if k >= ebiten.KeyF1 && k <= ebiten.KeyF24 {
		return fmt.Sprintf("F%d", k-ebiten.KeyF1+1), false
	}
	named := map[ebiten.Key]string{
		ebiten.KeyEnter: "Return", ebiten.KeyNumpadEnter: "Return", ebiten.KeyTab: "Tab",
		ebiten.KeyEscape: "Escape", ebiten.KeyBackspace: "Backspace", ebiten.KeyDelete: "Delete", ebiten.KeyInsert: "Insert",
		ebiten.KeyArrowUp: "Up", ebiten.KeyArrowDown: "Down", ebiten.KeyArrowLeft: "Left", ebiten.KeyArrowRight: "Right",
		ebiten.KeyHome: "Home", ebiten.KeyEnd: "End", ebiten.KeyPageUp: "PageUp", ebiten.KeyPageDown: "PageDown",
	}
	if name := named[k]; name != "" {
		return name, false
	}
	if k >= ebiten.KeyA && k <= ebiten.KeyZ {
		return k.String(), true
	}
	if k >= ebiten.KeyNumpad0 && k <= ebiten.KeyNumpad9 {
		return fmt.Sprint(int(k - ebiten.KeyNumpad0)), true
	}
	shifted := strings.Contains(mods, "shift-") && !strings.Contains(mods, "cmd-")
	if k >= ebiten.KeyDigit0 && k <= ebiten.KeyDigit9 {
		if shifted {
			return string(")!@#$%^&*("[k-ebiten.KeyDigit0]), true
		}
		return fmt.Sprint(int(k - ebiten.KeyDigit0)), true
	}
	punctuation := map[ebiten.Key][2]string{
		ebiten.KeySpace: {"Space", "Space"}, ebiten.KeyBackquote: {"`", "~"},
		ebiten.KeyMinus: {"minus", "_"}, ebiten.KeyEqual: {"=", "+"},
		ebiten.KeyBracketLeft: {"[", "{"}, ebiten.KeyBracketRight: {"]", "}"},
		ebiten.KeyBackslash: {"\\", "|"}, ebiten.KeyIntlBackslash: {"\\", "|"},
		ebiten.KeySemicolon: {";", ":"}, ebiten.KeyQuote: {"'", "\""},
		ebiten.KeyComma: {",", "<"}, ebiten.KeyPeriod: {".", ">"}, ebiten.KeySlash: {"/", "?"},
		ebiten.KeyNumpadAdd: {"+", "+"}, ebiten.KeyNumpadSubtract: {"minus", "minus"},
		ebiten.KeyNumpadMultiply: {"*", "*"}, ebiten.KeyNumpadDivide: {"/", "/"},
		ebiten.KeyNumpadDecimal: {".", "."}, ebiten.KeyNumpadEqual: {"=", "="},
	}
	if names, ok := punctuation[k]; ok {
		if shifted {
			return names[1], true
		}
		return names[0], true
	}
	// Modifier/lock keys update OS state. PrintScreen/Pause/media keys are OS
	// actions and have no bytes in the legacy VT keyboard protocol.
	return "", false
}

func (w *EbitengineWindow) initComposer() {
	w.composer.OnNewSession = func() *textinput.SessionOptions {
		c := w.active()
		if c == nil || !ebiten.IsFocused() {
			return nil
		}
		c.layoutMu.Lock()
		defer c.layoutMu.Unlock()
		r := image.Rect(w.dp(20), w.dp(100), w.dp(22), w.dp(120))
		var editor *widget.Editor
		if rename := w.view(c).rename; rename != nil {
			editor = &rename.editor
			for _, hit := range c.hitRegions {
				if hit.Kind == hitRenameField {
					r = hit.Rect
					break
				}
			}
		} else if c.settings.visible && c.settings.focus >= 0 && c.settings.focus < len(c.settings.fields) {
			f := &c.settings.fields[c.settings.focus]
			if f.kind != reflect.Bool {
				editor = &f.editor
				for _, hit := range c.hitRegions {
					if hit.Label == "field:"+f.group+"."+f.name {
						r = hit.Rect
						break
					}
				}
			}
		} else if w.view(c).remoteFocused {
			editor = &c.remoteEditor
			for _, hit := range c.hitRegions {
				if hit.Kind == hitRemoteField {
					r = hit.Rect
					break
				}
			}
		}
		if editor != nil {
			value := []rune(editor.Text())
			_, caret := editor.Selection()
			caret = min(max(0, caret), len(value))
			face := w.fonts.face(c.currentConfig().UI.UIFontFamily, float64(w.dp(float64(c.currentConfig().UI.UIFontSize))), false, false, false)
			width, _ := text.Measure(string(value[:caret]), face, 0)
			x := min(r.Max.X-w.dp(10), r.Min.X+w.dp(10)+int(width))
			return &textinput.SessionOptions{CaretBounds: image.Rect(x, r.Min.Y+w.dp(6), x+1, r.Max.Y-w.dp(6))}
		}
		if pane, ok := w.view(c).panes[c.frameFocusedPane]; ok {
			pane.term.mu.RLock()
			snap := pane.term.snapshot
			pane.term.mu.RUnlock()
			x, y := pane.rect.Min.X+snap.CursorX*pane.cw, pane.rect.Min.Y+snap.CursorY*pane.lh
			r = image.Rect(x, y, x+pane.cw, y+pane.lh)
		}
		return &textinput.SessionOptions{CaretBounds: r}
	}
	w.composer.OnComposition = func(comp *textinput.Composition) {
		w.composition = comp.Text()
		if c := w.active(); c != nil {
			c.layoutMu.Lock()
			id := c.frameActiveTerminal
			if c.settings.visible || w.view(c).remoteFocused || w.view(c).rename != nil {
				id = uuid.Nil
			}
			c.layoutMu.Unlock()
			c.mu.RLock()
			term := c.terminals[id]
			c.mu.RUnlock()
			if term != nil {
				term.mu.Lock()
				term.imePreedit = w.composition
				term.imeComposing = w.composition != ""
				term.mu.Unlock()
			}
		}
	}
	w.composer.OnCommit = func(commit *textinput.Commit) {
		if c := w.active(); c != nil && commit.Text() != "" {
			w.key(c, "text:"+commit.Text())
		}
	}
}
func (w *EbitengineWindow) updateComposer(c *WorkspaceClient) (bool, error) {
	c.layoutMu.Lock()
	target := fmt.Sprintf("%p:%s", c, c.frameActiveTerminal)
	if w.view(c).rename != nil {
		target = fmt.Sprintf("%p:rename:%s", c, w.view(c).rename.id)
	} else if c.settings.visible {
		target = fmt.Sprintf("%p:settings:%d", c, c.settings.focus)
	} else if w.view(c).remoteFocused {
		target = fmt.Sprintf("%p:remote", c)
	}
	c.layoutMu.Unlock()
	if target != w.inputTarget || !ebiten.IsFocused() {
		w.composer.Cancel()
		w.inputTarget = target
	}
	if !ebiten.IsFocused() {
		return false, nil
	}
	handled, err := w.composer.Update()
	if err != nil {
		return false, err
	}
	if !handled {
		chars := ebiten.AppendInputChars(nil)
		if len(chars) > 0 && !ebiten.IsKeyPressed(ebiten.KeyMeta) && !ebiten.IsKeyPressed(ebiten.KeyControl) {
			w.key(c, "text:"+string(chars))
			handled = true
		}
	}
	return handled, nil
}
