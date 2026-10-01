package goserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/SurTeam/Water/internal/gomodel"
	"github.com/SurTeam/Water/internal/goprotocol"
	"github.com/SurTeam/Water/internal/goterminal"
	"github.com/google/uuid"
)

type Server struct {
	SocketPath string
	Build      string
	Version    string

	registry *goterminal.Registry
	model    *gomodel.Model
	listener net.Listener
	closing  chan struct{}
	once     sync.Once

	opsMu sync.RWMutex
	ops   map[uuid.UUID]OperationSnapshot

	sessionsMu sync.RWMutex
	sessions map[*session]struct{}
}

type OperationSnapshot struct {
	ID      uuid.UUID            `json:"id"`
	Command json.RawMessage      `json:"command"`
	Status  string               `json:"status"`
	Result  any                  `json:"result,omitempty"`
	Error   *goprotocol.RPCError `json:"error,omitempty"`
}

type terminalAttachResponse struct {
	TerminalID uuid.UUID                      `json:"terminal_id"`
	FirstSeq   *uint64                        `json:"first_seq"`
	LastSeq    uint64                         `json:"last_seq"`
	Size       goprotocol.TerminalSize        `json:"size"`
	Replay     []goprotocol.WireTerminalEvent `json:"replay"`
}

func New(socketPath string) *Server {
	return &Server{
		SocketPath: socketPath,
		Build:      "dev",
		Version:    "go-rewrite",
		registry:   goterminal.NewRegistry(),
		model:      gomodel.New(),
		closing:    make(chan struct{}),
		ops:        make(map[uuid.UUID]OperationSnapshot),
		sessions:   make(map[*session]struct{}),
	}
}

func (s *Server) ListenAndServe() error {
	if s.SocketPath == "" {
		return errors.New("socket path is required")
	}
	if err := os.MkdirAll(filepath.Dir(s.SocketPath), 0o755); err != nil {
		return err
	}
	_ = os.Remove(s.SocketPath)
	ln, err := net.Listen("unix", s.SocketPath)
	if err != nil {
		return err
	}
	if err := os.Chmod(s.SocketPath, 0o600); err != nil {
		_ = ln.Close()
		return err
	}
	s.listener = ln
	defer os.Remove(s.SocketPath)

	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-s.closing:
				return nil
			default:
				return err
			}
		}
		go s.handleConn(conn)
	}
}

func (s *Server) Close() error {
	var err error
	s.once.Do(func() {
		close(s.closing)
		s.registry.CloseAll()
		if s.listener != nil {
			err = s.listener.Close()
		}
	})
	return err
}

type session struct {
	conn        net.Conn
	mu          sync.Mutex
	attachments map[uuid.UUID]func()
	compactSnapshots bool
}

func (ss *session) write(v any) error {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	return goprotocol.WriteJSON(ss.conn, v)
}

func (ss *session) writeTerminal(id uuid.UUID, ev goprotocol.TerminalEvent) error {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	return goprotocol.WriteTerminal(ss.conn, id, ev)
}

func (ss *session) close() {
	for _, cancel := range ss.attachments {
		cancel()
	}
	_ = ss.conn.Close()
}

func (s *Server) handleConn(conn net.Conn) {
	ss := &session{conn: conn, attachments: make(map[uuid.UUID]func())}
	defer func() {
		s.dropSession(ss)
		ss.close()
	}()

	for {
		frame, err := goprotocol.ReadFrame(conn)
		if err != nil {
			if !errors.Is(err, io.EOF) {
				// Session failures must not take down the server.
			}
			return
		}
		if frame.Terminal != nil {
			_ = ss.write(goprotocol.Failure(0, "INVALID_FRAME", "client terminal frames are not accepted"))
			continue
		}
		var msg goprotocol.WireMessage
		if err := json.Unmarshal(frame.JSON, &msg); err != nil {
			_ = ss.write(goprotocol.Failure(0, "INVALID_JSON", err.Error()))
			continue
		}
		if msg.ProtocolVersion != goprotocol.ProtocolVersion {
			_ = ss.write(goprotocol.Failure(msg.RequestID, "PROTOCOL_MISMATCH",
				fmt.Sprintf("got %d want %d", msg.ProtocolVersion, goprotocol.ProtocolVersion)))
			continue
		}
		if err := s.dispatch(ss, msg); err != nil {
			_ = ss.write(goprotocol.Failure(msg.RequestID, "REQUEST_FAILED", err.Error()))
		}
	}
}

func (s *Server) dispatch(ss *session, msg goprotocol.WireMessage) error {
	switch msg.Method {
	case "ping":
		return ss.write(goprotocol.Success(msg.RequestID, map[string]any{
			"protocol_version": goprotocol.ProtocolVersion,
		}))
	case "server.info":
		return ss.write(goprotocol.Success(msg.RequestID, map[string]any{
			"build_variant":    s.Build,
			"server_pid":       os.Getpid(),
			"protocol_version": goprotocol.ProtocolVersion,
			"server_version":   s.Version,
			"api_signature":    goprotocol.APISignature,
			"socket_path":      s.SocketPath,
			"ui_sessions":      s.uiSessionCount(),
		}))
	case "session.open":
		var p struct {
			Role string `json:"role"`
			CompactSnapshots bool `json:"compact_snapshots"`
		}
		if len(msg.Params) > 0 {
			if err := json.Unmarshal(msg.Params, &p); err != nil {
				return err
			}
		}
		if err := ss.write(goprotocol.Success(msg.RequestID, map[string]any{
			"server_pid":       os.Getpid(),
			"protocol_version": goprotocol.ProtocolVersion,
			"server_version":   s.Version,
			"api_signature":    goprotocol.APISignature,
			"socket_path":      s.SocketPath,
		})); err != nil {
			return err
		}
		if p.Role == "gui" {
			ss.compactSnapshots = p.CompactSnapshots
			s.addSession(ss)
			return s.pushSnapshot(ss)
		}
		return nil
	case "state.dump":
		return ss.write(goprotocol.Success(msg.RequestID, s.model.Dump()))
	case "debug.memory":
		return ss.write(goprotocol.Success(msg.RequestID, map[string]any{
			"terminal_count": s.registry.Count(),
			"scrollback_lines": 2000,
			"inactive_scrollback_lines": 500,
			"replay_history_bytes": 8 * 1024 * 1024,
			"retained_replay_bytes": 0,
			"visible_cells": 0,
			"surface_count": 0,
			"shape_cache_entries": 0,
			"image_cache_bytes": 0,
		}))
	case "connection.list":
		return ss.write(goprotocol.Success(msg.RequestID, map[string]any{
			"connections": []any{},
		}))
	case "terminal.replay", "terminal.snapshot":
		var p struct {
			TerminalID uuid.UUID `json:"terminal_id"`
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			return err
		}
		t, ok := s.registry.Get(p.TerminalID)
		if !ok {
			return errors.New("terminal not found")
		}
		return ss.write(goprotocol.Success(msg.RequestID, terminalReplayPayload(p.TerminalID, t)))
	case "terminal.attach":
		var p struct {
			TerminalID uuid.UUID `json:"terminal_id"`
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			return err
		}
		return s.attach(ss, msg.RequestID, p.TerminalID)
	case "terminal.detach":
		var p struct {
			TerminalID uuid.UUID `json:"terminal_id"`
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			return err
		}
		if cancel := ss.attachments[p.TerminalID]; cancel != nil {
			cancel()
			delete(ss.attachments, p.TerminalID)
		}
		return ss.write(goprotocol.Success(msg.RequestID, map[string]any{}))
	case "terminal.contains":
		return s.contains(ss, msg)
	case "terminal.wait_exit":
		return s.waitExit(ss, msg)
	case "command.dispatch":
		return s.command(ss, msg)
	case "operation.get", "operation.wait":
		var p struct {
			OperationID uuid.UUID `json:"operation_id"`
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			return err
		}
		s.opsMu.RLock()
		op, ok := s.ops[p.OperationID]
		s.opsMu.RUnlock()
		if !ok {
			return errors.New("operation not found")
		}
		return ss.write(goprotocol.Success(msg.RequestID, op))
	case "server.shutdown":
		if err := ss.write(goprotocol.Success(msg.RequestID, map[string]bool{"ack": true})); err != nil {
			return err
		}
		go s.Close()
		return nil
	default:
		return fmt.Errorf("unsupported method %q", msg.Method)
	}
}

func (s *Server) attach(ss *session, requestID uint64, id uuid.UUID) error {
	t, ok := s.registry.Get(id)
	if !ok {
		return errors.New("terminal not found")
	}
	if cancel := ss.attachments[id]; cancel != nil {
		cancel()
	}

	ch, done, cancel := t.Subscribe()
	replay := t.Replay()
	var first *uint64
	var last uint64
	wire := make([]goprotocol.WireTerminalEvent, 0, len(replay))
	for i, ev := range replay {
		if i == 0 {
			v := ev.Seq
			first = &v
		}
		last = ev.Seq
		wire = append(wire, ev.Wire())
	}
	resp := terminalAttachResponse{
		TerminalID: id,
		FirstSeq:   first,
		LastSeq:    last,
		Size:       t.Size(),
		Replay:     wire,
	}
	if err := ss.write(goprotocol.Success(requestID, resp)); err != nil {
		cancel()
		return err
	}
	ss.attachments[id] = cancel

	go func() {
		defer cancel()
		for {
			select {
			case ev := <-ch:
				if err := ss.writeTerminal(id, ev); err != nil {
					return
				}
			case <-done:
				return
			}
		}
	}()
	return nil
}

func (s *Server) contains(ss *session, msg goprotocol.WireMessage) error {
	var p struct {
		TerminalID uuid.UUID `json:"terminal_id"`
		Text       string    `json:"text"`
		TimeoutMS  int       `json:"timeout_ms"`
	}
	if err := json.Unmarshal(msg.Params, &p); err != nil {
		return err
	}
	t, ok := s.registry.Get(p.TerminalID)
	if !ok {
		return ss.write(goprotocol.Failure(msg.RequestID, "TERMINAL_NOT_FOUND", "terminal not found"))
	}
	deadline := time.Now().Add(time.Duration(p.TimeoutMS) * time.Millisecond)
	for {
		replay := t.Replay()
		var all []byte
		for _, ev := range replay {
			if ev.Kind == goprotocol.OutputEvent {
				all = append(all, ev.Data...)
			}
		}
		if containsBytes(all, []byte(p.Text)) {
			return ss.write(goprotocol.Success(msg.RequestID, terminalReplayPayload(p.TerminalID, t)))
		}
		if replayExited(replay) {
			return ss.write(goprotocol.Failure(msg.RequestID, "TERMINAL_PROCESS_EXITED", "terminal exited before requested text appeared"))
		}
		if time.Now().After(deadline) {
			return ss.write(goprotocol.Failure(msg.RequestID, "TERMINAL_TIMEOUT", "terminal contains timed out"))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func containsBytes(haystack, needle []byte) bool {
	if len(needle) == 0 {
		return true
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		match := true
		for j := range needle {
			if haystack[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func (s *Server) waitExit(ss *session, msg goprotocol.WireMessage) error {
	var p struct {
		TerminalID uuid.UUID `json:"terminal_id"`
		TimeoutMS  int       `json:"timeout_ms"`
	}
	if err := json.Unmarshal(msg.Params, &p); err != nil {
		return err
	}
	t, ok := s.registry.Get(p.TerminalID)
	if !ok {
		return ss.write(goprotocol.Failure(msg.RequestID, "TERMINAL_NOT_FOUND", "terminal not found"))
	}
	deadline := time.Now().Add(time.Duration(p.TimeoutMS) * time.Millisecond)
	for {
		replay := t.Replay()
		if replayExited(replay) {
			return ss.write(goprotocol.Success(msg.RequestID, terminalReplayPayload(p.TerminalID, t)))
		}
		if time.Now().After(deadline) {
			return ss.write(goprotocol.Failure(msg.RequestID, "TERMINAL_TIMEOUT", "terminal wait-exit timed out"))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func replayExited(replay []goprotocol.TerminalEvent) bool {
	return len(replay) > 0 && replay[len(replay)-1].Kind == goprotocol.ExitEvent
}

func terminalReplayPayload(id uuid.UUID, t *goterminal.Terminal) map[string]any {
	replay := t.Replay()
	wire := make([]goprotocol.WireTerminalEvent, 0, len(replay))
	var first *uint64
	var last uint64
	size := t.Size()
	process := any("running")
	for i, ev := range replay {
		if i == 0 {
			v := ev.Seq
			first = &v
			if ev.Kind == goprotocol.OutputEvent || ev.Kind == goprotocol.ResizeEvent {
				size = ev.Size
			}
		}
		last = ev.Seq
		wire = append(wire, ev.Wire())
		if ev.Kind == goprotocol.ExitEvent {
			process = map[string]any{"exited": map[string]any{"code": ev.Code}}
		}
	}
	return map[string]any{
		"terminal_id": id,
		"process": process,
		"first_seq": first,
		"last_seq": last,
		"size": size,
		"events": wire,
	}
}

func (s *Server) addSession(ss *session) {
	s.sessionsMu.Lock()
	s.sessions[ss] = struct{}{}
	s.sessionsMu.Unlock()
}

func (s *Server) dropSession(ss *session) {
	s.sessionsMu.Lock()
	delete(s.sessions, ss)
	s.sessionsMu.Unlock()
}

func (s *Server) uiSessionCount() int {
	s.sessionsMu.RLock()
	n := len(s.sessions)
	s.sessionsMu.RUnlock()
	return n
}

func (s *Server) pushSnapshot(ss *session) error {
	state := s.model.Dump()
	params, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return ss.write(goprotocol.WireMessage{
		BuildVariant: s.Build,
		ProtocolVersion: goprotocol.ProtocolVersion,
		RequestID: 0,
		Method: "push.snapshot",
		Params: params,
	})
}

func (s *Server) broadcastSnapshot() {
	s.sessionsMu.RLock()
	sessions := make([]*session, 0, len(s.sessions))
	for ss := range s.sessions {
		sessions = append(sessions, ss)
	}
	s.sessionsMu.RUnlock()
	for _, ss := range sessions {
		_ = s.pushSnapshot(ss)
	}
}
