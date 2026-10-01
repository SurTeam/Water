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
	"sync/atomic"
	"time"

	"github.com/SurTeam/Water/internal/goprotocol"
	"github.com/SurTeam/Water/internal/goterminal"
	"github.com/google/uuid"
)

type Server struct {
	SocketPath string
	Build      string
	Version    string

	registry *goterminal.Registry
	listener net.Listener
	closing  chan struct{}
	once     sync.Once

	opSeq atomic.Uint64
	opsMu sync.RWMutex
	ops   map[uint64]OperationSnapshot
}

type OperationSnapshot struct {
	ID      uint64               `json:"id"`
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
		closing:    make(chan struct{}),
		ops:        make(map[uint64]OperationSnapshot),
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
	defer ss.close()

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
			"ui_sessions":      0,
		}))
	case "session.open":
		return ss.write(goprotocol.Success(msg.RequestID, map[string]any{
			"server_pid":       os.Getpid(),
			"protocol_version": goprotocol.ProtocolVersion,
			"server_version":   s.Version,
			"api_signature":    goprotocol.APISignature,
			"socket_path":      s.SocketPath,
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
		replay := t.Replay()
		wire := make([]goprotocol.WireTerminalEvent, 0, len(replay))
		for _, ev := range replay {
			wire = append(wire, ev.Wire())
		}
		return ss.write(goprotocol.Success(msg.RequestID, map[string]any{
			"terminal_id": p.TerminalID,
			"size":        t.Size(),
			"events":      wire,
		}))
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
			OperationID uint64 `json:"operation_id"`
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

	ch, cancel := t.Subscribe()
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
		for ev := range ch {
			if err := ss.writeTerminal(id, ev); err != nil {
				cancel()
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
		return errors.New("terminal not found")
	}
	deadline := time.Now().Add(time.Duration(p.TimeoutMS) * time.Millisecond)
	for {
		var all []byte
		for _, ev := range t.Replay() {
			if ev.Kind == goprotocol.OutputEvent {
				all = append(all, ev.Data...)
			}
		}
		if containsBytes(all, []byte(p.Text)) {
			return ss.write(goprotocol.Success(msg.RequestID, map[string]any{
				"terminal_id": p.TerminalID,
				"contains":    true,
			}))
		}
		if time.Now().After(deadline) {
			return ss.write(goprotocol.Success(msg.RequestID, map[string]any{
				"terminal_id": p.TerminalID,
				"contains":    false,
			}))
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
		return errors.New("terminal not found")
	}
	deadline := time.Now().Add(time.Duration(p.TimeoutMS) * time.Millisecond)
	for {
		replay := t.Replay()
		if len(replay) > 0 && replay[len(replay)-1].Kind == goprotocol.ExitEvent {
			return ss.write(goprotocol.Success(msg.RequestID, map[string]any{
				"terminal_id": p.TerminalID,
				"exited":      true,
			}))
		}
		if time.Now().After(deadline) {
			return ss.write(goprotocol.Success(msg.RequestID, map[string]any{
				"terminal_id": p.TerminalID,
				"exited":      false,
			}))
		}
		time.Sleep(10 * time.Millisecond)
	}
}
