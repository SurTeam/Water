package goui

import (
	"encoding/json"
	"github.com/SurTeam/Water/internal/gomodel"
	"github.com/google/uuid"
)

// Window selection is a client projection, independent of the shared model's
// command defaults. Terminal contents and pane topology remain server-owned.
type windowSelection struct {
	workspace uuid.UUID
	tabs      map[uuid.UUID]uuid.UUID
	panes     map[uuid.UUID]uuid.UUID
	pending   *selectionEffect
}

type selectionEffect struct {
	Workspace uuid.UUID `json:"workspace_id"`
	Tab       uuid.UUID `json:"tab_id"`
	Pane      uuid.UUID `json:"pane_id"`
}

func (c *WorkspaceClient) applySelection(raw json.RawMessage) {
	var effect selectionEffect
	if json.Unmarshal(raw, &effect) != nil {
		return
	}
	c.mu.Lock()
	c.selection.pending = &effect
	c.state = c.selection.project(c.state)
	c.mu.Unlock()
	if c.invalidate != nil {
		c.invalidate()
	}
}

func treeHasPane(node *paneTree, id uuid.UUID) bool {
	return node != nil && (node.Type == "leaf" && node.PaneID == id || treeHasPane(node.First, id) || treeHasPane(node.Second, id))
}

func firstTreePane(node *paneTree) uuid.UUID {
	if node == nil {
		return uuid.Nil
	}
	if node.Type == "leaf" {
		return node.PaneID
	}
	if id := firstTreePane(node.First); id != uuid.Nil {
		return id
	}
	return firstTreePane(node.Second)
}

func (s *windowSelection) project(state gomodel.StateDump) gomodel.StateDump {
	if s.tabs == nil {
		s.tabs = make(map[uuid.UUID]uuid.UUID)
		s.panes = make(map[uuid.UUID]uuid.UUID)
	}
	// Clone containers before projecting; frames can retain the previous state.
	state.Workspaces = append([]gomodel.WorkspaceDump(nil), state.Workspaces...)
	for wi := range state.Workspaces {
		w := &state.Workspaces[wi]
		w.Tabs = append([]gomodel.TabDump(nil), w.Tabs...)
		if p := s.pending; p != nil && p.Workspace == w.ID {
			s.workspace = w.ID
			s.pending = nil
		}
		for ti := range w.Tabs {
			t := &w.Tabs[ti]
			var tree paneTree
			_ = json.Unmarshal(t.Tree, &tree)
			if p := s.pending; p != nil && (p.Tab == t.ID || p.Pane != uuid.Nil && treeHasPane(&tree, p.Pane)) {
				s.workspace, s.tabs[w.ID] = w.ID, t.ID
				if p.Pane != uuid.Nil {
					s.panes[t.ID] = p.Pane
				} else {
					s.panes[t.ID] = t.ActivePane
				}
				s.pending = nil
			}
			pane := s.panes[t.ID]
			if !treeHasPane(&tree, pane) {
				pane = t.ActivePane
				if !treeHasPane(&tree, pane) {
					pane = firstTreePane(&tree)
				}
			}
			s.panes[t.ID], t.ActivePane = pane, pane
		}
		tab := s.tabs[w.ID]
		found := false
		for _, t := range w.Tabs {
			if t.ID == tab {
				found = true
			}
		}
		if !found {
			if w.ActiveTab != nil {
				tab = *w.ActiveTab
			}
			found = false
			for _, t := range w.Tabs {
				if t.ID == tab {
					found = true
				}
			}
			if !found && len(w.Tabs) > 0 {
				tab = w.Tabs[0].ID
			}
		}
		s.tabs[w.ID] = tab
		if tab != uuid.Nil {
			w.ActiveTab = &tab
		} else {
			w.ActiveTab = nil
		}
	}
	found := false
	for _, w := range state.Workspaces {
		if w.ID == s.workspace {
			found = true
		}
	}
	if !found {
		s.workspace = uuid.Nil
		if len(state.Workspaces) > 0 {
			s.workspace = state.Workspaces[0].ID
		}
		if state.ActiveWorkspace != nil {
			for _, w := range state.Workspaces {
				if w.ID == *state.ActiveWorkspace {
					s.workspace = w.ID
				}
			}
		}
	}
	validWorkspaces, validTabs := make(map[uuid.UUID]bool), make(map[uuid.UUID]bool)
	for _, w := range state.Workspaces {
		validWorkspaces[w.ID] = true
		for _, t := range w.Tabs {
			validTabs[t.ID] = true
		}
	}
	for id := range s.tabs {
		if !validWorkspaces[id] {
			delete(s.tabs, id)
		}
	}
	for id := range s.panes {
		if !validTabs[id] {
			delete(s.panes, id)
		}
	}
	state.ActiveWorkspace, state.Workspace, state.FocusedPane = nil, nil, nil
	for i := range state.Workspaces {
		w := &state.Workspaces[i]
		if w.ID != s.workspace {
			continue
		}
		id := w.ID
		state.ActiveWorkspace, state.Workspace = &id, w
		if t := activeTab(*w); t != nil {
			pane := t.ActivePane
			state.FocusedPane = &pane
		}
	}
	return state
}
