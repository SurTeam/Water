package gomodel

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/SurTeam/Water/internal/goagent"
	"github.com/SurTeam/Water/internal/goprotocol"
	"github.com/google/uuid"
)

type SurfaceKind string

const (
	SurfaceEmpty    SurfaceKind = "empty"
	SurfaceTerminal SurfaceKind = "terminal"
)

type TerminalMeta struct {
	TerminalID  uuid.UUID
	SessionID   uuid.UUID
	Program     string
	Args        []string
	Size        goprotocol.TerminalSize
	ProcessName string
	CWD         string
	Exited      bool
	ExitCode    *int32
	Title       *string
	Agent       *goagent.DetectedAgent
	AgentLabel  *string
}

type Pane struct {
	ID        uuid.UUID
	SurfaceID uuid.UUID
	Terminal  *TerminalMeta
}

type Node struct {
	Leaf   *uuid.UUID
	Axis   string
	Ratio  float32
	First  *Node
	Second *Node
}

func Leaf(id uuid.UUID) *Node { return &Node{Leaf: &id} }

func (n *Node) Contains(id uuid.UUID) bool {
	if n == nil {
		return false
	}
	if n.Leaf != nil {
		return *n.Leaf == id
	}
	return n.First.Contains(id) || n.Second.Contains(id)
}

func (n *Node) LeafIDs(out *[]uuid.UUID) {
	if n == nil {
		return
	}
	if n.Leaf != nil {
		*out = append(*out, *n.Leaf)
		return
	}
	n.First.LeafIDs(out)
	n.Second.LeafIDs(out)
}

func (n *Node) SplitLeaf(target uuid.UUID, axis string, ratio float32, newPane uuid.UUID, newFirst bool) bool {
	if n == nil {
		return false
	}
	if n.Leaf != nil {
		if *n.Leaf != target {
			return false
		}
		existing := Leaf(*n.Leaf)
		created := Leaf(newPane)
		n.Leaf = nil
		n.Axis = axis
		n.Ratio = clampRatio(ratio)
		if newFirst {
			n.First, n.Second = created, existing
		} else {
			n.First, n.Second = existing, created
		}
		return true
	}
	return n.First.SplitLeaf(target, axis, ratio, newPane, newFirst) ||
		n.Second.SplitLeaf(target, axis, ratio, newPane, newFirst)
}

func removeLeaf(n *Node, target uuid.UUID) (*Node, bool) {
	if n == nil {
		return nil, false
	}
	if n.Leaf != nil {
		if *n.Leaf == target {
			return nil, true
		}
		return n, false
	}
	if n.First.Contains(target) {
		next, removed := removeLeaf(n.First, target)
		if !removed {
			return n, false
		}
		if next == nil {
			return n.Second, true
		}
		n.First = next
		return n, true
	}
	if n.Second.Contains(target) {
		next, removed := removeLeaf(n.Second, target)
		if !removed {
			return n, false
		}
		if next == nil {
			return n.First, true
		}
		n.Second = next
		return n, true
	}
	return n, false
}

func (n *Node) CloseLeaf(target uuid.UUID) bool {
	next, ok := removeLeaf(n, target)
	if !ok || next == nil {
		return false
	}
	*n = *next
	return true
}

func (n *Node) ResizeNearest(target uuid.UUID, ratio float32) bool {
	if n == nil || n.Leaf != nil {
		return false
	}
	inFirst := n.First.Contains(target)
	inSecond := n.Second.Contains(target)
	if !inFirst && !inSecond {
		return false
	}
	if inFirst && n.First.ResizeNearest(target, ratio) {
		return true
	}
	if inSecond && n.Second.ResizeNearest(target, ratio) {
		return true
	}
	n.Ratio = clampRatio(ratio)
	return true
}

func (n *Node) ResizeAtPath(path []bool, ratio float32) bool {
	if n == nil || n.Leaf != nil {
		return false
	}
	if len(path) == 0 {
		n.Ratio = clampRatio(ratio)
		return true
	}
	if path[0] {
		return n.Second.ResizeAtPath(path[1:], ratio)
	}
	return n.First.ResizeAtPath(path[1:], ratio)
}

func clampRatio(v float32) float32 {
	if v < 0.05 {
		return 0.05
	}
	if v > 0.95 {
		return 0.95
	}
	return v
}

type Tab struct {
	ID            uuid.UUID
	Title         string
	TitleOverride *string
	Root          *Node
	ActivePane    uuid.UUID
}

type Workspace struct {
	ID        uuid.UUID
	Title     string
	Tabs      []uuid.UUID
	ActiveTab *uuid.UUID
}

type Model struct {
	mu sync.RWMutex

	revision uint64
	workspaces map[uuid.UUID]*Workspace
	workspaceOrder []uuid.UUID
	activeWorkspace *uuid.UUID
	tabs map[uuid.UUID]*Tab
	panes map[uuid.UUID]*Pane
	nextWorkspaceNumber int
	nextTabNumber int
}

func New() *Model {
	return &Model{
		workspaces: make(map[uuid.UUID]*Workspace),
		tabs: make(map[uuid.UUID]*Tab),
		panes: make(map[uuid.UUID]*Pane),
		nextWorkspaceNumber: 1,
		nextTabNumber: 1,
	}
}

func ptr[T any](v T) *T { return &v }

func (m *Model) bump() { m.revision++ }

func (m *Model) Revision() uint64 {
	m.mu.RLock()
	revision := m.revision
	m.mu.RUnlock()
	return revision
}

func (m *Model) CreateWorkspace(title string) uuid.UUID {
	m.mu.Lock()
	defer m.mu.Unlock()
	if title == "" {
		title = fmt.Sprintf("Workspace %d", m.nextWorkspaceNumber)
		m.nextWorkspaceNumber++
	}
	id := uuid.New()
	m.workspaces[id] = &Workspace{ID:id, Title:title}
	m.workspaceOrder = append(m.workspaceOrder, id)
	m.activeWorkspace = ptr(id)
	m.bump()
	return id
}

func (m *Model) EnsureWorkspace() uuid.UUID {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.activeWorkspace != nil {
		return *m.activeWorkspace
	}
	id := uuid.New()
	title := fmt.Sprintf("Workspace %d", m.nextWorkspaceNumber)
	m.nextWorkspaceNumber++
	m.workspaces[id] = &Workspace{ID:id, Title:title}
	m.workspaceOrder = append(m.workspaceOrder, id)
	m.activeWorkspace = ptr(id)
	m.bump()
	return id
}

func (m *Model) ResolveWorkspace(id *uuid.UUID) (uuid.UUID, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if id != nil {
		if _, ok := m.workspaces[*id]; ok {
			return *id, nil
		}
		return uuid.Nil, errors.New("workspace not found")
	}
	if m.activeWorkspace == nil {
		return uuid.Nil, errors.New("there is no active workspace")
	}
	return *m.activeWorkspace, nil
}

func (m *Model) ActivateWorkspace(id uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.workspaces[id]; !ok {
		return errors.New("workspace not found")
	}
	if m.activeWorkspace == nil || *m.activeWorkspace != id {
		m.activeWorkspace = ptr(id)
		m.bump()
	}
	return nil
}

func (m *Model) RenameWorkspace(id uuid.UUID, title string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	w, ok := m.workspaces[id]
	if !ok {
		return errors.New("workspace not found")
	}
	if title == "" {
		return errors.New("workspace title cannot be empty")
	}
	if w.Title != title {
		w.Title = title
		m.bump()
	}
	return nil
}

func (m *Model) ReorderWorkspace(id uuid.UUID, index int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.workspaces[id]; !ok {
		return errors.New("workspace not found")
	}
	old := -1
	for i, candidate := range m.workspaceOrder {
		if candidate == id { old = i; break }
	}
	if old < 0 { return errors.New("workspace order is missing workspace") }
	if index < 0 { index = 0 }
	if index >= len(m.workspaceOrder) { index = len(m.workspaceOrder)-1 }
	if index == old { return nil }
	m.workspaceOrder = append(m.workspaceOrder[:old], m.workspaceOrder[old+1:]...)
	m.workspaceOrder = append(m.workspaceOrder, uuid.Nil)
	copy(m.workspaceOrder[index+1:], m.workspaceOrder[index:])
	m.workspaceOrder[index] = id
	m.bump()
	return nil
}

func (m *Model) CloseWorkspace(id uuid.UUID) ([]uuid.UUID, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	w, ok := m.workspaces[id]
	if !ok { return nil, errors.New("workspace not found") }
	var terminals []uuid.UUID
	for _, tid := range w.Tabs {
		tab := m.tabs[tid]
		if tab == nil { continue }
		var paneIDs []uuid.UUID
		tab.Root.LeafIDs(&paneIDs)
		for _, pid := range paneIDs {
			if p := m.panes[pid]; p != nil && p.Terminal != nil {
				terminals = append(terminals, p.Terminal.TerminalID)
			}
			delete(m.panes, pid)
		}
		delete(m.tabs, tid)
	}
	delete(m.workspaces, id)
	for i, candidate := range m.workspaceOrder {
		if candidate == id {
			m.workspaceOrder = append(m.workspaceOrder[:i], m.workspaceOrder[i+1:]...)
			break
		}
	}
	if m.activeWorkspace != nil && *m.activeWorkspace == id {
		m.activeWorkspace = nil
		if len(m.workspaceOrder)>0 {
			v := m.workspaceOrder[len(m.workspaceOrder)-1]
			m.activeWorkspace = &v
		}
	}
	m.bump()
	return terminals,nil
}

func (m *Model) CreateTab(workspaceID uuid.UUID, title string, pinned bool) (uuid.UUID, uuid.UUID, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	w, ok := m.workspaces[workspaceID]
	if !ok { return uuid.Nil,uuid.Nil,errors.New("workspace not found") }
	tabID := uuid.New()
	paneID := uuid.New()
	surfaceID := uuid.New()
	if title=="" {
		title = fmt.Sprintf("Terminal %d",m.nextTabNumber)
		m.nextTabNumber++
	}
	var override *string
	if pinned { override=ptr(title) }
	m.panes[paneID]=&Pane{ID:paneID,SurfaceID:surfaceID}
	m.tabs[tabID]=&Tab{ID:tabID,Title:title,TitleOverride:override,Root:Leaf(paneID),ActivePane:paneID}
	w.Tabs=append(w.Tabs,tabID)
	w.ActiveTab=ptr(tabID)
	m.bump()
	return tabID,paneID,nil
}

func (m *Model) ResolveTab(id *uuid.UUID, index *int) (uuid.UUID,error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if id!=nil {
		if _,ok:=m.tabs[*id]; ok { return *id,nil }
		return uuid.Nil,errors.New("tab not found")
	}
	if m.activeWorkspace==nil { return uuid.Nil,errors.New("workspace not found") }
	w:=m.workspaces[*m.activeWorkspace]
	if index!=nil {
		if *index<0 || *index>=len(w.Tabs) { return uuid.Nil,errors.New("tab index out of range") }
		return w.Tabs[*index],nil
	}
	if w.ActiveTab==nil { return uuid.Nil,errors.New("there is no active tab") }
	return *w.ActiveTab,nil
}

func (m *Model) RenameTab(id uuid.UUID,title string) error {
	m.mu.Lock(); defer m.mu.Unlock()
	t:=m.tabs[id]; if t==nil { return errors.New("tab not found") }
	if title=="" { return errors.New("tab title cannot be empty") }
	t.Title=title; t.TitleOverride=ptr(title); m.bump(); return nil
}

func (m *Model) ActivateTab(id uuid.UUID) error {
	m.mu.Lock(); defer m.mu.Unlock()
	if m.tabs[id]==nil { return errors.New("tab not found") }
	for _,wid:=range m.workspaceOrder {
		w:=m.workspaces[wid]
		for _,tid:=range w.Tabs {
			if tid==id {
				w.ActiveTab=ptr(id); m.activeWorkspace=ptr(wid); m.bump(); return nil
			}
		}
	}
	return errors.New("tab not found")
}

func (m *Model) CloseTab(id uuid.UUID) ([]uuid.UUID,error) {
	m.mu.Lock(); defer m.mu.Unlock()
	tab:=m.tabs[id]; if tab==nil { return nil,errors.New("tab not found") }
	var terminals []uuid.UUID
	var paneIDs []uuid.UUID; tab.Root.LeafIDs(&paneIDs)
	for _,pid:=range paneIDs {
		if p:=m.panes[pid]; p!=nil && p.Terminal!=nil { terminals=append(terminals,p.Terminal.TerminalID) }
		delete(m.panes,pid)
	}
	delete(m.tabs,id)
	for _,wid:=range m.workspaceOrder {
		w:=m.workspaces[wid]
		for i,tid:=range w.Tabs {
			if tid==id {
				w.Tabs=append(w.Tabs[:i],w.Tabs[i+1:]...)
				if w.ActiveTab!=nil && *w.ActiveTab==id {
					w.ActiveTab=nil
					if len(w.Tabs)>0 { v:=w.Tabs[len(w.Tabs)-1]; w.ActiveTab=&v }
				}
				m.bump(); return terminals,nil
			}
		}
	}
	m.bump(); return terminals,nil
}

func (m *Model) MoveTab(id,target uuid.UUID) error {
	m.mu.Lock(); defer m.mu.Unlock()
	if m.tabs[id]==nil { return errors.New("tab not found") }
	dst:=m.workspaces[target]; if dst==nil { return errors.New("workspace not found") }
	var src *Workspace
	for _,wid:=range m.workspaceOrder {
		w:=m.workspaces[wid]
		for _,tid:=range w.Tabs { if tid==id { src=w; break } }
		if src!=nil { break }
	}
	if src==nil { return errors.New("tab not found") }
	if src.ID==target { return nil }
	for i,tid:=range src.Tabs {
		if tid==id { src.Tabs=append(src.Tabs[:i],src.Tabs[i+1:]...); break }
	}
	if src.ActiveTab!=nil && *src.ActiveTab==id {
		src.ActiveTab=nil
		if len(src.Tabs)>0 { v:=src.Tabs[len(src.Tabs)-1]; src.ActiveTab=&v }
	}
	dst.Tabs=append(dst.Tabs,id); dst.ActiveTab=ptr(id); m.bump(); return nil
}

func (m *Model) ResolvePane(id *uuid.UUID) (uuid.UUID,uuid.UUID,error) {
	m.mu.RLock(); defer m.mu.RUnlock()
	if id!=nil {
		if m.panes[*id]==nil { return uuid.Nil,uuid.Nil,errors.New("pane not found") }
		for tid,t:=range m.tabs { if t.Root.Contains(*id) { return tid,*id,nil } }
		return uuid.Nil,uuid.Nil,errors.New("pane not found")
	}
	if m.activeWorkspace==nil { return uuid.Nil,uuid.Nil,errors.New("workspace not found") }
	w:=m.workspaces[*m.activeWorkspace]; if w.ActiveTab==nil { return uuid.Nil,uuid.Nil,errors.New("tab not found") }
	t:=m.tabs[*w.ActiveTab]; return t.ID,t.ActivePane,nil
}

func (m *Model) SplitPane(target uuid.UUID,direction string) (uuid.UUID,error) {
	m.mu.Lock(); defer m.mu.Unlock()
	var tab *Tab
	for _,t:=range m.tabs { if t.Root.Contains(target) { tab=t; break } }
	if tab==nil { return uuid.Nil,errors.New("pane not found") }
	newPane:=uuid.New()
	m.panes[newPane]=&Pane{ID:newPane,SurfaceID:uuid.New()}
	axis:="horizontal"; newFirst:=false
	switch direction {
	case "left": axis="horizontal"; newFirst=true
	case "right": axis="horizontal"
	case "up": axis="vertical"; newFirst=true
	case "down": axis="vertical"
	default: delete(m.panes,newPane); return uuid.Nil,errors.New("invalid split direction")
	}
	if !tab.Root.SplitLeaf(target,axis,0.5,newPane,newFirst) {
		delete(m.panes,newPane); return uuid.Nil,errors.New("pane not found")
	}
	tab.ActivePane=newPane; m.bump(); return newPane,nil
}

func (m *Model) ClosePane(id uuid.UUID) ([]uuid.UUID,error) {
	m.mu.Lock(); defer m.mu.Unlock()
	var tab *Tab
	for _,t:=range m.tabs { if t.Root.Contains(id) { tab=t; break } }
	if tab==nil { return nil,errors.New("pane not found") }
	var all []uuid.UUID; tab.Root.LeafIDs(&all)
	if len(all)<=1 { return nil,errors.New("cannot close final pane") }
	p:=m.panes[id]
	var terms []uuid.UUID
	if p!=nil && p.Terminal!=nil { terms=append(terms,p.Terminal.TerminalID) }
	if !tab.Root.CloseLeaf(id) { return nil,errors.New("pane not found") }
	delete(m.panes,id)
	if tab.ActivePane==id {
		all=nil; tab.Root.LeafIDs(&all); tab.ActivePane=all[0]
	}
	m.bump(); return terms,nil
}

func (m *Model) FocusPane(id uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for tabID, tab := range m.tabs {
		if !tab.Root.Contains(id) {
			continue
		}
		workspaceID := m.workspaceIDForTabLocked(tabID)
		if workspaceID == uuid.Nil {
			return errors.New("workspace not found")
		}
		tab.ActivePane = id
		m.activeWorkspace = ptr(workspaceID)
		m.workspaces[workspaceID].ActiveTab = ptr(tabID)
		m.bump()
		return nil
	}
	return errors.New("pane not found")
}

func (m *Model) ResizePane(id uuid.UUID,ratio float32) error {
	m.mu.Lock(); defer m.mu.Unlock()
	for _,t:=range m.tabs {
		if t.Root.Contains(id) {
			if !t.Root.ResizeNearest(id,ratio) { return errors.New("pane has no split") }
			m.bump(); return nil
		}
	}
	return errors.New("pane not found")
}

func (m *Model) ResizeSplit(tabID uuid.UUID,path []bool,ratio float32) error {
	m.mu.Lock(); defer m.mu.Unlock()
	t:=m.tabs[tabID]; if t==nil { return errors.New("tab not found") }
	if !t.Root.ResizeAtPath(path,ratio) { return errors.New("split not found") }
	m.bump(); return nil
}

func (m *Model) InstallTerminal(paneID uuid.UUID, meta TerminalMeta) error {
	m.mu.Lock(); defer m.mu.Unlock()
	p:=m.panes[paneID]; if p==nil { return errors.New("pane not found") }
	p.SurfaceID=uuid.New(); copyMeta:=meta; p.Terminal=&copyMeta; m.bump(); return nil
}

func (m *Model) TerminalForPane(paneID uuid.UUID) (uuid.UUID,bool) {
	m.mu.RLock(); defer m.mu.RUnlock()
	p:=m.panes[paneID]; if p==nil || p.Terminal==nil { return uuid.Nil,false }
	return p.Terminal.TerminalID,true
}

func (m *Model) PaneForTerminal(termID uuid.UUID) (uuid.UUID,bool) {
	m.mu.RLock(); defer m.mu.RUnlock()
	for pid,p:=range m.panes {
		if p.Terminal!=nil && p.Terminal.TerminalID==termID { return pid,true }
	}
	return uuid.Nil,false
}

func (m *Model) SetTerminalSize(termID uuid.UUID,size goprotocol.TerminalSize) {
	m.mu.Lock(); defer m.mu.Unlock()
	for _,p:=range m.panes {
		if p.Terminal!=nil && p.Terminal.TerminalID==termID {
			p.Terminal.Size=size.Normalized(); m.bump(); return
		}
	}
}

func (m *Model) AutoCloseExitedTerminal(termID uuid.UUID) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	var paneID uuid.UUID
	var tabID uuid.UUID
	for pid, pane := range m.panes {
		if pane.Terminal != nil && pane.Terminal.TerminalID == termID {
			paneID = pid
			break
		}
	}
	if paneID == uuid.Nil {
		return false
	}
	for tid, tab := range m.tabs {
		if tab.Root.Contains(paneID) {
			tabID = tid
			break
		}
	}
	if tabID == uuid.Nil {
		return false
	}
	tab := m.tabs[tabID]
	var paneIDs []uuid.UUID
	tab.Root.LeafIDs(&paneIDs)
	if len(paneIDs) <= 1 {
		delete(m.panes, paneID)
		delete(m.tabs, tabID)
		for _, wid := range m.workspaceOrder {
			workspace := m.workspaces[wid]
			if workspace == nil {
				continue
			}
			for index, candidate := range workspace.Tabs {
				if candidate != tabID {
					continue
				}
				workspace.Tabs = append(workspace.Tabs[:index], workspace.Tabs[index+1:]...)
				if workspace.ActiveTab != nil && *workspace.ActiveTab == tabID {
					workspace.ActiveTab = nil
					if len(workspace.Tabs) > 0 {
						next := workspace.Tabs[len(workspace.Tabs)-1]
						workspace.ActiveTab = &next
					}
				}
				m.bump()
				return true
			}
		}
		m.bump()
		return true
	}
	if !tab.Root.CloseLeaf(paneID) {
		return false
	}
	delete(m.panes, paneID)
	if tab.ActivePane == paneID {
		paneIDs = paneIDs[:0]
		tab.Root.LeafIDs(&paneIDs)
		if len(paneIDs) > 0 {
			tab.ActivePane = paneIDs[0]
		}
	}
	m.bump()
	return true
}

func (m *Model) SetTerminalExit(termID uuid.UUID,code *int32) {
	m.mu.Lock(); defer m.mu.Unlock()
	for _,p:=range m.panes {
		if p.Terminal!=nil && p.Terminal.TerminalID==termID {
			p.Terminal.Exited=true; p.Terminal.ExitCode=code; m.bump(); return
		}
	}
}

type StateDump struct {
	StateRevision   uint64          `json:"state_revision"`
	Workspace       *WorkspaceDump  `json:"workspace"`
	Workspaces      []WorkspaceDump `json:"workspaces"`
	ActiveWorkspace *uuid.UUID      `json:"active_workspace"`
	FocusedPane     *uuid.UUID      `json:"focused_pane"`
	Agents          []any           `json:"agents"`
}

type WorkspaceDump struct {
	ID        uuid.UUID  `json:"id"`
	Title     string     `json:"title"`
	ActiveTab *uuid.UUID `json:"active_tab"`
	Tabs      []TabDump  `json:"tabs"`
}

type TabDump struct {
	ID            uuid.UUID       `json:"id"`
	Title         string          `json:"title"`
	TitleOverride *string         `json:"title_override"`
	ActivePane    uuid.UUID       `json:"active_pane"`
	Tree          json.RawMessage `json:"tree"`
}

func (m *Model) Dump() StateDump {
	m.mu.RLock(); defer m.mu.RUnlock()
	out:=StateDump{StateRevision:m.revision,Agents:[]any{}}
	if m.activeWorkspace!=nil { v:=*m.activeWorkspace; out.ActiveWorkspace=&v }
	for _,wid:=range m.workspaceOrder {
		w:=m.workspaces[wid]
		if w==nil { continue }
		out.Workspaces=append(out.Workspaces,m.dumpWorkspaceLocked(w))
		for _,tabID:=range w.Tabs {
			tab:=m.tabs[tabID]
			if tab==nil { continue }
			var paneIDs []uuid.UUID
			tab.Root.LeafIDs(&paneIDs)
			for _,paneID:=range paneIDs {
				pane:=m.panes[paneID]
				if pane==nil || pane.Terminal==nil || pane.Terminal.Agent==nil { continue }
				status:=any("running")
				if pane.Terminal.Exited {
					status=map[string]any{"exited":map[string]any{"code":pane.Terminal.ExitCode}}
				}
				out.Agents=append(out.Agents,map[string]any{
					"kind":pane.Terminal.Agent.Kind,
					"label":goagent.Label(pane.Terminal.Agent.Kind),
					"custom_label":pane.Terminal.AgentLabel,
					"active":pane.Terminal.Agent.Active,
					"workspace_id":wid,
					"tab_id":tabID,
					"pane_id":paneID,
					"terminal_id":pane.Terminal.TerminalID,
					"cwd":pane.Terminal.CWD,
					"status":status,
				})
			}
		}
	}
	if out.ActiveWorkspace!=nil {
		for i:=range out.Workspaces {
			if out.Workspaces[i].ID==*out.ActiveWorkspace {
				w:=out.Workspaces[i]; out.Workspace=&w; break
			}
		}
	}
	if out.Workspace!=nil && out.Workspace.ActiveTab!=nil {
		for _,t:=range out.Workspace.Tabs {
			if t.ID==*out.Workspace.ActiveTab { v:=t.ActivePane; out.FocusedPane=&v; break }
		}
	}
	return out
}

func (m *Model) dumpWorkspaceLocked(w *Workspace) WorkspaceDump {
	out:=WorkspaceDump{ID:w.ID,Title:w.Title}
	if w.ActiveTab!=nil { v:=*w.ActiveTab; out.ActiveTab=&v }
	for _,tid:=range w.Tabs {
		t:=m.tabs[tid]; if t==nil { continue }
		tree,_:=json.Marshal(m.dumpNodeLocked(t.Root))
		out.Tabs=append(out.Tabs,TabDump{
			ID:t.ID,Title:t.Title,TitleOverride:t.TitleOverride,ActivePane:t.ActivePane,Tree:tree,
		})
	}
	return out
}

func (m *Model) dumpNodeLocked(n *Node) any {
	if n.Leaf!=nil {
		p:=m.panes[*n.Leaf]
		surfaceState:=any(map[string]any{"Empty":map[string]any{"label":"Empty"}})
		kind:=SurfaceEmpty
		var terminal any
		if p!=nil && p.Terminal!=nil {
			kind=SurfaceTerminal
			status:=any("running")
			process:=any("running")
			if p.Terminal.Exited {
				status=map[string]any{"exited":map[string]any{"code":p.Terminal.ExitCode}}
				process=map[string]any{"exited":map[string]any{"code":p.Terminal.ExitCode}}
			}
			tstate:=map[string]any{
				"terminal_id":p.Terminal.TerminalID,
				"session_id":p.Terminal.SessionID,
				"program":p.Terminal.Program,
				"title":p.Terminal.Title,
				"process_name":p.Terminal.ProcessName,
				"cwd":p.Terminal.CWD,
				"args":p.Terminal.Args,
				"status":status,
				"columns":p.Terminal.Size.Columns,
				"lines":p.Terminal.Size.Lines,
				"agent":p.Terminal.Agent,
				"agent_label":p.Terminal.AgentLabel,
			}
			surfaceState=map[string]any{"Terminal":tstate}
			terminal=map[string]any{"summary":map[string]any{
				"terminal_id":p.Terminal.TerminalID,
				"size":p.Terminal.Size,
				"process":process,
				"process_name":p.Terminal.ProcessName,
				"cwd":p.Terminal.CWD,
			}}
		}
		return map[string]any{
			"type":"leaf","pane_id":p.ID,"surface_id":p.SurfaceID,
			"surface_kind":kind,"surface_state":surfaceState,"terminal":terminal,
		}
	}
	return map[string]any{
		"type":"split","axis":n.Axis,"ratio":n.Ratio,
		"first":m.dumpNodeLocked(n.First),"second":m.dumpNodeLocked(n.Second),
	}
}

func (m *Model) SortedTerminalIDs() []uuid.UUID {
	m.mu.RLock(); defer m.mu.RUnlock()
	var ids []uuid.UUID
	for _,p:=range m.panes { if p.Terminal!=nil { ids=append(ids,p.Terminal.TerminalID) } }
	sort.Slice(ids,func(i,j int)bool{return ids[i].String()<ids[j].String()})
	return ids
}
