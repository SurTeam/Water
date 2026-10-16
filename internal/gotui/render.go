package gotui

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"

	"github.com/SurTeam/Water/internal/gomodel"
	"github.com/SurTeam/Water/internal/goprotocol"
	"github.com/SurTeam/Water/internal/govt"
	"github.com/gdamore/tcell/v2"
	"github.com/google/uuid"
)

var base = tcell.StyleDefault.Foreground(tcell.ColorSilver).Background(tcell.ColorBlack)
var chrome = tcell.StyleDefault.Foreground(tcell.ColorWhite).Background(tcell.ColorDarkSlateGray)
var selected = chrome.Background(tcell.ColorDarkBlue)

func (a *app) text(x, y, width int, s string, style tcell.Style) {
	for _, r := range s {
		if width <= 0 {
			break
		}
		if unicode.IsControl(r) {
			r = ' '
		}
		a.screen.SetContent(x, y, r, nil, style)
		x++
		width--
	}
}
func (a *app) bar(r rect, s string, style tcell.Style) {
	for x := r.x; x < r.x+r.w; x++ {
		a.screen.SetContent(x, r.y, ' ', nil, style)
	}
	a.text(r.x, r.y, r.w, s, style)
}
func (a *app) activeTab() *gomodel.TabDump {
	for _, w := range a.state.Workspaces {
		if w.ID == a.workspace {
			for i := range w.Tabs {
				if w.Tabs[i].ID == a.tab {
					t := w.Tabs[i]
					return &t
				}
			}
		}
	}
	return nil
}
func (a *app) layout(n *tree, r rect, path []bool) {
	if n == nil || r.w < 2 || r.h < 2 {
		return
	}
	if n.Type == "leaf" {
		a.panes = append(a.panes, paneForLeaf(n, r))
		return
	}
	ratio := n.Ratio
	if ratio <= 0 || ratio >= 1 {
		ratio = .5
	}
	first, second, split := r, r, r
	if n.Axis == "horizontal" {
		v := max(2, min(r.w-3, int(float64(r.w-1)*ratio)))
		first.w = v
		second.x = r.x + v + 1
		second.w = r.w - v - 1
		split = rect{r.x + v, r.y, 1, r.h}
	} else {
		v := max(2, min(r.h-3, int(float64(r.h-1)*ratio)))
		first.h = v
		second.y = r.y + v + 1
		second.h = r.h - v - 1
		split = rect{r.x, r.y + v, r.w, 1}
	}
	a.dividers = append(a.dividers, divider{split, r, n.Axis, append([]bool{}, path...)})
	a.layout(n.First, first, append(append([]bool{}, path...), false))
	a.layout(n.Second, second, append(append([]bool{}, path...), true))
}
func (a *app) draw() {
	w, h := a.screen.Size()
	if w < 4 || h < 4 {
		return
	}
	a.screen.SetStyle(base)
	a.screen.Clear()
	a.screen.HideCursor()
	a.panes = nil
	a.hits = nil
	a.dividers = nil
	side := 0
	if w >= 90 {
		side = 22
		for y := 0; y < h-1; y++ {
			a.bar(rect{0, y, side, 1}, "", chrome)
		}
		a.text(1, 0, side-2, "Water • workspaces", chrome)
		for i, ws := range a.state.Workspaces {
			if i+2 >= h-1 {
				break
			}
			style := chrome
			if ws.ID == a.workspace {
				style = selected
			}
			a.bar(rect{0, i + 2, side, 1}, fmt.Sprintf(" %d %s", i+1, ws.Title), style)
			a.hits = append(a.hits, hit{rect{0, i + 2, side, 1}, "workspace", ws.ID})
		}
	}
	if side > 0 {
		row := len(a.state.Workspaces) + 4
		if row < h-2 {
			a.text(1, row, side-2, "Agents", chrome)
			row++
		}
		for _, agent := range a.state.Agents {
			if row >= h-1 {
				break
			}
			raw, _ := json.Marshal(agent)
			var item struct {
				Label string    `json:"label"`
				Pane  uuid.UUID `json:"pane_id"`
			}
			if json.Unmarshal(raw, &item) != nil {
				continue
			}
			a.text(1, row, side-2, item.Label, chrome)
			a.hits = append(a.hits, hit{rect{0, row, side, 1}, "agent-pane", item.Pane})
			row++
		}
	}
	x := side
	for _, ws := range a.state.Workspaces {
		if ws.ID != a.workspace {
			continue
		}
		for i, tab := range ws.Tabs {
			label := fmt.Sprintf(" %d:%s ", i+1, tab.Title)
			width := min(len([]rune(label)), max(0, w-x))
			style := chrome
			if tab.ID == a.tab {
				style = selected
			}
			a.bar(rect{x, 0, width, 1}, label, style)
			a.hits = append(a.hits, hit{rect{x, 0, width, 1}, "tab", tab.ID})
			x += width
		}
	}
	if tab := a.activeTab(); tab != nil {
		var root tree
		if json.Unmarshal(tab.Tree, &root) == nil {
			leaf := selectedLeaf(&root, a.focus, tab.ActivePane)
			if leaf != nil {
				a.focus = leaf.Pane
			}
			viewport := rect{side, 1, w - side, h - 2}
			if w < 90 {
				// Narrow mode must select from the topology, before split
				// geometry can prune deeply nested or very short leaves.
				if leaf != nil {
					a.panes = []pane{paneForLeaf(leaf, viewport)}
				}
			} else {
				a.layout(&root, viewport, nil)
			}
		}
	}
	for _, d := range a.dividers {
		for y := d.box.y; y < d.box.y+d.box.h; y++ {
			for x := d.box.x; x < d.box.x+d.box.w; x++ {
				r := '│'
				if d.axis == "vertical" {
					r = '─'
				}
				a.screen.SetContent(x, y, r, nil, chrome)
			}
		}
	}
	visible := map[uuid.UUID]bool{}
	for _, p := range a.panes {
		style := chrome
		if p.id == a.focus {
			style = selected
		}
		a.bar(rect{p.box.x, p.box.y, p.box.w, 1}, p.title, style)
		if p.term == uuid.Nil {
			continue
		}
		visible[p.term] = true
		t := a.attach(p.term)
		if t == nil || t.emu == nil {
			continue
		}
		cols, rows := max(2, p.box.w), max(1, p.box.h-1)
		a.resizeTerminal(p.term, t, cols, rows)
		s := t.emu.FrameSnapshot()
		for y, row := range s.RowsData {
			if y >= rows {
				break
			}
			for x, c := range row.Cells {
				if x >= cols {
					break
				}
				if c.Width == 0 {
					continue
				}
				runes := []rune(c.Text)
				if len(runes) == 0 {
					runes = []rune{' '}
				}
				if c.Invisible {
					runes = []rune{' '}
				}
				st := cellStyle(c)
				if sel := a.selectionRange; sel != nil && sel.term == p.term {
					sr, sc, er, ec := sel.startRow, sel.startCol, sel.endRow, sel.endCol
					if sr > er || sr == er && sc > ec {
						sr, sc, er, ec = er, ec, sr, sc
					}
					row := s.YDisp + y
					if row >= sr && row <= er && (row > sr || x >= sc) && (row < er || x <= ec) {
						st = st.Reverse(true)
					}
				}
				a.screen.SetContent(p.box.x+x, p.box.y+1+y, runes[0], runes[1:], st)
			}
		}
		if p.id == a.focus && !s.CursorHide && s.CursorX >= 0 && s.CursorX < cols && s.CursorY >= 0 && s.CursorY < rows {
			a.screen.ShowCursor(p.box.x+s.CursorX, p.box.y+1+s.CursorY)
		}
	}
	for id, t := range a.terms {
		if t.emu == nil {
			continue
		}
		if !visible[id] {
			if a.selectionRange != nil && a.selectionRange.term == id {
				a.selectionRange = nil
			}
			a.session.Detach(id)
			if t.emu != nil {
				t.emu.Close()
			}
			delete(a.terms, id)
		}
	}
	status := "Ctrl-B: commands  •  Ctrl-B q: detach"
	if a.prefix || a.menu {
		status = "n tab  w workspace  % / \" split  o pane  [/] tab  {/} workspace  x close  r rename  : command  q detach  Q stop server"
	}
	if a.status != "" {
		status = a.status
	}
	if a.promptKind != "" {
		status = a.promptKind + "> " + a.prompt
	}
	a.bar(rect{0, h - 1, w, 1}, status, chrome)
	a.hits = append(a.hits, hit{rect{0, h - 1, w, 1}, "menu:?", uuid.Nil})
	if a.menu {
		a.drawMenu(w, h)
	}
	a.screen.Show()
}
func cellStyle(c govt.Cell) tcell.Style {
	fg, bg := color(c.FG), color(c.BG)
	if c.Invisible {
		fg = bg
	}
	return tcell.StyleDefault.Foreground(fg).Background(bg).Bold(c.Bold).Dim(c.Dim).Italic(c.Italic).Underline(c.Underline).StrikeThrough(c.Strikethrough).Reverse(c.Inverse)
}
func color(c govt.Color) tcell.Color {
	switch c.Mode {
	case govt.ColorPalette:
		return tcell.PaletteColor(int(c.Value))
	case govt.ColorRGB:
		return tcell.NewHexColor(int32(c.Value))
	}
	return tcell.ColorDefault
}
func (a *app) drawMenu(w, h int) {
	a.menuItems = nil
	switch a.menuMode {
	case "panes":
		if tab := a.activeTab(); tab != nil {
			var root tree
			_ = json.Unmarshal(tab.Tree, &root)
			walk(&root, func(n *tree) {
				title := n.Pane.String()[:8]
				if n.Terminal != nil && n.Terminal.Summary.ProcessName != "" {
					title = n.Terminal.Summary.ProcessName + " " + title
				}
				a.menuItems = append(a.menuItems, menuItem{title, "choose-pane", n.Pane})
			})
		}
	case "workspaces":
		for _, ws := range a.state.Workspaces {
			a.menuItems = append(a.menuItems, menuItem{ws.Title, "workspace", ws.ID})
		}
	case "tabs":
		for _, ws := range a.state.Workspaces {
			if ws.ID == a.workspace {
				for _, tab := range ws.Tabs {
					a.menuItems = append(a.menuItems, menuItem{tab.Title, "tab", tab.ID})
				}
			}
		}
	default:
		items := []string{"p  Select pane", "s  Select workspace", "T  Select tab", "n  New tab", "w  New workspace", "%  Split right", "\"  Split down", "o  Next pane", "[  Previous tab", "]  Next tab", "{  Previous workspace", "}  Next workspace", "x  Close pane (confirm)", "t  Close tab (confirm)", "W  Close workspace (confirm)", "r  Rename tab", "R  Rename workspace", ":  Dispatch JSON command", "i  Interrupt foreground Agent", "e  Exit foreground Agent", "k  Force-stop Agent (confirm)", "Q  Exit TUI and stop server (confirm)", "q  Detach (keep shells)"}
		for _, item := range items {
			a.menuItems = append(a.menuItems, menuItem{item, "menu:" + strings.TrimSpace(item)[:1], uuid.Nil})
		}
	}
	if len(a.menuItems) == 0 {
		return
	}
	a.menuIndex = max(0, min(a.menuIndex, len(a.menuItems)-1))
	width := min(w, 48)
	x := (w - width) / 2
	count := min(h-3, len(a.menuItems))
	start := max(0, min(a.menuIndex-count+1, len(a.menuItems)-count))
	y := max(1, (h-count-1)/2)
	for i := 0; i < count; i++ {
		item := a.menuItems[start+i]
		style := chrome
		if start+i == a.menuIndex {
			style = selected
		}
		a.bar(rect{x, y + i, width, 1}, " "+item.label, style)
		a.hits = append(a.hits, hit{rect{x, y + i, width, 1}, item.kind, item.id})
	}
	a.bar(rect{x, y + count, width, 1}, " ↑/↓ Enter select • Esc return", selected)
}

// selectedLeaf resolves client selection against the complete topology. The
// renderer may omit a leaf for lack of space, but that cannot erase selection.
func selectedLeaf(root *tree, focus, fallback uuid.UUID) *tree {
	var first, selected, active *tree
	walk(root, func(n *tree) {
		if first == nil {
			first = n
		}
		if n.Pane == focus {
			selected = n
		}
		if n.Pane == fallback {
			active = n
		}
	})
	if selected != nil {
		return selected
	}
	if active != nil {
		return active
	}
	return first
}
func paneForLeaf(n *tree, r rect) pane {
	p := pane{id: n.Pane, box: r, title: "Terminal"}
	if n.Terminal != nil {
		p.term = n.Terminal.Summary.ID
		p.title = n.Terminal.Summary.ProcessName
	}
	return p
}
func (a *app) resizeTerminal(id uuid.UUID, t *terminal, cols, rows int) {
	if !a.focused || t.exited {
		return
	}
	size := (goprotocol.TerminalSize{Columns: cols, Lines: rows}).Normalized()
	// Requested layout and observed PTY geometry are different state. A remote
	// resize updates observed geometry without causing a draw/resize feedback
	// loop. Reassert the layout only after a layout change or renewed focus.
	if t.requestCols == size.Columns && t.requestRows == size.Lines && t.requestGeneration == a.focusGeneration {
		return
	}
	t.requestCols, t.requestRows = size.Columns, size.Lines
	t.requestGeneration = a.focusGeneration
	a.command(map[string]any{"type": "terminal.resize", "terminal_id": id, "columns": size.Columns, "lines": size.Lines})
}
