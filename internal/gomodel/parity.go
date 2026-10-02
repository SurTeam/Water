package gomodel

import (
	"errors"
	"math"
	"strings"

	"github.com/google/uuid"
)

type PaneMove struct {
	SourceWorkspaceID       uuid.UUID
	SourceTabID             uuid.UUID
	TargetTabID             uuid.UUID
	SourceTabRemoved        bool
	SourceActiveTabChanged  bool
}

func (n *Node) PaneCount() int {
	if n == nil {
		return 0
	}
	if n.Leaf != nil {
		return 1
	}
	return n.First.PaneCount() + n.Second.PaneCount()
}

type paneBounds struct {
	left, top, right, bottom float32
}

func (b paneBounds) centerX() float32 { return (b.left + b.right) / 2 }
func (b paneBounds) centerY() float32 { return (b.top + b.bottom) / 2 }

type leafBounds struct {
	id uuid.UUID
	bounds paneBounds
}

func collectLeafBounds(n *Node, bounds paneBounds, out *[]leafBounds) {
	if n == nil {
		return
	}
	if n.Leaf != nil {
		*out = append(*out, leafBounds{id:*n.Leaf, bounds:bounds})
		return
	}
	ratio := clampRatio(n.Ratio)
	switch n.Axis {
	case "vertical":
		split := bounds.top + (bounds.bottom-bounds.top)*ratio
		collectLeafBounds(n.First, paneBounds{left:bounds.left, top:bounds.top, right:bounds.right, bottom:split}, out)
		collectLeafBounds(n.Second, paneBounds{left:bounds.left, top:split, right:bounds.right, bottom:bounds.bottom}, out)
	default:
		split := bounds.left + (bounds.right-bounds.left)*ratio
		collectLeafBounds(n.First, paneBounds{left:bounds.left, top:bounds.top, right:split, bottom:bounds.bottom}, out)
		collectLeafBounds(n.Second, paneBounds{left:split, top:bounds.top, right:bounds.right, bottom:bounds.bottom}, out)
	}
}

const geometryEpsilon float32 = 0.0001

func intervalGap(a0,a1,b0,b1 float32) float32 {
	if a1 < b0 {
		return b0-a1
	}
	if b1 < a0 {
		return a0-b1
	}
	return 0
}

type directionScore struct {
	primary float32
	cross float32
	center float32
}

func scoreLess(a,b directionScore) bool {
	if math.Abs(float64(a.primary-b.primary)) > 1e-7 {
		return a.primary < b.primary
	}
	if math.Abs(float64(a.cross-b.cross)) > 1e-7 {
		return a.cross < b.cross
	}
	return a.center < b.center
}

func directionalScore(candidate,current paneBounds,direction string)(directionScore,bool){
	var primary,cross float32
	switch direction {
	case "left":
		if candidate.right > current.left+geometryEpsilon { return directionScore{},false }
		primary=(current.left-candidate.right)
		cross=intervalGap(candidate.top,candidate.bottom,current.top,current.bottom)
	case "right":
		if candidate.left < current.right-geometryEpsilon { return directionScore{},false }
		primary=(candidate.left-current.right)
		cross=intervalGap(candidate.top,candidate.bottom,current.top,current.bottom)
	case "up":
		if candidate.bottom > current.top+geometryEpsilon { return directionScore{},false }
		primary=(current.top-candidate.bottom)
		cross=intervalGap(candidate.left,candidate.right,current.left,current.right)
	case "down":
		if candidate.top < current.bottom-geometryEpsilon { return directionScore{},false }
		primary=(candidate.top-current.bottom)
		cross=intervalGap(candidate.left,candidate.right,current.left,current.right)
	default:
		return directionScore{},false
	}
	center:=float32(math.Abs(float64(current.centerX()-candidate.centerX()))+
		math.Abs(float64(current.centerY()-candidate.centerY())))
	return directionScore{primary:primary,cross:cross,center:center},true
}

func (m *Model) DirectionalPane(tabID,paneID uuid.UUID,direction string)(uuid.UUID,bool){
	m.mu.RLock()
	defer m.mu.RUnlock()
	tab:=m.tabs[tabID]
	if tab==nil || !tab.Root.Contains(paneID) { return uuid.Nil,false }
	var leaves []leafBounds
	collectLeafBounds(tab.Root,paneBounds{right:1,bottom:1},&leaves)
	var current paneBounds
	found:=false
	for _,leaf:=range leaves{
		if leaf.id==paneID{current=leaf.bounds;found=true;break}
	}
	if !found{return uuid.Nil,false}
	var best uuid.UUID
	var bestScore directionScore
	have:=false
	for _,leaf:=range leaves{
		if leaf.id==paneID{continue}
		score,ok:=directionalScore(leaf.bounds,current,direction)
		if !ok{continue}
		if !have || scoreLess(score,bestScore){
			best=leaf.id;bestScore=score;have=true
		}
	}
	return best,have
}

func (m *Model) MovePaneToWorkspace(paneID,targetWorkspaceID uuid.UUID)(PaneMove,bool,error){
	m.mu.Lock()
	defer m.mu.Unlock()
	sourceTabID:=m.tabIDForPaneLocked(paneID)
	if sourceTabID==uuid.Nil{return PaneMove{},false,errors.New("pane not found")}
	sourceWorkspaceID:=m.workspaceIDForTabLocked(sourceTabID)
	if sourceWorkspaceID==uuid.Nil{return PaneMove{},false,errors.New("workspace not found")}
	if m.workspaces[targetWorkspaceID]==nil{return PaneMove{},false,errors.New("workspace not found")}
	if sourceWorkspaceID==targetWorkspaceID{return PaneMove{},false,nil}
	move,err:=m.movePaneIntoNewTabLocked(paneID,targetWorkspaceID,uuid.New())
	if err!=nil{return PaneMove{},false,err}
	m.bump()
	return move,true,nil
}

func (m *Model) PromotePaneToTab(paneID uuid.UUID)(PaneMove,bool,error){
	m.mu.Lock()
	defer m.mu.Unlock()
	sourceTabID:=m.tabIDForPaneLocked(paneID)
	if sourceTabID==uuid.Nil{return PaneMove{},false,errors.New("pane not found")}
	tab:=m.tabs[sourceTabID]
	if tab==nil{return PaneMove{},false,errors.New("tab not found")}
	if tab.Root.PaneCount()<=1{return PaneMove{},false,nil}
	sourceWorkspaceID:=m.workspaceIDForTabLocked(sourceTabID)
	if sourceWorkspaceID==uuid.Nil{return PaneMove{},false,errors.New("workspace not found")}
	move,err:=m.movePaneIntoNewTabLocked(paneID,sourceWorkspaceID,uuid.New())
	if err!=nil{return PaneMove{},false,err}
	if m.activeWorkspace!=nil && *m.activeWorkspace==sourceWorkspaceID {
		m.workspaces[sourceWorkspaceID].ActiveTab=ptr(move.TargetTabID)
	}
	m.bump()
	return move,true,nil
}

func (m *Model) movePaneIntoNewTabLocked(paneID,targetWorkspaceID,targetTabID uuid.UUID)(PaneMove,error){
	sourceTabID:=m.tabIDForPaneLocked(paneID)
	if sourceTabID==uuid.Nil{return PaneMove{},errors.New("pane not found")}
	sourceWorkspaceID:=m.workspaceIDForTabLocked(sourceTabID)
	if sourceWorkspaceID==uuid.Nil{return PaneMove{},errors.New("workspace not found")}
	sourceTab:=m.tabs[sourceTabID]
	sourceWorkspace:=m.workspaces[sourceWorkspaceID]
	targetWorkspace:=m.workspaces[targetWorkspaceID]
	if sourceTab==nil{return PaneMove{},errors.New("tab not found")}
	if sourceWorkspace==nil || targetWorkspace==nil{return PaneMove{},errors.New("workspace not found")}
	if m.tabs[targetTabID]!=nil{return PaneMove{},errors.New("tab ID already exists")}

	title:="shell"
	if pane:=m.panes[paneID];pane!=nil && pane.Terminal!=nil && pane.Terminal.ProcessName!=""{
		title=pane.Terminal.ProcessName
	}
	sourceTabRemoved:=sourceTab.Root.PaneCount()==1
	sourceActiveChanged:=sourceWorkspace.ActiveTab!=nil && *sourceWorkspace.ActiveTab==sourceTabID

	if sourceTabRemoved{
		delete(m.tabs,sourceTabID)
		for i,id:=range sourceWorkspace.Tabs{
			if id==sourceTabID{
				sourceWorkspace.Tabs=append(sourceWorkspace.Tabs[:i],sourceWorkspace.Tabs[i+1:]...)
				break
			}
		}
		if sourceActiveChanged{
			sourceWorkspace.ActiveTab=nil
			if len(sourceWorkspace.Tabs)>0{
				v:=sourceWorkspace.Tabs[len(sourceWorkspace.Tabs)-1]
				sourceWorkspace.ActiveTab=&v
			}
		}
	}else{
		if !sourceTab.Root.CloseLeaf(paneID){return PaneMove{},errors.New("pane not found")}
		if sourceTab.ActivePane==paneID{
			var remaining []uuid.UUID
			sourceTab.Root.LeafIDs(&remaining)
			if len(remaining)==0{return PaneMove{},errors.New("pane move left empty tab")}
			sourceTab.ActivePane=remaining[0]
		}
	}

	m.tabs[targetTabID]=&Tab{
		ID:targetTabID,
		Title:title,
		Root:Leaf(paneID),
		ActivePane:paneID,
	}
	targetWorkspace.Tabs=append(targetWorkspace.Tabs,targetTabID)
	targetWorkspace.ActiveTab=ptr(targetTabID)

	return PaneMove{
		SourceWorkspaceID:sourceWorkspaceID,
		SourceTabID:sourceTabID,
		TargetTabID:targetTabID,
		SourceTabRemoved:sourceTabRemoved,
		SourceActiveTabChanged:sourceActiveChanged,
	},nil
}

func (m *Model) ReplaceSurfaceEmpty(paneID uuid.UUID)(uuid.UUID,*uuid.UUID,error){
	m.mu.Lock()
	defer m.mu.Unlock()
	pane:=m.panes[paneID]
	if pane==nil{return uuid.Nil,nil,errors.New("pane not found")}
	var old *uuid.UUID
	if pane.Terminal!=nil{
		v:=pane.Terminal.TerminalID
		old=&v
	}
	surfaceID:=uuid.New()
	pane.SurfaceID=surfaceID
	pane.Terminal=nil
	m.bump()
	return surfaceID,old,nil
}

func (m *Model) RenameAgent(paneID uuid.UUID,label string)error{
	m.mu.Lock()
	defer m.mu.Unlock()
	pane:=m.panes[paneID]
	if pane==nil{return errors.New("pane not found")}
	if pane.Terminal==nil || pane.Terminal.Agent==nil{return errors.New("agent not found")}
	label=strings.TrimSpace(label)
	if label==""{
		pane.Terminal.AgentLabel=nil
	}else{
		pane.Terminal.AgentLabel=ptr(label)
	}
	m.bump()
	return nil
}

func (m *Model) tabIDForPaneLocked(paneID uuid.UUID)uuid.UUID{
	for id,tab:=range m.tabs{
		if tab!=nil && tab.Root.Contains(paneID){return id}
	}
	return uuid.Nil
}

func (m *Model) workspaceIDForTabLocked(tabID uuid.UUID)uuid.UUID{
	for id,workspace:=range m.workspaces{
		if workspace==nil{continue}
		for _,candidate:=range workspace.Tabs{
			if candidate==tabID{return id}
		}
	}
	return uuid.Nil
}
