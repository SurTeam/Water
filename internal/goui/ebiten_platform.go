package goui

import (
	"github.com/SurTeam/Water/internal/goconfig"
	"github.com/hajimehoshi/ebiten/v2"
	"os"
	"strings"
)

var finderFolderRequests = make(chan []string, 16)

type nativePlatform interface {
	Update(goconfig.AppConfig)
	Hide()
	Show(activate bool)
	Invoke(string) error
	Snapshot() map[string]any
	RegisterFileDrop(func(paths []string))
}

func (w *EbitengineWindow) SetQuitServerHandler(handler func() error) { w.quitServer = handler }

func (w *EbitengineWindow) menuAction(action string) {
	if strings.HasPrefix(action, "open-folder:") {
		directory := strings.TrimPrefix(action, "open-folder:")
		info, err := os.Stat(directory)
		if err != nil || !info.IsDir() {
			return
		}
		w.multi.mu.RLock()
		var local *managedWorkspaceClient
		for _, id := range w.multi.order {
			connection := w.multi.connections[id]
			if connection != nil && connection.entry.Kind == "local" {
				local = connection
				break
			}
		}
		w.multi.mu.RUnlock()
		if local == nil {
			return
		}
		w.multi.ActivateConnection(local.entry.ID)
		if err := local.view.session.DispatchAsync(map[string]any{"type": "workspace.new", "cwd": directory}); err != nil {
			w.quitError = err.Error()
			return
		}
		w.menuAction("show-window")
		return
	}
	switch action {
	case "check-updates":
		w.updateAction("open")
	case "hide-window":
		w.platform.Hide()
	case "show-window":
		if ebiten.IsWindowMinimized() {
			ebiten.RestoreWindow()
		}
		// In unfocused-test mode the regression runner keeps every GUI
		// InitUnfocused so no test window steals the user's key window.
		// Showing without activating lets tests assert the peer's
		// window_focused=false transition.
		w.platform.Show(!w.testUnfocused())
	case "minimize-window":
		w.minimize()
	case "quit-gui":
		w.closing = true
	case "quit-and-server":
		if w.quitResult != nil || w.quitServer == nil {
			return
		}
		w.quitError = ""
		result := make(chan error, 1)
		w.quitResult = result
		go func() { result <- w.quitServer() }()
	}
}
