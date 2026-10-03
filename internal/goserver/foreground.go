package goserver

import (
	"context"
	"encoding/json"
	"time"
)

func (s *Server) monitorForegroundProcesses() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-s.closing:
			cancel()
		case <-ctx.Done():
		}
	}()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-s.closing:
			return
		case <-ticker.C:
			changed := false
			for id, name := range s.registry.ForegroundNames(ctx) {
				command, _ := json.Marshal(map[string]any{"terminal_id": id, "name": name})
				result, err := s.executeCommand("internal.terminal.foreground", command)
				if err == nil && result == true {
					changed = true
				}
			}
			if changed {
				s.broadcastSnapshot()
			}
		}
	}
}
