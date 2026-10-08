package goui

import (
	"fmt"
	"image"
	"strings"

	"github.com/SurTeam/Water/internal/goupdate"
	"github.com/google/uuid"
	"github.com/hajimehoshi/ebiten/v2"
)

func (w *EbitengineWindow) SetUpdateManager(m *goupdate.Manager) {
	w.updates = m
	w.updateVisible = m.Snapshot().Error != ""
}
func (w *EbitengineWindow) SetUpdateRestartAllowed(allowed bool) {
	w.updateRestartAllowed.Store(allowed)
}
func (w *EbitengineWindow) Updating() bool { return w.updating.Load() }

func (w *EbitengineWindow) updateAction(action string) {
	if w.updates == nil {
		return
	}
	switch action {
	case "open":
		w.updateVisible = true
		if w.updates.Snapshot().Phase == "idle" {
			w.updates.Check()
		}
	case "close":
		if w.updateResult == nil {
			w.updateVisible = false
		}
	case "check":
		w.updates.Check()
	case "download":
		w.updates.Download()
	case "install":
		if !w.updateRestartAllowed.Load() || w.updateResult != nil || w.multi != nil && w.multi.serverOperationBusy() {
			return
		}
		result := make(chan error, 1)
		w.updateResult = result
		w.updating.Store(true)
		go func() { result <- w.updates.Install(); w.Invalidate() }()
	}
	w.Invalidate()
}

func (w *EbitengineWindow) drawUpdate(c *WorkspaceClient, dst *ebiten.Image) {
	if w.updates == nil {
		return
	}
	s := w.updates.Snapshot()
	r := w.overlayPanel(c, dst, w.dp(590), w.dp(320), !c.settings.visible)
	cfg := c.currentConfig()
	fg := configColor(cfg.Theme.UIForeground, 0xe6eaea)
	muted := mixColor(configColor(cfg.Theme.ChromeBackground, 0x171b20), fg, .6)
	label := func(y int, text string, size float64, clr bool) {
		color := fg
		if clr {
			color = muted
		}
		w.label(dst, c, image.Rect(r.Min.X+w.dp(24), r.Min.Y+w.dp(float64(y)), r.Max.X-w.dp(24), r.Min.Y+w.dp(float64(y+24))), text, size, color, false)
	}
	label(22, c.tr("Software update"), 18, false)
	label(58, c.trf("Current version: %s", s.Current), 12, true)
	message := "Check for updates"
	switch s.Phase {
	case "checking":
		message = "Checking for updates…"
	case "current":
		message = "No compatible updates are available"
	case "available":
		message = c.trf("Version %s is available", s.Version)
	case "downloading":
		message = fmt.Sprintf("%s  %.1f / %.1f MB", c.tr("Downloading and verifying…"), float64(s.Downloaded)/(1<<20), float64(s.Total)/(1<<20))
	case "ready":
		message = c.trf("Version %s is ready to install", s.Version)
	case "installing":
		message = "Installing update…"
	case "error":
		message = "Update failed. You can try again."
	}
	label(98, c.tr(message), 12, false)
	if s.Error != "" {
		words := strings.Fields(s.Error)
		line, y := "", 130
		for _, word := range words {
			if len([]rune(line+word)) > 74 && line != "" {
				label(y, line, 10, true)
				y += 22
				line = ""
				if y > 174 {
					break
				}
			}
			line += word + " "
		}
		if line != "" {
			label(y, line, 10, true)
		}
	} else {
		label(136, c.tr("Updates are downloaded from Water's GitHub Releases."), 11, true)
	}
	if s.Phase == "ready" {
		warning := "Restart keeps detached terminal servers running."
		if !w.updateRestartAllowed.Load() {
			warning = "Restart requires a detached local server. Change Server settings first."
		}
		label(210, c.tr(warning), 10, true)
	}
	primary, action := "Check for updates", "check"
	if s.Phase == "available" || s.Phase == "error" && s.Version != "" {
		primary, action = "Download update", "download"
	}
	if s.Phase == "ready" {
		primary, action = "Install and restart", "install"
	}
	button := func(rect image.Rectangle, title, action string) {
		w.button(dst, c, rect, title, hitSettingsControl, uuid.Nil, false)
		c.hitRegions[len(c.hitRegions)-1].Label = "update:" + action
	}
	if s.Phase != "checking" && s.Phase != "downloading" && s.Phase != "installing" && w.updateResult == nil && (action != "install" || w.updateRestartAllowed.Load()) {
		button(image.Rect(r.Max.X-w.dp(250), r.Max.Y-w.dp(58), r.Max.X-w.dp(24), r.Max.Y-w.dp(24)), primary, action)
	}
	if w.updateResult == nil {
		button(image.Rect(r.Min.X+w.dp(24), r.Max.Y-w.dp(58), r.Min.X+w.dp(144), r.Max.Y-w.dp(24)), "Close", "close")
	}
}
