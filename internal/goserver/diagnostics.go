package goserver

import (
	"encoding/json"
	"sync/atomic"

	"github.com/SurTeam/Water/internal/goprotocol"
)

var (
	metricStateDumps atomic.Uint64
	metricSnapshotPushes atomic.Uint64
	metricTerminalStreamEvents atomic.Uint64
	metricTerminalOutputBytes atomic.Uint64
)

func (s *Server) eventList(ss *session, msg goprotocol.WireMessage) error {
	var p struct {
		AfterSequence *uint64 `json:"after_sequence"`
	}
	if len(msg.Params)>0 {
		if err:=json.Unmarshal(msg.Params,&p);err!=nil{return err}
	}
	return ss.write(goprotocol.Success(msg.RequestID,s.eventsSince(p.AfterSequence)))
}

func (s *Server) eventsSince(after *uint64) []any {
	// Event history is populated by command/terminal hooks; keep the method
	// stable even before the first event.
	s.eventsMu.RLock()
	defer s.eventsMu.RUnlock()
	threshold:=uint64(0)
	if after!=nil{threshold=*after}
	out:=make([]any,0,len(s.events))
	for _,event:=range s.events {
		if event.Sequence>threshold{out=append(out,event)}
	}
	return out
}

func (s *Server) metricsSnapshot() map[string]any {
	return map[string]any{
		"pty_bytes_read": uint64(0),
		"pty_read_calls": uint64(0),
		"pty_read_would_block": uint64(0),
		"pty_reader_poll_wakeups": uint64(0),
		"pty_reader_zero_event_wakeups": uint64(0),
		"pty_reader_spurious_wakeups": uint64(0),
		"pty_reader_pty_ready_wakeups": uint64(0),
		"pty_reader_empty_readiness": uint64(0),
		"pty_reader_empty_wake_backoffs": uint64(0),
		"terminal_output_bytes_sent": metricTerminalOutputBytes.Load(),
		"terminal_stream_events": metricTerminalStreamEvents.Load(),
		"terminal_resize_events": uint64(0),
		"state_dumps": metricStateDumps.Load(),
		"model_snapshot_pushes": metricSnapshotPushes.Load(),
		"replay_ring_bytes": uint64(0),
		"terminal_bytes_received": uint64(0),
		"processor_advances": uint64(0),
		"terminal_bytes_advanced": uint64(0),
		"bytes_per_advance": float64(0),
		"terminal_notifies": uint64(0),
		"terminal_renders": uint64(0),
		"hidden_terminal_updates": uint64(0),
		"replay_bytes_received": uint64(0),
		"terminal_server_queue_events": uint64(0),
		"terminal_server_queue_bytes": uint64(0),
		"terminal_server_queue_max_events": uint64(0),
		"terminal_server_queue_max_bytes": uint64(0),
		"terminal_client_queue_events": uint64(0),
		"terminal_client_queue_bytes": uint64(0),
		"terminal_client_queue_max_events": uint64(0),
		"terminal_client_queue_max_bytes": uint64(0),
	}
}

type appEvent struct {
	Sequence uint64 `json:"sequence"`
	StateRevision uint64 `json:"state_revision"`
	Kind any `json:"kind"`
}


func (s *Server) emitEvent(kind map[string]any) {
	seq:=s.eventSeq.Add(1)
	revision:=s.model.Revision()
	event:=appEvent{Sequence:seq,StateRevision:revision,Kind:kind}
	s.eventsMu.Lock()
	const maxEvents=4096
	if len(s.events)==maxEvents {
		copy(s.events,s.events[1:])
		s.events[len(s.events)-1]=event
	} else {
		s.events=append(s.events,event)
	}
	s.eventsMu.Unlock()
}

func (s *Server) recordCommandEvents(commandType string,raw json.RawMessage,result any) {
	eventType:=""
	switch commandType {
	case "workspace.create","workspace.new","workspace.ensure":
		eventType="workspace_created"
	case "workspace.close","workspace.delete":
		eventType="workspace_closed"
	case "workspace.activate":
		eventType="workspace_activated"
	case "workspace.rename":
		eventType="workspace_renamed"
	case "workspace.reorder":
		eventType="workspace_reordered"
	case "tab.new","tab.new_in_workspace":
		eventType="tab_created"
	case "tab.rename":
		eventType="tab_renamed"
	case "tab.move_to_workspace":
		eventType="tab_moved_to_workspace"
	case "tab.close":
		eventType="tab_closed"
	case "tab.activate":
		eventType="tab_activated"
	case "pane.close":
		eventType="pane_closed"
	case "pane.focus":
		eventType="pane_focused"
	case "pane.resize":
		eventType="pane_resized"
	case "pane.resize_split":
		eventType="split_resized"
	case "pane.move_to_workspace":
		eventType="pane_moved_to_workspace"
	case "pane.promote_to_tab":
		eventType="pane_promoted_to_tab"
	case "pane.agent_rename":
		eventType="agent_renamed"
	case "surface.replace":
		eventType="surface_changed"
	case "terminal.spawn":
		eventType="terminal_spawned"
	case "terminal.resize":
		eventType="terminal_resized"
	default:
		return
	}
	fields:=map[string]any{"type":eventType}
	if data,err:=json.Marshal(result);err==nil {
		var resultFields map[string]any
		if json.Unmarshal(data,&resultFields)==nil {
			for key,value:=range resultFields {
				if key!="type"{fields[key]=value}
			}
		}
	}
	var commandFields map[string]any
	if json.Unmarshal(raw,&commandFields)==nil {
		for _,key:=range []string{"workspace_id","tab_id","pane_id","target_pane","new_pane","ratio","index","label","columns","lines"} {
			if value,ok:=commandFields[key];ok {
				if _,exists:=fields[key];!exists{fields[key]=value}
			}
		}
	}
	s.emitEvent(fields)

	if commandType=="pane.split" {
		created:=map[string]any{"type":"pane_created"}
		if value,ok:=fields["pane_id"];ok{created["pane_id"]=value}
		s.emitEvent(created)
	}
}
