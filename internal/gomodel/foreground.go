package gomodel

import "github.com/google/uuid"

// Terminal metadata is updated by the server's command dispatcher. Titles stay
// a client projection, so foreground changes cannot overwrite a custom name.
func (m *Model) SetTerminalForeground(id uuid.UUID, name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, pane := range m.panes {
		if term := pane.Terminal; term != nil && term.TerminalID == id && !term.Exited {
			if term.ProcessName == name {
				return false
			}
			term.ProcessName = name
			m.bump()
			return true
		}
	}
	return false
}
