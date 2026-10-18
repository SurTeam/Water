package goui

import (
	"fmt"
	"image"
	"reflect"
	"strings"

	"github.com/SurTeam/Water/internal/goagent"
	"github.com/google/uuid"
	"github.com/hajimehoshi/ebiten/v2"
)

func (w *EbitengineWindow) drawAgentSettings(c *WorkspaceClient, dst *ebiten.Image, r image.Rectangle, top, footer int) {
	s := &c.settings
	a := &s.agents
	if a.rulesEditor != nil {
		w.drawAgentRules(c, dst)
		return
	}
	cfg := c.currentConfig()
	fg := configColor(cfg.Theme.UIForeground, 0xe6eaea)
	muted := mixColor(configColor(cfg.Theme.ChromeBackground, 0), fg, .55)
	left := r.Min.X + w.dp(24)
	right := r.Max.X - w.dp(24)
	listWidth := w.dp(156)
	button := func(x, y, width int, title, action string, selected bool) {
		w.button(dst, c, image.Rect(x, y, x+width, y+w.dp(28)), title, hitSettingsControl, uuid.Nil, selected)
		c.hitRegions[len(c.hitRegions)-1].Label = "agent:" + action
	}
	connection := "Local"
	if c.remoteDestination != "" {
		connection = c.remoteDestination
	}
	w.label(dst, c, image.Rect(left, top, right, top+w.dp(22)), c.trf("Agent settings belong to: %s", connection), 11, fg, true)
	top += w.dp(30)
	if !a.loaded {
		w.label(dst, c, image.Rect(left, top, right, top+w.dp(36)), c.tr(a.message), 11, fg, false)
		if !a.busy {
			button(left, top+w.dp(42), w.dp(100), "Reload", "reload", false)
		}
		button(right-w.dp(90), footer+w.dp(24), w.dp(90), "Close", "close", false)
		return
	}
	profiles := goagent.Profiles(a.config)
	rows := max(1, (footer-top-w.dp(100))/w.dp(28))
	offset, end := a.agentListPage(rows)
	for i := offset; i < end; i++ {
		p := profiles[i]
		name := p.Name
		if !p.Enabled {
			name = "○ " + name
		}
		button(left, top+(i-offset)*w.dp(28), listWidth, name, "select:"+string(p.ID), p.ID == a.selected)
	}
	bottom := footer - w.dp(88)
	button(left, bottom, listWidth/2-w.dp(2), "Previous page", "previous", false)
	button(left+listWidth/2, bottom, listWidth/2, "Next page", "next", false)
	button(left, bottom+w.dp(32), listWidth/2-w.dp(2), "New", "new", false)
	button(left+listWidth/2, bottom+w.dp(32), listWidth/2, "Duplicate", "duplicate", false)
	editorLeft := left + listWidth + w.dp(20)
	labelWidth := min(w.dp(200), max(w.dp(100), (right-editorLeft)*45/100))
	testY := footer - w.dp(74)
	visible := max(1, (testY-w.dp(42)-top)/w.dp(30))
	var fields []int
	for i, f := range s.fields {
		if f.group == "Agents" {
			fields = append(fields, i)
		}
	}
	view := w.view(c)
	view.settingsScroll = min(max(0, view.settingsScroll), max(0, len(fields)-visible))
	y := top
	for row := view.settingsScroll; row < len(fields) && row < view.settingsScroll+visible; row++ {
		i := fields[row]
		f := &s.fields[i]
		fr := image.Rect(editorLeft+labelWidth, y, right, y+w.dp(29))
		w.label(dst, c, image.Rect(editorLeft, y+w.dp(6), fr.Min.X-w.dp(8), fr.Max.Y), c.tr(f.label), 10, fg, false)
		if f.kind == reflect.Bool {
			w.drawSettingsToggle(c, dst, fr, f.toggle.Value)
			w.hit(c, fr, hitSettingsControl, uuid.Nil, "field:Agents."+f.name)
		} else if f.name == "Rules" {
			button(fr.Min.X, y, fr.Dx(), "Edit recognition rules…", "edit-rules", false)
		} else if choices := sidebarSettingChoices(*f); len(choices) > 0 {
			// Presets are cycled by a click; the full choice is shown in the field.
			w.button(dst, c, fr, f.editor.Text(), hitSettingsControl, uuid.Nil, false)
			c.hitRegions[len(c.hitRegions)-1].Label = "agent:cycle:" + f.name
		} else {
			w.field(c, dst, fr, f.editor.Text(), s.focus == i, &f.editor)
			w.hit(c, fr, hitSettingsControl, uuid.Nil, "field:Agents."+f.name)
		}
		y += w.dp(30)
	}
	y = testY - w.dp(42)
	w.label(dst, c, image.Rect(editorLeft, y, right, y+w.dp(20)), c.tr("Edit recognition rules to choose which programs match this Agent."), 9, muted, false)
	y += w.dp(20)
	w.label(dst, c, image.Rect(editorLeft, y, right, y+w.dp(20)), c.tr("When several Agents match, the higher recognition priority wins."), 9, muted, false)
	y += w.dp(22)
	button(editorLeft, y, w.dp(120), "Test recognition", "test", false)
	message := a.message
	if s.confirmDiscard {
		message = s.message
	}
	if a.diagnostic != nil {
		d := a.diagnostic
		details := fmt.Sprintf("PID %d · %s · %s", d.Process.PID, d.Process.Name, strings.Join(d.Process.Args, " "))
		w.label(dst, c, image.Rect(editorLeft, y+w.dp(34), right, y+w.dp(54)), details, 9, muted, false)
		var matches []string
		for _, m := range d.Matches {
			matches = append(matches, fmt.Sprintf("%s (priority %d, rule %d)", m.Name, m.Priority, m.Rule+1))
		}
		w.label(dst, c, image.Rect(editorLeft, y+w.dp(54), right, y+w.dp(74)), strings.Join(matches, "; "), 9, muted, false)
	}
	w.label(dst, c, image.Rect(left, footer+w.dp(2), right, footer+w.dp(24)), c.tr(message), 10, fg, false)
	if goagent.IsBuiltin(a.selected) {
		button(left, footer+w.dp(30), w.dp(130), "Restore defaults", "restore", false)
	}
	if !goagent.IsBuiltin(a.selected) {
		button(left+w.dp(138), footer+w.dp(30), w.dp(80), "Delete", "delete", false)
	}
	button(right-w.dp(190), footer+w.dp(30), w.dp(90), "Save", "save", true)
	title := "Cancel"
	if s.confirmDiscard {
		title = "Discard changes"
	}
	button(right-w.dp(90), footer+w.dp(30), w.dp(90), title, "close", false)
}
