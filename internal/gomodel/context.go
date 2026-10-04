package gomodel

import "github.com/google/uuid"

func (m *Model) WorkspaceActivePane(workspaceID uuid.UUID) (uuid.UUID, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	w := m.workspaces[workspaceID]
	if w == nil || w.ActiveTab == nil {
		return uuid.Nil, false
	}
	tab := m.tabs[*w.ActiveTab]
	if tab == nil {
		return uuid.Nil, false
	}
	return tab.ActivePane, tab.ActivePane != uuid.Nil
}

func (m *Model) PaneCWD(paneID uuid.UUID) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	pane := m.panes[paneID]
	if pane == nil || pane.Terminal == nil {
		return ""
	}
	return pane.Terminal.CWD
}

func (m *Model) WorkspaceActiveCWD(workspaceID uuid.UUID) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	workspace := m.workspaces[workspaceID]
	if workspace == nil {
		return ""
	}

	cwdForTab := func(tabID uuid.UUID) string {
		tab := m.tabs[tabID]
		if tab == nil {
			return ""
		}
		if pane := m.panes[tab.ActivePane]; pane != nil && pane.Terminal != nil && pane.Terminal.CWD != "" {
			return pane.Terminal.CWD
		}
		var paneIDs []uuid.UUID
		tab.Root.LeafIDs(&paneIDs)
		for _, paneID := range paneIDs {
			if pane := m.panes[paneID]; pane != nil && pane.Terminal != nil && pane.Terminal.CWD != "" {
				return pane.Terminal.CWD
			}
		}
		return ""
	}

	if workspace.ActiveTab != nil {
		if cwd := cwdForTab(*workspace.ActiveTab); cwd != "" {
			return cwd
		}
	}
	for i := len(workspace.Tabs) - 1; i >= 0; i-- {
		if cwd := cwdForTab(workspace.Tabs[i]); cwd != "" {
			return cwd
		}
	}
	return ""
}

func (m *Model) PaneWorkspace(paneID uuid.UUID) (uuid.UUID, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	tabID := m.tabIDForPaneLocked(paneID)
	if tabID == uuid.Nil {
		return uuid.Nil, false
	}
	workspaceID := m.workspaceIDForTabLocked(tabID)
	return workspaceID, workspaceID != uuid.Nil
}
