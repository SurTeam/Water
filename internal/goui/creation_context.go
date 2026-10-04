package goui

import (
	"net/url"
	"path/filepath"
)

// Carry the active pane's standard OSC 7 directory through AppCommand. The
// server prefers live process CWD, with OSC 7 as the portable fallback; an old
// OSC report must not undo a later cd in a shell without directory integration.
func (c *WorkspaceClient) creationCommand(kind string) map[string]any {
	command := map[string]any{"type": kind}
	if kind != "tab.new" && kind != "pane.split" {
		return command
	}
	id, ok := c.activeTerminalID()
	if !ok {
		return command
	}
	c.mu.RLock()
	term := c.terminals[id]
	c.mu.RUnlock()
	if term == nil {
		return command
	}
	term.mu.RLock()
	uri := term.snapshot.WorkingDirectoryURI
	term.mu.RUnlock()
	if u, err := url.Parse(uri); err == nil && u.Scheme == "file" && filepath.IsAbs(u.Path) {
		command["cwd"] = u.Path
	}
	return command
}
