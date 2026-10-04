package gomodel

import (
	"github.com/SurTeam/Water/internal/goagent"
	"github.com/google/uuid"
)

// Terminal metadata is updated by the server's command dispatcher. Titles stay
// a client projection, so foreground changes cannot overwrite a custom name.
func (m *Model) SetTerminalForeground(id uuid.UUID, name string) bool {
	return m.SetTerminalForegroundProcess(id, name, nil)
}

func (m *Model) SetTerminalForegroundProcess(id uuid.UUID, name string, args []string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, pane := range m.panes {
		if term := pane.Terminal; term != nil && term.TerminalID == id && !term.Exited {
			agent := goagent.Detect(name, args)
			sameAgent := term.Agent == nil && agent == nil || term.Agent != nil && agent != nil && term.Agent.Kind == agent.Kind
			if term.ProcessName == name && sameAgent {
				return false
			}
			term.ProcessName = name
			if !sameAgent {
				term.Agent = agent
				term.AgentLabel = nil
			}
			m.bump()
			return true
		}
	}
	return false
}
