package goserver

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/SurTeam/Water/internal/goagent"
	"github.com/SurTeam/Water/internal/gomodel"
	"github.com/SurTeam/Water/internal/goprotocol"
	"github.com/google/uuid"
)

func (s *Server) command(ss *session, msg goprotocol.WireMessage) error {
	var p struct {
		Command json.RawMessage `json:"command"`
	}
	if err := json.Unmarshal(msg.Params, &p); err != nil {
		return err
	}
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(p.Command, &head); err != nil {
		return err
	}

	id := uuid.New()
	op := OperationSnapshot{
		ID:      id,
		Command: append(json.RawMessage(nil), p.Command...),
		Status:  "running",
	}
	result, rpcErr := s.executeCommand(head.Type, p.Command)
	if rpcErr != nil {
		op.Status = "failed"
		op.Error = rpcErr
	} else {
		op.Status = "succeeded"
		op.Result = result
		s.recordCommandEvents(head.Type, p.Command, result)
		s.broadcastSnapshot()
	}
	s.opsMu.Lock()
	s.ops[id] = op
	s.opOrder = append(s.opOrder, id)
	const maxOperationHistory = 4096
	if len(s.opOrder) > maxOperationHistory {
		drop := s.opOrder[0]
		s.opOrder = s.opOrder[1:]
		delete(s.ops, drop)
	}
	s.opsMu.Unlock()

	return ss.write(goprotocol.Success(msg.RequestID, map[string]uuid.UUID{
		"operation_id": id,
	}))
}

func (s *Server) executeCommand(kind string, raw json.RawMessage) (any, *goprotocol.RPCError) {
	fail := func(code string, err error) (any, *goprotocol.RPCError) {
		return nil, &goprotocol.RPCError{Code: code, Message: err.Error()}
	}

	switch kind {
	case "workspace.create":
		id := s.model.CreateWorkspace("")
		return map[string]any{"type": "workspace_created", "workspace_id": id}, nil

	case "workspace.ensure":
		id := s.model.EnsureWorkspace()
		return map[string]any{"type": "workspace_created", "workspace_id": id}, nil

	case "workspace.new":
		wid := s.model.CreateWorkspace("")
		_, paneID, err := s.model.CreateTab(wid, "", false)
		if err != nil {
			return fail("WORKSPACE_CREATE_FAILED", err)
		}
		if _, err := s.spawnInPane(paneID, "", nil, s.defaultTerminalSize()); err != nil {
			return fail("TERMINAL_SPAWN_FAILED", err)
		}
		return map[string]any{"type": "workspace_created", "workspace_id": wid}, nil

	case "workspace.activate":
		var c struct{ WorkspaceID *uuid.UUID `json:"workspace_id"` }
		if err := json.Unmarshal(raw, &c); err != nil { return fail("INVALID_COMMAND", err) }
		id, err := s.model.ResolveWorkspace(c.WorkspaceID)
		if err != nil { return fail("WORKSPACE_NOT_FOUND", err) }
		if err := s.model.ActivateWorkspace(id); err != nil { return fail("WORKSPACE_NOT_FOUND", err) }
		return map[string]any{"type":"workspace_activated","workspace_id":id},nil

	case "workspace.rename":
		var c struct {
			WorkspaceID *uuid.UUID `json:"workspace_id"`
			Title string `json:"title"`
		}
		if err:=json.Unmarshal(raw,&c);err!=nil{return fail("INVALID_COMMAND",err)}
		id,err:=s.model.ResolveWorkspace(c.WorkspaceID);if err!=nil{return fail("WORKSPACE_NOT_FOUND",err)}
		if err:=s.model.RenameWorkspace(id,c.Title);err!=nil{return fail("WORKSPACE_NOT_FOUND",err)}
		return map[string]any{"type":"workspace_renamed","workspace_id":id},nil

	case "workspace.reorder":
		var c struct{
			WorkspaceID *uuid.UUID `json:"workspace_id"`
			Index int `json:"index"`
		}
		if err:=json.Unmarshal(raw,&c);err!=nil{return fail("INVALID_COMMAND",err)}
		id,err:=s.model.ResolveWorkspace(c.WorkspaceID);if err!=nil{return fail("WORKSPACE_NOT_FOUND",err)}
		if err:=s.model.ReorderWorkspace(id,c.Index);err!=nil{return fail("WORKSPACE_NOT_FOUND",err)}
		return map[string]any{"type":"none"},nil

	case "workspace.close", "workspace.delete":
		var c struct{WorkspaceID *uuid.UUID `json:"workspace_id"`}
		if err:=json.Unmarshal(raw,&c);err!=nil{return fail("INVALID_COMMAND",err)}
		id,err:=s.model.ResolveWorkspace(c.WorkspaceID);if err!=nil{return fail("WORKSPACE_NOT_FOUND",err)}
		terms,err:=s.model.CloseWorkspace(id);if err!=nil{return fail("WORKSPACE_NOT_FOUND",err)}
		for _,tid:=range terms{s.registry.Remove(tid)}
		return map[string]any{"type":"workspace_closed","workspace_id":id},nil

	case "tab.new", "tab.new_in_workspace":
		var c struct{
			WorkspaceID *uuid.UUID `json:"workspace_id"`
			Title *string `json:"title"`
		}
		if err:=json.Unmarshal(raw,&c);err!=nil{return fail("INVALID_COMMAND",err)}
		wid,err:=s.model.ResolveWorkspace(c.WorkspaceID);if err!=nil{return fail("WORKSPACE_NOT_FOUND",err)}
		title:="";pinned:=false;if c.Title!=nil{title=*c.Title;pinned=true}
		tabID,paneID,err:=s.model.CreateTab(wid,title,pinned);if err!=nil{return fail("TAB_CREATE_FAILED",err)}
		if _,err:=s.spawnInPane(paneID,"",nil,s.defaultTerminalSize());err!=nil{
			return fail("TERMINAL_SPAWN_FAILED",err)
		}
		return map[string]any{"type":"tab_created","tab_id":tabID},nil

	case "tab.rename":
		var c struct{TabID *uuid.UUID `json:"tab_id"`;Title string `json:"title"`}
		if err:=json.Unmarshal(raw,&c);err!=nil{return fail("INVALID_COMMAND",err)}
		id,err:=s.model.ResolveTab(c.TabID,nil);if err!=nil{return fail("TAB_NOT_FOUND",err)}
		if err:=s.model.RenameTab(id,c.Title);err!=nil{return fail("TAB_NOT_FOUND",err)}
		return map[string]any{"type":"tab_renamed","tab_id":id},nil

	case "tab.activate":
		var c struct{TabID *uuid.UUID `json:"tab_id"`;Index *int `json:"index"`}
		if err:=json.Unmarshal(raw,&c);err!=nil{return fail("INVALID_COMMAND",err)}
		id,err:=s.model.ResolveTab(c.TabID,c.Index);if err!=nil{return fail("TAB_NOT_FOUND",err)}
		if err:=s.model.ActivateTab(id);err!=nil{return fail("TAB_NOT_FOUND",err)}
		return map[string]any{"type":"tab_activated","tab_id":id},nil

	case "tab.close":
		var c struct{TabID *uuid.UUID `json:"tab_id"`}
		if err:=json.Unmarshal(raw,&c);err!=nil{return fail("INVALID_COMMAND",err)}
		id,err:=s.model.ResolveTab(c.TabID,nil);if err!=nil{return fail("TAB_NOT_FOUND",err)}
		terms,err:=s.model.CloseTab(id);if err!=nil{return fail("TAB_NOT_FOUND",err)}
		for _,tid:=range terms{s.registry.Remove(tid)}
		return map[string]any{"type":"tab_closed","tab_id":id},nil

	case "tab.move_to_workspace":
		var c struct{TabID uuid.UUID `json:"tab_id"`;WorkspaceID uuid.UUID `json:"workspace_id"`}
		if err:=json.Unmarshal(raw,&c);err!=nil{return fail("INVALID_COMMAND",err)}
		if err:=s.model.MoveTab(c.TabID,c.WorkspaceID);err!=nil{return fail("TAB_MOVE_FAILED",err)}
		return map[string]any{"type":"tab_moved_to_workspace","tab_id":c.TabID,"workspace_id":c.WorkspaceID},nil

	case "pane.split":
		var c struct{PaneID *uuid.UUID `json:"pane_id"`;Direction string `json:"direction"`}
		if err:=json.Unmarshal(raw,&c);err!=nil{return fail("INVALID_COMMAND",err)}
		_,pid,err:=s.model.ResolvePane(c.PaneID);if err!=nil{return fail("PANE_NOT_FOUND",err)}
		newPane,err:=s.model.SplitPane(pid,c.Direction);if err!=nil{return fail("PANE_SPLIT_FAILED",err)}
		if _,err:=s.spawnInPane(newPane,"",nil,s.defaultTerminalSize());err!=nil{
			return fail("TERMINAL_SPAWN_FAILED",err)
		}
		return map[string]any{"type":"pane_created","pane_id":newPane},nil

	case "pane.close":
		var c struct{PaneID *uuid.UUID `json:"pane_id"`}
		if err:=json.Unmarshal(raw,&c);err!=nil{return fail("INVALID_COMMAND",err)}
		_,pid,err:=s.model.ResolvePane(c.PaneID);if err!=nil{return fail("PANE_NOT_FOUND",err)}
		terms,err:=s.model.ClosePane(pid);if err!=nil{return fail("PANE_CLOSE_FAILED",err)}
		for _,tid:=range terms{s.registry.Remove(tid)}
		return map[string]any{"type":"pane_closed","pane_id":pid},nil

	case "pane.focus":
		var c struct{PaneID *uuid.UUID `json:"pane_id"`;Direction *string `json:"direction"`}
		if err:=json.Unmarshal(raw,&c);err!=nil{return fail("INVALID_COMMAND",err)}
		tabID,pid,err:=s.model.ResolvePane(c.PaneID);if err!=nil{return fail("PANE_NOT_FOUND",err)}
		if c.Direction!=nil{
			target,ok:=s.model.DirectionalPane(tabID,pid,*c.Direction)
			if !ok{return fail("FOCUS_TARGET_NOT_FOUND",errors.New("no pane is available in the requested direction"))}
			pid=target
		}
		if err:=s.model.FocusPane(pid);err!=nil{return fail("PANE_NOT_FOUND",err)}
		return map[string]any{"type":"pane_focused","pane_id":pid},nil

	case "pane.resize":
		var c struct{PaneID *uuid.UUID `json:"pane_id"`;Ratio float32 `json:"ratio"`}
		if err:=json.Unmarshal(raw,&c);err!=nil{return fail("INVALID_COMMAND",err)}
		if c.Ratio<0.05 || c.Ratio>0.95{return fail("INVALID_SPLIT",errors.New("split ratio must be between 0.05 and 0.95"))}
		_,pid,err:=s.model.ResolvePane(c.PaneID);if err!=nil{return fail("PANE_NOT_FOUND",err)}
		if err:=s.model.ResizePane(pid,c.Ratio);err!=nil{return fail("PANE_RESIZE_FAILED",err)}
		return map[string]any{"type":"pane_resized","pane_id":pid,"ratio":c.Ratio},nil

	case "pane.resize_split":
		var c struct{TabID uuid.UUID `json:"tab_id"`;Path []bool `json:"path"`;Ratio float32 `json:"ratio"`}
		if err:=json.Unmarshal(raw,&c);err!=nil{return fail("INVALID_COMMAND",err)}
		if c.Ratio<0.05 || c.Ratio>0.95{return fail("INVALID_SPLIT",errors.New("split ratio must be between 0.05 and 0.95"))}
		if err:=s.model.ResizeSplit(c.TabID,c.Path,c.Ratio);err!=nil{return fail("PANE_RESIZE_FAILED",err)}
		return map[string]any{"type":"none"},nil

	case "pane.move_to_workspace":
		var c struct{PaneID *uuid.UUID `json:"pane_id"`;WorkspaceID uuid.UUID `json:"workspace_id"`}
		if err:=json.Unmarshal(raw,&c);err!=nil{return fail("INVALID_COMMAND",err)}
		_,pid,err:=s.model.ResolvePane(c.PaneID);if err!=nil{return fail("PANE_NOT_FOUND",err)}
		move,moved,err:=s.model.MovePaneToWorkspace(pid,c.WorkspaceID)
		if err!=nil{return fail("PANE_MOVE_FAILED",err)}
		if !moved{return map[string]any{"type":"none"},nil}
		return map[string]any{
			"type":"none",
			"pane_id":pid,
			"tab_id":move.TargetTabID,
			"workspace_id":c.WorkspaceID,
		},nil

	case "pane.promote_to_tab":
		var c struct{PaneID *uuid.UUID `json:"pane_id"`}
		if err:=json.Unmarshal(raw,&c);err!=nil{return fail("INVALID_COMMAND",err)}
		_,pid,err:=s.model.ResolvePane(c.PaneID);if err!=nil{return fail("PANE_NOT_FOUND",err)}
		move,moved,err:=s.model.PromotePaneToTab(pid)
		if err!=nil{return fail("PANE_MOVE_FAILED",err)}
		if !moved{return map[string]any{"type":"none"},nil}
		return map[string]any{"type":"tab_created","tab_id":move.TargetTabID},nil

	case "pane.agent_rename":
		var c struct{PaneID *uuid.UUID `json:"pane_id"`;Label string `json:"label"`}
		if err:=json.Unmarshal(raw,&c);err!=nil{return fail("INVALID_COMMAND",err)}
		_,pid,err:=s.model.ResolvePane(c.PaneID);if err!=nil{return fail("PANE_NOT_FOUND",err)}
		if err:=s.model.RenameAgent(pid,c.Label);err!=nil{return fail("AGENT_NOT_FOUND",err)}
		return map[string]any{"type":"none"},nil

	case "surface.replace":
		var c struct{PaneID *uuid.UUID `json:"pane_id"`;Kind string `json:"kind"`}
		if err:=json.Unmarshal(raw,&c);err!=nil{return fail("INVALID_COMMAND",err)}
		if c.Kind!="empty"{return fail("SURFACE_NOT_AVAILABLE",errors.New("only EmptySurface is implemented"))}
		_,pid,err:=s.model.ResolvePane(c.PaneID);if err!=nil{return fail("PANE_NOT_FOUND",err)}
		surfaceID,oldTerminal,err:=s.model.ReplaceSurfaceEmpty(pid);if err!=nil{return fail("PANE_NOT_FOUND",err)}
		if oldTerminal!=nil{s.registry.Remove(*oldTerminal)}
		return map[string]any{"type":"surface_replaced","surface_id":surfaceID},nil

	case "terminal.spawn":
		var c struct {
			PaneID  *uuid.UUID `json:"pane_id"`
			Program string     `json:"program"`
			Args    []string   `json:"args"`
			Columns int        `json:"columns"`
			Lines   int        `json:"lines"`
		}
		if err := json.Unmarshal(raw, &c); err != nil { return fail("INVALID_COMMAND", err) }
		if c.Columns==0{c.Columns=s.Config.Terminal.DefaultColumns}
		if c.Lines==0{c.Lines=s.Config.Terminal.DefaultLines}
		_,paneID,err:=s.model.ResolvePane(c.PaneID)
		if err!=nil{
			wid:=s.model.EnsureWorkspace()
			_,paneID,err=s.model.CreateTab(wid,"",false)
			if err!=nil{return fail("PANE_NOT_FOUND",err)}
		}
		t, err := s.spawnInPane(paneID,c.Program,c.Args,goprotocol.TerminalSize{Columns:c.Columns,Lines:c.Lines})
		if err != nil { return fail("TERMINAL_SPAWN_FAILED", err) }
		return map[string]any{"type":"terminal_spawned","terminal_id":t.ID},nil

	case "terminal.send_text":
		var c struct{TerminalID *uuid.UUID `json:"terminal_id"`;PaneID *uuid.UUID `json:"pane_id"`;Text string `json:"text"`}
		if err:=json.Unmarshal(raw,&c);err!=nil{return fail("INVALID_COMMAND",err)}
		id,err:=s.resolveTerminal(c.TerminalID,c.PaneID);if err!=nil{return fail("TERMINAL_NOT_FOUND",err)}
		t,_:=s.registry.Get(id);if err:=t.Write([]byte(c.Text));err!=nil{return fail("TERMINAL_WRITE_FAILED",err)}
		return map[string]any{"type":"terminal_text_sent","terminal_id":id},nil

	case "terminal.send_bytes":
		var c struct{TerminalID *uuid.UUID `json:"terminal_id"`;PaneID *uuid.UUID `json:"pane_id"`;Bytes []int `json:"bytes"`}
		if err:=json.Unmarshal(raw,&c);err!=nil{return fail("INVALID_COMMAND",err)}
		id,err:=s.resolveTerminal(c.TerminalID,c.PaneID);if err!=nil{return fail("TERMINAL_NOT_FOUND",err)}
		data:=make([]byte,len(c.Bytes));for i,v:=range c.Bytes{if v<0||v>255{return fail("INVALID_COMMAND",errors.New("byte value out of range"))};data[i]=byte(v)}
		t,_:=s.registry.Get(id);if err:=t.Write(data);err!=nil{return fail("TERMINAL_WRITE_FAILED",err)}
		return map[string]any{"type":"terminal_bytes_sent","terminal_id":id},nil

	case "terminal.resize":
		var c struct{TerminalID *uuid.UUID `json:"terminal_id"`;PaneID *uuid.UUID `json:"pane_id"`;Columns int `json:"columns"`;Lines int `json:"lines"`}
		if err:=json.Unmarshal(raw,&c);err!=nil{return fail("INVALID_COMMAND",err)}
		id,err:=s.resolveTerminal(c.TerminalID,c.PaneID);if err!=nil{return fail("TERMINAL_NOT_FOUND",err)}
		t,_:=s.registry.Get(id);size:=goprotocol.TerminalSize{Columns:c.Columns,Lines:c.Lines}.Normalized()
		if err:=t.Resize(size);err!=nil{return fail("TERMINAL_RESIZE_FAILED",err)}
		s.model.SetTerminalSize(id,size)
		return map[string]any{"type":"terminal_resized","terminal_id":id,"columns":size.Columns,"lines":size.Lines},nil

	case "terminal.scroll":
		var c struct{TerminalID *uuid.UUID `json:"terminal_id"`;PaneID *uuid.UUID `json:"pane_id"`;Lines int `json:"lines"`}
		if err:=json.Unmarshal(raw,&c);err!=nil{return fail("INVALID_COMMAND",err)}
		id,err:=s.resolveTerminal(c.TerminalID,c.PaneID);if err!=nil{return fail("TERMINAL_NOT_FOUND",err)}
		return map[string]any{"type":"terminal_scrolled","terminal_id":id,"lines":c.Lines},nil

	case "terminal.set_viewport_position":
		var c struct{TerminalID *uuid.UUID `json:"terminal_id"`;PaneID *uuid.UUID `json:"pane_id"`;Target int64 `json:"target"`}
		if err:=json.Unmarshal(raw,&c);err!=nil{return fail("INVALID_COMMAND",err)}
		id,err:=s.resolveTerminal(c.TerminalID,c.PaneID);if err!=nil{return fail("TERMINAL_NOT_FOUND",err)}
		return map[string]any{"type":"terminal_viewport_position_set","terminal_id":id,"target":c.Target},nil

	default:
		return nil, &goprotocol.RPCError{Code:"UNSUPPORTED_COMMAND",Message:"Go server does not implement command "+kind+" yet"}
	}
}

func (s *Server) resolveTerminal(terminalID,paneID *uuid.UUID)(uuid.UUID,error){
	if terminalID!=nil{
		if _,ok:=s.registry.Get(*terminalID);ok{return *terminalID,nil}
		return uuid.Nil,errors.New("terminal not found")
	}
	if paneID!=nil{
		if id,ok:=s.model.TerminalForPane(*paneID);ok{
			if _,exists:=s.registry.Get(id);exists{return id,nil}
		}
		return uuid.Nil,errors.New("terminal not found")
	}
	_,pid,err:=s.model.ResolvePane(nil);if err!=nil{return uuid.Nil,err}
	if id,ok:=s.model.TerminalForPane(pid);ok{
		if _,exists:=s.registry.Get(id);exists{return id,nil}
	}
	return uuid.Nil,errors.New("terminal not found")
}

func (s *Server) spawnInPane(paneID uuid.UUID,program string,args []string,size goprotocol.TerminalSize)(*goterminalRef,error){
	cwd:=s.model.PaneCWD(paneID)
	if cwd=="" {
		if workspaceID,ok:=s.model.PaneWorkspace(paneID);ok{
			cwd=s.model.WorkspaceActiveCWD(workspaceID)
		}
	}
	if cwd==""{cwd=s.Config.DefaultCWD()}
	if cwd==""{cwd,_=os.Getwd()}

	if program==""{
		program=s.Config.Shell.Program
		args=append([]string(nil),s.Config.Shell.Args...)
	}
	oldID,hadOld:=s.model.TerminalForPane(paneID)
	t,err:=s.registry.SpawnWithDir(program,args,size,cwd);if err!=nil{return nil,err}
	processName:=strings.TrimLeft(filepath.Base(program),"-")
	cmdline:=append([]string{program},args...)
	meta:=gomodel.TerminalMeta{
		TerminalID:t.ID,SessionID:uuid.New(),Program:program,Args:append([]string(nil),args...),
		Size:t.Size(),ProcessName:processName,CWD:cwd,
		Agent:goagent.Detect(processName,cmdline),
	}
	if err:=s.model.InstallTerminal(paneID,meta);err!=nil{s.registry.Remove(t.ID);return nil,err}

	// Replace transaction order matters: once the model points at the new
	// terminal, an Exit from the retired worker can no longer auto-close this
	// pane out from under the replacement.
	if hadOld{s.registry.Remove(oldID)}

	if meta.Agent!=nil {
		s.emitEvent(map[string]any{
			"type":"agent_started",
			"terminal_id":t.ID,
			"pane_id":paneID,
			"kind":meta.Agent.Kind,
		})
	}

	ch,done,cancel:=t.Subscribe()
	replay:=t.Replay()
	var replayLast uint64
	for _,ev:=range replay{
		if ev.Seq>replayLast{replayLast=ev.Seq}
	}
	if len(replay)>0 && replay[len(replay)-1].Kind==goprotocol.ExitEvent{
		s.handleTerminalLifecycleEvent(t.ID,paneID,meta,replay[len(replay)-1])
		cancel()
		return &goterminalRef{ID:t.ID},nil
	}

	go func(lastSeq uint64){
		defer cancel()
		for {
			select {
			case ev:=<-ch:
				if ev.Seq<=lastSeq{continue}
				lastSeq=ev.Seq
				if s.handleTerminalLifecycleEvent(t.ID,paneID,meta,ev){return}
			case <-done:
				return
			}
		}
	}(replayLast)
	return &goterminalRef{ID:t.ID},nil
}

func (s *Server) handleTerminalLifecycleEvent(terminalID,paneID uuid.UUID,meta gomodel.TerminalMeta,ev goprotocol.TerminalEvent)bool{
	switch ev.Kind {
	case goprotocol.OutputEvent:
		s.emitEvent(map[string]any{"type":"terminal_output_changed","terminal_id":terminalID})
	case goprotocol.ResizeEvent:
		s.emitEvent(map[string]any{
			"type":"terminal_resized","terminal_id":terminalID,
			"columns":ev.Size.Columns,"lines":ev.Size.Lines,
		})
	case goprotocol.ExitEvent:
		s.model.SetTerminalExit(terminalID,ev.Code)
		exitCode:=any(nil)
		if ev.Code!=nil{exitCode=*ev.Code}
		s.emitEvent(map[string]any{
			"type":"terminal_exited","terminal_id":terminalID,"exit_code":exitCode,
		})
		if meta.Agent!=nil {
			s.emitEvent(map[string]any{
				"type":"agent_stopped","terminal_id":terminalID,
				"pane_id":paneID,"kind":meta.Agent.Kind,
			})
		}
		s.model.AutoCloseExitedTerminal(terminalID)
		s.broadcastSnapshot()
		return true
	}
	return false
}

type goterminalRef struct{ID uuid.UUID}

func (s *Server) defaultTerminalSize() goprotocol.TerminalSize {
	return goprotocol.TerminalSize{
		Columns:s.Config.Terminal.DefaultColumns,
		Lines:s.Config.Terminal.DefaultLines,
	}.Normalized()
}
