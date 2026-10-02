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
