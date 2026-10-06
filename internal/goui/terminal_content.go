package goui

import (
	"encoding/json"
	"errors"

	"github.com/SurTeam/Water/internal/govt"
	"github.com/google/uuid"
)

type terminalContentResponse struct {
	govt.TextContent
	Source     string    `json:"source"`
	WindowID   uuid.UUID `json:"window_id"`
	PaneID     uuid.UUID `json:"pane_id"`
	TerminalID uuid.UUID `json:"terminal_id"`
	LastSeq    uint64    `json:"last_seq"`
	Selection  Selection `json:"selection"`
}

// readTerminalContent runs on the owning session worker, outside native Update.
// It observes the client's emulator, including its clear policy and viewport.
func (c *WorkspaceClient) readTerminalContent(params json.RawMessage) (any, error) {
	var p struct {
		PaneID   uuid.UUID `json:"pane_id"`
		StartRow *int      `json:"start_row"`
		Rows     *int      `json:"rows"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}
	if p.PaneID == uuid.Nil {
		return nil, errors.New("ui content requires a pane UUID")
	}
	c.mu.RLock()
	var term *terminalClient
	for _, workspace := range c.state.Workspaces {
		for _, tab := range workspace.Tabs {
			var root paneTree
			if json.Unmarshal(tab.Tree, &root) == nil {
				if id, ok := terminalForPane(&root, p.PaneID); ok {
					term = c.terminals[id]
					break
				}
			}
		}
		if term != nil {
			break
		}
	}
	c.mu.RUnlock()
	if term == nil {
		return nil, errors.New("pane has no attached terminal in this connection/window")
	}
	term.parseMu.Lock()
	defer term.parseMu.Unlock()
	term.mu.RLock()
	defer term.mu.RUnlock()
	if term.emu == nil {
		return nil, errors.New("terminal is closed")
	}
	content, err := term.emu.ReadContent(p.StartRow, p.Rows)
	if err != nil {
		return nil, err
	}
	return terminalContentResponse{
		TextContent: content, Source: "gui-client", WindowID: c.session.WindowID,
		PaneID: p.PaneID, TerminalID: term.id, LastSeq: term.lastSeq,
		Selection: term.selection,
	}, nil
}
