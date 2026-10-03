package goui

import (
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"gioui.org/unit"
	"gioui.org/widget"
	"github.com/SurTeam/Water/internal/gomodel"
	"github.com/google/uuid"
	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/text/v2"
	"github.com/hajimehoshi/ebiten/v2/vector"
)

func nativeRect(dst *ebiten.Image, r image.Rectangle, c color.NRGBA) {
	if dst != nil && !r.Empty() {
		vector.FillRect(dst, float32(r.Min.X), float32(r.Min.Y), float32(r.Dx()), float32(r.Dy()), c, false)
	}
}

func (w *EbitengineWindow) round(dst *ebiten.Image, r image.Rectangle, radius int, c color.NRGBA) {
	if dst == nil || r.Empty() {
		return
	}
	radius = min(radius, min(r.Dx(), r.Dy())/2)
	if radius <= 0 {
		nativeRect(dst, r, c)
		return
	}
	x, y, right, bottom, rad := float32(r.Min.X), float32(r.Min.Y), float32(r.Max.X), float32(r.Max.Y), float32(radius)
	var p vector.Path
	p.MoveTo(x+rad, y)
	p.LineTo(right-rad, y)
	p.QuadTo(right, y, right, y+rad)
	p.LineTo(right, bottom-rad)
	p.QuadTo(right, bottom, right-rad, bottom)
	p.LineTo(x+rad, bottom)
	p.QuadTo(x, bottom, x, bottom-rad)
	p.LineTo(x, y+rad)
	p.QuadTo(x, y, x+rad, y)
	p.Close()
	op := &vector.DrawPathOptions{AntiAlias: true}
	op.ColorScale.ScaleWithColor(c)
	vector.FillPath(dst, &p, nil, op)
}

func (w *EbitengineWindow) label(dst *ebiten.Image, c *WorkspaceClient, r image.Rectangle, value string, size float64, clr color.NRGBA, bold bool) {
	if dst == nil || r.Empty() || value == "" {
		return
	}
	face := w.fonts.face(c.currentConfig().UI.UIFontFamily, float64(w.dp(size)), false, bold, false)
	if tw, _ := text.Measure(value, face, 0); tw > float64(r.Dx()) {
		runes := []rune(value)
		for len(runes) > 0 {
			runes = runes[:len(runes)-1]
			if tw, _ := text.Measure(string(runes)+"…", face, 0); tw <= float64(r.Dx()) {
				value = string(runes) + "…"
				break
			}
		}
	}
	op := &text.DrawOptions{}
	op.GeoM.Translate(float64(r.Min.X), float64(r.Min.Y))
	op.ColorScale.ScaleWithColor(clr)
	text.Draw(dst.SubImage(r.Intersect(dst.Bounds())).(*ebiten.Image), value, face, op)
}
func (w *EbitengineWindow) hit(c *WorkspaceClient, r image.Rectangle, kind automationHitKind, id uuid.UUID, label string) {
	if !r.Empty() {
		c.hitRegions = append(c.hitRegions, automationHit{Rect: r, Kind: kind, ID: id, Label: label})
	}
}
func (w *EbitengineWindow) button(dst *ebiten.Image, c *WorkspaceClient, r image.Rectangle, title string, kind automationHitKind, id uuid.UUID, selected bool) {
	cfg := c.currentConfig()
	fg := configColor(cfg.Theme.UIForeground, 0xe6eaea)
	if selected {
		w.round(dst, r, w.dp(7), configColor(cfg.Theme.SidebarWorkspaceActiveBackground, 0x353539))
	} else if w.mouse.In(r) && !c.settings.visible {
		w.round(dst, r, w.dp(6), mixColor(configColor(cfg.Theme.ChromeBackground, 0x171b20), fg, .06))
	}
	w.label(dst, c, image.Rect(r.Min.X+w.dp(12), r.Min.Y+w.dp(9), r.Max.X-w.dp(10), r.Max.Y), title, float64(cfg.UI.UIFontSize), fg, selected)
	w.hit(c, r, kind, id, "")
}

func (w *EbitengineWindow) layout(c *WorkspaceClient, dst *ebiten.Image) {
	c.layoutMu.Lock()
	defer c.layoutMu.Unlock()
	c.pollSettingsSave()
	c.mu.RLock()
	state, connectionError := c.state, c.connectionError
	c.mu.RUnlock()
	cfg := c.currentConfig()
	v := w.view(c)
	c.frameSize = w.size
	c.frameMetric = unit.Metric{PxPerDp: float32(w.scale), PxPerSp: float32(w.scale)}
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
	c.hitRegions = c.hitRegions[:0]
	v.panes = map[uuid.UUID]nativePane{}
	v.splits = map[string]nativeSplit{}
	chrome, sidebar, fg := configColor(cfg.Theme.ChromeBackground, 0x28282b), configColor(cfg.Theme.SidebarBackground, 0x252528), configColor(cfg.Theme.UIForeground, 0xe6eaea)
	muted := mixColor(chrome, fg, .48)
	line := mixColor(chrome, fg, .09)
	if dst != nil {
		dst.Fill(chrome)
	}
	titleHeight := w.titleHeight(c)
	if v.lastUI != cfg.UI {
		if v.lastUI.SidebarVisible != cfg.UI.SidebarVisible {
			c.sidebarHidden = !cfg.UI.SidebarVisible
		}
		if v.lastUI.SidebarWidth != cfg.UI.SidebarWidth {
			v.sidebarWidth = cfg.UI.SidebarWidth
		}
		v.lastUI = cfg.UI
	}
	sideWidth := w.dp(float64(v.sidebarWidth))
	if c.sidebarHidden {
		sideWidth = 0
	}
	sideWidth = min(sideWidth, max(0, w.size.X-w.dp(300)))
	// The undecorated window's titlebar is entirely ours, including controls.
	titlebar := image.Rect(0, 0, w.size.X, titleHeight)
	nativeRect(dst, titlebar, chrome)
	controlsEnd := w.dp(float64(cfg.UI.TitlebarPadding)*2 + 33 + float64(cfg.UI.TitlebarGap)*2)
	w.hit(c, image.Rect(controlsEnd, 0, w.size.X, titleHeight), hitTitlebar, uuid.Nil, "")
	for i, item := range []struct {
		kind  automationHitKind
		clr   color.NRGBA
		glyph string
	}{{hitWindowClose, configColor("#ef766f", 0), "×"}, {hitWindowMinimize, configColor("#e3bb63", 0), "−"}, {hitWindowMaximize, configColor("#79bf93", 0), "+"}} {
		cx := w.dp(float64(cfg.UI.TitlebarPadding) + 5.5 + float64(i)*(11+float64(cfg.UI.TitlebarGap)))
		cy := titleHeight / 2
		r := image.Rect(cx-w.dp(10), cy-w.dp(12), cx+w.dp(10), cy+w.dp(12))
		if dst != nil {
			vector.FillCircle(dst, float32(cx), float32(cy), float32(w.dp(5.5)), item.clr, true)
		}
		if w.mouse.In(r) {
			w.label(dst, c, image.Rect(r.Min.X+w.dp(6), r.Min.Y+w.dp(2), r.Max.X, r.Max.Y), item.glyph, 11, configColor("#272b2d", 0), false)
		}
		w.hit(c, r, item.kind, uuid.Nil, "")
	}
	workspace := activeWorkspace(state)
	if sideWidth > 0 {
		w.label(dst, c, image.Rect(controlsEnd, w.dp(10), sideWidth-w.dp(14), titleHeight), "Water", 11, muted, true)
		w.drawSidebar(c, dst, image.Rect(0, titleHeight, sideWidth, w.size.Y), sidebar)
		handle := w.dp(float64(cfg.UI.SidebarResizeHandleWidth))
		w.hit(c, image.Rect(sideWidth-handle/2, titleHeight, sideWidth+(handle+1)/2, w.size.Y), hitSidebarResize, uuid.Nil, "sidebar-resize")
	} else {
		v.sidebarRect = image.Rectangle{}
	}
	if workspace != nil {
		x := max(sideWidth+w.dp(float64(cfg.UI.TitlebarGap)), controlsEnd)
		available := max(0, w.size.X-x-w.dp(132))
		tabWidth := min(w.dp(140+2*(float64(cfg.UI.TabPadding)-12)), max(w.dp(80), available/max(1, len(workspace.Tabs))))
		tabWidth = max(w.dp(40), tabWidth)
		tabHeight := min(titleHeight-w.dp(4), w.dp(float64(cfg.UI.TabHeight)))
		tabTop := (titleHeight - tabHeight) / 2
		v.tabRect = image.Rect(x, 0, x+available, titleHeight)
		maxScroll := max(0, len(workspace.Tabs)*tabWidth-available)
		if workspace.ActiveTab != nil && v.lastActiveTab != *workspace.ActiveTab {
			for i, tab := range workspace.Tabs {
				if tab.ID == *workspace.ActiveTab {
					v.tabScroll = max(i*tabWidth-available+tabWidth, min(v.tabScroll, i*tabWidth))
					break
				}
			}
			v.lastActiveTab = *workspace.ActiveTab
		}
		v.tabScroll = min(max(0, v.tabScroll), maxScroll)
		for i, tab := range workspace.Tabs {
			r := image.Rect(x+i*tabWidth-v.tabScroll, tabTop, x+(i+1)*tabWidth-v.tabScroll-w.dp(float64(cfg.UI.TabGap)), tabTop+tabHeight)
			if r.Min.X < x || r.Max.X > x+available {
				continue
			}
			selected := workspace.ActiveTab != nil && tab.ID == *workspace.ActiveTab
			if selected {
				w.round(dst, r.Add(image.Pt(0, w.dp(1))), w.dp(5), color.NRGBA{A: 40})
				w.round(dst, r, w.dp(5), configColor(cfg.Theme.TabActiveBackground, 0x414145))
			} else {
				w.round(dst, r, w.dp(5), configColor(cfg.Theme.TabInactiveBackground, 0x28282b))
				if w.mouse.In(r) {
					w.round(dst, r, w.dp(5), mixColor(chrome, fg, .05))
				}
			}
			labelColor := muted
			if selected {
				labelColor = fg
			}
			w.label(dst, c, image.Rect(r.Min.X+w.dp(float64(cfg.UI.TabPadding)), r.Min.Y+(tabHeight-w.dp(float64(cfg.UI.UIFontSize)))/2, r.Max.X-w.dp(float64(cfg.UI.TabPadding)), r.Max.Y), tab.Title, float64(cfg.UI.UIFontSize), labelColor, selected)
			w.hit(c, r, hitTab, tab.ID, "")
		}
		addX := min(x+len(workspace.Tabs)*tabWidth-v.tabScroll, x+available)
		add := image.Rect(addX, tabTop, addX+w.dp(26), tabTop+tabHeight)
		w.round(dst, add, w.dp(5), configColor(cfg.Theme.TabAddBackground, 0x28282b))
		if w.mouse.In(add) {
			w.round(dst, add, w.dp(5), mixColor(chrome, fg, .06))
		}
		w.label(dst, c, image.Rect(add.Min.X+w.dp(8), add.Min.Y+w.dp(4), add.Max.X, add.Max.Y), "+", 12, muted, false)
		w.hit(c, add, hitNewTab, uuid.Nil, "")
		for i, kind := range []automationHitKind{hitSplitRight, hitSplitDown, hitSettings} {
			r := image.Rect(w.size.X-w.dp(98)+i*w.dp(30), tabTop, w.size.X-w.dp(72)+i*w.dp(30), tabTop+tabHeight)
			if w.mouse.In(r) {
				w.round(dst, r, w.dp(6), mixColor(chrome, fg, .07))
			}
			w.drawIcon(dst, r, kind, muted)
			w.hit(c, r, kind, uuid.Nil, "")
		}
		if tab := activeTab(*workspace); tab != nil {
			var root paneTree
			if json.Unmarshal(tab.Tree, &root) == nil {
				pad := w.dp(float64(cfg.UI.WindowPadding))
				w.layoutNativePane(c, dst, &root, *tab, image.Rect(sideWidth+pad, titleHeight+pad, w.size.X-pad, w.size.Y-pad), nil)
			}
		}
	} else {
		w.label(dst, c, image.Rect(sideWidth+w.dp(30), titleHeight+w.dp(30), w.size.X, w.size.Y), "Creating workspace…", 13, muted, false)
	}
	nativeRect(dst, image.Rect(0, titleHeight-1, w.size.X, titleHeight), line)
	notice := w.quitError
	if notice == "" && connectionError != "" {
		notice = "Connection closed: " + connectionError
	}
	if notice != "" {
		r := image.Rect(sideWidth+w.dp(12), titleHeight+w.dp(10), w.size.X-w.dp(12), titleHeight+w.dp(42))
		w.round(dst, r, w.dp(7), chrome)
		w.label(dst, c, r.Inset(w.dp(8)), notice, 11, configColor("#ef766f", 0), false)
	}
	c.connectionMu.RLock()
	remoteVisible := c.remoteFormVisible
	clearRemote := c.remoteClearEditor
	c.connectionMu.RUnlock()
	if clearRemote {
		c.remoteEditor.SetText("")
		c.connectionMu.Lock()
		c.remoteClearEditor = false
		c.connectionMu.Unlock()
	}
	if remoteVisible {
		w.drawRemote(c, dst)
	}
	if c.settings.visible {
		w.drawSettings(c, dst)
	}
	if v.rename != nil {
		w.drawRename(c, dst)
	}
	w.drawHyperlink(c, dst)
}

func (w *EbitengineWindow) drawIcon(dst *ebiten.Image, r image.Rectangle, kind automationHitKind, clr color.NRGBA) {
	if dst == nil {
		return
	}
	cx, cy := float32((r.Min.X+r.Max.X)/2), float32((r.Min.Y+r.Max.Y)/2)
	s := float32(w.dp(6))
	stroke := float32(w.dp(1))
	if kind == hitSettings {
		for i := 0; i < 3; i++ {
			y := cy + float32(i-1)*s*.8
			x := cx - s*.4
			if i == 1 {
				x = cx + s*.4
			}
			vector.StrokeLine(dst, cx-s, y, x-s*.22, y, stroke, clr, true)
			vector.StrokeLine(dst, x+s*.22, y, cx+s, y, stroke, clr, true)
			vector.StrokeCircle(dst, x, y, s*.22, stroke, clr, true)
		}
		return
	}
	vector.StrokeRect(dst, cx-s, cy-s, 2*s, 2*s, stroke, clr, true)
	if kind == hitSplitRight {
		vector.StrokeLine(dst, cx, cy-s, cx, cy+s, stroke, clr, true)
	} else {
		vector.StrokeLine(dst, cx-s, cy, cx+s, cy, stroke, clr, true)
	}
}

func (w *EbitengineWindow) layoutNativePane(c *WorkspaceClient, dst *ebiten.Image, node *paneTree, tab gomodel.TabDump, r image.Rectangle, path []bool) {
	if node == nil || r.Empty() {
		return
	}
	v := w.view(c)
	cfg := c.currentConfig()
	if node.Type == "split" {
		vertical := node.Axis == "vertical"
		span := r.Dx()
		if vertical {
			span = r.Dy()
		}
		gap := min(span, w.dp(float64(cfg.UI.PaneMargin)))
		ratio := node.Ratio
		if ratio < .05 || ratio > .95 {
			ratio = .5
		}
		name := fmt.Sprintf("%s:%v", tab.ID, path)
		if w.drag != nil && w.drag.kind == hitDivider && w.drag.hit.Label == name {
			ratio = w.drag.split.ratio
		}
		first := int(float32(span-gap) * ratio)
		a, b, divider := r, r, r
		if vertical {
			a.Max.Y = r.Min.Y + first
			divider.Min.Y = a.Max.Y
			divider.Max.Y = a.Max.Y + gap
			b.Min.Y = divider.Max.Y
		} else {
			a.Max.X = r.Min.X + first
			divider.Min.X = a.Max.X
			divider.Max.X = a.Max.X + gap
			b.Min.X = divider.Max.X
		}
		v.splits[name] = nativeSplit{r, vertical, tab.ID, append([]bool{}, path...), ratio}
		hitDividerRect := divider
		if vertical {
			hitDividerRect.Min.Y -= w.dp(3)
			hitDividerRect.Max.Y += w.dp(3)
		} else {
			hitDividerRect.Min.X -= w.dp(3)
			hitDividerRect.Max.X += w.dp(3)
		}
		w.layoutNativePane(c, dst, node.First, tab, a, append(append([]bool{}, path...), false))
		w.layoutNativePane(c, dst, node.Second, tab, b, append(append([]bool{}, path...), true))
		if w.mouse.In(hitDividerRect) || w.drag != nil && w.drag.hit.Label == name {
			guide := divider
			half := w.dp(float64(cfg.UI.PaneDividerWidth)) / 2
			if vertical {
				mid := (divider.Min.Y + divider.Max.Y) / 2
				guide.Min.Y = mid - half
				guide.Max.Y = mid + max(1, half)
			} else {
				mid := (divider.Min.X + divider.Max.X) / 2
				guide.Min.X = mid - half
				guide.Max.X = mid + max(1, half)
			}
			nativeRect(dst, guide, configColor(cfg.Theme.ActivePaneBorder, 0x45454a))
		}
		w.hit(c, hitDividerRect, hitDivider, tab.ID, name)
		return
	}
	border := configColor(cfg.Theme.InactivePaneBorder, 0x292f35)
	if tab.ActivePane == node.PaneID {
		border = configColor(cfg.Theme.ActivePaneBorder, 0x35483f)
	}
	radius := w.dp(float64(cfg.UI.PaneCornerRadius))
	header := 0
	if len(path) > 0 {
		w.round(dst, r, radius, border)
		w.round(dst, r.Inset(1), max(0, radius-1), configColor(cfg.Theme.PaneBackground, 0x1e1e20))
		header = w.dp(28)
	} else {
		w.round(dst, r, radius, configColor(cfg.Theme.PaneBackground, 0x1e1e20))
	}
	fg := configColor(cfg.Theme.UIForeground, 0xe6eaea)
	muted := mixColor(configColor(cfg.Theme.ChromeBackground, 0x171b20), fg, .5)
	if node.Terminal == nil {
		w.label(dst, c, r.Inset(w.dp(12)), "Empty pane", 12, muted, false)
		return
	}
	summary := node.Terminal.Summary
	name := summary.ProcessName
	if name == "" {
		name = filepath.Base(cfg.Shell.Program)
	}
	cwd := summary.CWD
	if home, _ := os.UserHomeDir(); home != "" {
		cwd = strings.Replace(cwd, home, "~", 1)
	}
	if header > 0 {
		w.label(dst, c, image.Rect(r.Min.X+w.dp(12), r.Min.Y+w.dp(7), r.Min.X+w.dp(130), r.Min.Y+header), name, 9, muted, false)
		w.label(dst, c, image.Rect(r.Min.X+w.dp(140), r.Min.Y+w.dp(7), r.Max.X-w.dp(12), r.Min.Y+header), cwd, 9, muted, false)
	}
	content := r.Inset(w.dp(float64(cfg.UI.PanePadding)))
	if header > 0 {
		content.Min.Y = r.Min.Y + header + w.dp(4)
	}
	c.mu.RLock()
	term := c.terminals[summary.TerminalID]
	c.mu.RUnlock()
	if term == nil {
		w.label(dst, c, content, "Attaching terminal…", 12, muted, false)
		return
	}
	applyViewConfig(term.view, cfg)
	cell := measureNativeCell(w.terminalFaces(c), w.dp(float64(cfg.Terminal.LineHeight)))
	cw, lh := cell.width, cell.height
	v.panes[node.PaneID] = nativePane{content, term, node.PaneID, cw, lh}
	w.hit(c, content, hitPane, node.PaneID, "")
	w.resizeTerminal(c, term, content, cw, lh)
	if dst != nil {
		w.drawTerminal(c, dst, term, content, cw, lh, tab.ActivePane == node.PaneID)
		if cfg.UI.DimInactivePanes && tab.ActivePane != node.PaneID {
			w.round(dst, r, radius, color.NRGBA{A: 55})
		}
	}
}

func (w *EbitengineWindow) overlay(c *WorkspaceClient, dst *ebiten.Image, width, height int) image.Rectangle {
	width = min(width, w.size.X-w.dp(32))
	height = min(height, w.size.Y-w.dp(96))
	r := image.Rect((w.size.X-width)/2, (w.size.Y-height)/2, (w.size.X+width)/2, (w.size.Y+height)/2)
	nativeRect(dst, image.Rect(0, w.titleHeight(c), w.size.X, w.size.Y), color.NRGBA{A: 130})
	w.round(dst, r.Add(image.Pt(0, w.dp(8))), w.dp(12), color.NRGBA{A: 80})
	w.round(dst, r, w.dp(10), mixColor(configColor(c.currentConfig().Theme.ChromeBackground, 0x171b20), configColor(c.currentConfig().Theme.UIForeground, 0xe6eaea), .15))
	w.round(dst, r.Inset(1), w.dp(10), configColor(c.currentConfig().Theme.ChromeBackground, 0x171b20))
	kept := c.hitRegions[:0]
	for _, h := range c.hitRegions {
		if h.Kind == hitWindowClose || h.Kind == hitWindowMinimize || h.Kind == hitWindowMaximize || h.Kind == hitTitlebar {
			kept = append(kept, h)
		}
	}
	c.hitRegions = kept
	w.hit(c, image.Rect(0, w.titleHeight(c), w.size.X, w.size.Y), hitSettingsControl, uuid.Nil, "scrim")
	return r
}
func (w *EbitengineWindow) field(c *WorkspaceClient, dst *ebiten.Image, r image.Rectangle, value string, focused bool, editors ...*widget.Editor) {
	cfg := c.currentConfig()
	fg := configColor(cfg.Theme.UIForeground, 0xe6eaea)
	bg := configColor(cfg.Theme.ChromeBackground, 0x171b20)
	border := mixColor(bg, fg, .13)
	if focused {
		border = configColor(cfg.Theme.Accent, 0x72d6ab)
	}
	w.round(dst, r, w.dp(5), border)
	w.round(dst, r.Inset(1), w.dp(4), mixColor(bg, fg, .025))
	if focused && len(editors) > 0 {
		editor := editors[0]
		runes := []rune(editor.Text())
		anchor, caret := editor.Selection()
		anchor = min(max(0, anchor), len(runes))
		caret = min(max(0, caret), len(runes))
		start, end := min(anchor, caret), max(anchor, caret)
		face := w.fonts.face(cfg.UI.UIFontFamily, float64(w.dp(float64(cfg.UI.UIFontSize))), false, false, false)
		before, _ := text.Measure(string(runes[:start]), face, 0)
		after, _ := text.Measure(string(runes[:end]), face, 0)
		x := r.Min.X + w.dp(10)
		if w.composition == "" && start != end {
			clr := configColor(cfg.Theme.Accent, 0x72d6ab)
			clr.A = 65
			nativeRect(dst, image.Rect(x+int(before), r.Min.Y+w.dp(5), min(r.Max.X-w.dp(8), x+int(after)), r.Max.Y-w.dp(5)), clr)
		}
		prefix, _ := text.Measure(string(runes[:caret]), face, 0)
		if w.composition != "" {
			value = string(runes[:start]) + w.composition + string(runes[end:])
			preeditWidth, _ := text.Measure(w.composition, face, 0)
			nativeRect(dst, image.Rect(x+int(before), r.Max.Y-w.dp(5), min(r.Max.X-w.dp(8), x+int(before+preeditWidth)), r.Max.Y-w.dp(5)+1), fg)
			prefix = before + preeditWidth
		}
		if time.Now().UnixMilli()%1000 < 600 {
			cx := min(r.Max.X-w.dp(8), x+int(prefix))
			nativeRect(dst, image.Rect(cx, r.Min.Y+w.dp(6), cx+1, r.Max.Y-w.dp(6)), fg)
		}
	}
	w.label(dst, c, image.Rect(r.Min.X+w.dp(10), r.Min.Y+w.dp(8), r.Max.X-w.dp(10), r.Max.Y), value, float64(cfg.UI.UIFontSize), fg, false)
}
func (w *EbitengineWindow) drawSettings(c *WorkspaceClient, dst *ebiten.Image) {
	s := &c.settings
	v := w.view(c)
	r := w.overlay(c, dst, w.dp(680), w.dp(540))
	cfg := c.currentConfig()
	fg := configColor(cfg.Theme.UIForeground, 0xe6eaea)
	muted := mixColor(configColor(cfg.Theme.ChromeBackground, 0x171b20), fg, .52)
	w.label(dst, c, image.Rect(r.Min.X+w.dp(24), r.Min.Y+w.dp(20), r.Max.X-w.dp(24), r.Min.Y+w.dp(44)), "Settings", 16, fg, true)
	groups := []string{"Terminal", "UI", "Shortcuts", "Theme", "Startup"}
	top := r.Min.Y + w.dp(60)
	tabWidth := (r.Dx() - w.dp(48)) / 5
	w.round(dst, image.Rect(r.Min.X+w.dp(24), top-w.dp(3), r.Max.X-w.dp(24), top+w.dp(33)), w.dp(8), mixColor(configColor(cfg.Theme.ChromeBackground, 0), fg, .045))
	for i, name := range groups {
		tr := image.Rect(r.Min.X+w.dp(24)+i*tabWidth, top, r.Min.X+w.dp(24)+(i+1)*tabWidth-w.dp(4), top+w.dp(32))
		w.button(dst, c, tr, name, hitSettingsControl, uuid.Nil, s.group == i)
		c.hitRegions[len(c.hitRegions)-1].Label = "category:" + name
	}
	listTop := top + w.dp(52)
	footer := r.Max.Y - w.dp(72)
	rowHeight := w.dp(44)
	fields := []int{}
	for i, f := range s.fields {
		if f.group == groups[s.group] || s.group == 4 && (f.group == "Shell" || f.group == "Server" || f.group == "Features") {
			fields = append(fields, i)
		}
	}
	visible := max(1, (footer-listTop-w.dp(20))/rowHeight)
	v.settingsScroll = min(max(0, v.settingsScroll), max(0, len(fields)-visible))
	for j := v.settingsScroll; j < len(fields) && j < v.settingsScroll+visible; j++ {
		i := fields[j]
		f := &s.fields[i]
		y := listTop + (j-v.settingsScroll)*rowHeight
		labelR := image.Rect(r.Min.X+w.dp(24), y+w.dp(3), r.Min.X+r.Dx()*42/100-w.dp(8), y+w.dp(22))
		fr := image.Rect(r.Min.X+r.Dx()*42/100, y, r.Max.X-w.dp(36), y+w.dp(34))
		w.label(dst, c, labelR, f.label, 11, fg, false)
		w.label(dst, c, image.Rect(labelR.Min.X, y+w.dp(23), labelR.Max.X, y+rowHeight), settingsApplyKind(*f), 9, muted, false)
		if f.kind == reflect.Bool {
			toggle := image.Rect(fr.Min.X, y+w.dp(7), fr.Min.X+w.dp(30), y+w.dp(24))
			bg := mixColor(configColor(cfg.Theme.ChromeBackground, 0x171b20), fg, .15)
			if f.toggle.Value {
				bg = configColor(cfg.Theme.Accent, 0x72d6ab)
			}
			w.round(dst, toggle, w.dp(9), bg)
			cx := toggle.Min.X + w.dp(8)
			if f.toggle.Value {
				cx = toggle.Max.X - w.dp(8)
			}
			if dst != nil {
				vector.FillCircle(dst, float32(cx), float32((toggle.Min.Y+toggle.Max.Y)/2), float32(w.dp(6)), fg, true)
			}
		} else {
			w.field(c, dst, fr, f.editor.Text(), s.focus == i, &f.editor)
			if f.group == "Theme" {
				w.round(dst, image.Rect(fr.Max.X-w.dp(27), fr.Min.Y+w.dp(7), fr.Max.X-w.dp(9), fr.Min.Y+w.dp(25)), w.dp(3), configColor(f.editor.Text(), 0x555555))
			}
		}
		w.hit(c, fr, hitSettingsControl, uuid.Nil, "field:"+f.group+"."+f.name)
	}
	if len(fields) > visible {
		track := image.Rect(r.Max.X-w.dp(23), listTop, r.Max.X-w.dp(20), footer-w.dp(20))
		nativeRect(dst, track, mixColor(configColor(cfg.Theme.ChromeBackground, 0x171b20), fg, .08))
		h := max(w.dp(24), track.Dy()*visible/len(fields))
		y := track.Min.Y + (track.Dy()-h)*v.settingsScroll/max(1, len(fields)-visible)
		w.round(dst, image.Rect(track.Min.X, y, track.Max.X, y+h), w.dp(2), muted)
	}
	message := s.message
	messageColor := configColor("#ef766f", 0)
	if font := w.fonts.resolution(fontKey{family: cfg.Terminal.FontFamily}); message == "" && font["status"] == "fallback" {
		message = "Font unavailable: " + cfg.Terminal.FontFamily + ". Using Go Mono."
		messageColor = muted
	}
	w.label(dst, c, image.Rect(r.Min.X+w.dp(24), footer-w.dp(16), r.Max.X-w.dp(24), footer+w.dp(12)), message, 10, messageColor, false)
	nativeRect(dst, image.Rect(r.Min.X+w.dp(24), footer+w.dp(7), r.Max.X-w.dp(24), footer+w.dp(7)+1), mixColor(configColor(cfg.Theme.ChromeBackground, 0), fg, .08))
	for i, item := range []struct{ label, name string }{{"Save", "save"}, {"Cancel", "cancel"}, {"Reset defaults", "defaults"}} {
		x := r.Max.X - w.dp(112)
		width := w.dp(88)
		if i == 1 {
			x -= w.dp(100)
		}
		if i == 2 {
			x = r.Min.X + w.dp(24)
			width = w.dp(134)
		}
		br := image.Rect(x, footer+w.dp(24), x+width, footer+w.dp(52))
		if i == 0 {
			w.round(dst, br, w.dp(5), configColor(cfg.Theme.Accent, 0x72d6ab))
			w.label(dst, c, br.Inset(w.dp(8)), item.label, 11, configColor(cfg.Theme.AccentForeground, 0x111714), true)
			w.hit(c, br, hitSettingsControl, uuid.Nil, item.name)
		} else {
			w.button(dst, c, br, item.label, hitSettingsControl, uuid.Nil, false)
			c.hitRegions[len(c.hitRegions)-1].Label = item.name
		}
	}
}

func (w *EbitengineWindow) drawRemote(c *WorkspaceClient, dst *ebiten.Image) {
	r := w.overlay(c, dst, w.dp(440), w.dp(270))
	cfg := c.currentConfig()
	fg := configColor(cfg.Theme.UIForeground, 0xe6eaea)
	w.label(dst, c, image.Rect(r.Min.X+w.dp(24), r.Min.Y+w.dp(24), r.Max.X-w.dp(24), r.Max.Y), "Connect to a remote", 20, fg, true)
	w.label(dst, c, image.Rect(r.Min.X+w.dp(24), r.Min.Y+w.dp(60), r.Max.X-w.dp(24), r.Max.Y), "SSH destination", 11, mixColor(configColor(cfg.Theme.ChromeBackground, 0), fg, .5), false)
	fr := image.Rect(r.Min.X+w.dp(24), r.Min.Y+w.dp(94), r.Max.X-w.dp(24), r.Min.Y+w.dp(134))
	value := c.remoteEditor.Text()
	if value == "" {
		value = "user@host"
	}
	w.field(c, dst, fr, value, w.view(c).remoteFocused, &c.remoteEditor)
	w.hit(c, fr, hitRemoteField, uuid.Nil, "")
	c.connectionMu.RLock()
	message := c.remoteError
	connecting := c.remoteConnecting
	c.connectionMu.RUnlock()
	if connecting {
		message = "Connecting…"
	}
	w.label(dst, c, image.Rect(fr.Min.X, fr.Max.Y+w.dp(14), fr.Max.X, r.Max.Y), message, 10, fg, false)
	w.button(dst, c, image.Rect(fr.Min.X, r.Max.Y-w.dp(58), fr.Min.X+w.dp(120), r.Max.Y-w.dp(24)), "Connect", hitRemoteSubmit, uuid.Nil, true)
	w.button(dst, c, image.Rect(fr.Min.X+w.dp(136), r.Max.Y-w.dp(58), fr.Min.X+w.dp(240), r.Max.Y-w.dp(24)), "Cancel", hitRemoteCancel, uuid.Nil, false)
}
func (w *EbitengineWindow) drawHyperlink(c *WorkspaceClient, dst *ebiten.Image) {
	c.hyperlinkMu.Lock()
	defer c.hyperlinkMu.Unlock()
	p := c.hyperlinkPrompt
	if p == nil {
		return
	}
	r := w.overlay(c, dst, w.dp(500), w.dp(230))
	fg := configColor(c.currentConfig().Theme.UIForeground, 0xe6eaea)
	w.label(dst, c, image.Rect(r.Min.X+w.dp(24), r.Min.Y+w.dp(24), r.Max.X-w.dp(24), r.Max.Y), "Download this file?", 20, fg, true)
	w.label(dst, c, image.Rect(r.Min.X+w.dp(24), r.Min.Y+w.dp(68), r.Max.X-w.dp(24), r.Max.Y), p.URI, 11, fg, false)
	w.label(dst, c, image.Rect(r.Min.X+w.dp(24), r.Min.Y+w.dp(104), r.Max.X-w.dp(24), r.Max.Y), p.Error, 10, fg, false)
	w.button(dst, c, image.Rect(r.Min.X+w.dp(24), r.Max.Y-w.dp(58), r.Min.X+w.dp(150), r.Max.Y-w.dp(24)), "Download", hitHyperlinkConfirm, uuid.Nil, true)
	w.button(dst, c, image.Rect(r.Min.X+w.dp(166), r.Max.Y-w.dp(58), r.Min.X+w.dp(280), r.Max.Y-w.dp(24)), "Cancel", hitHyperlinkCancel, uuid.Nil, false)
}
