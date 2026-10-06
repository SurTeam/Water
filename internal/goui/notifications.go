package goui

import (
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/SurTeam/Water/internal/gomodel"
	"github.com/google/uuid"
)

type notificationRun struct {
	name      string
	startedAt time.Time
	notified  bool
}

type foregroundCommand struct {
	pane, terminal uuid.UUID
	name           string
}

// Baseline the first snapshot, then report meaningful Agent transitions and
// long-running work only once per process run.
func (w *EbitengineWindow) processNotifications(c *WorkspaceClient) {
	w.processNotificationsAt(c, false, time.Now())
}

func (w *EbitengineWindow) processNotificationsAt(c *WorkspaceClient, windowFocused bool, now time.Time) {
	if w.lastBellNotification == nil {
		w.lastBellNotification = map[uuid.UUID]time.Time{}
	}
	hosts := w.hosts()
	agents := map[string]nativeAgent{}
	agentsByTerminal := map[uuid.UUID]nativeAgent{}
	connections := map[uuid.UUID]string{}
	for _, host := range hosts {
		connections[host.entry.ID] = host.entry.Status
		for _, a := range host.view.sidebarAgents(host.state) {
			key := host.entry.ID.String() + ":" + a.pane.String()
			agents[key] = a
			agentsByTerminal[a.terminal] = a
		}
	}
	config := c.currentConfig().UI
	notify := func(body string) bool {
		if !config.SystemNotifications {
			return false
		}
		platform, ok := w.platform.(interface{ Notify(string, string) })
		if !ok {
			return false
		}
		platform.Notify("Water", body)
		return true
	}
	for i := 0; i < 32; i++ {
		select {
		case terminal := <-w.bellNotifications:
			if config.SystemNotifications && now.Sub(w.lastBellNotification[terminal]) >= 5*time.Second {
				body := c.tr("Terminal needs attention")
				if agent, ok := agentsByTerminal[terminal]; ok {
					body = c.trf("Agent paused; it may need your attention: %s", agent.label)
				}
				if notify(body) {
					w.lastBellNotification[terminal] = now
				}
			}
		default:
			i = 32
		}
	}
	for terminal, last := range w.lastBellNotification {
		if now.Sub(last) > time.Minute {
			delete(w.lastBellNotification, terminal)
		}
	}

	baseline := w.notificationAgents != nil
	if baseline && config.SystemNotifications {
		for key, current := range agents {
			if _, exists := w.notificationAgents[key]; exists || !current.running {
				continue
			}
			connection, _, _ := strings.Cut(key, ":")
			connectionID := nativeUUID(connection)
			if connections[connectionID] == "connected" && w.notificationConnections[connectionID] == "connected" {
				notify(c.trf("Agent started: %s", current.label))
			}
		}
		for key, old := range w.notificationAgents {
			connection, _, _ := strings.Cut(key, ":")
			connectionID := nativeUUID(connection)
			if status, exists := connections[connectionID]; !exists || status != "connected" || w.notificationConnections[connectionID] != "connected" {
				continue
			}
			current, exists := agents[key]
			if !old.running {
				continue
			}
			if !exists {
				if old.status != "Error" && old.status != "Completed" && old.status != "Idle" {
					notify(c.trf("Agent completed: %s", old.label))
				}
				continue
			}
			if current.status == old.status {
				continue
			}
			switch current.status {
			case "Idle", "Completed":
				if old.status != "Error" && old.status != "Completed" {
					notify(c.trf("Agent completed: %s", current.label))
				}
			case "Paused":
				notify(c.trf("Agent paused; it may need your attention: %s", current.label))
			case "Error":
				notify(c.trf("Agent reported an error: %s", current.label))
			}
		}
		for id, status := range connections {
			old, exists := w.notificationConnections[id]
			if exists && old == "connected" && status != "connected" {
				notify(c.tr("Remote connection lost. Reconnecting."))
			}
			if exists && old != "connected" && status == "connected" {
				notify(c.tr("Remote connection restored."))
			}
		}
	}

	activeConnection := w.multi.ActiveConnectionID()
	w.notificationAgentRuns = updateAgentLongRunNotifications(
		w, c, hosts, agents, activeConnection, windowFocused, now, config.AgentLongRunNotificationSeconds, notify,
	)
	w.notificationShellRuns = updateShellLongRunNotifications(
		w, c, hosts, agentsByTerminal, activeConnection, windowFocused, now, config.ShellLongRunNotificationSeconds, notify,
	)
	w.notificationAgents = agents
	w.notificationConnections = connections
}

func updateAgentLongRunNotifications(w *EbitengineWindow, c *WorkspaceClient, hosts []nativeHost, agents map[string]nativeAgent, activeConnection uuid.UUID, windowFocused bool, now time.Time, thresholdSeconds int, notify func(string) bool) map[string]notificationRun {
	next := make(map[string]notificationRun)
	for key, agent := range agents {
		if !agent.running || agent.status != "Running" {
			continue
		}
		run := w.notificationAgentRuns[key]
		if run.startedAt.IsZero() || run.name != agent.kind {
			run = notificationRun{name: agent.kind, startedAt: now}
		}
		next[key] = run
		if thresholdSeconds <= 0 || run.notified || now.Sub(run.startedAt) < time.Duration(thresholdSeconds)*time.Second || windowFocused {
			continue
		}
		connection, _, _ := strings.Cut(key, ":")
		connectionID := nativeUUID(connection)
		if !agentConnectionConnected(hosts, connectionID) || paneIsFocused(hosts, connectionID, agent.pane, activeConnection) {
			continue
		}
		if notify(c.trf("Agent is still running: %s", agent.label)) {
			run.notified = true
			next[key] = run
		}
	}
	return next
}

func updateShellLongRunNotifications(w *EbitengineWindow, c *WorkspaceClient, hosts []nativeHost, agents map[uuid.UUID]nativeAgent, activeConnection uuid.UUID, windowFocused bool, now time.Time, thresholdSeconds int, notify func(string) bool) map[string]notificationRun {
	next := make(map[string]notificationRun)
	for _, host := range hosts {
		if host.entry.Status != "" && host.entry.Status != "connected" && host.entry.Status != "online" {
			continue
		}
		for _, command := range foregroundCommands(host.state) {
			if _, isAgent := agents[command.terminal]; isAgent || isInteractiveShell(command.name) {
				continue
			}
			key := host.entry.ID.String() + ":" + command.terminal.String()
			run := w.notificationShellRuns[key]
			if run.startedAt.IsZero() || run.name != command.name {
				run = notificationRun{name: command.name, startedAt: now}
			}
			next[key] = run
			if thresholdSeconds <= 0 || run.notified || now.Sub(run.startedAt) < time.Duration(thresholdSeconds)*time.Second || windowFocused || paneIsFocused(hosts, host.entry.ID, command.pane, activeConnection) {
				continue
			}
			if notify(c.trf("Long-running command: %s", command.name)) {
				run.notified = true
				next[key] = run
			}
		}
	}
	return next
}

func agentConnectionConnected(hosts []nativeHost, connection uuid.UUID) bool {
	for _, host := range hosts {
		if host.entry.ID == connection {
			return host.entry.Status == "" || host.entry.Status == "connected" || host.entry.Status == "online"
		}
	}
	return false
}

func paneIsFocused(hosts []nativeHost, connection, pane, activeConnection uuid.UUID) bool {
	if connection != activeConnection {
		return false
	}
	for _, host := range hosts {
		if host.entry.ID == connection {
			return host.state.FocusedPane != nil && *host.state.FocusedPane == pane
		}
	}
	return false
}

func foregroundCommands(state gomodel.StateDump) []foregroundCommand {
	commands := map[uuid.UUID]foregroundCommand{}
	for _, workspace := range state.Workspaces {
		for _, tab := range workspace.Tabs {
			var root paneTree
			if json.Unmarshal(tab.Tree, &root) == nil {
				collectForegroundCommands(&root, commands)
			}
		}
	}
	out := make([]foregroundCommand, 0, len(commands))
	for _, command := range commands {
		out = append(out, command)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].terminal.String() < out[j].terminal.String() })
	return out
}

func collectForegroundCommands(node *paneTree, commands map[uuid.UUID]foregroundCommand) {
	if node == nil {
		return
	}
	if node.Type == "leaf" {
		if node.Terminal == nil || node.Terminal.Summary.TerminalID == uuid.Nil || !terminalProcessRunning(node.Terminal.Summary.Process) {
			return
		}
		name := strings.TrimSpace(node.Terminal.Summary.ProcessName)
		if name != "" {
			commands[node.Terminal.Summary.TerminalID] = foregroundCommand{pane: node.PaneID, terminal: node.Terminal.Summary.TerminalID, name: name}
		}
		return
	}
	collectForegroundCommands(node.First, commands)
	collectForegroundCommands(node.Second, commands)
}

func terminalProcessRunning(raw json.RawMessage) bool {
	var status any
	if len(raw) == 0 || json.Unmarshal(raw, &status) != nil {
		return false
	}
	return status == "running"
}

func isInteractiveShell(name string) bool {
	base := strings.TrimPrefix(strings.ToLower(filepath.Base(name)), "-")
	switch base {
	case "sh", "bash", "zsh", "fish", "ksh", "dash", "nu", "pwsh", "powershell", "cmd":
		return true
	default:
		return false
	}
}

func agentNotificationStatus(raw any) (string, bool) {
	if raw == "running" {
		return "Running", true
	}
	status, ok := raw.(map[string]any)
	if !ok {
		return "Exited", false
	}
	exited, ok := status["exited"].(map[string]any)
	if !ok {
		return "Exited", false
	}
	code, ok := exited["code"]
	if !ok || code == nil {
		return "Completed", false
	}
	switch value := code.(type) {
	case float64:
		if value == 0 {
			return "Completed", false
		}
	case int:
		if value == 0 {
			return "Completed", false
		}
	case int32:
		if value == 0 {
			return "Completed", false
		}
	case int64:
		if value == 0 {
			return "Completed", false
		}
	case *int32:
		if value == nil || *value == 0 {
			return "Completed", false
		}
	}
	return "Error", false
}
