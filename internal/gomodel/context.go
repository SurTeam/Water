package gomodel

import "github.com/google/uuid"

func (m *Model) PaneCWD(paneID uuid.UUID) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	pane:=m.panes[paneID]
	if pane==nil || pane.Terminal==nil{return ""}
	return pane.Terminal.CWD
}

func (m *Model) WorkspaceActiveCWD(workspaceID uuid.UUID) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	workspace:=m.workspaces[workspaceID]
	if workspace==nil || workspace.ActiveTab==nil{return ""}
	tab:=m.tabs[*workspace.ActiveTab]
	if tab==nil{return ""}
	pane:=m.panes[tab.ActivePane]
	if pane==nil || pane.Terminal==nil{return ""}
	return pane.Terminal.CWD
}

func (m *Model) PaneWorkspace(paneID uuid.UUID)(uuid.UUID,bool){
	m.mu.RLock()
	defer m.mu.RUnlock()
	tabID:=m.tabIDForPaneLocked(paneID)
	if tabID==uuid.Nil{return uuid.Nil,false}
	workspaceID:=m.workspaceIDForTabLocked(tabID)
	return workspaceID,workspaceID!=uuid.Nil
}
