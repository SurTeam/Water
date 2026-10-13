package goclient

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/SurTeam/Water/internal/gobuild"
	"github.com/SurTeam/Water/internal/goprotocol"
	"github.com/google/uuid"
)

type Client struct {
	SocketPath string
	Build      string
	nextID     atomic.Uint64
}

func New(socketPath string) *Client {
	return &Client{SocketPath: socketPath, Build: gobuild.Variant}
}

func marshalParams(params any) (json.RawMessage, error) {
	if params == nil {
		return nil, nil
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	trimmed := bytes.TrimSpace(raw)
	if bytes.Equal(trimmed, []byte("{}")) || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	return json.RawMessage(raw), nil
}

func (c *Client) Call(method string, params any, out any) error {
	return c.call(method, params, out, 0)
}

// CallTimeout bounds both connection setup and the RPC exchange.
func (c *Client) CallTimeout(method string, params any, out any, timeout time.Duration) error {
	return c.call(method, params, out, timeout)
}

func (c *Client) call(method string, params any, out any, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	conn, err := net.DialTimeout("unix", c.SocketPath, timeout)
	if err != nil {
		return err
	}
	defer conn.Close()
	if timeout > 0 {
		_ = conn.SetDeadline(deadline)
	}

	requestID := c.nextID.Add(1)
	raw, err := marshalParams(params)
	if err != nil {
		return err
	}
	req := goprotocol.WireMessage{
		BuildVariant:    c.Build,
		ProtocolVersion: goprotocol.ProtocolVersion,
		RequestID:       requestID,
		Method:          method,
		Params:          raw,
	}
	if err := goprotocol.WriteJSON(conn, req); err != nil {
		return err
	}

	for {
		frame, err := goprotocol.ReadFrame(conn)
		if err != nil {
			return err
		}
		if frame.Terminal != nil {
			continue
		}
		var resp goprotocol.WireMessage
		if err := json.Unmarshal(frame.JSON, &resp); err != nil {
			return err
		}
		if resp.RequestID != requestID || resp.OK == nil {
			continue
		}
		if !*resp.OK {
			if resp.Error != nil {
				return resp.Error
			}
			return errors.New("request failed")
		}
		if out == nil || len(resp.Result) == 0 {
			return nil
		}
		return json.Unmarshal(resp.Result, out)
	}
}

func (c *Client) Dispatch(command any, out any) error {
	var dispatched struct {
		OperationID uuid.UUID `json:"operation_id"`
	}
	if err := c.Call("command.dispatch", map[string]any{"command": command}, &dispatched); err != nil {
		return err
	}

	var op struct {
		Status string               `json:"status"`
		Result json.RawMessage      `json:"result"`
		Error  *goprotocol.RPCError `json:"error"`
	}
	if err := c.Call("operation.wait", map[string]any{"operation_id": dispatched.OperationID}, &op); err != nil {
		return err
	}
	if op.Status != "succeeded" {
		if op.Error != nil {
			return op.Error
		}
		return fmt.Errorf("operation ended with status %s", op.Status)
	}
	if out != nil && len(op.Result) > 0 {
		return json.Unmarshal(op.Result, out)
	}
	return nil
}

const terminalEventQueueCapacity = 64
// Each dedicated terminal connection has its own bounded queue in addition to
// the GUI's aggregate queue; keep per-terminal buffering small.
const terminalStreamQueueCapacity = 8

const sessionSendQueueCapacity = 64
const sessionSendQueueBytes = goprotocol.MaxFrameBytes

var ErrSessionBackpressure = errors.New("session send queue is full")

type pendingSessionFrame struct {
	message goprotocol.WireMessage
	cost    int
	written chan error
	staged  bool
}

// stagedTerminal is one coalesced terminal.resize or terminal.send_bytes.
// Adjacent operations of the same kind merge, so a busy PTY cannot fill the
// session queue and tear the GUI connection down.
type stagedTerminal struct {
	id                           uuid.UUID
	kind                         string
	data                         []byte
	columns, lines, cellW, cellH int
	enqueued                     bool
}

type Session struct {
	SocketPath    string
	// Bulk terminal output travels over separate connections (and independent
	// SSH-forwarded channels) so a slow TUI cannot block control RPC replies.
	terminalMu      sync.Mutex
	terminalStreams map[uuid.UUID]*terminalStream
	terminalReaders sync.WaitGroup

	Server        goprotocol.ServerInfo
	Compatibility goprotocol.Compatibility
	Diagnostic    bool
	conn          net.Conn
	writeMu       sync.Mutex
	outbound      chan pendingSessionFrame
	queuedBytes   int
	staged        []stagedTerminal
	stagedBytes   int
	errMu         sync.Mutex
	terminalError error
	nextID        atomic.Uint64
	Build         string
	WindowID      uuid.UUID

	Events chan TerminalPush
	Pushes chan goprotocol.WireMessage

	replyMu sync.Mutex
	replies map[uint64]chan goprotocol.WireMessage
	done    chan struct{}
	once    sync.Once
}

type TerminalPush struct {
	TerminalID uuid.UUID
	Event      goprotocol.TerminalEvent
}

type terminalStream struct {
	session *Session
	stop    chan struct{}
}

func (c *Client) OpenSession() (*Session, error) {
	return c.openSession("gui")
}

func (c *Client) openSession(role string) (*Session, error) {
	conn, err := net.DialTimeout("unix", c.SocketPath, time.Second)
	if err != nil {
		return nil, err
	}
	eventCapacity := terminalEventQueueCapacity
	if role == "terminal" {
		eventCapacity = terminalStreamQueueCapacity
	}
	s := &Session{
		SocketPath: c.SocketPath,
		conn:     conn,
		Build:    c.Build,
		Events:   make(chan TerminalPush, eventCapacity),
		Pushes:   make(chan goprotocol.WireMessage, 64),
		replies:  make(map[uint64]chan goprotocol.WireMessage),
		done:     make(chan struct{}),
		outbound: make(chan pendingSessionFrame, sessionSendQueueCapacity),
	}
	go s.writeLoop()
	go s.readLoop()

	var info goprotocol.ServerInfo
	// Inspection is part of GUI startup, not a health-check ping. A busy but
	// healthy server can take longer than one second to schedule the reply.
	// A timeout closes this Session, so only fall back to server.info when
	// the first request failed at the RPC level and the stream remains usable.
	if err := s.CallTimeout("server.inspect", nil, &info, 5*time.Second); err != nil {
		if s.Err() != nil {
			_ = s.Close()
			return nil, fmt.Errorf("inspect server compatibility: %w", err)
		}
		if err := s.CallTimeout("server.info", nil, &info, 5*time.Second); err != nil {
			_ = s.Close()
			return nil, fmt.Errorf("inspect server compatibility: %w", err)
		}
	}
	s.Server = info
	clientInfo := Descriptor(c.Build)
	s.Compatibility = goprotocol.Assess(clientInfo, info.Descriptor)
	s.Diagnostic = !s.Compatibility.Compatible
	var result struct {
		WindowID      uuid.UUID                `json:"window_id"`
		Server        goprotocol.ServerInfo    `json:"server"`
		Diagnostic    bool                     `json:"diagnostic"`
		Compatibility goprotocol.Compatibility `json:"compatibility"`
	}
	if err := s.Call("session.open", map[string]any{
		"role":              role,
		"client":            clientInfo,
		"compact_snapshots": true,
	}, &result); err != nil {
		_ = s.Close()
		return nil, err
	}
	s.WindowID = result.WindowID
	if result.Server.ProtocolVersion != 0 {
		s.Server = result.Server
		s.Compatibility = result.Compatibility
		s.Diagnostic = result.Diagnostic
	}
	return s, nil
}

// Notify queues a control message without blocking the GUI frame loop.
func (s *Session) Notify(method string, params any) error {
	raw, err := marshalParams(params)
	if err != nil {
		return err
	}
	return s.enqueue(goprotocol.WireMessage{BuildVariant: s.Build, ProtocolVersion: goprotocol.ProtocolVersion, RequestID: s.nextID.Add(1), Method: method, Params: raw}, false)
}

func (s *Session) Call(method string, params any, out any) error {
	return s.CallTimeout(method, params, out, 10*time.Second)
}

func (s *Session) CallTimeout(method string, params any, out any, timeout time.Duration) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	requestID := s.nextID.Add(1)
	raw, err := marshalParams(params)
	if err != nil {
		return err
	}
	req := goprotocol.WireMessage{
		BuildVariant:    s.Build,
		ProtocolVersion: goprotocol.ProtocolVersion,
		RequestID:       requestID,
		Method:          method,
		Params:          raw,
	}

	ch := make(chan goprotocol.WireMessage, 1)
	s.replyMu.Lock()
	s.replies[requestID] = ch
	s.replyMu.Unlock()
	defer func() {
		s.replyMu.Lock()
		delete(s.replies, requestID)
		s.replyMu.Unlock()
	}()

	err = s.enqueue(req, false)
	if err != nil {
		return err
	}

	select {
	case resp, ok := <-ch:
		if !ok {
			return errors.New("session closed")
		}
		if resp.OK == nil || !*resp.OK {
			if resp.Error != nil {
				return resp.Error
			}
			return errors.New("request failed")
		}
		if out != nil && len(resp.Result) > 0 {
			return json.Unmarshal(resp.Result, out)
		}
		return nil
	case <-s.done:
		return errors.New("session closed")
	case <-timer.C:
		err := fmt.Errorf("session RPC %s timed out after %s", method, timeout)
		_ = s.closeWithError(err)
		return err
	}
}

func (s *Session) readLoop() {
	defer func() {
		_ = s.Close()
		// The terminal forwarders may still be sending into Events. Cancel and
		// join them before closing that channel to prevent send-on-closed races.
		s.terminalReaders.Wait()
		close(s.Events)
		close(s.Pushes)
		s.failReplies()
	}()

	for {
		frame, err := goprotocol.ReadFrame(s.conn)
		if err != nil {
			_ = s.closeWithError(err)
			return
		}
		if frame.Terminal != nil {
			select {
			case s.Events <- TerminalPush{TerminalID: frame.TerminalID, Event: *frame.Terminal}:
			case <-s.done:
				return
			}
			continue
		}

		var msg goprotocol.WireMessage
		if err := json.Unmarshal(frame.JSON, &msg); err != nil {
			continue
		}
		if msg.OK == nil {
			select {
			case s.Pushes <- msg:
			default:
			}
			continue
		}
		s.replyMu.Lock()
		ch := s.replies[msg.RequestID]
		s.replyMu.Unlock()
		if ch != nil {
			select {
			case ch <- msg:
			default:
			}
		}
	}
}

func (s *Session) failReplies() {
	s.replyMu.Lock()
	defer s.replyMu.Unlock()
	for id, ch := range s.replies {
		close(ch)
		delete(s.replies, id)
	}
}

func (s *Session) DispatchAsync(command any) error {
	if handled, err := s.stageTerminal(command); handled {
		return err
	}
	requestID := s.nextID.Add(1)
	params, err := json.Marshal(map[string]any{"command": command})
	if err != nil {
		return err
	}
	req := goprotocol.WireMessage{
		BuildVariant:    s.Build,
		ProtocolVersion: goprotocol.ProtocolVersion,
		RequestID:       requestID,
		Method:          "command.dispatch",
		Params:          params,
	}
	return s.enqueue(req, false)
}

// stageTerminal merges PTY writes and resizes ahead of the socket queue.
// One frame stays in flight; everything behind it collapses. Saturation keeps
// the session alive and refuses only the overflow, instead of closing the GUI.
func (s *Session) stageTerminal(command any) (bool, error) {
	if s.Diagnostic {
		return false, nil
	}
	m, ok := command.(map[string]any)
	if !ok {
		return false, nil
	}
	kind, _ := m["type"].(string)
	if kind != "terminal.send_bytes" && kind != "terminal.resize" {
		return false, nil
	}
	id, ok := commandUUID(m["terminal_id"])
	if !ok {
		return false, nil
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	select {
	case <-s.done:
		return true, errors.New("session closed")
	default:
	}
	switch kind {
	case "terminal.send_bytes":
		data := commandBytes(m["bytes"])
		if s.stagedBytes+len(data) > sessionSendQueueBytes {
			return true, ErrSessionBackpressure
		}
		if n := len(s.staged); n > 0 && !s.staged[n-1].enqueued && s.staged[n-1].kind == "bytes" && s.staged[n-1].id == id {
			s.staged[n-1].data = append(s.staged[n-1].data, data...)
		} else {
			s.staged = append(s.staged, stagedTerminal{id: id, kind: "bytes", data: append([]byte(nil), data...)})
		}
		s.stagedBytes += len(data)
	case "terminal.resize":
		op := stagedTerminal{id: id, kind: "resize", columns: commandInt(m["columns"]), lines: commandInt(m["lines"]), cellW: commandInt(m["cell_width"]), cellH: commandInt(m["cell_height"])}
		if n := len(s.staged); n > 0 && !s.staged[n-1].enqueued && s.staged[n-1].kind == "resize" && s.staged[n-1].id == id {
			s.staged[n-1] = op
		} else {
			s.staged = append(s.staged, op)
		}
	}
	s.flushStagedLocked()
	return true, nil
}

func (s *Session) flushStagedLocked() {
	if len(s.staged) == 0 || s.staged[0].enqueued {
		return
	}
	op := s.staged[0]
	if op.kind == "bytes" && len(op.data) > 128*1024 {
		chunk := append([]byte(nil), op.data[:128*1024]...)
		rest := append([]byte(nil), op.data[128*1024:]...)
		s.staged[0].data = chunk
		inserted := make([]stagedTerminal, 0, len(s.staged)+1)
		inserted = append(inserted, s.staged[0], stagedTerminal{id: op.id, kind: "bytes", data: rest})
		inserted = append(inserted, s.staged[1:]...)
		s.staged = inserted
		op = s.staged[0]
	}
	params, err := json.Marshal(map[string]any{"command": stagedCommand(op)})
	if err != nil {
		return
	}
	message := goprotocol.WireMessage{BuildVariant: s.Build, ProtocolVersion: goprotocol.ProtocolVersion, RequestID: s.nextID.Add(1), Method: "command.dispatch", Params: params}
	cost := len(message.Params) + len(message.Method) + len(message.BuildVariant) + 256
	if cost > sessionSendQueueBytes-s.queuedBytes || len(s.outbound) == cap(s.outbound) {
		return
	}
	s.staged[0].enqueued = true
	s.outbound <- pendingSessionFrame{message: message, cost: cost, staged: true}
	s.queuedBytes += cost
}

func stagedCommand(op stagedTerminal) map[string]any {
	if op.kind == "resize" {
		return map[string]any{"type": "terminal.resize", "terminal_id": op.id, "columns": op.columns, "lines": op.lines, "cell_width": op.cellW, "cell_height": op.cellH}
	}
	return map[string]any{"type": "terminal.send_bytes", "terminal_id": op.id, "bytes": bytesAsInts(op.data)}
}

func bytesAsInts(data []byte) []int {
	out := make([]int, len(data))
	for i, b := range data {
		out[i] = int(b)
	}
	return out
}

func commandUUID(v any) (uuid.UUID, bool) {
	switch id := v.(type) {
	case uuid.UUID:
		return id, id != uuid.Nil
	case string:
		parsed, err := uuid.Parse(id)
		return parsed, err == nil && parsed != uuid.Nil
	default:
		return uuid.Nil, false
	}
}

func commandInt(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int32:
		return int(n)
	case int64:
		return int(n)
	case float64:
		return int(n)
	default:
		return 0
	}
}

func commandBytes(v any) []byte {
	switch b := v.(type) {
	case []byte:
		return append([]byte(nil), b...)
	case []int:
		out := make([]byte, len(b))
		for i, n := range b {
			out[i] = byte(n)
		}
		return out
	case []any:
		out := make([]byte, len(b))
		for i, n := range b {
			out[i] = byte(commandInt(n))
		}
		return out
	default:
		return nil
	}
}

func (s *Session) Dispatch(command any, out any) error {
	var dispatched struct {
		OperationID uuid.UUID `json:"operation_id"`
	}
	if err := s.Call("command.dispatch", map[string]any{"command": command}, &dispatched); err != nil {
		return err
	}

	var op struct {
		Status string               `json:"status"`
		Result json.RawMessage      `json:"result"`
		Error  *goprotocol.RPCError `json:"error"`
	}
	if err := s.Call("operation.wait", map[string]any{"operation_id": dispatched.OperationID}, &op); err != nil {
		return err
	}
	if op.Status != "succeeded" {
		if op.Error != nil {
			return op.Error
		}
		return fmt.Errorf("operation ended with status %s", op.Status)
	}
	if out != nil && len(op.Result) > 0 {
		return json.Unmarshal(op.Result, out)
	}
	return nil
}

func (s *Session) ReplySuccess(requestID uint64, result any) error {
	raw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	ok := true
	reply := goprotocol.WireMessage{
		BuildVariant:    s.Build,
		ProtocolVersion: goprotocol.ProtocolVersion,
		RequestID:       requestID,
		OK:              &ok,
		Result:          raw,
	}
	return s.enqueue(reply, true)
}

func (s *Session) ReplyFailure(requestID uint64, code, message string) error {
	ok := false
	reply := goprotocol.WireMessage{
		BuildVariant:    s.Build,
		ProtocolVersion: goprotocol.ProtocolVersion,
		RequestID:       requestID,
		OK:              &ok,
		Error:           &goprotocol.RPCError{Code: code, Message: message},
	}
	return s.enqueue(reply, true)
}

func (s *Session) Attach(id uuid.UUID, out any) error {
	if s.SocketPath == "" {
		// In-memory transport fixtures retain the original single connection.
		return s.Call("terminal.attach", map[string]any{"terminal_id": id}, out)
	}

	// A new transport for each terminal is analogous to using independent SSH
	// channels. Backpressure can halt that PTY's delivery without preventing
	// another terminal from attaching or blocking GUI control-plane replies.
	client := New(s.SocketPath)
	client.Build = s.Build
	stream, err := client.openSession("terminal")
	if err != nil {
		return err
	}
	if err := stream.Call("terminal.attach", map[string]any{"terminal_id": id}, out); err != nil {
		_ = stream.Close()
		return err
	}

	next := &terminalStream{session: stream, stop: make(chan struct{})}
	s.terminalMu.Lock()
	select {
	case <-s.done:
		s.terminalMu.Unlock()
		_ = stream.Close()
		return errors.New("session closed")
	default:
	}
	if s.terminalStreams == nil {
		s.terminalStreams = make(map[uuid.UUID]*terminalStream)
	}
	previous := s.terminalStreams[id]
	s.terminalStreams[id] = next
	s.terminalReaders.Add(1)
	s.terminalMu.Unlock()
	if previous != nil {
		close(previous.stop)
		_ = previous.session.Close()
	}
	go s.forwardTerminalEvents(next)
	return nil
}

func (s *Session) forwardTerminalEvents(stream *terminalStream) {
	defer s.terminalReaders.Done()
	for {
		// Do not forward pending events from a detached/replaced stream.
		select {
		case <-s.done:
			return
		case <-stream.stop:
			return
		default:
		}
		select {
		case <-s.done:
			return
		case <-stream.stop:
			return
		case event, ok := <-stream.session.Events:
			if !ok {
				return
			}
			select {
			case s.Events <- event:
			case <-s.done:
				return
			case <-stream.stop:
				return
			}
		}
	}
}

func (s *Session) Detach(id uuid.UUID) error {
	if s.SocketPath == "" {
		return s.Call("terminal.detach", map[string]any{"terminal_id": id}, nil)
	}
	s.terminalMu.Lock()
	stream := s.terminalStreams[id]
	delete(s.terminalStreams, id)
	s.terminalMu.Unlock()
	if stream != nil {
		close(stream.stop)
		return stream.session.Close()
	}
	return nil
}

func (s *Session) Close() error {
	return s.closeWithError(errors.New("session closed"))
}

func (s *Session) closeWithError(cause error) error {
	var err error
	s.once.Do(func() {
		s.writeMu.Lock()
		s.errMu.Lock()
		s.terminalError = cause
		s.errMu.Unlock()
		close(s.done)
		s.writeMu.Unlock()
		err = s.conn.Close()
		s.terminalMu.Lock()
		streams := s.terminalStreams
		s.terminalStreams = nil
		s.terminalMu.Unlock()
		for _, stream := range streams {
			close(stream.stop)
			_ = stream.session.Close()
		}
	})
	return err
}

func (s *Session) Err() error { s.errMu.Lock(); defer s.errMu.Unlock(); return s.terminalError }

// Enqueue never performs socket I/O. One writer preserves accepted frame order.
// Saturation ends the session explicitly instead of dropping input silently.
func (s *Session) enqueue(message goprotocol.WireMessage, wait bool) error {
	if s.Diagnostic && message.OK == nil && message.Method != "session.open" && message.Method != "session.release" && message.Method != "server.inspect" && message.Method != "server.info" && !strings.HasPrefix(message.Method, "recovery.") && !strings.HasPrefix(message.Method, "ui.") {
		return &goprotocol.RPCError{Code: "INCOMPATIBLE_SERVER", Message: s.Compatibility.Reason}
	}
	cost := len(message.Params) + len(message.Result) + len(message.Method) + len(message.BuildVariant) + 256
	if message.Error != nil {
		cost += len(message.Error.Code) + len(message.Error.Message)
	}
	frame := pendingSessionFrame{message: message, cost: cost}
	if wait {
		frame.written = make(chan error, 1)
	}
	s.writeMu.Lock()
	select {
	case <-s.done:
		s.writeMu.Unlock()
		return errors.New("session closed")
	default:
	}
	if frame.cost > sessionSendQueueBytes-s.queuedBytes {
		s.writeMu.Unlock()
		_ = s.closeWithError(ErrSessionBackpressure)
		return ErrSessionBackpressure
	}
	select {
	case s.outbound <- frame:
		s.queuedBytes += frame.cost
		s.writeMu.Unlock()
	default:
		s.writeMu.Unlock()
		_ = s.closeWithError(ErrSessionBackpressure)
		return ErrSessionBackpressure
	}
	if !wait {
		return nil
	}
	select {
	case err := <-frame.written:
		return err
	case <-s.done:
		return errors.New("session closed")
	}
}

func (s *Session) writeLoop() {
	defer func() {
		_ = s.Close()
		s.writeMu.Lock()
		defer s.writeMu.Unlock()
		for {
			select {
			case frame := <-s.outbound:
				if frame.written != nil {
					frame.written <- errors.New("session closed")
				}
			default:
				s.queuedBytes = 0
				return
			}
		}
	}()
	for {
		select {
		case <-s.done:
			return
		case frame := <-s.outbound:
			err := s.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err == nil {
				err = goprotocol.WriteJSON(s.conn, frame.message)
			}
			s.writeMu.Lock()
			s.queuedBytes -= frame.cost
			if err == nil && frame.staged && len(s.staged) > 0 && s.staged[0].enqueued {
				if s.staged[0].kind == "bytes" {
					s.stagedBytes -= len(s.staged[0].data)
				}
				s.staged = s.staged[1:]
				s.flushStagedLocked()
			}
			s.writeMu.Unlock()
			if frame.written != nil {
				frame.written <- err
			}
			if err != nil {
				_ = s.closeWithError(err)
				return
			}
		}
	}
}
