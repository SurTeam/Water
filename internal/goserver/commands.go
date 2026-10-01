package goserver

import (
	"encoding/json"
	"errors"

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

	id := s.opSeq.Add(1)
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
	}
	s.opsMu.Lock()
	s.ops[id] = op
	s.opsMu.Unlock()

	return ss.write(goprotocol.Success(msg.RequestID, map[string]uint64{
		"operation_id": id,
	}))
}

func (s *Server) executeCommand(kind string, raw json.RawMessage) (any, *goprotocol.RPCError) {
	fail := func(code string, err error) (any, *goprotocol.RPCError) {
		return nil, &goprotocol.RPCError{Code: code, Message: err.Error()}
	}

	switch kind {
	case "terminal.spawn":
		var c struct {
			Program string   `json:"program"`
			Args    []string `json:"args"`
			Columns int      `json:"columns"`
			Lines   int      `json:"lines"`
		}
		if err := json.Unmarshal(raw, &c); err != nil {
			return fail("INVALID_COMMAND", err)
		}
		t, err := s.registry.Spawn(c.Program, c.Args, goprotocol.TerminalSize{
			Columns: c.Columns,
			Lines:   c.Lines,
		})
		if err != nil {
			return fail("TERMINAL_SPAWN_FAILED", err)
		}
		return map[string]any{
			"type":        "terminal_spawned",
			"terminal_id": t.ID,
		}, nil

	case "terminal.send_text":
		var c struct {
			TerminalID uuid.UUID `json:"terminal_id"`
			Text       string    `json:"text"`
		}
		if err := json.Unmarshal(raw, &c); err != nil {
			return fail("INVALID_COMMAND", err)
		}
		t, ok := s.registry.Get(c.TerminalID)
		if !ok {
			return fail("TERMINAL_NOT_FOUND", errors.New("terminal not found"))
		}
		if err := t.Write([]byte(c.Text)); err != nil {
			return fail("TERMINAL_WRITE_FAILED", err)
		}
		return map[string]any{
			"type":        "terminal_text_sent",
			"terminal_id": c.TerminalID,
		}, nil

	case "terminal.send_bytes":
		var c struct {
			TerminalID uuid.UUID `json:"terminal_id"`
			Bytes      []byte    `json:"bytes"`
		}
		if err := json.Unmarshal(raw, &c); err != nil {
			return fail("INVALID_COMMAND", err)
		}
		t, ok := s.registry.Get(c.TerminalID)
		if !ok {
			return fail("TERMINAL_NOT_FOUND", errors.New("terminal not found"))
		}
		if err := t.Write(c.Bytes); err != nil {
			return fail("TERMINAL_WRITE_FAILED", err)
		}
		return map[string]any{
			"type":        "terminal_bytes_sent",
			"terminal_id": c.TerminalID,
		}, nil

	case "terminal.resize":
		var c struct {
			TerminalID uuid.UUID `json:"terminal_id"`
			Columns    int       `json:"columns"`
			Lines      int       `json:"lines"`
		}
		if err := json.Unmarshal(raw, &c); err != nil {
			return fail("INVALID_COMMAND", err)
		}
		t, ok := s.registry.Get(c.TerminalID)
		if !ok {
			return fail("TERMINAL_NOT_FOUND", errors.New("terminal not found"))
		}
		if err := t.Resize(goprotocol.TerminalSize{Columns: c.Columns, Lines: c.Lines}); err != nil {
			return fail("TERMINAL_RESIZE_FAILED", err)
		}
		return map[string]any{
			"type":        "terminal_resized",
			"terminal_id": c.TerminalID,
			"columns":     c.Columns,
			"lines":       c.Lines,
		}, nil

	default:
		return nil, &goprotocol.RPCError{
			Code:    "UNSUPPORTED_COMMAND",
			Message: "Go server does not implement command " + kind + " yet",
		}
	}
}
