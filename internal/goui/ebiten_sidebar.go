package goui

import (
	"fmt"
	"image"
	"image/color"
	"strings"

	"github.com/SurTeam/Water/internal/gomodel"
	"github.com/google/uuid"
	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/vector"
)

type nativeHost struct {
	entry ConnectionEntry
	view  *WorkspaceClient
	state gomodel.StateDump
}
type nativeAgent struct {
	kind, label          string
	workspace, tab, pane uuid.UUID
	active, running      bool
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
		if custom, ok := a["custom_label"].(string); ok && custom != "" {
			label = custom
		}
		kind := fmt.Sprint(a["kind"])
		active, _ := a["active"].(bool)
		agents = append(agents, nativeAgent{kind, label, nativeUUID(a["workspace_id"]), nativeUUID(a["tab_id"]), nativeUUID(a["pane_id"]), active, a["status"] == "running"})
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
		if hosts[i].view.connectionError != "" {
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
	d := func(n float32) int { return w.dp(float64(n)) }
	fg := configColor(t.UIForeground, 0xe6eaea)
	pad := d(u.WindowPadding)
	surface := r.Inset(pad)
	surface.Min.Y += d(u.SidebarSurfaceMargin)
	surface.Max.Y -= d(u.SidebarSurfaceMargin)
	w.round(dst, surface, d(u.SidebarCardRadius), bg)
	margin := d(u.SidebarMargin)
	list := surface.Inset(margin)
	list.Max.Y -= w.dp(72)
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
		agents := sidebarAgents(host.state)
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
		agents := sidebarAgents(host.state)
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
		clr := fg
		if offline {
			clr = configColor(t.SidebarConnectionOfflineColor, 0xef7d83)
		}
		hostName := host.entry.Name
		if host.entry.Kind == "local" {
			hostName = c.tr("Local")
		}
		headerText := image.Rect(header.Min.X+d(u.SidebarRowPadding), header.Min.Y, header.Max.X-d(u.SidebarRowPadding), header.Max.Y)
		if host.entry.Kind == "remote" {
			headerText.Max.X -= w.dp(20)
		}
		w.centeredLabel(content, c, headerText, hostName, float64(u.UIFontSize), clr, true)
		w.hit(c, header, hitConnection, host.entry.ID, "")
		if host.entry.Kind == "remote" {
			dr := image.Rect(header.Max.X-w.dp(20), header.Min.Y, header.Max.X, header.Max.Y)
			w.centeredLabel(content, c, dr, "×", float64(u.UIFontSize), fg, false)
			w.hit(c, dr, hitDisconnectConnection, host.entry.ID, "")
		}
		y = header.Max.Y + d(u.SidebarHostWorkspaceGap)
		for _, ws := range host.state.Workspaces {
			row := image.Rect(card.Min.X+cp, y, card.Max.X-cp, y+d(u.SidebarHeaderHeight))
			selected := host.entry.ID == activeID && host.state.ActiveWorkspace != nil && *host.state.ActiveWorkspace == ws.ID
			rowBg := configColor(t.SidebarWorkspaceBackground, 0x252528)
			if selected {
				rowBg = configColor(t.SidebarWorkspaceActiveBackground, 0x353539)
			}
			w.round(content, row, d(u.SidebarWorkspaceRadius), rowBg)
			title := ws.Title
			count := 0
			for _, a := range agents {
				if a.workspace == ws.ID && a.running {
					count++
				}
			}
			if u.SidebarShowAgentCount && count > 0 {
				title = fmt.Sprintf("%s  · %d", title, count)
			}
			rowPadding := d(u.SidebarWorkspaceRowPadding)
			w.centeredLabel(content, c, image.Rect(row.Min.X+rowPadding, row.Min.Y, row.Max.X-rowPadding, row.Max.Y), title, float64(u.UIFontSize), fg, selected)
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
				ac := configColor(t.AgentColors[a.kind], 0x8eaeed)
				if !a.running {
					ac = mixColor(ac, bg, .65)
				}
				cx := ar.Min.X + d(u.SidebarAgentRowPadding) + w.dp(3)
				cy := (ar.Min.Y + ar.Max.Y) / 2
				if content != nil {
					vector.FillCircle(content, float32(cx), float32(cy), float32(w.dp(2.5)), ac, true)
					if a.active {
						vector.StrokeCircle(content, float32(cx), float32(cy), float32(w.dp(4)), 1, ac, true)
					}
				}
				w.centeredLabel(content, c, image.Rect(cx+w.dp(9), ar.Min.Y, ar.Max.X-d(u.SidebarAgentRowPadding), ar.Max.Y), a.label, float64(u.UIFontSize), fg, focused)
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
	w.button(dst, c, image.Rect(list.Min.X, bottom-w.dp(64), list.Max.X, bottom-w.dp(34)), "+  Connect remote", hitNewRemote, uuid.Nil, false)
	w.button(dst, c, image.Rect(list.Min.X, bottom-w.dp(30), list.Max.X, bottom), "+  New workspace", hitNewWorkspace, uuid.Nil, false)
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
