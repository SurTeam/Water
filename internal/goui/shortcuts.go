package goui

import (
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/layout"
	"strconv"
	"strings"
)

func numberedTabBindings(template string) []string {
	if strings.Count(template, "#") != 1 || !strings.HasSuffix(template, "-#") {
		return nil
	}
	out := make([]string, 10)
	for i := range out {
		out[i] = strings.Replace(template, "#", strconv.Itoa((i+1)%10), 1)
		ev, ok := automationInputEvent(out[i])
		if _, isKey := ev.(key.Event); !ok || !isKey {
			return nil
		}
	}
	return out
}

func (c *WorkspaceClient) windowShortcutBindings() []string {
	s := c.currentConfig().Shortcuts
	return append([]string{s.OpenSettings, s.SplitRight, s.SplitDown, s.NextTab, s.PreviousTab, s.NextWorkspace, s.PreviousWorkspace, s.ToggleSidebar, s.NewTerminalTab, s.NewWorkspace, s.ClosePane, s.PromotePaneToTab, s.FocusLeft, s.FocusRight, s.FocusUp, s.FocusDown}, numberedTabBindings(s.SwitchTab)...)
}

func (c *WorkspaceClient) activateTabAt(index int) bool {
	c.mu.RLock()
	state := c.state
	c.mu.RUnlock()
	workspace := activeWorkspace(state)
	if workspace == nil || index < 0 || index >= len(workspace.Tabs) {
		return false
	}
	_ = c.session.DispatchAsync(map[string]any{"type": "tab.activate", "tab_id": workspace.Tabs[index].ID})
	return true
}

func (c *WorkspaceClient) cycleWorkspace(delta int) bool {
	c.mu.RLock()
	state := c.state
	c.mu.RUnlock()
	if state.ActiveWorkspace == nil || len(state.Workspaces) == 0 {
		return false
	}
	for i, w := range state.Workspaces {
		if w.ID == *state.ActiveWorkspace {
			next := (i + delta + len(state.Workspaces)) % len(state.Workspaces)
			_ = c.session.DispatchAsync(map[string]any{"type": "workspace.activate", "workspace_id": state.Workspaces[next].ID})
			return true
		}
	}
	return false
}

// Window commands remain available when there is no terminal or an editor is focused.
func (c *WorkspaceClient) processWindowShortcuts(gtx layout.Context) {
	shortcuts := c.currentConfig().Shortcuts
	bindings := []string{shortcuts.OpenSettings}
	if !c.settings.visible {
		bindings = c.windowShortcutBindings()
	}
	filters := []event.Filter{}
	for _, spec := range bindings {
		parsed, ok := automationInputEvent(spec)
		if !ok {
			continue
		}
		ev, ok := parsed.(key.Event)
		if ok {
			filters = append(filters, key.Filter{Name: ev.Name, Required: ev.Modifiers})
		}
	}
	if len(filters) == 0 {
		return
	}
	for {
		ev, ok := gtx.Event(filters...)
		if !ok {
			break
		}
		if pressed, ok := ev.(key.Event); ok && pressed.State == key.Press {
			c.dispatchWindowShortcut(pressed)
		}
	}
}
