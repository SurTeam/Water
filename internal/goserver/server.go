package goserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/SurTeam/Water/internal/gobuild"
	"github.com/SurTeam/Water/internal/goconfig"
	"github.com/SurTeam/Water/internal/gometrics"
	"github.com/SurTeam/Water/internal/gomodel"
	"github.com/SurTeam/Water/internal/goprotocol"
	"github.com/SurTeam/Water/internal/goterminal"
	"github.com/google/uuid"
)

type Server struct {
	SocketPath string
	Build      string
	Version    string
	Config     goconfig.AppConfig

	registry *goterminal.Registry
	model    *gomodel.Model
	listener net.Listener
	closing  chan struct{}
	once     sync.Once

	opsMu   sync.RWMutex
	ops     map[uuid.UUID]OperationSnapshot
	opOrder []uuid.UUID

	sessionsMu      sync.RWMutex
	sessions        map[*session]struct{}
	focusedSession  *session
	guiDraining     bool
	sessionsChanged chan struct{}

	uiSeq       atomic.Uint64
	pendingUIMu sync.Mutex
	pendingUI   map[uint64]chan goprotocol.WireMessage

	eventsMu sync.RWMutex
	events   []appEvent
	eventSeq atomic.Uint64
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
	return NewWithConfig(socketPath, goconfig.Default())
}

func NewWithConfig(socketPath string, config goconfig.AppConfig) *Server {
	config = config.Normalized()
	return &Server{
		SocketPath:      socketPath,
		Build:           gobuild.Variant,
		Version:         gobuild.Version,
		Config:          config,
		registry:        goterminal.NewRegistryWithReplayLimit(config.Terminal.ReplayHistoryBytes),
		model:           gomodel.New(),
		closing:         make(chan struct{}),
		ops:             make(map[uuid.UUID]OperationSnapshot),
		sessions:        make(map[*session]struct{}),
		sessionsChanged: make(chan struct{}, 1),
		pendingUI:       make(map[uint64]chan goprotocol.WireMessage),
	}
}

func (s *Server) Initialize(initialWorkspace, initialTerminal bool) error {
	if !initialWorkspace {
		return nil
	}
	if len(s.model.Dump().Workspaces) != 0 {
		return nil
	}
	if initialTerminal {
		_, rpcErr := s.executeCommand("workspace.new", json.RawMessage(`{"type":"workspace.new"}`))
		if rpcErr != nil {
			return rpcErr
		}
		return nil
	}
	s.model.CreateWorkspace("")
	return nil
}

func (s *Server) ListenAndServe() error {
	if s.SocketPath == "" {
		return errors.New("socket path is required")
	}
	if err := os.MkdirAll(filepath.Dir(s.SocketPath), 0o755); err != nil {
		return err
	}
	ln, cleanup, err := listenOwnedSocket(s.SocketPath)
	if err != nil {
		return err
	}
	s.listener = ln
	defer cleanup()
	go s.monitorForegroundProcesses()

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
		s.sessionsMu.RLock()
		for ss := range s.sessions {
			_ = ss.conn.Close()
		}
		s.sessionsMu.RUnlock()
	})
	return err
}

type session struct {
	conn             net.Conn
	mu               sync.Mutex
	attachments      map[uuid.UUID]func()
	compactSnapshots bool
	id               uuid.UUID

	snapshotRequests chan struct{}
	done             chan struct{}
	closeOnce        sync.Once
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
	ss.closeOnce.Do(func() {
		close(ss.done)
		for _, cancel := range ss.attachments {
			cancel()
		}
		_ = ss.conn.Close()
	})
}

func (ss *session) requestSnapshot() {
	select {
	case ss.snapshotRequests <- struct{}{}:
	default:
		// Latest-wins: a pending notification will serialize the newest model
		// revision once the session writer becomes available.
	}
}

func (s *Server) handleConn(conn net.Conn) {
	ss := &session{
		conn:             conn,
		id:               uuid.New(),
		attachments:      make(map[uuid.UUID]func()),
		snapshotRequests: make(chan struct{}, 1),
		done:             make(chan struct{}),
	}
	go s.snapshotLoop(ss)
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
		if msg.OK == nil && (msg.ProtocolVersion != goprotocol.ProtocolVersion || msg.BuildVariant != s.Build) {
			_ = ss.write(goprotocol.Failure(
				msg.RequestID,
				"INCOMPATIBLE_SERVER",
				"protocol version and dev/release variant must match",
			))
			continue
		}
		if msg.OK != nil {
			s.deliverUIReply(msg)
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
			"windows":          s.windowSessions(),
		}))
	case "session.open":
		var p struct {
			Role             string `json:"role"`
			CompactSnapshots bool   `json:"compact_snapshots"`
		}
		if len(msg.Params) > 0 {
			if err := json.Unmarshal(msg.Params, &p); err != nil {
				return err
			}
		}
		isGUI := p.Role == "gui"
		if isGUI {
			ss.compactSnapshots = p.CompactSnapshots
			// Register before acknowledging session.open. Once OpenSession
			// returns, callers must be able to route UI automation immediately.
			if !s.addSession(ss) {
				return errors.New("server is shutting down after the last window closed")
			}
		}
		if err := ss.write(goprotocol.Success(msg.RequestID, map[string]any{
			"window_id":        ss.id,
			"server_pid":       os.Getpid(),
			"protocol_version": goprotocol.ProtocolVersion,
			"server_version":   s.Version,
			"api_signature":    goprotocol.APISignature,
			"socket_path":      s.SocketPath,
		})); err != nil {
			if isGUI {
				s.dropSession(ss)
			}
			return err
		}
		if isGUI {
			return s.pushSnapshot(ss)
		}
		return nil
	case "session.focus":
		var p struct {
			Focused bool `json:"focused"`
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			return err
		}
		s.sessionsMu.Lock()
		if _, ok := s.sessions[ss]; ok && p.Focused {
			s.focusedSession = ss
		}
		s.sessionsMu.Unlock()
		return ss.write(goprotocol.Success(msg.RequestID, map[string]any{}))
	case "session.release":
		var p struct {
			ShutdownIfLast bool `json:"shutdown_if_last"`
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			return err
		}
		shutdown := s.releaseSession(ss, p.ShutdownIfLast)
		if shutdown {
			defer func() { go s.Close() }()
		}
		if err := ss.write(goprotocol.Success(msg.RequestID, map[string]any{})); err != nil {
			return err
		}
		return nil
	case "state.dump":
		gometrics.StateDumps.Add(1)
		return ss.write(goprotocol.Success(msg.RequestID, s.model.Dump()))
	case "event.list":
		return s.eventList(ss, msg)
	case "debug.metrics":
		return ss.write(goprotocol.Success(msg.RequestID, s.metricsSnapshot()))
	case "debug.memory":
		modelMemory := s.model.MemoryProjection()
		var memory runtime.MemStats
		runtime.ReadMemStats(&memory)
		return ss.write(goprotocol.Success(msg.RequestID, map[string]any{
			"heap_alloc_bytes":          memory.HeapAlloc,
			"heap_inuse_bytes":          memory.HeapInuse,
			"heap_idle_bytes":           memory.HeapIdle,
			"heap_released_bytes":       memory.HeapReleased,
			"terminal_count":            s.registry.Count(),
			"scrollback_lines":          s.Config.Terminal.ScrollbackLines,
			"inactive_scrollback_lines": s.Config.Terminal.InactiveScrollbackLines,
			"replay_history_bytes":      s.Config.Terminal.ReplayHistoryBytes,
			"retained_replay_bytes":     s.registry.RetainedReplayBytes(),
			"visible_cells":             modelMemory.VisibleCells,
			"surface_count":             modelMemory.SurfaceCount,
			"shape_cache_entries":       0,
			"image_cache_bytes":         0,
		}))
	case "connection.list":
		if s.uiSessionCount() > 0 {
			return s.forwardUI(ss, msg)
		}
		return ss.write(goprotocol.Success(msg.RequestID, map[string]any{
			"connections": []any{map[string]any{
				"id":                 uuid.MustParse("00000000-0000-0000-0000-000000000001"),
				"name":               "Local",
				"kind":               "local",
				"status":             "connected",
				"socket_path":        s.SocketPath,
				"remote_socket_path": nil,
				"destination":        nil,
			}},
		}))
	case "ui.keystroke", "ui.snapshot", "ui.click", "ui.drag", "ui.screenshot", "ui.wheel", "ui.menu":
		return s.forwardUI(ss, msg)
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
				gometrics.TerminalStreamEvents.Add(1)
				if ev.Kind == goprotocol.OutputEvent {
					gometrics.TerminalOutputBytesSent.Add(uint64(len(ev.Data)))
				}
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
		"process":     process,
		"first_seq":   first,
		"last_seq":    last,
		"size":        size,
		"events":      wire,
	}
}

func (s *Server) addSession(ss *session) bool {
	s.sessionsMu.Lock()
	if s.guiDraining {
		s.sessionsMu.Unlock()
		return false
	}
	s.sessions[ss] = struct{}{}
	if s.focusedSession == nil {
		s.focusedSession = ss
	}
	s.sessionsMu.Unlock()
	s.notifySessionsChanged()
	return true
}

func (s *Server) releaseSession(ss *session, shutdownIfLast bool) bool {
	s.sessionsMu.Lock()
	_, registered := s.sessions[ss]
	delete(s.sessions, ss)
	if s.focusedSession == ss {
		s.focusedSession = nil
	}
	shutdown := registered && shutdownIfLast && len(s.sessions) == 0
	if shutdown {
		s.guiDraining = true
	}
	s.sessionsMu.Unlock()
	s.notifySessionsChanged()
	return shutdown
}

func (s *Server) dropSession(ss *session) {
	s.sessionsMu.Lock()
	delete(s.sessions, ss)
	if s.focusedSession == ss {
		s.focusedSession = nil
	}
	s.sessionsMu.Unlock()
	s.notifySessionsChanged()
}

func (s *Server) notifySessionsChanged() {
	select {
	case s.sessionsChanged <- struct{}{}:
	default:
	}
}

// Called after the owner's frame loop and session have finished. Other windows
// can keep using an embedded server without keeping its original GUI open.
func (s *Server) WaitForGUIRelease() {
	for {
		s.sessionsMu.Lock()
		empty := len(s.sessions) == 0
		if empty {
			s.guiDraining = true
		}
		s.sessionsMu.Unlock()
		if empty {
			return
		}
		select {
		case <-s.sessionsChanged:
		case <-s.closing:
			return
		}
	}
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
	err = ss.write(goprotocol.WireMessage{
		BuildVariant:    s.Build,
		ProtocolVersion: goprotocol.ProtocolVersion,
		RequestID:       0,
		Method:          "push.snapshot",
		Params:          params,
	})
	if err == nil {
		gometrics.ModelSnapshotPushes.Add(1)
	}
	return err
}

func (s *Server) snapshotLoop(ss *session) {
	for {
		select {
		case <-ss.snapshotRequests:
			if err := s.pushSnapshot(ss); err != nil {
				return
			}
		case <-ss.done:
			return
		}
	}
}

func (s *Server) broadcastSnapshot() {
	s.sessionsMu.RLock()
	sessions := make([]*session, 0, len(s.sessions))
	for ss := range s.sessions {
		sessions = append(sessions, ss)
	}
	s.sessionsMu.RUnlock()
	for _, ss := range sessions {
		ss.requestSnapshot()
	}
}
