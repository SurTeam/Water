package goui

import (
	"fmt"
	"image"
	"image/color"
	"strings"
	"unicode"

	"github.com/SurTeam/Water/internal/gomodel"
	"github.com/google/uuid"
	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/text/v2"
	"github.com/hajimehoshi/ebiten/v2/vector"
)

type nativeHost struct {
	entry ConnectionEntry
	view  *WorkspaceClient
	state gomodel.StateDump
}
type nativeAgent struct {
	kind, label                        string
	workspace, tab, pane               uuid.UUID
	active, running                    bool
	terminal                           uuid.UUID
	workspaceName, status, titleSource string
}

func nativeUUID(value any) uuid.UUID {
	if id, ok := value.(uuid.UUID); ok {
		return id
	}
	id, _ := uuid.Parse(fmt.Sprint(value))
	return id
}
func sidebarAgents(state gomodel.StateDump) []nativeAgent {
	var agents []nativeAgent
	for _, raw := range state.Agents {
		a, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		label, _ := a["label"].(string)
		titleSource := "kind"
		if custom, ok := a["custom_label"].(string); ok && custom != "" {
			label = custom
			titleSource = "custom"
		}
		if custom, ok := a["custom_label"].(*string); ok && custom != nil && *custom != "" {
			label = *custom
			titleSource = "custom"
		}
		kind := fmt.Sprint(a["kind"])
		active, _ := a["active"].(bool)
		status := "Exited"
		if a["status"] == "running" {
			status = "Running"
		}
		agents = append(agents, nativeAgent{kind: kind, label: label, workspace: nativeUUID(a["workspace_id"]), tab: nativeUUID(a["tab_id"]), pane: nativeUUID(a["pane_id"]), terminal: nativeUUID(a["terminal_id"]), active: active, running: a["status"] == "running", status: status, titleSource: titleSource})
	}
	return agents
}

// OSC 0/2 titles are parsed by the client emulator, including inactive panes
// and remote replay/live streams. They never mutate server-owned model state.
func (c *WorkspaceClient) sidebarAgents(state gomodel.StateDump) []nativeAgent {
	agents := sidebarAgents(state)
	for i := range agents {
		a := &agents[i]
		for _, ws := range state.Workspaces {
			if ws.ID == a.workspace {
				a.workspaceName = ws.Title
				break
			}
		}
		c.mu.RLock()
		term := c.terminals[a.terminal]
		c.mu.RUnlock()
		if term != nil && a.titleSource != "custom" {
			term.mu.RLock()
			var title string
			if term.emu != nil {
				title = term.emu.Title()
			}
			term.mu.RUnlock()
			title = strings.TrimSpace(strings.Map(func(r rune) rune {
				if unicode.IsControl(r) {
					return -1
				}
				return r
			}, title))
			if title != "" {
				a.label = string([]rune(title)[:min(256, len([]rune(title)))])
				a.titleSource = "osc"
			}
		}
	}
	return agents
}
func (w *EbitengineWindow) hosts() []nativeHost {
	w.multi.mu.RLock()
	var hosts []nativeHost
	for _, id := range w.multi.order {
		if m := w.multi.connections[id]; m != nil {
			hosts = append(hosts, nativeHost{entry: m.entry, view: m.view})
		}
	}
	w.multi.mu.RUnlock()
	for i := range hosts {
		hosts[i].view.mu.RLock()
		hosts[i].state = hosts[i].view.state
		if hosts[i].view.connectionError != "" && hosts[i].entry.Status != "reconnecting" {
			hosts[i].entry.Status = "disconnected"
		}
		hosts[i].view.mu.RUnlock()
	}
	return hosts
}
func (w *EbitengineWindow) connectionView(id uuid.UUID) *WorkspaceClient {
	w.multi.mu.RLock()
	defer w.multi.mu.RUnlock()
	if m := w.multi.connections[id]; m != nil {
		return m.view
	}
	return nil
}
func (w *EbitengineWindow) drawSidebar(c *WorkspaceClient, dst *ebiten.Image, r image.Rectangle, bg color.NRGBA) {
	cfg := c.currentConfig()
	u := cfg.UI
	t := cfg.Theme
	v := w.view(c)
	// Controls added below take precedence over the blank draggable surface.
	w.hit(c, r, hitTitlebar, uuid.Nil, "sidebar-background")
	d := func(n float32) int { return w.dp(float64(n)) }
	fg := configColor(t.UIForeground, 0xe6eaea)
	pad := d(u.WindowPadding)
	surface := r.Inset(pad)
	surface.Min.Y += d(u.SidebarSurfaceMargin)
	surface.Max.Y -= d(u.SidebarSurfaceMargin)
	w.round(dst, surface, d(u.SidebarCardRadius), bg)
	margin := d(u.SidebarMargin)
	list := surface.Inset(margin)
	remoteHeight, workspaceHeight, buttonGap := d(u.SidebarRemoteButtonHeight), d(u.SidebarWorkspaceButtonHeight), w.dp(8)
	list.Max.Y -= remoteHeight + workspaceHeight + buttonGap + w.dp(8)
	fullList := list
	split := u.SidebarAgentMode == "split"
	if split {
		list.Max.Y = list.Min.Y + list.Dy()/2 - w.dp(6)
	}
	v.sidebarRect = list
	if list.Empty() {
		return
	}
	var content *ebiten.Image
	if dst != nil {
		content = dst.SubImage(list.Intersect(dst.Bounds())).(*ebiten.Image)
	}
	hosts := w.hosts()
	height := 0
	for _, host := range hosts {
		agents := host.view.sidebarAgents(host.state)
		if split {
			agents = nil
		}
		height += 2*d(u.SidebarCardPadding) + d(u.SidebarHostHeaderHeight) + d(u.SidebarHostWorkspaceGap) + d(u.SidebarCardGap)
		for _, ws := range host.state.Workspaces {
			height += d(u.SidebarHeaderHeight) + d(u.SidebarWorkspaceGap)
			count := 0
			for _, a := range agents {
				if a.workspace == ws.ID {
					count++
				}
			}
			if count > 0 {
				height += 2*d(u.SidebarAgentPadding) + count*d(u.SidebarAgentRowHeight) + max(0, count-1)*d(u.SidebarAgentRowGap)
			}
		}
	}
	v.sidebarContentHeight = height
	v.sidebarScroll = min(max(0, v.sidebarScroll), max(0, height-list.Dy()))
	y := list.Min.Y - v.sidebarScroll
	hitStart := len(c.hitRegions)
	activeID := w.multi.ActiveConnectionID()
	for _, host := range hosts {
		top := y
		card := image.Rect(list.Min.X, top, list.Max.X, top)
		cp := d(u.SidebarCardPadding)
		agents := host.view.sidebarAgents(host.state)
		allAgents := agents
		if split {
			agents = nil
		}
		cardHeight := 2*cp + d(u.SidebarHostHeaderHeight) + d(u.SidebarHostWorkspaceGap)
		for _, ws := range host.state.Workspaces {
			cardHeight += d(u.SidebarHeaderHeight) + d(u.SidebarWorkspaceGap)
			count := 0
			for _, a := range agents {
				if a.workspace == ws.ID {
					count++
				}
			}
			if count > 0 {
				cardHeight += 2*d(u.SidebarAgentPadding) + count*d(u.SidebarAgentRowHeight) + max(0, count-1)*d(u.SidebarAgentRowGap)
			}
		}
		card.Max.Y = top + cardHeight
		border := configColor(t.SidebarConnectionInactiveBorder, 0x303034)
		cardBg := configColor(t.SidebarConnectionBackground, 0x252528)
		if host.entry.ID == activeID {
			border = configColor(t.SidebarConnectionActiveBorder, 0x45454a)
			cardBg = configColor(t.SidebarConnectionActiveBackground, 0x353539)
		}
		offline := host.entry.Status != "" && host.entry.Status != "connected" && host.entry.Status != "online"
		if offline {
			border = configColor(t.SidebarConnectionOfflineBorder, 0x70444a)
		}
		w.round(content, card, d(u.SidebarCardRadius), border)
		w.round(content, card.Inset(1), max(0, d(u.SidebarCardRadius)-1), cardBg)
		y += cp
		header := image.Rect(card.Min.X+cp, y, card.Max.X-cp, y+d(u.SidebarHostHeaderHeight))
		header = sidebarRowWidth(header, u.SidebarHostRowWidth)
		clr := fg
		if offline {
			clr = configColor(t.SidebarConnectionOfflineColor, 0xef7d83)
		}
		hostName := host.entry.Name
		if host.entry.Kind == "local" {
			hostName = c.tr("Local")
		}
		if host.entry.Kind == "remote" && offline {
			if host.entry.Status == "reconnecting" {
				hostName += " · " + c.tr("Reconnecting")
			} else {
				hostName += " · " + c.tr("Offline")
			}
		}
		headerText := image.Rect(header.Min.X+d(u.SidebarRowPadding), header.Min.Y, header.Max.X-d(u.SidebarRowPadding), header.Max.Y)
		if host.entry.Kind == "remote" {
			headerText.Max.X -= w.dp(20)
		}
		w.sidebarLabel(content, c, headerText, hostName, u.SidebarHostFontSize, clr, true, u.SidebarHostAlignment)
		w.hit(c, header, hitConnection, host.entry.ID, "")
		if host.entry.Kind == "remote" {
			dr := image.Rect(header.Max.X-w.dp(20), header.Min.Y, header.Max.X, header.Max.Y)
			w.centeredLabel(content, c, dr, "×", float64(u.UIFontSize), fg, false)
			w.hit(c, dr, hitDisconnectConnection, host.entry.ID, "")
		}
		y = header.Max.Y + d(u.SidebarHostWorkspaceGap)
		for _, ws := range host.state.Workspaces {
			row := image.Rect(card.Min.X+cp, y, card.Max.X-cp, y+d(u.SidebarHeaderHeight))
			row = sidebarRowWidth(row, u.SidebarWorkspaceRowWidth)
			selected := host.entry.ID == activeID && host.state.ActiveWorkspace != nil && *host.state.ActiveWorkspace == ws.ID
			rowBg := configColor(t.SidebarWorkspaceBackground, 0x252528)
			if selected {
				rowBg = configColor(t.SidebarWorkspaceActiveBackground, 0x353539)
			}
			w.round(content, row, d(u.SidebarWorkspaceRadius), rowBg)
			title := ws.Title
			count := 0
			for _, a := range allAgents {
				if a.workspace == ws.ID && a.running {
					count++
				}
			}
			if u.SidebarShowAgentCount && count > 0 {
				title = fmt.Sprintf("%s  · %d", title, count)
			}
			rowPadding := d(u.SidebarWorkspaceRowPadding)
			w.sidebarLabel(content, c, image.Rect(row.Min.X+rowPadding, row.Min.Y, row.Max.X-rowPadding, row.Max.Y), title, u.SidebarWorkspaceFontSize, fg, selected, u.SidebarWorkspaceAlignment)
			w.hit(c, row, hitWorkspace, ws.ID, host.entry.ID.String())
			y = row.Max.Y
			var rows []nativeAgent
			for _, a := range agents {
				if a.workspace == ws.ID {
					rows = append(rows, a)
				}
			}
			if len(rows) > 0 {
				y += d(u.SidebarAgentPadding)
			}
			for _, a := range rows {
				aw := int(float32(row.Dx()) * u.SidebarAgentRowWidth)
				ar := image.Rect(row.Min.X+(row.Dx()-aw)/2, y, row.Min.X+(row.Dx()+aw)/2, y+d(u.SidebarAgentRowHeight))
				focused := host.entry.ID == activeID && host.state.FocusedPane != nil && *host.state.FocusedPane == a.pane
				ab := configColor(t.SidebarAgentBackground, 0x242426)
				if focused {
					ab = configColor(t.SidebarAgentActiveBackground, 0x353539)
				}
				w.round(content, ar, d(u.SidebarWorkspaceRadius), ab)
				w.drawAgentLabel(c, content, ar, a, focused, false, offline)
				w.hit(c, ar, hitAgent, a.pane, host.entry.ID.String()+":"+a.tab.String())
				y = ar.Max.Y + d(u.SidebarAgentRowGap)
			}
			if len(rows) > 0 {
				y += d(u.SidebarAgentPadding) - d(u.SidebarAgentRowGap)
			}
			y += d(u.SidebarWorkspaceGap)
		}
		y = card.Max.Y + d(u.SidebarCardGap)
	}
	for i := hitStart; i < len(c.hitRegions); i++ {
		c.hitRegions[i].Rect = c.hitRegions[i].Rect.Intersect(list)
	}
	if w.drag != nil && w.drag.kind == hitWorkspace {
		for _, h := range c.hitRegions[hitStart:] {
			if h.Kind == hitWorkspace && w.mouse.In(h.Rect) && h.Label == w.drag.hit.Label {
				nativeRect(content, image.Rect(h.Rect.Min.X, h.Rect.Min.Y, h.Rect.Max.X, h.Rect.Min.Y+w.dp(2)), configColor(t.SidebarDragIndicatorColor, 0x8eaeed))
				break
			}
		}
	}
	bottom := surface.Max.Y - margin
	if split {
		lower := fullList
		lower.Min.Y = list.Max.Y + w.dp(12)
		nativeRect(dst, image.Rect(fullList.Min.X, list.Max.Y+w.dp(6), fullList.Max.X, list.Max.Y+w.dp(6)+1), fg)
		w.drawAgentShelf(c, dst, lower, hosts, bg)
	}
	w.buttonWithFontSize(dst, c, image.Rect(list.Min.X, bottom-workspaceHeight-buttonGap-remoteHeight, list.Max.X, bottom-workspaceHeight-buttonGap), "+  Connect remote", hitNewRemote, uuid.Nil, false, u.SidebarRemoteButtonFontSize)
	w.buttonWithFontSize(dst, c, image.Rect(list.Min.X, bottom-workspaceHeight, list.Max.X, bottom), "+  New workspace", hitNewWorkspace, uuid.Nil, false, u.SidebarWorkspaceButtonFontSize)
}

func sidebarRowWidth(r image.Rectangle, width float32) image.Rectangle {
	n := int(float32(r.Dx()) * width)
	r.Min.X += (r.Dx() - n) / 2
	r.Max.X = r.Min.X + n
	return r
}

func (w *EbitengineWindow) sidebarLabel(dst *ebiten.Image, c *WorkspaceClient, r image.Rectangle, value string, size float32, clr color.NRGBA, bold bool, alignment string) {
	if alignment != "center" && dst != nil && !r.Empty() {
		face := w.fonts.face(c.currentConfig().UI.UIFontFamily, float64(w.dp(float64(size))), false, bold, false)
		width, _ := text.Measure(value, face, 0)
		n := min(r.Dx(), int(width)+1)
		if alignment == "right" {
			r.Min.X = r.Max.X - n
		} else {
			r.Max.X = r.Min.X + n
		}
	}
	w.centeredLabel(dst, c, r, value, float64(size), clr, bold)
}

func (w *EbitengineWindow) drawAgentShelf(c *WorkspaceClient, dst *ebiten.Image, r image.Rectangle, hosts []nativeHost, bg color.NRGBA) {
	if r.Empty() {
		return
	}
	u, t := c.currentConfig().UI, c.currentConfig().Theme
	v := w.view(c)
	count := 0
	for _, host := range hosts {
		count += len(host.view.sidebarAgents(host.state))
	}
	rowHeight, gap := w.dp(float64(u.SidebarAgentRowHeight)), w.dp(float64(u.SidebarAgentRowGap))
	v.agentShelfRect = r
	v.agentShelfScroll = min(max(0, v.agentShelfScroll), max(0, count*(rowHeight+gap)-r.Dy()))
	var content *ebiten.Image
	if dst != nil {
		content = dst.SubImage(r.Intersect(dst.Bounds())).(*ebiten.Image)
	}
	y := r.Min.Y - v.agentShelfScroll
	for _, host := range hosts {
		for _, a := range host.view.sidebarAgents(host.state) {
			row := image.Rect(r.Min.X, y, r.Max.X, y+rowHeight)
			focused := host.entry.ID == w.multi.ActiveConnectionID() && host.state.FocusedPane != nil && *host.state.FocusedPane == a.pane
			color := configColor(t.SidebarAgentBackground, 0x242426)
			if focused {
				color = configColor(t.SidebarAgentActiveBackground, 0x353539)
			}
			w.round(content, row, w.dp(float64(u.SidebarWorkspaceRadius)), color)
			offline := host.entry.Status != "" && host.entry.Status != "connected" && host.entry.Status != "online"
			w.drawAgentLabel(c, content, row, a, focused, true, offline)
			w.hit(c, row.Intersect(r), hitAgent, a.pane, host.entry.ID.String()+":"+a.tab.String())
			y += rowHeight + gap
		}
	}
}

func (w *EbitengineWindow) drawAgentLabel(c *WorkspaceClient, dst *ebiten.Image, row image.Rectangle, a nativeAgent, focused, shelf, offline bool) {
	u, t := c.currentConfig().UI, c.currentConfig().Theme
	fg := configColor(t.UIForeground, 0xe6eaea)
	statusColor := configColor(t.AgentColors[a.kind], 0x8eaeed)
	status := a.status
	if !a.running {
		statusColor = mixColor(statusColor, fg, .5)
	}
	if offline {
		status = "Offline"
		statusColor = configColor(t.SidebarConnectionOfflineColor, 0xef7d83)
	}
	pad := min(w.dp(float64(u.SidebarAgentRowPadding)), max(0, row.Dx()/8))
	if !shelf {
		glyph := "▶"
		if !a.running || offline {
			glyph = "Ⅱ"
		}
		icon := image.Rect(row.Min.X+pad, row.Min.Y, row.Min.X+pad+w.dp(16), row.Max.Y)
		w.centeredLabel(dst, c, icon, glyph, float64(u.SidebarAgentFontSize), statusColor, false)
		name := image.Rect(icon.Max.X+w.dp(6), row.Min.Y, row.Max.X-pad, row.Max.Y)
		w.sidebarLabel(dst, c, name, a.label, u.SidebarAgentFontSize, fg, focused, u.SidebarAgentAlignment)
		return
	}
	cx, cy := row.Min.X+pad+w.dp(3), (row.Min.Y+row.Max.Y)/2
	if dst != nil {
		vector.FillCircle(dst, float32(cx), float32(cy), float32(min(w.dp(2.5), max(1, row.Dy()/3))), statusColor, true)
	}
	metaWidth := min(w.dp(100), row.Dx()*2/5)
	meta := image.Rect(row.Max.X-pad-metaWidth, row.Min.Y, row.Max.X-pad, row.Max.Y)
	name := image.Rect(cx+w.dp(8), row.Min.Y, meta.Min.X-w.dp(6), row.Max.Y)
	w.sidebarLabel(dst, c, name, a.label, u.SidebarAgentFontSize, fg, focused, u.SidebarAgentAlignment)
	metaSize := max(float32(6), min(u.SidebarAgentFontSize, 10))
	if shelf {
		middle := (row.Min.Y + row.Max.Y) / 2
		w.sidebarLabel(dst, c, image.Rect(meta.Min.X, meta.Min.Y, meta.Max.X, middle), a.workspaceName, metaSize, fg, false, "right")
		meta.Min.Y = middle
	}
	w.sidebarLabel(dst, c, meta, c.tr(status), metaSize, statusColor, false, "right")
}

func (w *EbitengineWindow) activateAgent(c *WorkspaceClient, h automationHit) {
	parts := strings.Split(h.Label, ":")
	if len(parts) != 2 {
		return
	}
	connection := nativeUUID(parts[0])
	tab := nativeUUID(parts[1])
	view := w.connectionView(connection)
	if view == nil {
		return
	}
	w.multi.ActivateConnection(connection)
	_ = view.session.DispatchAsync(map[string]any{"type": "tab.activate", "tab_id": tab})
	_ = view.session.DispatchAsync(map[string]any{"type": "pane.focus", "pane_id": h.ID})
}
