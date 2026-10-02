package goui

import (
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"fmt"
	"strings"
	"sync"

	"gioui.org/layout"
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
		// Gio pointer injection is platform-owned; return the post-click UI
		// snapshot for protocol parity until synthetic pointer routing is wired.
		return map[string]any{
			"window_count":1,
			"has_active_window":true,
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
		return nil,fmt.Errorf("Gio screenshot capture is not implemented yet")
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
	view.Theme.Foreground=configColor(c.config.Theme.TerminalForeground,0xe4e4e4)
	view.Theme.Background=configColor(c.config.Theme.TerminalBackground,0x2c2c2c)
	view.Theme.Cursor=configColor(c.config.Theme.CursorBackground,0xe4e4e4)
	term:=&terminalClient{
		id:summary.TerminalID,emu:emu,lastSeq:last,
		cols:attached.Size.Columns,rows:attached.Size.Lines,
		view:view,
	}
	term.input=&TerminalInput{OnInput:func(data []byte){
		_ = c.session.DispatchAsync(map[string]any{
			"type":"terminal.send_bytes",
			"terminal_id":summary.TerminalID,
			"bytes":bytesAsInts(data),
		})
	}}
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
	case goprotocol.ResizeEvent:
		term.emu.Resize(push.Event.Size.Columns,push.Event.Size.Lines)
		term.cols=push.Event.Size.Columns
		term.rows=push.Event.Size.Lines
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
	c.mu.RLock()
	state:=c.state
	c.mu.RUnlock()

	for c.newWorkspace.Clicked(gtx) {
		_ = c.session.DispatchAsync(map[string]any{"type":"workspace.new"})
	}
	for c.newTab.Clicked(gtx) {
		_ = c.session.DispatchAsync(map[string]any{"type":"tab.new"})
	}

	return layout.Flex{Axis:layout.Horizontal}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context)layout.Dimensions{
			gtx.Constraints.Min.X=gtx.Dp(unit.Dp(190))
			gtx.Constraints.Max.X=gtx.Dp(unit.Dp(190))
			return c.layoutSidebar(gtx,th,state)
		}),
		layout.Flexed(1,func(gtx layout.Context)layout.Dimensions{
			return c.layoutWorkspace(gtx,th,state)
		}),
	)
}

func (c *WorkspaceClient) layoutSidebar(gtx layout.Context,th *material.Theme,state gomodel.StateDump)layout.Dimensions{
	items:=make([]layout.FlexChild,0,len(state.Workspaces)+2)
	header:=material.Label(th,unit.Sp(16),"Water")
	header.Font.Weight=600
	items=append(items,layout.Rigid(func(gtx layout.Context)layout.Dimensions{
		return layout.UniformInset(unit.Dp(12)).Layout(gtx,header.Layout)
	}))
	for _,workspace:=range state.Workspaces {
		w:=workspace
		click:=c.workspaceClicks[w.ID]
		if click==nil{click=new(widget.Clickable);c.workspaceClicks[w.ID]=click}
		for click.Clicked(gtx) {
			_ = c.session.DispatchAsync(map[string]any{"type":"workspace.activate","workspace_id":w.ID})
		}
		items=append(items,layout.Rigid(func(gtx layout.Context)layout.Dimensions{
			button:=material.Button(th,click,w.Title)
			return layout.Inset{Left:unit.Dp(8),Right:unit.Dp(8),Bottom:unit.Dp(4)}.Layout(gtx,button.Layout)
		}))
	}
	items=append(items,layout.Rigid(func(gtx layout.Context)layout.Dimensions{
		button:=material.Button(th,&c.newWorkspace,"+ Workspace")
		return layout.UniformInset(unit.Dp(8)).Layout(gtx,button.Layout)
	}))
	return layout.Flex{Axis:layout.Vertical}.Layout(gtx,items...)
}

func (c *WorkspaceClient) layoutWorkspace(gtx layout.Context,th *material.Theme,state gomodel.StateDump)layout.Dimensions{
	workspace:=activeWorkspace(state)
	if workspace==nil {
		label:=material.Label(th,unit.Sp(14),"Creating workspace…")
		return layout.Center.Layout(gtx,label.Layout)
	}
	return layout.Flex{Axis:layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context)layout.Dimensions{return c.layoutTabs(gtx,th,*workspace)}),
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
			return c.layoutPane(gtx,th,&root,*tab)
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

func (c *WorkspaceClient) layoutTabs(gtx layout.Context,th *material.Theme,workspace gomodel.WorkspaceDump)layout.Dimensions{
	children:=make([]layout.FlexChild,0,len(workspace.Tabs)+1)
	for _,tab:=range workspace.Tabs {
		t:=tab
		click:=c.tabClicks[t.ID]
		if click==nil{click=new(widget.Clickable);c.tabClicks[t.ID]=click}
		for click.Clicked(gtx){_ = c.session.DispatchAsync(map[string]any{"type":"tab.activate","tab_id":t.ID})}
		children=append(children,layout.Rigid(func(gtx layout.Context)layout.Dimensions{
			button:=material.Button(th,click,t.Title)
			return layout.Inset{Left:unit.Dp(4),Top:unit.Dp(4),Bottom:unit.Dp(4)}.Layout(gtx,button.Layout)
		}))
	}
	children=append(children,layout.Rigid(func(gtx layout.Context)layout.Dimensions{
		button:=material.Button(th,&c.newTab,"+")
		return layout.UniformInset(unit.Dp(4)).Layout(gtx,button.Layout)
	}))
	return layout.Flex{Axis:layout.Horizontal}.Layout(gtx,children...)
}

func (c *WorkspaceClient) layoutPane(gtx layout.Context,th *material.Theme,node *paneTree,tab gomodel.TabDump)layout.Dimensions{
	if node==nil{return layout.Dimensions{}}
	if node.Type=="split" {
		ratio:=node.Ratio
		if ratio<=0.05||ratio>=0.95{ratio=0.5}
		axis:=layout.Horizontal
		if node.Axis=="vertical"{axis=layout.Vertical}
		return layout.Flex{Axis:axis}.Layout(gtx,
			layout.Flexed(ratio,func(gtx layout.Context)layout.Dimensions{return c.layoutPane(gtx,th,node.First,tab)}),
			layout.Flexed(1-ratio,func(gtx layout.Context)layout.Dimensions{return c.layoutPane(gtx,th,node.Second,tab)}),
		)
	}
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
	term.mu.RUnlock()

	c.ensureTerminalSize(gtx,term)
	term.input.Process(gtx,snapshot)
	dims:=term.view.Layout(gtx,th,snapshot)
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
