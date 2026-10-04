package goui

import (
	"fmt"
	"github.com/google/uuid"
	"strings"
	"time"
)

// Baseline the first snapshot and de-duplicate by connection and pane.
func (w *EbitengineWindow) processNotifications(c *WorkspaceClient) {
	if w.lastBellNotification == nil {
		w.lastBellNotification = map[uuid.UUID]time.Time{}
	}
	for i := 0; i < 32; i++ {
		select {
		case terminal := <-w.bellNotifications:
			if c.currentConfig().UI.SystemNotifications && time.Since(w.lastBellNotification[terminal]) >= 5*time.Second {
				if platform, ok := w.platform.(interface{ Notify(string, string) }); ok {
					platform.Notify("Water", c.tr("Terminal needs attention"))
					w.lastBellNotification[terminal] = time.Now()
				}
			}
		default:
			i = 32
		}
	}
	for terminal, last := range w.lastBellNotification {
		if time.Since(last) > time.Minute {
			delete(w.lastBellNotification, terminal)
		}
	}
	agents := map[string]nativeAgent{}
	connections := map[uuid.UUID]string{}
	for _, host := range w.hosts() {
		connections[host.entry.ID] = host.entry.Status
		for _, a := range host.view.sidebarAgents(host.state) {
			agents[host.entry.ID.String()+":"+a.pane.String()] = a
		}
	}
	if c.currentConfig().UI.SystemNotifications {
		if platform, ok := w.platform.(interface{ Notify(string, string) }); ok {
			for key, old := range w.notificationAgents {
				connection, _, _ := strings.Cut(key, ":")
				connectionID := nativeUUID(connection)
				if status, exists := connections[connectionID]; !exists || status != "connected" || w.notificationConnections[connectionID] != "connected" {
					continue
				}
				current, exists := agents[key]
				if old.running && (!exists || !current.running || current.kind != old.kind) {
					platform.Notify("Water", fmt.Sprintf("%s has stopped", old.label))
				} else if exists && current.kind == old.kind && current.status != old.status {
					platform.Notify("Water", fmt.Sprintf("%s: %s", current.label, c.tr(current.status)))
				}
			}
			for key, current := range agents {
				connection, _, _ := strings.Cut(key, ":")
				id := nativeUUID(connection)
				if _, existed := w.notificationAgents[key]; !existed && current.running && w.notificationAgents != nil && connections[id] == "connected" && w.notificationConnections[id] == "connected" {
					platform.Notify("Water", fmt.Sprintf("%s: %s", current.label, c.tr(current.status)))
				}
			}
			for id, status := range connections {
				old, exists := w.notificationConnections[id]
				if exists && old == "connected" && status != "connected" {
					platform.Notify("Water", "Remote connection lost. Reconnecting.")
				}
				if exists && old != "connected" && status == "connected" {
					platform.Notify("Water", "Remote connection restored.")
				}
			}
		}
	}
	w.notificationAgents = agents
	w.notificationConnections = connections
}
