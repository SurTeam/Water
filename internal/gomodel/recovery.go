package gomodel

import (
	"errors"
	"fmt"
	"math"
	"path/filepath"

	"github.com/SurTeam/Water/internal/goprotocol"
	"github.com/google/uuid"
)

const RecoverySchema = 1

type RecoveryLayout struct {
	Schema          int                 `json:"schema"`
	ID              uuid.UUID           `json:"id"`
	SourceInstance  string              `json:"source_instance"`
	Revision        uint64              `json:"revision"`
	ActiveWorkspace *uuid.UUID          `json:"active_workspace"`
	Workspaces      []RecoveryWorkspace `json:"workspaces"`
}
type RecoveryWorkspace struct {
	ID        uuid.UUID     `json:"id"`
	Title     string        `json:"title"`
	ActiveTab *uuid.UUID    `json:"active_tab"`
	Tabs      []RecoveryTab `json:"tabs"`
}
type RecoveryTab struct {
	ID            uuid.UUID     `json:"id"`
	Title         string        `json:"title"`
	TitleOverride *string       `json:"title_override"`
	ActivePane    uuid.UUID     `json:"active_pane"`
	Tree          *RecoveryNode `json:"tree"`
}
type RecoveryNode struct {
	PaneID     uuid.UUID               `json:"pane_id,omitempty"`
	SurfaceID  uuid.UUID               `json:"surface_id,omitempty"`
	Axis       string                  `json:"axis,omitempty"`
	Ratio      float32                 `json:"ratio,omitempty"`
	First      *RecoveryNode           `json:"first,omitempty"`
	Second     *RecoveryNode           `json:"second,omitempty"`
	Terminal   bool                    `json:"terminal,omitempty"`
	CWD        string                  `json:"cwd,omitempty"`
	Size       goprotocol.TerminalSize `json:"size,omitempty"`
	AgentLabel *string                 `json:"agent_label,omitempty"`
}
type RecoveryPane struct {
	ID   uuid.UUID
	Node *RecoveryNode
}

func (m *Model) ExportLayout(instance string) RecoveryLayout {
	m.mu.RLock()
	defer m.mu.RUnlock()
	d := RecoveryLayout{Schema: RecoverySchema, ID: uuid.New(), SourceInstance: instance, Revision: m.revision, Workspaces: []RecoveryWorkspace{}}
	if m.activeWorkspace != nil {
		d.ActiveWorkspace = ptr(*m.activeWorkspace)
	}
	var tree func(*Node) *RecoveryNode
	tree = func(n *Node) *RecoveryNode {
		if n.Leaf == nil {
			return &RecoveryNode{Axis: n.Axis, Ratio: n.Ratio, First: tree(n.First), Second: tree(n.Second)}
		}
		p := m.panes[*n.Leaf]
		out := &RecoveryNode{PaneID: p.ID, SurfaceID: p.SurfaceID}
		if t := p.Terminal; t != nil {
			out.Terminal = true
			out.CWD = t.CWD
			out.Size = t.Size
			if t.AgentLabel != nil {
				out.AgentLabel = ptr(*t.AgentLabel)
			}
		}
		return out
	}
	for _, wid := range m.workspaceOrder {
		w := m.workspaces[wid]
		rw := RecoveryWorkspace{ID: w.ID, Title: w.Title, Tabs: []RecoveryTab{}}
		if w.ActiveTab != nil {
			rw.ActiveTab = ptr(*w.ActiveTab)
		}
		for _, tid := range w.Tabs {
			t := m.tabs[tid]
			rt := RecoveryTab{ID: t.ID, Title: t.Title, ActivePane: t.ActivePane, Tree: tree(t.Root)}
			if t.TitleOverride != nil {
				rt.TitleOverride = ptr(*t.TitleOverride)
			}
			rw.Tabs = append(rw.Tabs, rt)
		}
		d.Workspaces = append(d.Workspaces, rw)
	}
	return d
}

func (d RecoveryLayout) Validate() error {
	if d.Schema != RecoverySchema {
		return fmt.Errorf("unsupported recovery schema %d", d.Schema)
	}
	if d.ID == uuid.Nil || len(d.Workspaces) > 128 {
		return errors.New("invalid recovery identity or workspace count")
	}
	seen := map[uuid.UUID]bool{}
	claim := func(id uuid.UUID) error {
		if id == uuid.Nil || id.Version() != 4 || seen[id] {
			return errors.New("recovery IDs must be unique UUIDv4")
		}
		seen[id] = true
		return nil
	}
	count := 0
	var check func(*RecoveryNode, int, map[uuid.UUID]bool) error
	check = func(n *RecoveryNode, depth int, leaves map[uuid.UUID]bool) error {
		if n == nil || depth > 64 {
			return errors.New("invalid recovery tree depth")
		}
		if n.PaneID != uuid.Nil {
			count++
			if count > 1024 {
				return errors.New("too many recovery panes")
			}
			if n.First != nil || n.Second != nil || n.Axis != "" {
				return errors.New("mixed leaf/split recovery node")
			}
			if err := claim(n.PaneID); err != nil {
				return err
			}
			if err := claim(n.SurfaceID); err != nil {
				return err
			}
			if n.Terminal && !filepath.IsAbs(n.CWD) {
				return errors.New("terminal recovery requires an absolute current directory")
			}
			leaves[n.PaneID] = true
			return nil
		}
		if n.Axis != "horizontal" && n.Axis != "vertical" {
			return errors.New("invalid split axis")
		}
		if math.IsNaN(float64(n.Ratio)) || n.Ratio < .05 || n.Ratio > .95 {
			return errors.New("invalid split ratio")
		}
		if n.Terminal || n.CWD != "" {
			return errors.New("terminal data on split node")
		}
		if err := check(n.First, depth+1, leaves); err != nil {
			return err
		}
		return check(n.Second, depth+1, leaves)
	}
	workspaces := map[uuid.UUID]bool{}
	for _, w := range d.Workspaces {
		if err := claim(w.ID); err != nil {
			return err
		}
		workspaces[w.ID] = true
		tabs := map[uuid.UUID]bool{}
		if len(w.Tabs) > 1024 {
			return errors.New("too many tabs")
		}
		for _, t := range w.Tabs {
			if err := claim(t.ID); err != nil {
				return err
			}
			tabs[t.ID] = true
			leaves := map[uuid.UUID]bool{}
			if err := check(t.Tree, 0, leaves); err != nil {
				return err
			}
			if !leaves[t.ActivePane] {
				return errors.New("active pane is outside its tab")
			}
		}
		if len(w.Tabs) > 0 && (w.ActiveTab == nil || !tabs[*w.ActiveTab]) {
			return errors.New("invalid active tab")
		}
		if len(w.Tabs) == 0 && w.ActiveTab != nil {
			return errors.New("empty workspace has active tab")
		}
	}
	if len(d.Workspaces) > 0 && (d.ActiveWorkspace == nil || !workspaces[*d.ActiveWorkspace]) {
		return errors.New("invalid active workspace")
	}
	if len(d.Workspaces) == 0 && d.ActiveWorkspace != nil {
		return errors.New("empty layout has active workspace")
	}
	return nil
}

func RecoveryModel(d RecoveryLayout) (*Model, []RecoveryPane, error) {
	if err := d.Validate(); err != nil {
		return nil, nil, err
	}
	m := New()
	var panes []RecoveryPane
	var tree func(*RecoveryNode) *Node
	tree = func(n *RecoveryNode) *Node {
		if n.PaneID != uuid.Nil {
			m.panes[n.PaneID] = &Pane{ID: n.PaneID, SurfaceID: n.SurfaceID}
			panes = append(panes, RecoveryPane{n.PaneID, n})
			return Leaf(n.PaneID)
		}
		return &Node{Axis: n.Axis, Ratio: n.Ratio, First: tree(n.First), Second: tree(n.Second)}
	}
	for _, w := range d.Workspaces {
		mw := &Workspace{ID: w.ID, Title: w.Title, ActiveTab: w.ActiveTab}
		for _, t := range w.Tabs {
			mw.Tabs = append(mw.Tabs, t.ID)
			m.tabs[t.ID] = &Tab{ID: t.ID, Title: t.Title, TitleOverride: t.TitleOverride, ActivePane: t.ActivePane, Root: tree(t.Tree)}
		}
		m.workspaces[w.ID] = mw
		m.workspaceOrder = append(m.workspaceOrder, w.ID)
	}
	m.activeWorkspace = d.ActiveWorkspace
	m.nextWorkspaceNumber = len(d.Workspaces) + 1
	m.nextTabNumber = len(m.tabs) + 1
	return m, panes, nil
}

// AdoptRecovery commits a fully prepared layout into an empty server model.
// The existing model pointer remains stable for foreground and snapshot workers.
func (m *Model) AdoptRecovery(prepared *Model) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.workspaces) > 0 {
		return errors.New("recovery requires an empty server; existing workspaces were left untouched")
	}
	m.workspaces = prepared.workspaces
	m.workspaceOrder = prepared.workspaceOrder
	m.tabs = prepared.tabs
	m.panes = prepared.panes
	m.activeWorkspace = prepared.activeWorkspace
	m.nextWorkspaceNumber = prepared.nextWorkspaceNumber
	m.nextTabNumber = prepared.nextTabNumber
	m.bump()
	return nil
}
