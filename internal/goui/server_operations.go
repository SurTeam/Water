package goui

import (
	"errors"
	"maps"
	"time"

	"github.com/SurTeam/Water/internal/goclient"
	"github.com/SurTeam/Water/internal/gomodel"
	"github.com/SurTeam/Water/internal/goprotocol"
	"github.com/google/uuid"
)

func (m *MultiWorkspaceClient) serverOperationBusy() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, connection := range m.connections {
		if connection.busy {
			return true
		}
	}
	return false
}

func (m *MultiWorkspaceClient) SetServerOperator(operator func(ConnectionEntry, *goclient.Session, string) (ConnectionEntry, *WorkspaceClient, func(), string, error)) {
	m.mu.Lock()
	m.serverOperator = operator
	m.syncSwitchersLocked()
	m.mu.Unlock()
}
func (m *MultiWorkspaceClient) ProjectRecoveryLayout(id uuid.UUID, d *gomodel.RecoveryLayout) {
	m.mu.RLock()
	connection := m.connections[id]
	m.mu.RUnlock()
	if connection == nil {
		return
	}
	c := connection.view
	c.mu.RLock()
	defer c.mu.RUnlock()
	var contains func(*gomodel.RecoveryNode, uuid.UUID) bool
	contains = func(n *gomodel.RecoveryNode, p uuid.UUID) bool {
		if n == nil {
			return false
		}
		return n.PaneID == p || contains(n.First, p) || contains(n.Second, p)
	}
	for wi := range d.Workspaces {
		w := &d.Workspaces[wi]
		if c.selection.workspace == w.ID {
			id := w.ID
			d.ActiveWorkspace = &id
		}
		for ti := range w.Tabs {
			tab := &w.Tabs[ti]
			if c.selection.tabs[w.ID] == tab.ID {
				id := tab.ID
				w.ActiveTab = &id
			}
			if pane := c.selection.panes[tab.ID]; pane != uuid.Nil && contains(tab.Tree, pane) {
				tab.ActivePane = pane
			}
		}
	}
}

func (m *MultiWorkspaceClient) ConnectionSession(id uuid.UUID) *goclient.Session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if c := m.connections[id]; c != nil {
		return c.view.session
	}
	return nil
}
func (m *MultiWorkspaceClient) serverOperation(id uuid.UUID, action string) (goprotocol.ServerInfo, string, error) {
	m.mu.Lock()
	old := m.connections[id]
	operator := m.serverOperator
	if old == nil || m.closed || old.busy {
		m.mu.Unlock()
		return goprotocol.ServerInfo{}, "", errors.New("connection is unavailable or busy")
	}
	old.busy = true
	if action == "restart" || action == "restore" {
		old.entry.Status = "restarting"
	}
	m.syncSwitchersLocked()
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		if m.connections[id] == old {
			old.busy = false
			if old.entry.Status == "restarting" {
				old.entry.Status = "connected"
				if old.view.session.Err() != nil {
					old.entry.Status = "disconnected"
				}
			}
			m.syncSwitchersLocked()
		}
		m.mu.Unlock()
		if m.invalidate != nil {
			m.invalidate()
		}
	}()
	if action == "inspect" {
		var info goprotocol.ServerInfo
		err := old.view.session.CallTimeout("server.inspect", nil, &info, time.Second)
		if err != nil {
			err = old.view.session.CallTimeout("server.info", nil, &info, time.Second)
		}
		return info, "", err
	}
	if operator == nil {
		return goprotocol.ServerInfo{}, "", errors.New("server operations are unavailable")
	}
	entry, view, closeFn, path, err := operator(old.entry, old.view.session, action)
	if err == nil && view != nil {
		err = view.Bootstrap()
	}
	if err != nil {
		if view != nil {
			view.Close()
		}
		if closeFn != nil {
			closeFn()
		}
		return goprotocol.ServerInfo{}, path, err
	}
	if view == nil {
		return old.view.ServerPanelState().Server, path, nil
	}
	// Preserve per-window selection and settings; recovered topology retains IDs.
	old.view.layoutMu.Lock()
	view.layoutMu.Lock()
	view.settings = old.view.settings
	old.view.settings = settingsPanel{}
	view.settings.serverConfirm = false
	view.layoutMu.Unlock()
	old.view.layoutMu.Unlock()
	old.view.mu.RLock()
	selection := old.view.selection
	selection.tabs = maps.Clone(selection.tabs)
	selection.panes = maps.Clone(selection.panes)
	if selection.pending != nil {
		value := *selection.pending
		selection.pending = &value
	}
	old.view.mu.RUnlock()
	view.mu.Lock()
	view.selection = selection
	view.state = view.selection.project(view.state)
	view.mu.Unlock()
	view.server.mu.Lock()
	view.server.recoveryPath = path
	view.server.message = "Server restarted and layout restored"
	view.server.mu.Unlock()
	entry.ID = id
	entry.Status = "connected"
	view.connectionMu.Lock()
	view.onDisconnected = func(error) { m.markViewDisconnected(id, view) }
	view.connectionMu.Unlock()
	m.mu.Lock()
	if m.closed || m.connections[id] != old {
		m.mu.Unlock()
		view.Close()
		if closeFn != nil {
			closeFn()
		}
		return goprotocol.ServerInfo{}, path, errors.New("connection closed during restart")
	}
	m.connections[id] = &managedWorkspaceClient{entry: entry, view: view, close: closeFn}
	m.syncSwitchersLocked()
	running := m.running
	m.mu.Unlock()
	old.view.connectionMu.Lock()
	old.view.onDisconnected = nil
	old.view.connectionMu.Unlock()
	old.view.Close()
	if old.close != nil {
		old.close()
	}
	if running {
		go view.Run()
	}
	if m.invalidate != nil {
		m.invalidate()
	}
	return view.ServerPanelState().Server, path, nil
}
