package goui

import (
	"github.com/SurTeam/Water/internal/goconfig"
	"github.com/hajimehoshi/ebiten/v2"
)

func (w *EbitengineWindow) titleHeight(c *WorkspaceClient) int {
	return w.dp(float64(titlebarHeight(w.chromeUI(c))))
}

func (w *EbitengineWindow) chromeUI(c *WorkspaceClient) goconfig.UIConfig {
	u := c.currentConfig().UI
	u.SidebarVisible = !c.sidebarHidden
	if v := w.views[c]; v != nil && v.sidebarWidth > 0 {
		u.SidebarWidth = v.sidebarWidth
	}
	return u
}

// Compute corner alpha directly instead of keeping a window-sized mask texture.
const nativeCornerShader = `//kage:unit pixels
package main
var Size vec2
var Radius float
var Origin vec2
func Fragment(position vec4, texCoord vec2, color vec4) vec4 {
    local := position.xy-Origin
    center := clamp(local, vec2(Radius), Size-vec2(Radius))
    alpha := clamp(Radius+0.5-distance(local, center), 0, 1)
    return vec4(alpha)*color
}`

func (w *EbitengineWindow) roundedShader() *ebiten.Shader {
	if w.cornerShader == nil {
		shader, err := ebiten.NewShader([]byte(nativeCornerShader))
		if err != nil {
			panic(err)
		}
		w.cornerShader = shader
	}
	return w.cornerShader
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
	bounds := screen.Bounds()
	radius = min(radius, bounds.Dx()/2, bounds.Dy()/2)
	if radius <= 0 {
		return
	}
	op := &ebiten.DrawRectShaderOptions{Blend: ebiten.BlendDestinationIn,
		Uniforms: map[string]any{"Size": []float32{float32(bounds.Dx()), float32(bounds.Dy())}, "Radius": float32(radius), "Origin": []float32{float32(bounds.Min.X), float32(bounds.Min.Y)}}}
	screen.DrawRectShader(bounds.Dx(), bounds.Dy(), w.roundedShader(), op)
}
