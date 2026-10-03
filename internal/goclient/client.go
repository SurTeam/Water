package goclient

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
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

const sessionSendQueueCapacity = 64
const sessionSendQueueBytes = goprotocol.MaxFrameBytes

var ErrSessionBackpressure = errors.New("session send queue is full")

type pendingSessionFrame struct {
	message goprotocol.WireMessage
	cost    int
	written chan error
}

type Session struct {
	conn          net.Conn
	writeMu       sync.Mutex
	outbound      chan pendingSessionFrame
	queuedBytes   int
	errMu         sync.Mutex
	terminalError error
	nextID        atomic.Uint64
	Build         string

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

func (c *Client) OpenSession() (*Session, error) {
	conn, err := net.Dial("unix", c.SocketPath)
	if err != nil {
		return nil, err
	}
	s := &Session{
		conn:     conn,
		Build:    c.Build,
		Events:   make(chan TerminalPush, terminalEventQueueCapacity),
		Pushes:   make(chan goprotocol.WireMessage, 64),
		replies:  make(map[uint64]chan goprotocol.WireMessage),
		done:     make(chan struct{}),
		outbound: make(chan pendingSessionFrame, sessionSendQueueCapacity),
	}
	go s.writeLoop()
	go s.readLoop()

	var result map[string]any
	if err := s.Call("session.open", map[string]any{
		"role":              "gui",
		"compact_snapshots": true,
	}, &result); err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}

func (s *Session) Call(method string, params any, out any) error {
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

	err = s.enqueue(req, true)
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
	}
}

func (s *Session) readLoop() {
	defer close(s.Events)
	defer close(s.Pushes)
	defer s.Close()
	defer s.failReplies()

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
	return s.Call("terminal.attach", map[string]any{"terminal_id": id}, out)
}

func (s *Session) Detach(id uuid.UUID) error {
	return s.Call("terminal.detach", map[string]any{"terminal_id": id}, nil)
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
	})
	return err
}

func (s *Session) Err() error { s.errMu.Lock(); defer s.errMu.Unlock(); return s.terminalError }

// Enqueue never performs socket I/O. One writer preserves accepted frame order.
// Saturation ends the session explicitly instead of dropping input silently.
func (s *Session) enqueue(message goprotocol.WireMessage, wait bool) error {
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
			s.writeMu.Lock()
			s.queuedBytes -= frame.cost
			s.writeMu.Unlock()
			err := s.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err == nil {
				err = goprotocol.WriteJSON(s.conn, frame.message)
			}
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
