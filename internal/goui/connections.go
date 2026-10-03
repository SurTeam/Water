package goui

import (
	"errors"
	"sync"

	"gioui.org/layout"
	"gioui.org/widget/material"

	"github.com/google/uuid"
)

type managedWorkspaceClient struct {
	entry ConnectionEntry
	view  *WorkspaceClient
	close func()
}

type MultiWorkspaceClient struct {
	mu              sync.RWMutex
	invalidate      func()
	order           []uuid.UUID
	connections     map[uuid.UUID]*managedWorkspaceClient
	active          uuid.UUID
	closed          bool
	bootstrapped    bool
	running         bool
	remoteConnector func(string) (ConnectionEntry, *WorkspaceClient, func(), error)
}

func NewMultiWorkspaceClient(invalidate func()) *MultiWorkspaceClient {
	return &MultiWorkspaceClient{
		invalidate:  invalidate,
		connections: make(map[uuid.UUID]*managedWorkspaceClient),
	}
}

func (m *MultiWorkspaceClient) AddConnection(entry ConnectionEntry, view *WorkspaceClient, closeFn func(), activate bool) error {
	if view == nil {
		return errors.New("connection view is required")
	}
	if entry.ID == uuid.Nil {
		entry.ID = uuid.New()
	}
	if entry.Name == "" {
		entry.Name = "Connection"
	}
	if entry.Kind == "" {
		entry.Kind = "remote"
	}
	if entry.Status == "" {
		entry.Status = "connected"
	}

	m.mu.RLock()
	if m.closed {
		m.mu.RUnlock()
		if closeFn != nil {
			closeFn()
		}
		return errors.New("connection manager is closed")
	}
	_, duplicate := m.connections[entry.ID]
	bootstrapped := m.bootstrapped
	running := m.running
	m.mu.RUnlock()
	if duplicate {
		if closeFn != nil {
			closeFn()
		}
		return errors.New("connection ID already exists")
	}
	if bootstrapped {
		if err := view.Bootstrap(); err != nil {
			if closeFn != nil {
				closeFn()
			}
			return err
		}
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		view.Close()
		if closeFn != nil {
			closeFn()
		}
		return errors.New("connection manager is closed")
	}
	if _, exists := m.connections[entry.ID]; exists {
		m.mu.Unlock()
		view.Close()
		if closeFn != nil {
			closeFn()
		}
		return errors.New("connection ID already exists")
	}
	m.connections[entry.ID] = &managedWorkspaceClient{entry: entry, view: view, close: closeFn}
	view.connectionMu.Lock()
	view.onDisconnected = func(error) { m.markDisconnected(entry.ID) }
	view.connectionMu.Unlock()
	m.order = append(m.order, entry.ID)
	if m.active == uuid.Nil || activate {
		m.active = entry.ID
	}
	m.syncSwitchersLocked()
	m.mu.Unlock()
	if running {
		go view.Run()
	}
	if m.invalidate != nil {
		m.invalidate()
	}
	return nil
}

func (m *MultiWorkspaceClient) markDisconnected(id uuid.UUID) {
	m.mu.Lock()
	if connection := m.connections[id]; connection != nil {
		connection.entry.Status = "disconnected"
		m.syncSwitchersLocked()
	}
	m.mu.Unlock()
	if m.invalidate != nil {
		m.invalidate()
	}
}

func (m *MultiWorkspaceClient) SetRemoteConnector(connector func(string) (ConnectionEntry, *WorkspaceClient, func(), error)) {
	m.mu.Lock()
	m.remoteConnector = connector
	m.syncSwitchersLocked()
	m.mu.Unlock()
	if m.invalidate != nil {
		m.invalidate()
	}
}

func (m *MultiWorkspaceClient) ConnectRemote(destination string) error {
	m.mu.RLock()
	for _, connection := range m.connections {
		if connection.entry.Kind == "remote" && connection.entry.Destination == destination {
			id := connection.entry.ID
			m.mu.RUnlock()
			m.ActivateConnection(id)
			return nil
		}
	}
	connector := m.remoteConnector
	m.mu.RUnlock()
	if connector == nil {
		return errors.New("remote connector is unavailable")
	}
	entry, view, closeFn, err := connector(destination)
	if err != nil {
		return err
	}
	return m.AddConnection(entry, view, closeFn, true)
}

func (m *MultiWorkspaceClient) RemoveConnection(id uuid.UUID) bool {
	m.mu.Lock()
	connection, ok := m.connections[id]
	if !ok {
		m.mu.Unlock()
		return false
	}
	delete(m.connections, id)
	for index, candidate := range m.order {
		if candidate == id {
			m.order = append(m.order[:index], m.order[index+1:]...)
			break
		}
	}
	if m.active == id {
		m.active = uuid.Nil
		if len(m.order) > 0 {
			m.active = m.order[0]
		}
	}
	m.syncSwitchersLocked()
	m.mu.Unlock()

	connection.view.Close()
	if connection.close != nil {
		connection.close()
	}
	if m.invalidate != nil {
		m.invalidate()
	}
	return true
}

func (m *MultiWorkspaceClient) ActivateConnection(id uuid.UUID) bool {
	m.mu.Lock()
	if _, ok := m.connections[id]; !ok {
		m.mu.Unlock()
		return false
	}
	changed := m.active != id
	m.active = id
	m.syncSwitchersLocked()
	m.mu.Unlock()
	if changed && m.invalidate != nil {
		m.invalidate()
	}
	return true
}

func (m *MultiWorkspaceClient) ActiveConnectionID() uuid.UUID {
	m.mu.RLock()
	id := m.active
	m.mu.RUnlock()
	return id
}

func (m *MultiWorkspaceClient) ConnectionEntries() []ConnectionEntry {
	m.mu.RLock()
	entries := m.entriesLocked()
	m.mu.RUnlock()
	return entries
}

func (m *MultiWorkspaceClient) Bootstrap() error {
	m.mu.RLock()
	if m.bootstrapped {
		m.mu.RUnlock()
		return nil
	}
	views := make([]*WorkspaceClient, 0, len(m.order))
	for _, id := range m.order {
		if connection := m.connections[id]; connection != nil {
			views = append(views, connection.view)
		}
	}
	m.mu.RUnlock()
	for _, view := range views {
		if err := view.Bootstrap(); err != nil {
			return err
		}
	}
	m.mu.Lock()
	m.bootstrapped = true
	m.mu.Unlock()
	return nil
}

func (m *MultiWorkspaceClient) Run() {
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return
	}
	m.running = true
	views := make([]*WorkspaceClient, 0, len(m.order))
	for _, id := range m.order {
		if connection := m.connections[id]; connection != nil {
			views = append(views, connection.view)
		}
	}
	m.mu.Unlock()
	for _, view := range views {
		go view.Run()
	}
}

func (m *MultiWorkspaceClient) Layout(gtx layout.Context, th *material.Theme) layout.Dimensions {
	m.mu.RLock()
	connection := m.connections[m.active]
	m.mu.RUnlock()
	if connection == nil {
		return layout.Dimensions{}
	}
	return connection.view.Layout(gtx, th)
}

func (m *MultiWorkspaceClient) Close() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	connections := make([]*managedWorkspaceClient, 0, len(m.order))
	for index := len(m.order) - 1; index >= 0; index-- {
		if connection := m.connections[m.order[index]]; connection != nil {
			connections = append(connections, connection)
		}
	}
	m.connections = make(map[uuid.UUID]*managedWorkspaceClient)
	m.order = nil
	m.active = uuid.Nil
	m.mu.Unlock()

	for _, connection := range connections {
		connection.view.Close()
		if connection.close != nil {
			connection.close()
		}
	}
}

func (m *MultiWorkspaceClient) entriesLocked() []ConnectionEntry {
	entries := make([]ConnectionEntry, 0, len(m.order))
	for _, id := range m.order {
		if connection := m.connections[id]; connection != nil {
			entries = append(entries, connection.entry)
		}
	}
	return entries
}

func (m *MultiWorkspaceClient) syncSwitchersLocked() {
	entries := m.entriesLocked()
	active := m.active
	activate := func(id uuid.UUID) { _ = m.ActivateConnection(id) }
	var connect func(string) error
	if m.remoteConnector != nil {
		connect = func(destination string) error { return m.ConnectRemote(destination) }
	}
	remove := func(id uuid.UUID) bool { return m.RemoveConnection(id) }
	for _, connection := range m.connections {
		connection.view.SetConnectionSwitcher(active, entries, activate)
		connection.view.SetConnectionActions(connect, remove)
	}
}
