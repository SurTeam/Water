package gotui

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/SurTeam/Water/internal/govt"
	"github.com/gdamore/tcell/v2"
	"github.com/google/uuid"
)

func (a *app) active() *pane {
	for i := range a.panes {
		if a.panes[i].id == a.focus {
			return &a.panes[i]
		}
	}
	return nil
}
func (a *app) input(b []byte) {
	if p := a.active(); p != nil {
		a.bytes(p.term, b)
	}
}
func (a *app) sendPaste() {
	b := a.paste
	if p := a.active(); p != nil {
		if t := a.terms[p.term]; t != nil && t.emu != nil && t.emu.FrameSnapshot().BracketedPaste {
			b = append(append([]byte("\x1b[200~"), b...), []byte("\x1b[201~")...)
		}
	}
	a.input(b)
}
func (a *app) key(e *tcell.EventKey) bool {
	if a.pasting {
		a.paste = append(a.paste, keyBytes(e, false)...)
		return false
	}
	if a.promptKind != "" {
		switch e.Key() {
		case tcell.KeyEscape:
			a.promptKind = ""
			a.prompt = ""
			a.confirmWorkspace = uuid.Nil
		case tcell.KeyEnter:
			a.submitPrompt()
		case tcell.KeyBackspace, tcell.KeyBackspace2:
			r := []rune(a.prompt)
			if len(r) > 0 {
				a.prompt = string(r[:len(r)-1])
			}
		default:
			if e.Key() == tcell.KeyRune {
				a.prompt += string(e.Rune())
			}
		}
		return false
	}
	if matchesShortcut(e, a.closeWorkspaceShortcut) {
		a.requestCloseWorkspace()
		return false
	}
	a.selectionRange = nil
	if a.menu {
		switch e.Key() {
		case tcell.KeyUp:
			a.menuIndex--
			return false
		case tcell.KeyDown:
			a.menuIndex++
			return false
		case tcell.KeyEnter:
			if a.menuIndex >= 0 && a.menuIndex < len(a.menuItems) {
				item := a.menuItems[a.menuIndex]
				a.activateHit(hit{kind: item.kind, id: item.id})
			}
			return a.quit
		}
	}
	if a.prefix || a.menu {
		a.prefix = false
		if e.Key() == tcell.KeyEscape {
			a.menu = false
			return false
		}
		if e.Key() == tcell.KeyCtrlB {
			a.menu = false
			a.input([]byte{2})
			return false
		}
		if e.Key() == tcell.KeyRune {
			return a.action(e.Rune())
		}
		return false
	}
	if e.Key() == tcell.KeyCtrlB {
		a.prefix = true
		a.status = ""
		return false
	}
	appCursor := false
	if p := a.active(); p != nil {
		if t := a.terms[p.term]; t != nil && t.emu != nil {
			appCursor = t.emu.FrameSnapshot().ApplicationCursor
		}
	}
	a.input(keyBytes(e, appCursor))
	return false
}
func keyBytes(e *tcell.EventKey, application bool) []byte {
	k, mod := e.Key(), e.Modifiers()
	s := ""
	if k == tcell.KeyRune {
		s = string(e.Rune())
		if mod&tcell.ModCtrl != 0 {
			r := e.Rune()
			if r >= 'a' && r <= 'z' {
				s = string([]byte{byte(r - 'a' + 1)})
			} else if r >= '@' && r <= '_' {
				s = string([]byte{byte(r - '@')})
			}
		}
	} else if k >= tcell.KeyCtrlSpace && k <= tcell.KeyCtrlUnderscore {
		s = string([]byte{byte(k)})
	} else {
		switch k {
		case tcell.KeyBackspace, tcell.KeyBackspace2:
			s = "\x7f"
		case tcell.KeyEnter:
			s = "\r"
		case tcell.KeyTab:
			s = "\t"
		case tcell.KeyEscape:
			s = "\x1b"
		case tcell.KeyBacktab:
			s = "\x1b[Z"
		case tcell.KeyUp:
			s = "A"
		case tcell.KeyDown:
			s = "B"
		case tcell.KeyRight:
			s = "C"
		case tcell.KeyLeft:
			s = "D"
		case tcell.KeyHome:
			s = "H"
		case tcell.KeyEnd:
			s = "F"
		case tcell.KeyInsert:
			s = "\x1b[2~"
		case tcell.KeyDelete:
			s = "\x1b[3~"
		case tcell.KeyPgUp:
			s = "\x1b[5~"
		case tcell.KeyPgDn:
			s = "\x1b[6~"
		default:
			if k >= tcell.KeyF1 && k <= tcell.KeyF12 {
				seq := []string{"OP", "OQ", "OR", "OS", "[15~", "[17~", "[18~", "[19~", "[20~", "[21~", "[23~", "[24~"}
				s = "\x1b" + seq[int(k-tcell.KeyF1)]
			}
		}
		if len(s) == 1 && strings.Contains("ABCDHF", s) {
			m := 1
			if mod&tcell.ModShift != 0 {
				m++
			}
			if mod&tcell.ModAlt != 0 {
				m += 2
			}
			if mod&tcell.ModCtrl != 0 {
				m += 4
			}
			if m > 1 {
				s = fmt.Sprintf("\x1b[1;%d%s", m, s)
			} else if application {
				s = "\x1bO" + s
			} else {
				s = "\x1b[" + s
			}
			return []byte(s)
		}
	}
	if mod&tcell.ModAlt != 0 {
		s = "\x1b" + s
	}
	return []byte(s)
}
func (a *app) action(r rune) bool {
	a.menu = false
	a.status = ""
	cmd := map[string]any{}
	switch r {
	case 'q':
		return true
	case 'Q':
		a.promptKind = "confirm Q: stop server and all its terminals (yes)"
		a.prompt = ""
		return false
	case 'p', 's', 'T':
		a.menu = true
		a.menuIndex = 0
		switch r {
		case 'p':
			a.menuMode = "panes"
		case 's':
			a.menuMode = "workspaces"
		case 'T':
			a.menuMode = "tabs"
		}
		return false
	case '?', ' ':
		a.menu = true
		a.menuMode = ""
		a.menuIndex = 0
		return false
	case 'n':
		a.syncState()
		if a.workspace == uuid.Nil {
			cmd = map[string]any{"type": "workspace.new"}
		} else {
			cmd = map[string]any{"type": "tab.new", "workspace_id": a.workspace}
		}
	case 'w':
		cmd = map[string]any{"type": "workspace.new"}
	case '%', '"':
		direction := "right"
		if r == '"' {
			direction = "down"
		}
		cmd = map[string]any{"type": "pane.split", "pane_id": a.focus, "direction": direction}
	case 'W':
		a.requestCloseWorkspace()
		return false
	case 'x', 't':
		a.promptKind = "confirm " + string(r) + " (yes)"
		a.prompt = ""
		return false
	case 'r':
		a.promptKind = "rename tab"
		a.prompt = ""
		return false
	case 'R':
		a.promptKind = "rename workspace"
		a.prompt = ""
		return false
	case ':':
		a.promptKind = "command JSON"
		a.prompt = ""
		return false
	case 'o':
		a.cyclePane()
		return false
	case '[', ']':
		a.cycleTab(r == ']')
		return false
	case '{', '}':
		a.cycleWorkspace(r == '}')
		return false
	case 'b':
		if p := a.active(); p != nil {
			if t := a.terms[p.term]; t != nil && t.emu != nil {
				t.emu.ScrollToBottom()
			}
		}
		return false
	default:
		a.status = "Unknown command; Ctrl-B ? opens menu"
		return false
	}
	a.command(cmd)
	return false
}
func (a *app) submitPrompt() {
	kind, text := a.promptKind, a.prompt
	workspace := a.confirmWorkspace
	a.confirmWorkspace = uuid.Nil
	a.promptKind = ""
	a.prompt = ""
	c := map[string]any{}
	switch kind {
	case "rename tab":
		c = map[string]any{"type": "tab.rename", "tab_id": a.tab, "title": text}
	case "rename workspace":
		c = map[string]any{"type": "workspace.rename", "workspace_id": a.workspace, "title": text}
	case "command JSON":
		if err := json.Unmarshal([]byte(text), &c); err != nil {
			a.status = err.Error()
			return
		}
		if _, ok := c["type"].(string); !ok {
			a.status = "Command requires a type string"
			return
		}
		c["tui_show_result"] = true
	default:
		if text != "yes" {
			return
		}
		switch {
		case strings.HasPrefix(kind, "confirm Q"):
			if a.shutdownPending {
				return
			}
			select {
			case a.commands <- map[string]any{"type": "tui.shutdown"}:
				a.shutdownPending = true
				a.status = "Stopping server…"
			default:
				a.status = "Command queue full; retry"
			}
			return
		case strings.HasPrefix(kind, "confirm x"):
			c = map[string]any{"type": "pane.close", "pane_id": a.focus}
		case strings.HasPrefix(kind, "confirm t"):
			c = map[string]any{"type": "tab.close", "tab_id": a.tab}
		case strings.HasPrefix(kind, "confirm W"):
			c = map[string]any{"type": "workspace.close", "workspace_id": workspace}
		}
	}
	if len(c) > 0 {
		a.command(c)
	}
}
func (a *app) cyclePane() {
	tab := a.activeTab()
	if tab == nil {
		return
	}
	var root tree
	if json.Unmarshal(tab.Tree, &root) != nil {
		return
	}
	var ids []uuid.UUID
	walk(&root, func(n *tree) { ids = append(ids, n.Pane) })
	if len(ids) == 0 {
		return
	}
	index := 0
	for i, id := range ids {
		if id == a.focus {
			index = (i + 1) % len(ids)
		}
	}
	a.focus = ids[index]
	a.command(map[string]any{"type": "pane.focus", "pane_id": a.focus})
}
func (a *app) cycleTab(next bool) {
	for _, ws := range a.state.Workspaces {
		if ws.ID != a.workspace || len(ws.Tabs) == 0 {
			continue
		}
		index := 0
		for i, t := range ws.Tabs {
			if t.ID == a.tab {
				index = i
			}
		}
		delta := -1
		if next {
			delta = 1
		}
		a.tab = ws.Tabs[(index+delta+len(ws.Tabs))%len(ws.Tabs)].ID
		a.focus = uuid.Nil
		a.command(map[string]any{"type": "tab.activate", "tab_id": a.tab})
	}
}
func (a *app) cycleWorkspace(next bool) {
	ws := a.state.Workspaces
	if len(ws) == 0 {
		return
	}
	index := 0
	for i, w := range ws {
		if w.ID == a.workspace {
			index = i
		}
	}
	delta := -1
	if next {
		delta = 1
	}
	a.workspace = ws[(index+delta+len(ws))%len(ws)].ID
	a.tab = uuid.Nil
	a.syncState()
	a.command(map[string]any{"type": "workspace.activate", "workspace_id": a.workspace})
}
func (a *app) mouse(e *tcell.EventMouse) {
	x, y := e.Position()
	buttons := e.Buttons()
	previous := a.buttons
	a.buttons = buttons
	if a.dragging != nil {
		d := a.dragging
		ratio := float64(x-d.parent.x) / float64(max(1, d.parent.w))
		if d.axis == "vertical" {
			ratio = float64(y-d.parent.y) / float64(max(1, d.parent.h))
		}
		ratio = max(.1, min(.9, ratio))
		a.command(map[string]any{"type": "pane.resize_split", "tab_id": a.tab, "path": d.path, "ratio": ratio})
		if buttons&tcell.Button1 == 0 {
			a.dragging = nil
		}
		return
	}
	down := buttons&tcell.Button1 != 0 && previous&tcell.Button1 == 0
	if down {
		for _, h := range a.hits {
			if !h.box.contains(x, y) {
				continue
			}
			a.activateHit(h)
			return
		}
		if a.menu {
			return
		}
		for i, d := range a.dividers {
			if d.box.contains(x, y) {
				a.dragging = &a.dividers[i]
				return
			}
		}
	}
	if a.menu || a.promptKind != "" {
		return
	}
	if sel := a.selectionRange; sel != nil && sel.drag {
		for _, p := range a.panes {
			if p.term != sel.term {
				continue
			}
			t := a.terms[p.term]
			if t == nil || t.emu == nil {
				a.selectionRange = nil
				return
			}
			content := p.contentBox()
			sel.endRow = t.emu.FrameSnapshot().YDisp + max(0, min(content.h-1, y-content.y))
			sel.endCol = max(0, min(p.box.w-1, x-p.box.x))
			if buttons&tcell.Button1 == 0 {
				sel.drag = false
				a.screen.SetClipboard([]byte(t.emu.SelectionText(sel.startRow, sel.startCol, sel.endRow, sel.endCol)))
				a.flash = "Selection copied to terminal clipboard"
				a.flashUntil = time.Now().Add(1500 * time.Millisecond)
			}
			return
		}
	}
	candidates := a.panes
	if cap := a.mouseCapture; cap != nil {
		candidates = nil
		for _, p := range a.panes {
			if p.id == cap.id {
				candidates = append(candidates, p)
				x = max(p.box.x, min(p.box.x+p.box.w-1, x))
				content := p.contentBox()
				y = max(content.y, min(content.y+content.h-1, y))
				break
			}
		}
		if buttons == tcell.ButtonNone {
			a.mouseCapture = nil
		}
	}
	for _, p := range candidates {
		if !p.box.contains(x, y) {
			continue
		}
		if down && a.focus != p.id {
			a.focus = p.id
			a.command(map[string]any{"type": "pane.focus", "pane_id": p.id})
		}
		content := p.contentBox()
		if y < content.y {
			return
		}
		t := a.terms[p.term]
		if t == nil || t.emu == nil {
			return
		}
		ev := govt.MouseEvent{Col: x - p.box.x + 1, Row: y - content.y + 1, X: x - p.box.x + 1, Y: y - content.y + 1, Button: govt.MouseLeft, Action: govt.MouseMove, Ctrl: e.Modifiers()&tcell.ModCtrl != 0, Alt: e.Modifiers()&tcell.ModAlt != 0, Shift: e.Modifiers()&tcell.ModShift != 0}
		wheel := 0
		if buttons&tcell.WheelUp != 0 {
			wheel = -3
			ev.Button = govt.MouseWheel
			ev.Action = govt.MouseUp
		}
		if buttons&tcell.WheelDown != 0 {
			wheel = 3
			ev.Button = govt.MouseWheel
			ev.Action = govt.MouseDown
		}
		if wheel == 0 {
			switch {
			case buttons&tcell.Button2 != 0:
				ev.Button = govt.MouseRight
			case buttons&tcell.Button3 != 0:
				ev.Button = govt.MouseMiddle
			case buttons == tcell.ButtonNone:
				ev.Button = govt.MouseNone
			}
			if previous == tcell.ButtonNone && buttons != tcell.ButtonNone {
				ev.Action = govt.MouseDown
			} else if previous != tcell.ButtonNone && buttons == tcell.ButtonNone {
				ev.Action = govt.MouseUp
				switch {
				case previous&tcell.Button2 != 0:
					ev.Button = govt.MouseRight
				case previous&tcell.Button3 != 0:
					ev.Button = govt.MouseMiddle
				default:
					ev.Button = govt.MouseLeft
				}
			}
		}
		if !ev.Shift && t.emu.Mouse(ev) {
			if ev.Action == govt.MouseDown && wheel == 0 {
				copy := p
				a.mouseCapture = &copy
			}
			a.responses(p.term, t)
		} else if wheel != 0 {
			t.emu.Scroll(wheel)
		} else if down {
			row := t.emu.FrameSnapshot().YDisp + ev.Row - 1
			a.selectionRange = &localSelection{p.term, row, ev.Col - 1, row, ev.Col - 1, true}
		}
		return
	}
}

func (a *app) activateHit(h hit) {
	switch {
	case h.kind == "agent-pane":
		raw, _ := json.Marshal(selectionEffect{Pane: h.id})
		a.selection(raw)
		a.command(map[string]any{"type": "pane.focus", "pane_id": h.id})
	case h.kind == "workspace":
		a.menu = false
		a.workspace = h.id
		a.tab = uuid.Nil
		a.syncState()
		a.command(map[string]any{"type": "workspace.activate", "workspace_id": h.id})
	case h.kind == "tab":
		a.menu = false
		a.tab = h.id
		a.focus = uuid.Nil
		a.command(map[string]any{"type": "tab.activate", "tab_id": h.id})
	case h.kind == "choose-pane":
		a.menu = false
		a.focus = h.id
		a.command(map[string]any{"type": "pane.focus", "pane_id": h.id})
	case strings.HasPrefix(h.kind, "menu:"):
		a.quit = a.action([]rune(strings.TrimPrefix(h.kind, "menu:"))[0])
	}
}

// matchesShortcut normalizes control characters because terminal decoders may
// report Ctrl-W either as KeyCtrlW or as a rune with the Ctrl modifier.
func matchesShortcut(e *tcell.EventKey, spec string) bool {
	parts := strings.Split(strings.ToLower(strings.TrimSpace(spec)), "-")
	if len(parts) == 0 || parts[len(parts)-1] == "" {
		return false
	}
	var want tcell.ModMask
	for _, part := range parts[:len(parts)-1] {
		switch part {
		case "ctrl", "control":
			want |= tcell.ModCtrl
		case "alt", "option":
			want |= tcell.ModAlt
		case "shift":
			want |= tcell.ModShift
		default:
			return false
		}
	}
	name := parts[len(parts)-1]
	if name == "space" {
		name = " "
	}
	key, mods, r := e.Key(), e.Modifiers(), e.Rune()
	if want&tcell.ModCtrl != 0 && len(name) == 1 && name[0] >= 'a' && name[0] <= 'z' && key >= tcell.KeyCtrlA && key <= tcell.KeyCtrlZ {
		r = 'a' + rune(key-tcell.KeyCtrlA)
		key = tcell.KeyRune
		mods |= tcell.ModCtrl
	}
	if mods != want {
		return false
	}
	if key == tcell.KeyRune {
		return strings.ToLower(string(r)) == name
	}
	switch name {
	case "enter", "return":
		return key == tcell.KeyEnter
	case "escape", "esc":
		return key == tcell.KeyEscape
	case "tab":
		return key == tcell.KeyTab
	case "left":
		return key == tcell.KeyLeft
	case "right":
		return key == tcell.KeyRight
	case "up":
		return key == tcell.KeyUp
	case "down":
		return key == tcell.KeyDown
	}
	if key >= tcell.KeyF1 && key <= tcell.KeyF12 {
		return name == fmt.Sprintf("f%d", key-tcell.KeyF1+1)
	}
	return false
}

func (a *app) requestCloseWorkspace() {
	a.menu = false
	a.prefix = false
	for _, ws := range a.state.Workspaces {
		if ws.ID != a.workspace {
			continue
		}
		if len(ws.Tabs) == 0 {
			a.command(map[string]any{"type": "workspace.close", "workspace_id": ws.ID})
		} else {
			a.confirmWorkspace = ws.ID
			a.promptKind = "confirm W (yes)"
			a.prompt = ""
		}
		return
	}
}
