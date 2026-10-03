package goui

import (
	"github.com/hajimehoshi/ebiten/v2"
	"image/color"
)

func (w *EbitengineWindow) titleHeight(c *WorkspaceClient) int {
	u := c.currentConfig().UI
	return w.dp(float64(max(u.TitlebarHeight, u.TabHeight+4)))
}

func (w *EbitengineWindow) maskWindow(screen *ebiten.Image) {
	c := w.active()
	if c == nil {
		return
	}
	radius := w.dp(float64(c.currentConfig().UI.WindowCornerRadius))
	if ebiten.IsWindowMaximized() || ebiten.IsFullscreen() {
		radius = 0
	}
	if radius == 0 {
		return
	}
	bounds := screen.Bounds()
	if w.cornerMask == nil || w.cornerMask.Bounds() != bounds || w.cornerRadius != radius {
		if w.cornerMask != nil {
			w.cornerMask.Deallocate()
		}
		w.cornerMask = ebiten.NewImage(bounds.Dx(), bounds.Dy())
		w.round(w.cornerMask, bounds, radius, color.NRGBA{R: 255, G: 255, B: 255, A: 255})
		w.cornerRadius = radius
	}
	screen.DrawImage(w.cornerMask, &ebiten.DrawImageOptions{Blend: ebiten.BlendDestinationIn})
}
