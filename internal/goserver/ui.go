package goserver

import (
	"encoding/json"
	"time"

	"github.com/SurTeam/Water/internal/goprotocol"
	"github.com/google/uuid"
)

const uiForwardTimeout = 5 * time.Second

func (s *Server) windowSessions() []map[string]any {
	s.sessionsMu.RLock()
	defer s.sessionsMu.RUnlock()
	windows := make([]map[string]any, 0, len(s.sessions))
	for ss := range s.sessions {
		if ss.browser {
			continue
		}
		windows = append(windows, map[string]any{"window_id": ss.id, "focused": ss == s.focusedSession})
	}
	return windows
}

func (s *Server) firstUISession() *session {
	s.sessionsMu.RLock()
	defer s.sessionsMu.RUnlock()
	if s.focusedSession != nil && !s.focusedSession.browser {
		return s.focusedSession
	}
	for ss := range s.sessions {
		if !ss.browser {
			return ss
		}
	}
	return nil
}

func (s *Server) deliverUIReply(msg goprotocol.WireMessage) bool {
	s.pendingUIMu.Lock()
	ch := s.pendingUI[msg.RequestID]
	if ch != nil {
		delete(s.pendingUI, msg.RequestID)
	}
	s.pendingUIMu.Unlock()
	if ch == nil {
		return false
	}
	select {
	case ch <- msg:
	default:
	}
	return true
}

func (s *Server) forwardUI(caller *session, request goprotocol.WireMessage) error {
	gui := s.firstUISession()
	var target struct {
		WindowID uuid.UUID `json:"window_id"`
	}
	if err := json.Unmarshal(request.Params, &target); err == nil && target.WindowID != uuid.Nil {
		gui = nil
		s.sessionsMu.RLock()
		for ss := range s.sessions {
			if ss.id == target.WindowID && !ss.browser {
				gui = ss
				break
			}
		}
		s.sessionsMu.RUnlock()
	}
	if gui == nil {
		return caller.write(goprotocol.Failure(
			request.RequestID,
			"UI_AUTOMATION_UNAVAILABLE",
			"no GUI session is connected",
		))
	}

	uiID := s.uiSeq.Add(1)
	params := request.Params
	if len(params) == 0 {
		params = json.RawMessage(`{}`)
	}
	inner, err := json.Marshal(map[string]any{
		"method": request.Method,
		"params": json.RawMessage(params),
	})
	if err != nil {
		return err
	}
	replyCh := make(chan goprotocol.WireMessage, 1)
	s.pendingUIMu.Lock()
	s.pendingUI[uiID] = replyCh
	s.pendingUIMu.Unlock()

	push := goprotocol.WireMessage{
		BuildVariant:    s.Build,
		ProtocolVersion: goprotocol.ProtocolVersion,
		RequestID:       uiID,
		Method:          "push.ui",
		Params:          inner,
	}
	if err := gui.write(push); err != nil {
		s.pendingUIMu.Lock()
		delete(s.pendingUI, uiID)
		s.pendingUIMu.Unlock()
		return caller.write(goprotocol.Failure(
			request.RequestID,
			"UI_AUTOMATION_UNAVAILABLE",
			"GUI session is unavailable",
		))
	}

	timer := time.NewTimer(uiForwardTimeout)
	defer timer.Stop()
	select {
	case reply := <-replyCh:
		if reply.OK != nil && *reply.OK {
			ok := true
			return caller.write(goprotocol.WireMessage{
				BuildVariant:    s.Build,
				ProtocolVersion: goprotocol.ProtocolVersion,
				RequestID:       request.RequestID,
				OK:              &ok,
				Result:          reply.Result,
			})
		}
		if reply.Error != nil {
			return caller.write(goprotocol.Failure(
				request.RequestID,
				reply.Error.Code,
				reply.Error.Message,
			))
		}
		return caller.write(goprotocol.Failure(
			request.RequestID,
			"UI_AUTOMATION_FAILED",
			"malformed GUI reply",
		))
	case <-timer.C:
		s.pendingUIMu.Lock()
		delete(s.pendingUI, uiID)
		s.pendingUIMu.Unlock()
		return caller.write(goprotocol.Failure(
			request.RequestID,
			"UI_AUTOMATION_TIMEOUT",
			"timed out waiting for the GUI to answer",
		))
	}
}
