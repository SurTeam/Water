package gomodel

import (
	"reflect"

	"github.com/SurTeam/Water/internal/goagent"
	"github.com/google/uuid"
)

// Terminal metadata is updated by the server's command dispatcher. Titles stay
// a client projection, so foreground changes cannot overwrite a custom name.
func (m *Model) SetTerminalForeground(id uuid.UUID, name string) bool {
	return m.SetTerminalForegroundProcess(id, name, nil)
}

func (m *Model) SetTerminalForegroundProcess(id uuid.UUID, name string, args []string) bool {
	return m.SetTerminalForegroundAgent(id, name, goagent.Detect(name, args))
}

func (m *Model) SetTerminalForegroundAgent(id uuid.UUID, name string, agent *goagent.DetectedAgent) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, pane := range m.panes {
		if term := pane.Terminal; term != nil && term.TerminalID == id && !term.Exited {
			sameAgent := term.Agent == nil && agent == nil || term.Agent != nil && agent != nil && term.Agent.Kind == agent.Kind
			if sameAgent && agent != nil {
				agent.Active = term.Agent.Active
			}
			if term.ProcessName == name && sameAgent && reflect.DeepEqual(term.Agent, agent) {
				return false
			}
			term.ProcessName = name
			term.Agent = agent
			if !sameAgent {
				term.AgentLabel = nil
			}
			m.bump()
			return true
		}
	}
	return false
}
