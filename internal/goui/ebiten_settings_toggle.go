package goui

import (
	"image"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/vector"
)

// Settings and server dialogs share the same switch geometry and theme colors.
func (w *EbitengineWindow) drawSettingsToggle(c *WorkspaceClient, dst *ebiten.Image, field image.Rectangle, enabled bool) {
	cfg := c.currentConfig()
	fg := configColor(cfg.Theme.UIForeground, 0xe6eaea)
	toggle := image.Rect(field.Min.X, field.Min.Y+w.dp(7), field.Min.X+w.dp(30), field.Min.Y+w.dp(24))
	bg := mixColor(configColor(cfg.Theme.ChromeBackground, 0x171b20), fg, .15)
	if enabled {
		bg = configColor(cfg.Theme.Accent, 0x72d6ab)
	}
	w.round(dst, toggle, w.dp(9), bg)
	cx := toggle.Min.X + w.dp(8)
	if enabled {
		cx = toggle.Max.X - w.dp(8)
	}
	if dst != nil {
		vector.FillCircle(dst, float32(cx), float32((toggle.Min.Y+toggle.Max.Y)/2), float32(w.dp(6)), fg, true)
	}
}
