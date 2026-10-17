package goui

import (
	"image"
	"strings"

	"github.com/google/uuid"
	"github.com/hajimehoshi/ebiten/v2"
)

// Owned by layoutMu, like settings and other native modal state. The target
// remains fixed even if a newer snapshot changes the active workspace.
type workspaceClosePrompt struct {
	ID    uuid.UUID
	Title string
}

func workspaceCloseCommand(id uuid.UUID) map[string]any {
	return map[string]any{"type": "workspace.close", "workspace_id": id}
}

func (c *WorkspaceClient) requestWorkspaceClose(id uuid.UUID) {
	if c.workspaceClose != nil {
		return
	}
	c.mu.RLock()
	var prompt *workspaceClosePrompt
	tabs := 0
	for _, ws := range c.state.Workspaces {
		if ws.ID == id {
			prompt = &workspaceClosePrompt{ID: id, Title: ws.Title}
			tabs = len(ws.Tabs)
			break
		}
	}
	c.mu.RUnlock()
	if prompt == nil {
		return
	}
	if tabs == 0 {
		if c.session != nil {
			_ = c.session.DispatchAsync(workspaceCloseCommand(id))
		}
		return
	}
	c.workspaceClose = prompt
}

func (c *WorkspaceClient) confirmWorkspaceClose() {
	prompt := c.workspaceClose
	c.workspaceClose = nil
	if prompt != nil && c.session != nil {
		_ = c.session.DispatchAsync(workspaceCloseCommand(prompt.ID))
	}
}

func (c *WorkspaceClient) handleWorkspaceCloseKey(spec string) bool {
	switch strings.ToLower(spec) {
	case "escape":
		c.workspaceClose = nil
	case "enter", "return":
		c.confirmWorkspaceClose()
	}
	// Consume every key while the destructive confirmation is visible.
	return true
}

func (w *EbitengineWindow) drawWorkspaceClose(c *WorkspaceClient, dst *ebiten.Image) {
	p := c.workspaceClose
	if p == nil {
		return
	}
	r := w.overlay(c, dst, w.dp(500), w.dp(230))
	fg := configColor(c.currentConfig().Theme.UIForeground, 0xe6eaea)
	w.label(dst, c, image.Rect(r.Min.X+w.dp(24), r.Min.Y+w.dp(24), r.Max.X-w.dp(24), r.Max.Y), c.tr("Close workspace"), 20, fg, true)
	w.label(dst, c, image.Rect(r.Min.X+w.dp(24), r.Min.Y+w.dp(68), r.Max.X-w.dp(24), r.Max.Y), p.Title, 13, fg, false)
	w.label(dst, c, image.Rect(r.Min.X+w.dp(24), r.Min.Y+w.dp(104), r.Max.X-w.dp(24), r.Max.Y), c.tr("Close this workspace and all its tabs?"), 12, fg, false)
	w.button(dst, c, image.Rect(r.Min.X+w.dp(24), r.Max.Y-w.dp(58), r.Min.X+w.dp(220), r.Max.Y-w.dp(24)), "Close workspace", hitCloseWorkspaceConfirm, p.ID, true)
	w.button(dst, c, image.Rect(r.Min.X+w.dp(236), r.Max.Y-w.dp(58), r.Max.X-w.dp(24), r.Max.Y-w.dp(24)), "Cancel", hitCloseWorkspaceCancel, p.ID, false)
}
