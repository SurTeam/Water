package goclient

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"

	"github.com/SurTeam/Water/internal/goprotocol"
	"github.com/google/uuid"
)

type Client struct {
	SocketPath string
	Build      string
	nextID     atomic.Uint64
}

func New(socketPath string) *Client {
	return &Client{SocketPath: socketPath, Build: "dev"}
}

func (c *Client) Call(method string, params any, out any) error {
	conn, err := net.Dial("unix", c.SocketPath)
	if err != nil {
		return err
	}
	defer conn.Close()

	requestID := c.nextID.Add(1)
	raw, err := json.Marshal(params)
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

type Session struct {
	conn   net.Conn
	mu     sync.Mutex
	nextID atomic.Uint64
	Build  string

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
		conn:    conn,
		Build:   c.Build,
		Events:  make(chan TerminalPush, 256),
		Pushes:  make(chan goprotocol.WireMessage, 64),
		replies: make(map[uint64]chan goprotocol.WireMessage),
		done:    make(chan struct{}),
	}
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
	raw, err := json.Marshal(params)
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

	s.mu.Lock()
	err = goprotocol.WriteJSON(s.conn, req)
	s.mu.Unlock()
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
	defer close(s.done)
	defer s.failReplies()

	for {
		frame, err := goprotocol.ReadFrame(s.conn)
		if err != nil {
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
	s.mu.Lock()
	err = goprotocol.WriteJSON(s.conn, req)
	s.mu.Unlock()
	return err
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
	s.mu.Lock()
	err = goprotocol.WriteJSON(s.conn, reply)
	s.mu.Unlock()
	return err
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
	s.mu.Lock()
	err := goprotocol.WriteJSON(s.conn, reply)
	s.mu.Unlock()
	return err
}

func (s *Session) Attach(id uuid.UUID, out any) error {
	return s.Call("terminal.attach", map[string]any{"terminal_id": id}, out)
}

func (s *Session) Detach(id uuid.UUID) error {
	return s.Call("terminal.detach", map[string]any{"terminal_id": id}, nil)
}

func (s *Session) Close() error {
	var err error
	s.once.Do(func() {
		err = s.conn.Close()
	})
	return err
}
