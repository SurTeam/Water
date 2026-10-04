package goserver

import (
	"encoding/json"
	"github.com/SurTeam/Water/internal/goprotocol"
)

// Selection effects go only to the originating window. CLI commands target
// the focused GUI; all clients still receive the shared model snapshot.
func (s *Server) pushSelection(origin *session, kind string, result any) {
	switch kind {
	case "workspace.create", "workspace.ensure", "workspace.new", "workspace.activate",
		"tab.create", "tab.new", "tab.activate", "pane.split", "pane.focus", "pane.promote_to_tab":
	default:
		return
	}
	s.sessionsMu.RLock()
	_, gui := s.sessions[origin]
	s.sessionsMu.RUnlock()
	if !gui {
		origin = s.firstUISession()
	}
	if origin == nil {
		return
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return
	}
	_ = origin.write(goprotocol.WireMessage{BuildVariant: s.Build, ProtocolVersion: goprotocol.ProtocolVersion, Method: "push.selection", Params: raw})
}
