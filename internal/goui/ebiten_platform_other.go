//go:build !darwin

package goui

import (
	"fmt"
	"github.com/SurTeam/Water/internal/goconfig"
	"github.com/hajimehoshi/ebiten/v2"
)

type otherNativePlatform struct {
	emit          func(string)
	hidden        bool
	canQuitServer bool
}

func newNativePlatform(emit func(string), canQuitServer bool) (nativePlatform, error) {
	return &otherNativePlatform{emit: emit, canQuitServer: canQuitServer}, nil
}
func (p *otherNativePlatform) Update(goconfig.AppConfig) {}
func (p *otherNativePlatform) Hide()                     { ebiten.SetWindowVisible(false); p.hidden = true }
func (p *otherNativePlatform) Show()                     { ebiten.SetWindowVisible(true); p.hidden = false }
func (p *otherNativePlatform) Snapshot() map[string]any {
	return map[string]any{"ready": true, "hidden": p.hidden}
}
func (p *otherNativePlatform) Invoke(action string) error {
	switch action {
	case "hide-window", "show-window", "minimize-window", "quit-gui", "check-updates":
	case "quit-and-server":
		if !p.canQuitServer {
			return fmt.Errorf("no local server")
		}
	default:
		return fmt.Errorf("unknown menu action %q", action)
	}
	p.emit(action)
	return nil
}
