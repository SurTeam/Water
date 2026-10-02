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
	view *WorkspaceClient
	close func()
}

type MultiWorkspaceClient struct {
	mu sync.RWMutex
	invalidate func()
	order []uuid.UUID
	connections map[uuid.UUID]*managedWorkspaceClient
	active uuid.UUID
	closed bool
}

func NewMultiWorkspaceClient(invalidate func()) *MultiWorkspaceClient {
	return &MultiWorkspaceClient{
		invalidate: invalidate,
		connections: make(map[uuid.UUID]*managedWorkspaceClient),
	}
}

func (m *MultiWorkspaceClient) AddConnection(entry ConnectionEntry, view *WorkspaceClient, closeFn func(), activate bool) error {
	if view==nil{return errors.New("connection view is required")}
	if entry.ID==uuid.Nil{entry.ID=uuid.New()}
	if entry.Name==""{entry.Name="Connection"}
	if entry.Kind==""{entry.Kind="remote"}
	if entry.Status==""{entry.Status="connected"}

	m.mu.Lock()
	if m.closed{
		m.mu.Unlock()
		if closeFn!=nil{closeFn()}
		return errors.New("connection manager is closed")
	}
	if _,exists:=m.connections[entry.ID];exists{
		m.mu.Unlock()
		if closeFn!=nil{closeFn()}
		return errors.New("connection ID already exists")
	}
	m.connections[entry.ID]=&managedWorkspaceClient{entry:entry,view:view,close:closeFn}
	m.order=append(m.order,entry.ID)
	if m.active==uuid.Nil || activate{m.active=entry.ID}
	m.syncSwitchersLocked()
	m.mu.Unlock()
	if m.invalidate!=nil{m.invalidate()}
	return nil
}

func (m *MultiWorkspaceClient) RemoveConnection(id uuid.UUID) bool {
	m.mu.Lock()
	connection,ok:=m.connections[id]
	if !ok{
		m.mu.Unlock()
		return false
	}
	delete(m.connections,id)
	for index,candidate:=range m.order{
		if candidate==id{
			m.order=append(m.order[:index],m.order[index+1:]...)
			break
		}
	}
	if m.active==id{
		m.active=uuid.Nil
		if len(m.order)>0{m.active=m.order[0]}
	}
	m.syncSwitchersLocked()
	m.mu.Unlock()

	connection.view.Close()
	if connection.close!=nil{connection.close()}
	if m.invalidate!=nil{m.invalidate()}
	return true
}

func (m *MultiWorkspaceClient) ActivateConnection(id uuid.UUID) bool {
	m.mu.Lock()
	if _,ok:=m.connections[id];!ok{
		m.mu.Unlock()
		return false
	}
	changed:=m.active!=id
	m.active=id
	m.syncSwitchersLocked()
	m.mu.Unlock()
	if changed && m.invalidate!=nil{m.invalidate()}
	return true
}

func (m *MultiWorkspaceClient) ActiveConnectionID() uuid.UUID {
	m.mu.RLock()
	id:=m.active
	m.mu.RUnlock()
	return id
}

func (m *MultiWorkspaceClient) ConnectionEntries() []ConnectionEntry {
	m.mu.RLock()
	entries:=m.entriesLocked()
	m.mu.RUnlock()
	return entries
}

func (m *MultiWorkspaceClient) Bootstrap() error {
	m.mu.RLock()
	views:=make([]*WorkspaceClient,0,len(m.order))
	for _,id:=range m.order{
		if connection:=m.connections[id];connection!=nil{views=append(views,connection.view)}
	}
	m.mu.RUnlock()
	for _,view:=range views{
		if err:=view.Bootstrap();err!=nil{return err}
	}
	return nil
}

func (m *MultiWorkspaceClient) Run() {
	m.mu.RLock()
	views:=make([]*WorkspaceClient,0,len(m.order))
	for _,id:=range m.order{
		if connection:=m.connections[id];connection!=nil{views=append(views,connection.view)}
	}
	m.mu.RUnlock()
	for _,view:=range views{go view.Run()}
}

func (m *MultiWorkspaceClient) Layout(gtx layout.Context,th *material.Theme) layout.Dimensions {
	m.mu.RLock()
	connection:=m.connections[m.active]
	m.mu.RUnlock()
	if connection==nil{return layout.Dimensions{}}
	return connection.view.Layout(gtx,th)
}

func (m *MultiWorkspaceClient) Close() {
	m.mu.Lock()
	if m.closed{
		m.mu.Unlock()
		return
	}
	m.closed=true
	connections:=make([]*managedWorkspaceClient,0,len(m.order))
	for index:=len(m.order)-1;index>=0;index--{
		if connection:=m.connections[m.order[index]];connection!=nil{
			connections=append(connections,connection)
		}
	}
	m.connections=make(map[uuid.UUID]*managedWorkspaceClient)
	m.order=nil
	m.active=uuid.Nil
	m.mu.Unlock()

	for _,connection:=range connections{
		connection.view.Close()
		if connection.close!=nil{connection.close()}
	}
}

func (m *MultiWorkspaceClient) entriesLocked() []ConnectionEntry {
	entries:=make([]ConnectionEntry,0,len(m.order))
	for _,id:=range m.order{
		if connection:=m.connections[id];connection!=nil{
			entries=append(entries,connection.entry)
		}
	}
	return entries
}

func (m *MultiWorkspaceClient) syncSwitchersLocked() {
	entries:=m.entriesLocked()
	active:=m.active
	activate:=m.ActivateConnection
	for _,connection:=range m.connections{
		connection.view.SetConnectionSwitcher(active,entries,activate)
	}
}
