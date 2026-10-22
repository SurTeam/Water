package goserver

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/SurTeam/Water/internal/goprotocol"
	"github.com/SurTeam/Water/internal/goterminal"
	"github.com/google/uuid"
)

// Execution is bounded but deliberately runs outside commandMu: a long command
// in one pane must not stop model commands or input in other panes.
func (s *Server) paneExecute(ss *session, msg goprotocol.WireMessage) error {
	var p struct {
		PaneID    uuid.UUID `json:"pane_id"`
		Command   string    `json:"command"`
		TimeoutMS int       `json:"timeout_ms"`
		MaxRows   int       `json:"max_rows"`
		MaxBytes  int       `json:"max_bytes"`
	}
	if err := json.Unmarshal(msg.Params, &p); err != nil {
		return err
	}
	if p.PaneID == uuid.Nil {
		return errors.New("pane.execute requires pane_id")
	}
	if p.TimeoutMS < 0 || p.TimeoutMS > 600000 {
		return errors.New("timeout_ms must be between 1 and 600000 (or 0 for default)")
	}
	s.commandMu.Lock()
	if s.recovery != nil && time.Now().Before(s.recovery.expires) {
		s.commandMu.Unlock()
		return errors.New("server recovery is in progress")
	}
	id, err := s.resolveTerminal(nil, &p.PaneID)
	s.commandMu.Unlock()
	if err != nil {
		return err
	}
	terminal, ok := s.registry.Get(id)
	if !ok {
		return errors.New("terminal not found")
	}
	result, executeErr := terminal.Execute(context.Background(), goterminal.ExecuteOptions{
		Command: p.Command, Timeout: time.Duration(p.TimeoutMS) * time.Millisecond, MaxRows: p.MaxRows, MaxBytes: p.MaxBytes,
	})
	// Even failed cleanup must preserve the screen already collected.
	response := struct {
		goterminal.ExecuteResult
		PaneID     uuid.UUID `json:"pane_id"`
		TerminalID uuid.UUID `json:"terminal_id"`
		Status     string    `json:"status"`
		Error      string    `json:"error,omitempty"`
	}{ExecuteResult: result, PaneID: p.PaneID, TerminalID: id, Status: "completed"}
	if result.TimedOut {
		response.Status = "timed_out"
	}
	if executeErr != nil {
		response.Status = "failed"
		response.Error = executeErr.Error()
	}
	return ss.write(goprotocol.Success(msg.RequestID, response))
}
