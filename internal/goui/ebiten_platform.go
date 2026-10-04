package goui

import (
	"github.com/SurTeam/Water/internal/goconfig"
	"github.com/hajimehoshi/ebiten/v2"
)

type nativePlatform interface {
	Update(goconfig.AppConfig)
	Hide()
	Show()
	Invoke(string) error
	Snapshot() map[string]any
}

func (w *EbitengineWindow) SetQuitServerHandler(handler func() error) { w.quitServer = handler }

func (w *EbitengineWindow) menuAction(action string) {
	switch action {
	case "check-updates":
		w.updateAction("open")
	case "hide-window":
		w.platform.Hide()
	case "show-window":
		if ebiten.IsWindowMinimized() {
			ebiten.RestoreWindow()
		}
		w.platform.Show()
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
