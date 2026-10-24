package goui

import (
	"image"
	"image/color"
	"reflect"
	"time"

	"github.com/google/uuid"
	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/vector"
)

type longRunKey struct{ connection, terminal uuid.UUID }
type longRunLocation struct{ connection, workspace, tab uuid.UUID }
type longRunObservation struct {
	identity  foregroundIdentity
	startedAt time.Time
}

// Presentation has its own current-run state: notification records may retain
// commands that have finished, and disabling notifications must not hide work.
func (w *EbitengineWindow) updateLongRunIndicators(hosts []nativeHost, now time.Time) {
	next := make(map[longRunKey]longRunObservation)
	tabs := make(map[longRunLocation]int)
	workspaces := make(map[longRunLocation]int)
	for _, host := range hosts {
		if !agentConnectionConnected(hosts, host.entry.ID) {
			continue
		}
		agents := make(map[uuid.UUID]nativeAgent)
		for _, agent := range host.view.sidebarAgents(host.state) {
			agents[agent.terminal] = agent
		}
		cfg := host.view.currentConfig().UI
		for _, command := range foregroundCommands(host.state) {
			name := command.name
			identity := command.identity
			threshold := cfg.ShellLongRunNotificationSeconds
			if threshold <= 0 {
				threshold = 300
			}
			// Recognized agents have their own status and notifications. They
			// must not also appear as ordinary long-running shell commands.
			if _, ok := agents[command.terminal]; ok {
				continue
			}
			if isInteractiveShell(name) {
				continue
			}
			key := longRunKey{host.entry.ID, command.terminal}
			run := w.longRunObservations[key]
			if run.startedAt.IsZero() || run.identity != identity {
				run = longRunObservation{identity, now}
			}
			next[key] = run
			if now.Sub(run.startedAt) < time.Duration(threshold)*time.Second {
				continue
			}
			tabs[longRunLocation{host.entry.ID, command.workspace, command.tab}]++
			workspaces[longRunLocation{connection: host.entry.ID, workspace: command.workspace}]++
		}
	}
	w.longRunObservations = next
	if !reflect.DeepEqual(w.longRunTabs, tabs) || !reflect.DeepEqual(w.longRunWorkspaces, workspaces) {
		w.longRunTabs, w.longRunWorkspaces = tabs, workspaces
		w.Invalidate()
	}
}

// Overlay markers deliberately do not participate in label measurement or hits.
func (w *EbitengineWindow) drawLongRunDot(dst *ebiten.Image, row image.Rectangle) {
	if dst == nil || row.Empty() {
		return
	}
	radius := float32(max(1, w.dp(2.5)))
	vector.FillCircle(dst, float32(row.Min.X+w.dp(4)), float32(row.Min.Y+w.dp(4)), radius, color.NRGBA{R: 114, G: 214, B: 171, A: 255}, true)
}
