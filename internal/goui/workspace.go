package goui

import (
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"fmt"
	"strings"
	"sync"
	"time"

	"gioui.org/f32"
	"gioui.org/io/input"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/SurTeam/Water/internal/goclient"
	"github.com/SurTeam/Water/internal/goconfig"
	"github.com/SurTeam/Water/internal/gomodel"
	"github.com/SurTeam/Water/internal/goprotocol"
	"github.com/SurTeam/Water/internal/govt"
	"github.com/google/uuid"
)

type paneTree struct {
	Type        string          `json:"type"`
	PaneID      uuid.UUID       `json:"pane_id"`
	SurfaceID   uuid.UUID       `json:"surface_id"`
	SurfaceKind string          `json:"surface_kind"`
	Terminal    *terminalProjection `json:"terminal"`
	Axis        string          `json:"axis"`
	Ratio       float32         `json:"ratio"`
	First       *paneTree       `json:"first"`
	Second      *paneTree       `json:"second"`
}

type terminalProjection struct {
	Summary terminalSummary `json:"summary"`
}

type terminalSummary struct {
	TerminalID  uuid.UUID                `json:"terminal_id"`
	Size        goprotocol.TerminalSize  `json:"size"`
	Process     json.RawMessage           `json:"process"`
	ProcessName string                    `json:"process_name"`
	CWD         string                    `json:"cwd"`
}

type terminalAttach struct {
	TerminalID uuid.UUID                       `json:"terminal_id"`
	FirstSeq   *uint64                         `json:"first_seq"`
	LastSeq    uint64                          `json:"last_seq"`
	Size       goprotocol.TerminalSize         `json:"size"`
	Replay     []goprotocol.WireTerminalEvent  `json:"replay"`
}

type automationHitKind uint8

const (
	hitWorkspace automationHitKind = iota + 1
	hitTab
	hitNewWorkspace
	hitNewTab
	hitPane
)

type automationHit struct {
	Rect image.Rectangle
	Kind automationHitKind
	ID   uuid.UUID
}

type terminalClient struct {
	id uuid.UUID
	mu sync.RWMutex
	emu *govt.Emulator
	snapshot govt.Snapshot
	lastSeq uint64
	cols int
	rows int
	view *TerminalView
	input *TerminalInput
	selection Selection
}

func (t *terminalClient) close() {
	t.mu.Lock()
	if t.emu != nil {
		t.emu.Close()
		t.emu = nil
	}
	t.mu.Unlock()
}

type WorkspaceClient struct {
	session *goclient.Session
	invalidate func()
	config goconfig.AppConfig

	mu sync.RWMutex
	state gomodel.StateDump
	terminals map[uuid.UUID]*terminalClient

	layoutMu sync.Mutex
	frameSize image.Point
	frameMetric unit.Metric
	hitRegions []automationHit

	workspaceClicks map[uuid.UUID]*widget.Clickable
	tabClicks map[uuid.UUID]*widget.Clickable
	newWorkspace widget.Clickable
	newTab widget.Clickable
}

func NewWorkspaceClient(session *goclient.Session, invalidate func()) *WorkspaceClient {
	return NewWorkspaceClientWithConfig(session,invalidate,goconfig.Default())
}

func NewWorkspaceClientWithConfig(session *goclient.Session, invalidate func(), config goconfig.AppConfig) *WorkspaceClient {
	return &WorkspaceClient{
		session: session,
		invalidate: invalidate,
		config: config.Normalized(),
		terminals: make(map[uuid.UUID]*terminalClient),
		workspaceClicks: make(map[uuid.UUID]*widget.Clickable),
		tabClicks: make(map[uuid.UUID]*widget.Clickable),
	}
}

func (c *WorkspaceClient) Close() {
	c.mu.Lock()
	terms:=make([]*terminalClient,0,len(c.terminals))
	for _,term:=range c.terminals { terms=append(terms,term) }
	c.terminals=make(map[uuid.UUID]*terminalClient)
	c.mu.Unlock()
	for _,term:=range terms {
		_ = c.session.Detach(term.id)
		term.close()
	}
}

func (c *WorkspaceClient) Run() {
	pushes:=c.session.Pushes
	events:=c.session.Events
	for pushes!=nil || events!=nil {
		select {
		case msg,ok:=<-pushes:
			if !ok { pushes=nil; continue }
			switch msg.Method {
			case "push.snapshot":
				var state gomodel.StateDump
				if json.Unmarshal(msg.Params,&state)==nil {
					c.applyState(state)
				}
			case "push.ui":
				c.handleUIPush(msg)
			}
		case push,ok:=<-events:
			if !ok { events=nil; continue }
			c.applyTerminalEvent(push)
		}
	}
}

func (c *WorkspaceClient) handleUIPush(msg goprotocol.WireMessage) {
	var request struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err:=json.Unmarshal(msg.Params,&request);err!=nil{
		_ = c.session.ReplyFailure(msg.RequestID,"UI_AUTOMATION_FAILED",err.Error())
		return
	}
	result,err:=c.handleUIRequest(request.Method,request.Params)
	if err!=nil{
		_ = c.session.ReplyFailure(msg.RequestID,"UI_AUTOMATION_FAILED",err.Error())
		return
	}
	_ = c.session.ReplySuccess(msg.RequestID,result)
}

func (c *WorkspaceClient) handleUIRequest(method string, params json.RawMessage)(any,error){
	switch method {
	case "ui.snapshot":
		return map[string]any{
			"window_count":1,
			"has_active_window":true,
		},nil
	case "ui.keystroke":
		var p struct{Keystroke string `json:"keystroke"`}
		if err:=json.Unmarshal(params,&p);err!=nil{return nil,err}
		handled:=c.dispatchAutomationKeystroke(p.Keystroke)
		return map[string]any{
			"keystroke":p.Keystroke,
			"handled":handled,
			"window_count":1,
		},nil
	case "ui.click":
		var p struct{X float32 `json:"x"`;Y float32 `json:"y"`;ClickCount int `json:"click_count"`}
		if err:=json.Unmarshal(params,&p);err!=nil{return nil,err}
		handled:=c.automationClick(p.X,p.Y,p.ClickCount)
		return map[string]any{
			"window_count":1,
			"has_active_window":true,
			"handled":handled,
		},nil
	case "ui.wheel":
		var p struct{X float32 `json:"x"`;Y float32 `json:"y"`;DX float32 `json:"dx"`;DY float32 `json:"dy"`}
		if err:=json.Unmarshal(params,&p);err!=nil{return nil,err}
		scrolled:=c.scrollActiveTerminal(p.DY)
		return map[string]any{
			"position":[]float32{p.X,p.Y},
			"delta":[]float32{p.DX,p.DY},
			"propagate":!scrolled,
			"default_prevented":scrolled,
		},nil
	case "ui.screenshot":
		var p struct{Path string `json:"path"`}
		if err:=json.Unmarshal(params,&p);err!=nil{return nil,err}
		return c.Screenshot(p.Path)
	case "connection.list":
		return map[string]any{"connections":[]any{}},nil
	default:
		return nil,fmt.Errorf("unsupported UI method %q",method)
	}
}

func (c *WorkspaceClient) dispatchAutomationKeystroke(spec string)bool{
	key:=strings.ToLower(strings.TrimSpace(spec))
	switch key {
	case strings.ToLower(c.config.Shortcuts.NewTerminalTab), "command-t", "ctrl-shift-t":
		_ = c.session.DispatchAsync(map[string]any{"type":"tab.new"})
		return true
	case strings.ToLower(c.config.Shortcuts.NewWorkspace), "command-shift-n":
		_ = c.session.DispatchAsync(map[string]any{"type":"workspace.new"})
		return true
	case strings.ToLower(c.config.Shortcuts.ClosePane):
		_ = c.session.DispatchAsync(map[string]any{"type":"pane.close"})
		return true
	case strings.ToLower(c.config.Shortcuts.PromotePaneToTab):
		_ = c.session.DispatchAsync(map[string]any{"type":"pane.promote_to_tab"})
		return true
	case strings.ToLower(c.config.Shortcuts.FocusLeft), "cmd-left", "command-left":
		return c.focusDirection("left")
	case strings.ToLower(c.config.Shortcuts.FocusRight), "cmd-right", "command-right":
		return c.focusDirection("right")
	case strings.ToLower(c.config.Shortcuts.FocusUp), "cmd-up", "command-up":
		return c.focusDirection("up")
	case strings.ToLower(c.config.Shortcuts.FocusDown), "cmd-down", "command-down":
		return c.focusDirection("down")
	}
	data:=automationKeyBytes(key)
	if len(data)==0{return false}
	id,ok:=c.activeTerminalID()
	if !ok{return false}
	_ = c.session.DispatchAsync(map[string]any{
		"type":"terminal.send_bytes",
		"terminal_id":id,
		"bytes":bytesAsInts(data),
	})
	return true
}

func automationKeyBytes(spec string)[]byte{
	switch spec {
	case "enter","return": return []byte("\r")
	case "tab": return []byte("\t")
	case "escape","esc": return []byte{0x1b}
	case "backspace": return []byte{0x7f}
	case "up": return []byte("\x1b[A")
	case "down": return []byte("\x1b[B")
	case "right": return []byte("\x1b[C")
	case "left": return []byte("\x1b[D")
	case "home": return []byte("\x1b[H")
	case "end": return []byte("\x1b[F")
	case "pageup","page-up": return []byte("\x1b[5~")
	case "pagedown","page-down": return []byte("\x1b[6~")
	case "ctrl-l": return []byte{0x0c}
	case "ctrl-c": return []byte{0x03}
	case "ctrl-d": return []byte{0x04}
	case "ctrl-z": return []byte{0x1a}
	}
	if strings.HasPrefix(spec,"text:"){
		return []byte(strings.TrimPrefix(spec,"text:"))
	}
	if len([]rune(spec))==1{return []byte(spec)}
	return nil
}

func (c *WorkspaceClient) focusDirection(direction string)bool{
	c.mu.RLock()
	pane:=c.state.FocusedPane
	c.mu.RUnlock()
	if pane==nil{return false}
	_ = c.session.DispatchAsync(map[string]any{
		"type":"pane.focus","pane_id":*pane,"direction":direction,
	})
	return true
}

func (c *WorkspaceClient) activeTerminalID()(uuid.UUID,bool){
	c.mu.RLock()
	state:=c.state
	c.mu.RUnlock()
	workspace:=activeWorkspace(state)
	if workspace==nil{return uuid.Nil,false}
	tab:=activeTab(*workspace)
	if tab==nil{return uuid.Nil,false}
	var root paneTree
	if json.Unmarshal(tab.Tree,&root)!=nil{return uuid.Nil,false}
	return terminalForPane(&root,tab.ActivePane)
}

func terminalForPane(node *paneTree,paneID uuid.UUID)(uuid.UUID,bool){
	if node==nil{return uuid.Nil,false}
	if node.Type=="leaf"{
		if node.PaneID==paneID && node.Terminal!=nil && node.Terminal.Summary.TerminalID!=uuid.Nil{
			return node.Terminal.Summary.TerminalID,true
		}
		return uuid.Nil,false
	}
	if id,ok:=terminalForPane(node.First,paneID);ok{return id,true}
	return terminalForPane(node.Second,paneID)
}

func (c *WorkspaceClient) scrollActiveTerminal(deltaY float32)bool{
	id,ok:=c.activeTerminalID()
	if !ok{return false}
	c.mu.RLock();term:=c.terminals[id];c.mu.RUnlock()
	if term==nil{return false}
	lines:=int(deltaY/20)
	if lines==0{
		if deltaY<0{lines=-1}else if deltaY>0{lines=1}
	}
	if lines==0{return false}
	term.mu.Lock()
	if term.emu==nil{term.mu.Unlock();return false}
	term.emu.Scroll(lines)
	term.snapshot=term.emu.Snapshot()
	term.mu.Unlock()
	if c.invalidate!=nil{c.invalidate()}
	return true
}

func (c *WorkspaceClient) Bootstrap() error {
	var state gomodel.StateDump
	if err:=c.session.Call("state.dump",map[string]any{},&state);err!=nil{return err}
	c.applyState(state)
	if len(state.Workspaces)==0 {
		return c.session.DispatchAsync(map[string]any{"type":"workspace.new"})
	}
	return nil
}

func (c *WorkspaceClient) applyState(state gomodel.StateDump) {
	wanted:=collectTerminalIDs(state)
	c.mu.Lock()
	c.state=state
	var remove []*terminalClient
	for id,term:=range c.terminals {
		if _,ok:=wanted[id];!ok {
			delete(c.terminals,id)
			remove=append(remove,term)
		}
	}
	var add []terminalSummary
	for id,summary:=range wanted {
		if _,ok:=c.terminals[id];!ok { add=append(add,summary) }
	}
	c.mu.Unlock()

	for _,term:=range remove {
		_ = c.session.Detach(term.id)
		term.close()
	}
	for _,summary:=range add {
		c.attachTerminal(summary)
	}
	if c.invalidate!=nil { c.invalidate() }
}

func collectTerminalIDs(state gomodel.StateDump) map[uuid.UUID]terminalSummary {
	out:=make(map[uuid.UUID]terminalSummary)
	for _,workspace:=range state.Workspaces {
		for _,tab:=range workspace.Tabs {
			var root paneTree
			if json.Unmarshal(tab.Tree,&root)==nil {
				collectTreeTerminals(&root,out)
			}
		}
	}
	if state.Workspace!=nil {
		for _,tab:=range state.Workspace.Tabs {
			var root paneTree
			if json.Unmarshal(tab.Tree,&root)==nil {
				collectTreeTerminals(&root,out)
			}
		}
	}
	return out
}

func collectTreeTerminals(node *paneTree,out map[uuid.UUID]terminalSummary) {
	if node==nil{return}
	if node.Type=="leaf" {
		if node.Terminal!=nil && node.Terminal.Summary.TerminalID!=uuid.Nil {
			out[node.Terminal.Summary.TerminalID]=node.Terminal.Summary
		}
		return
	}
	collectTreeTerminals(node.First,out)
	collectTreeTerminals(node.Second,out)
}

func (c *WorkspaceClient) attachTerminal(summary terminalSummary) {
	var attached terminalAttach
	if err:=c.session.Attach(summary.TerminalID,&attached);err!=nil{return}
	emu:=govt.New(attached.Size.Columns,attached.Size.Lines,c.config.Terminal.ScrollbackLines)
	last:=uint64(0)
	for _,ev:=range attached.Replay {
		if ev.Seq<=last {continue}
		applyWireEvent(emu,ev)
		last=ev.Seq
	}
	if attached.LastSeq>last {last=attached.LastSeq}
	view:=NewTerminalView()
	view.FontSize=unit.Sp(c.config.Terminal.FontSize)
	view.LineHeight=unit.Dp(c.config.Terminal.LineHeight)
	view.CellWidth=unit.Dp(c.config.Terminal.FontSize*0.6)
	view.FontFamily=c.config.Terminal.FontFamily
	view.Hyperlinks=c.config.Terminal.Hyperlinks
	view.Theme.Foreground=configColor(c.config.Theme.TerminalForeground,0xe4e4e4)
	view.Theme.Background=configColor(c.config.Theme.TerminalBackground,0x2c2c2c)
	view.Theme.Cursor=configColor(c.config.Theme.CursorBackground,0xe4e4e4)
	view.Theme.Selection=configColor(c.config.Theme.Accent,0x5e81ac)
	view.Theme.Selection.A=0x88
	term:=&terminalClient{
		id:summary.TerminalID,emu:emu,lastSeq:last,
		cols:attached.Size.Columns,rows:attached.Size.Lines,
		view:view,
	}
	term.input=&TerminalInput{
		BracketedPaste:c.config.Features.BracketedPaste,
		OnInput:func(data []byte){
			term.mu.Lock()
			term.selection=Selection{}
			term.mu.Unlock()
			_ = c.session.DispatchAsync(map[string]any{
				"type":"terminal.send_bytes",
				"terminal_id":summary.TerminalID,
				"bytes":bytesAsInts(data),
			})
		},
		OnMouse:func(ev govt.MouseEvent)bool{
			term.mu.Lock()
			if term.emu==nil {term.mu.Unlock();return false}
			accepted:=term.emu.Mouse(ev)
			term.snapshot=term.emu.Snapshot()
			if accepted { term.selection=Selection{} }
			term.mu.Unlock()
			if accepted { c.flushVTResponses(term) }
			if accepted && c.invalidate!=nil { c.invalidate() }
			return accepted
		},
		OnScroll:func(lines int){
			term.mu.Lock()
			if term.emu==nil {term.mu.Unlock();return}
			term.emu.Scroll(lines)
			term.snapshot=term.emu.Snapshot()
			term.selection=Selection{}
			term.mu.Unlock()
			if c.invalidate!=nil { c.invalidate() }
		},
		OnSelectionStart:func(col,row int){
			term.mu.Lock()
			term.selection=Selection{AnchorCol:col,AnchorRow:row,FocusCol:col,FocusRow:row,Active:true}
			term.mu.Unlock()
			if c.invalidate!=nil { c.invalidate() }
		},
		OnSelectionMove:func(col,row int){
			term.mu.Lock()
			if term.selection.Active {
				term.selection.FocusCol=col
				term.selection.FocusRow=row
			}
			term.mu.Unlock()
			if c.invalidate!=nil { c.invalidate() }
		},
		OnSelectionEnd:func(col,row int){
			term.mu.Lock()
			if term.selection.Active {
				term.selection.FocusCol=col
				term.selection.FocusRow=row
				if term.selection.AnchorCol==col && term.selection.AnchorRow==row {
					term.selection=Selection{}
				}
			}
			term.mu.Unlock()
			if c.invalidate!=nil { c.invalidate() }
		},
		OnCopy:func()string{
			term.mu.RLock()
			text:=SelectedText(term.snapshot,term.selection)
			term.mu.RUnlock()
			return text
		},
	}
	if !c.config.Features.MouseReporting {
		term.input.OnMouse=nil
	}
	if !c.config.Features.Selection {
		term.input.OnSelectionStart=nil
		term.input.OnSelectionMove=nil
		term.input.OnSelectionEnd=nil
		term.input.OnCopy=nil
	}
	term.snapshot=emu.Snapshot()
	c.flushVTResponses(term)

	c.mu.Lock()
	if old:=c.terminals[summary.TerminalID];old!=nil {
		c.mu.Unlock()
		term.close()
		return
	}
	c.terminals[summary.TerminalID]=term
	c.mu.Unlock()
}

func applyWireEvent(emu *govt.Emulator,ev goprotocol.WireTerminalEvent) {
	switch ev.Type {
	case "output":
		if data,err:=base64.StdEncoding.DecodeString(ev.Bytes);err==nil{emu.Write(data)}
	case "resize":
		emu.Resize(ev.Columns,ev.Lines)
	}
}

func (c *WorkspaceClient) applyTerminalEvent(push goclient.TerminalPush) {
	c.mu.RLock()
	term:=c.terminals[push.TerminalID]
	c.mu.RUnlock()
	if term==nil{return}

	term.mu.Lock()
	if push.Event.Seq<=term.lastSeq {
		term.mu.Unlock()
		return
	}
	if term.lastSeq!=0 && push.Event.Seq!=term.lastSeq+1 {
		term.mu.Unlock()
		c.resyncTerminal(push.TerminalID)
		return
	}
	term.lastSeq=push.Event.Seq
	switch push.Event.Kind {
	case goprotocol.OutputEvent:
		term.emu.Write(push.Event.Data)
		term.selection=Selection{}
	case goprotocol.ResizeEvent:
		term.emu.Resize(push.Event.Size.Columns,push.Event.Size.Lines)
		term.cols=push.Event.Size.Columns
		term.rows=push.Event.Size.Lines
		term.selection=Selection{}
	}
	term.snapshot=term.emu.Snapshot()
	term.mu.Unlock()
	c.flushVTResponses(term)
	if c.invalidate!=nil{c.invalidate()}
}

func (c *WorkspaceClient) resyncTerminal(id uuid.UUID) {
	c.mu.RLock()
	old:=c.terminals[id]
	c.mu.RUnlock()
	if old==nil{return}
	var attached terminalAttach
	if err:=c.session.Attach(id,&attached);err!=nil{return}
	next:=govt.New(attached.Size.Columns,attached.Size.Lines,c.config.Terminal.ScrollbackLines)
	var last uint64
	for _,ev:=range attached.Replay {
		if ev.Seq<=last{continue}
		applyWireEvent(next,ev)
		last=ev.Seq
	}
	if attached.LastSeq>last{last=attached.LastSeq}
	old.mu.Lock()
	prev:=old.emu
	old.emu=next
	old.snapshot=next.Snapshot()
	old.selection=Selection{}
	old.lastSeq=last
	old.cols=attached.Size.Columns
	old.rows=attached.Size.Lines
	old.mu.Unlock()
	if prev!=nil{prev.Close()}
	c.flushVTResponses(old)
	if c.invalidate!=nil{c.invalidate()}
}

func (c *WorkspaceClient) flushVTResponses(term *terminalClient) {
	term.mu.RLock()
	if term.emu==nil {term.mu.RUnlock();return}
	responses:=term.emu.TakeResponses()
	term.mu.RUnlock()
	for _,data:=range responses {
		_ = c.session.DispatchAsync(map[string]any{
			"type":"terminal.send_bytes",
			"terminal_id":term.id,
			"bytes":bytesAsInts(data),
		})
	}
}

func bytesAsInts(data []byte)[]int{
	out:=make([]int,len(data))
	for i,b:=range data{out[i]=int(b)}
	return out
}

func (c *WorkspaceClient) Layout(gtx layout.Context,th *material.Theme) layout.Dimensions {
	c.layoutMu.Lock()
	defer c.layoutMu.Unlock()
	c.frameSize=gtx.Constraints.Max
	c.frameMetric=gtx.Metric
	c.hitRegions=c.hitRegions[:0]
	return c.layoutUnlocked(gtx,th)
}

func (c *WorkspaceClient) layoutUnlocked(gtx layout.Context,th *material.Theme) layout.Dimensions {
	c.mu.RLock()
	state:=c.state
	c.mu.RUnlock()

	for c.newWorkspace.Clicked(gtx) {
		_ = c.session.DispatchAsync(map[string]any{"type":"workspace.new"})
	}
	for c.newTab.Clicked(gtx) {
		_ = c.session.DispatchAsync(map[string]any{"type":"tab.new"})
	}

	sidebarWidth:=gtx.Dp(unit.Dp(190))
	return layout.Flex{Axis:layout.Horizontal}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context)layout.Dimensions{
			gtx.Constraints.Min.X=sidebarWidth
			gtx.Constraints.Max.X=sidebarWidth
			return c.layoutSidebar(gtx,th,state)
		}),
		layout.Flexed(1,func(gtx layout.Context)layout.Dimensions{
			return c.layoutWorkspace(gtx,th,state,image.Pt(sidebarWidth,0))
		}),
	)
}

func (c *WorkspaceClient) layoutSidebar(gtx layout.Context,th *material.Theme,state gomodel.StateDump)layout.Dimensions{
	items:=make([]layout.FlexChild,0,len(state.Workspaces)+2)
	y:=0
	header:=material.Label(th,unit.Sp(16),"Water")
	header.Font.Weight=600
	items=append(items,layout.Rigid(func(gtx layout.Context)layout.Dimensions{
		dims:=layout.UniformInset(unit.Dp(12)).Layout(gtx,header.Layout)
		y+=dims.Size.Y
		return dims
	}))
	for _,workspace:=range state.Workspaces {
		w:=workspace
		click:=c.workspaceClicks[w.ID]
		if click==nil{click=new(widget.Clickable);c.workspaceClicks[w.ID]=click}
		for click.Clicked(gtx) {
			_ = c.session.DispatchAsync(map[string]any{"type":"workspace.activate","workspace_id":w.ID})
		}
		items=append(items,layout.Rigid(func(gtx layout.Context)layout.Dimensions{
			top:=y
			button:=material.Button(th,click,w.Title)
			dims:=layout.Inset{Left:unit.Dp(8),Right:unit.Dp(8),Bottom:unit.Dp(4)}.Layout(gtx,button.Layout)
			c.hitRegions=append(c.hitRegions,automationHit{
				Rect:image.Rect(0,top,gtx.Constraints.Max.X,top+dims.Size.Y),
				Kind:hitWorkspace,ID:w.ID,
			})
			y+=dims.Size.Y
			return dims
		}))
	}
	items=append(items,layout.Rigid(func(gtx layout.Context)layout.Dimensions{
		top:=y
		button:=material.Button(th,&c.newWorkspace,"+ Workspace")
		dims:=layout.UniformInset(unit.Dp(8)).Layout(gtx,button.Layout)
		c.hitRegions=append(c.hitRegions,automationHit{
			Rect:image.Rect(0,top,gtx.Constraints.Max.X,top+dims.Size.Y),
			Kind:hitNewWorkspace,
		})
		y+=dims.Size.Y
		return dims
	}))
	return layout.Flex{Axis:layout.Vertical}.Layout(gtx,items...)
}

func (c *WorkspaceClient) layoutWorkspace(gtx layout.Context,th *material.Theme,state gomodel.StateDump,origin image.Point)layout.Dimensions{
	workspace:=activeWorkspace(state)
	if workspace==nil {
		label:=material.Label(th,unit.Sp(14),"Creating workspace…")
		return layout.Center.Layout(gtx,label.Layout)
	}
	tabHeight:=0
	return layout.Flex{Axis:layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context)layout.Dimensions{
			dims:=c.layoutTabs(gtx,th,*workspace,origin)
			tabHeight=dims.Size.Y
			return dims
		}),
		layout.Flexed(1,func(gtx layout.Context)layout.Dimensions{
			tab:=activeTab(*workspace)
			if tab==nil {
				label:=material.Label(th,unit.Sp(14),"No active tab")
				return layout.Center.Layout(gtx,label.Layout)
			}
			var root paneTree
			if err:=json.Unmarshal(tab.Tree,&root);err!=nil {
				label:=material.Label(th,unit.Sp(14),"Invalid pane tree")
				return layout.Center.Layout(gtx,label.Layout)
			}
			return c.layoutPane(gtx,th,&root,*tab,origin.Add(image.Pt(0,tabHeight)))
		}),
	)
}

func activeWorkspace(state gomodel.StateDump)*gomodel.WorkspaceDump{
	if state.ActiveWorkspace!=nil{
		for idx:=range state.Workspaces{if state.Workspaces[idx].ID==*state.ActiveWorkspace{return &state.Workspaces[idx]}}
	}
	if state.Workspace!=nil{return state.Workspace}
	if len(state.Workspaces)>0{return &state.Workspaces[0]}
	return nil
}

func activeTab(workspace gomodel.WorkspaceDump)*gomodel.TabDump{
	if workspace.ActiveTab!=nil{
		for idx:=range workspace.Tabs{if workspace.Tabs[idx].ID==*workspace.ActiveTab{return &workspace.Tabs[idx]}}
	}
	if len(workspace.Tabs)>0{return &workspace.Tabs[0]}
	return nil
}

func (c *WorkspaceClient) layoutTabs(gtx layout.Context,th *material.Theme,workspace gomodel.WorkspaceDump,origin image.Point)layout.Dimensions{
	children:=make([]layout.FlexChild,0,len(workspace.Tabs)+1)
	x:=0
	for _,tab:=range workspace.Tabs {
		t:=tab
		click:=c.tabClicks[t.ID]
		if click==nil{click=new(widget.Clickable);c.tabClicks[t.ID]=click}
		for click.Clicked(gtx){_ = c.session.DispatchAsync(map[string]any{"type":"tab.activate","tab_id":t.ID})}
		children=append(children,layout.Rigid(func(gtx layout.Context)layout.Dimensions{
			left:=x
			button:=material.Button(th,click,t.Title)
			dims:=layout.Inset{Left:unit.Dp(4),Top:unit.Dp(4),Bottom:unit.Dp(4)}.Layout(gtx,button.Layout)
			c.hitRegions=append(c.hitRegions,automationHit{
				Rect:image.Rect(origin.X+left,origin.Y,origin.X+left+dims.Size.X,origin.Y+dims.Size.Y),
				Kind:hitTab,ID:t.ID,
			})
			x+=dims.Size.X
			return dims
		}))
	}
	children=append(children,layout.Rigid(func(gtx layout.Context)layout.Dimensions{
		left:=x
		button:=material.Button(th,&c.newTab,"+")
		dims:=layout.UniformInset(unit.Dp(4)).Layout(gtx,button.Layout)
		c.hitRegions=append(c.hitRegions,automationHit{
			Rect:image.Rect(origin.X+left,origin.Y,origin.X+left+dims.Size.X,origin.Y+dims.Size.Y),
			Kind:hitNewTab,
		})
		x+=dims.Size.X
		return dims
	}))
	return layout.Flex{Axis:layout.Horizontal}.Layout(gtx,children...)
}

func (c *WorkspaceClient) layoutPane(gtx layout.Context,th *material.Theme,node *paneTree,tab gomodel.TabDump,origin image.Point)layout.Dimensions{
	if node==nil{return layout.Dimensions{}}
	if node.Type=="split" {
		ratio:=node.Ratio
		if ratio<=0.05||ratio>=0.95{ratio=0.5}
		axis:=layout.Horizontal
		if node.Axis=="vertical"{axis=layout.Vertical}
		if axis==layout.Horizontal {
			firstWidth:=int(float32(gtx.Constraints.Max.X)*ratio)
			return layout.Flex{Axis:axis}.Layout(gtx,
				layout.Flexed(ratio,func(gtx layout.Context)layout.Dimensions{return c.layoutPane(gtx,th,node.First,tab,origin)}),
				layout.Flexed(1-ratio,func(gtx layout.Context)layout.Dimensions{return c.layoutPane(gtx,th,node.Second,tab,origin.Add(image.Pt(firstWidth,0)))}),
			)
		}
		firstHeight:=int(float32(gtx.Constraints.Max.Y)*ratio)
		return layout.Flex{Axis:axis}.Layout(gtx,
			layout.Flexed(ratio,func(gtx layout.Context)layout.Dimensions{return c.layoutPane(gtx,th,node.First,tab,origin)}),
			layout.Flexed(1-ratio,func(gtx layout.Context)layout.Dimensions{return c.layoutPane(gtx,th,node.Second,tab,origin.Add(image.Pt(0,firstHeight)))}),
		)
	}
	c.hitRegions=append(c.hitRegions,automationHit{
		Rect:image.Rectangle{Min:origin,Max:origin.Add(gtx.Constraints.Max)},
		Kind:hitPane,ID:node.PaneID,
	})
	if node.Terminal==nil {
		label:=material.Label(th,unit.Sp(14),"Empty pane")
		return layout.Center.Layout(gtx,label.Layout)
	}
	id:=node.Terminal.Summary.TerminalID
	c.mu.RLock();term:=c.terminals[id];c.mu.RUnlock()
	if term==nil {
		label:=material.Label(th,unit.Sp(14),"Attaching terminal…")
		return layout.Center.Layout(gtx,label.Layout)
	}
	term.mu.RLock()
	snapshot:=term.snapshot
	selection:=term.selection
	term.mu.RUnlock()

	c.ensureTerminalSize(gtx,term)
	cellWidth:=gtx.Dp(term.view.CellWidth)
	lineHeight:=gtx.Dp(term.view.LineHeight)
	term.input.Process(gtx,snapshot,cellWidth,lineHeight)
	dims:=term.view.Layout(gtx,th,snapshot,selection)
	term.input.Add(gtx,dims.Size)
	return dims
}

func (c *WorkspaceClient) ensureTerminalSize(gtx layout.Context,term *terminalClient){
	cellWidth:=gtx.Dp(term.view.CellWidth)
	lineHeight:=gtx.Dp(term.view.LineHeight)
	if cellWidth<1||lineHeight<1{return}
	cols:=gtx.Constraints.Max.X/cellWidth
	rows:=gtx.Constraints.Max.Y/lineHeight
	if cols<2{cols=2};if cols>512{cols=512}
	if rows<1{rows=1};if rows>256{rows=256}

	term.mu.Lock()
	if cols==term.cols&&rows==term.rows{term.mu.Unlock();return}
	term.cols,term.rows=cols,rows
	term.emu.Resize(cols,rows)
	term.snapshot=term.emu.Snapshot()
	term.mu.Unlock()
	_ = c.session.DispatchAsync(map[string]any{
		"type":"terminal.resize","terminal_id":term.id,"columns":cols,"lines":rows,
		"cell_width":cellWidth,"cell_height":lineHeight,
	})
}

func (c *WorkspaceClient) Background() color.NRGBA {
	return DefaultTerminalTheme().Background
}

func exact(gtx layout.Context,size image.Point)layout.Context{
	gtx.Constraints=layout.Exact(size)
	return gtx
}


func configColor(value string,fallback uint32)color.NRGBA{
	rgb:=goconfig.ParseColor(value,fallback)
	return color.NRGBA{R:uint8(rgb>>16),G:uint8(rgb>>8),B:uint8(rgb),A:0xff}
}


func (c *WorkspaceClient) automationClick(x,y float32,count int)bool{
	c.layoutMu.Lock()
	defer c.layoutMu.Unlock()
	if count<1{count=1}

	size:=c.frameSize
	if size.X<=0||size.Y<=0{
		size=image.Pt(int(c.config.Startup.WindowWidth),int(c.config.Startup.WindowHeight))
	}
	if size.X<1{size.X=1};if size.Y<1{size.Y=1}
	metric:=c.frameMetric
	point:=image.Pt(int(x),int(y))

	var router input.Router
	th:=material.NewTheme()
	now:=time.Now()
	frame:=func(frameNow time.Time){
		var ops op.Ops
		gtx:=layout.Context{
			Constraints:layout.Exact(size),
			Metric:metric,
			Now:frameNow,
			Source:router.Source(),
			Ops:&ops,
		}
		c.hitRegions=c.hitRegions[:0]
		c.layoutUnlocked(gtx,th)
		router.Frame(&ops)
	}

	// First frame registers the same widget tags and clip regions used by the
	// real window. The synthetic pointer events below are then routed by Gio
	// itself instead of directly invoking widget callbacks.
	frame(now)
	handled:=false
	for _,hit:=range c.hitRegions{
		if point.In(hit.Rect){handled=true;break}
	}
	if !handled{return false}

	position:=f32.Pt(x,y)
	for n:=0;n<count;n++{
		eventTime:=now.Add(time.Duration(n+1)*50*time.Millisecond)
		router.Queue(
			pointer.Event{
				Kind:pointer.Press,
				Source:pointer.Mouse,
				PointerID:1,
				Buttons:pointer.ButtonPrimary,
				Position:position,
				Time:eventTime,
			},
			pointer.Event{
				Kind:pointer.Release,
				Source:pointer.Mouse,
				PointerID:1,
				Position:position,
				Time:eventTime.Add(10*time.Millisecond),
			},
		)
		frame(eventTime.Add(10*time.Millisecond))
	}
	if c.invalidate!=nil{c.invalidate()}
	return true
}
