package goui

import (
	"gioui.org/widget"
	"github.com/google/uuid"
	"github.com/hajimehoshi/ebiten/v2"
	"image"
	"strings"
)

type nativeRename struct {
	kind    string
	id      uuid.UUID
	editor  widget.Editor
	message string
}

func (w *EbitengineWindow) SetNewWindowHandler(handler func() error) { w.newWindow = handler }
func (w *EbitengineWindow) startNewWindow() {
	if w.newWindow == nil || w.windowResult != nil {
		return
	}
	result := make(chan error, 1)
	w.windowResult = result
	go func() { result <- w.newWindow() }()
}
func (w *EbitengineWindow) openRename(c *WorkspaceClient, kind string, id uuid.UUID) {
	c.mu.RLock()
	state := c.state
	c.mu.RUnlock()
	title := ""
	for _, ws := range state.Workspaces {
		if kind == "workspace" && ws.ID == id {
			title = ws.Title
		}
		for _, tab := range ws.Tabs {
			if kind == "tab" && tab.ID == id {
				title = tab.Title
			}
		}
	}
	r := &nativeRename{kind: kind, id: id}
	r.editor.SingleLine = true
	r.editor.SetText(title)
	r.editor.SetCaret(0, len([]rune(title)))
	w.view(c).rename = r
}
func (w *EbitengineWindow) submitRename(c *WorkspaceClient) {
	r := w.view(c).rename
	if r == nil {
		return
	}
	title := strings.TrimSpace(r.editor.Text())
	if title == "" {
		r.message = c.tr("Enter a name")
		return
	}
	if err := c.session.DispatchAsync(map[string]any{"type": r.kind + ".rename", r.kind + "_id": r.id, "title": title}); err != nil {
		r.message = err.Error()
		return
	}
	w.view(c).rename = nil
}
func (w *EbitengineWindow) drawRename(c *WorkspaceClient, dst *ebiten.Image) {
	rename := w.view(c).rename
	r := w.overlay(c, dst, w.dp(400), w.dp(190))
	cfg := c.currentConfig()
	fg := configColor(cfg.Theme.UIForeground, 0xe6eaea)
	w.label(dst, c, image.Rect(r.Min.X+w.dp(24), r.Min.Y+w.dp(20), r.Max.X-w.dp(24), r.Min.Y+w.dp(44)), c.tr("Rename "+rename.kind), 16, fg, true)
	field := image.Rect(r.Min.X+w.dp(24), r.Min.Y+w.dp(60), r.Max.X-w.dp(24), r.Min.Y+w.dp(94))
	w.field(c, dst, field, rename.editor.Text(), true, &rename.editor)
	w.hit(c, field, hitRenameField, rename.id, "rename-field")
	w.label(dst, c, image.Rect(field.Min.X, field.Max.Y+w.dp(4), field.Max.X, field.Max.Y+w.dp(24)), rename.message, 10, configColor("#ef766f", 0), false)
	for i, item := range []struct {
		title string
		kind  automationHitKind
	}{{"Cancel", hitRenameCancel}, {"Rename", hitRenameSave}} {
		br := image.Rect(r.Max.X-w.dp(204)+i*w.dp(96), r.Max.Y-w.dp(50), r.Max.X-w.dp(116)+i*w.dp(96), r.Max.Y-w.dp(20))
		w.button(dst, c, br, item.title, item.kind, uuid.Nil, i == 1)
	}
}
func (w *EbitengineWindow) cycleWorkspaceAcrossHosts(delta int) bool {
	type target struct {
		host      uuid.UUID
		view      *WorkspaceClient
		workspace uuid.UUID
	}
	var targets []target
	current := -1
	active := w.multi.ActiveConnectionID()
	for _, host := range w.hosts() {
		for _, ws := range host.state.Workspaces {
			if host.entry.ID == active && host.state.ActiveWorkspace != nil && *host.state.ActiveWorkspace == ws.ID {
				current = len(targets)
			}
			targets = append(targets, target{host.entry.ID, host.view, ws.ID})
		}
	}
	if current < 0 || len(targets) == 0 {
		return false
	}
	next := targets[(current+delta+len(targets))%len(targets)]
	w.multi.ActivateConnection(next.host)
	_ = next.view.session.DispatchAsync(map[string]any{"type": "workspace.activate", "workspace_id": next.workspace})
	return true
}
