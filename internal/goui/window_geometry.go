package goui

import (
	"image"
	"math"

	"github.com/SurTeam/Water/internal/goconfig"
	"github.com/hajimehoshi/ebiten/v2"
)

func windowGridSize(cfg goconfig.AppConfig, scale float64, cell nativeCellMetrics, columns, rows int) image.Point {
	dp := func(value float32) int { return int(float64(value)*scale + .5) }
	padding := dp(cfg.UI.WindowPadding)
	left := padding
	if cfg.UI.SidebarVisible {
		left = dp(cfg.UI.SidebarWidth) - padding + dp(cfg.UI.SidebarResizeHandleWidth)
	}
	width := columns*cell.width + left + padding + 2*dp(cfg.UI.PanePadding)
	title := dp(titlebarHeight(cfg.UI))
	height := rows*cell.height + title + 2*padding + 2*dp(cfg.UI.PanePadding)
	return image.Pt(int(math.Ceil(float64(width)/scale)), int(math.Ceil(float64(height)/scale)))
}

func titlebarBaseHeight(u goconfig.UIConfig) float32 {
	return max(u.TitlebarHeight, u.TabHeight+4, u.TabFontSize+12)
}
func titlebarHeight(u goconfig.UIConfig) float32 {
	return titlebarBaseHeight(u)
}

// Called before RunGame, after startup font resolution, using the same physical
// cell metrics and chrome geometry as Draw. DPI rounding leaves at most one
// logical pixel to distribute around the fitted grid.
func (w *EbitengineWindow) InitialWindowSize() image.Point {
	w.refreshDisplayScale()
	c := w.active()
	cell := measureNativeCell(w.terminalFaces(c), w.dp(float64(w.cfg.Terminal.LineHeight)))
	return windowGridSize(w.cfg, w.scale, cell, w.cfg.Startup.WindowColumns, w.cfg.Startup.WindowRows)
}

func (w *EbitengineWindow) refreshDisplayScale() {
	if monitor := ebiten.Monitor(); monitor != nil {
		w.scale = monitor.DeviceScaleFactor()
	}
	// macOS may temporarily expose no monitor while its display is asleep.
	// Preserve the last scale until the monitor becomes available again.
	w.scale = max(1, w.scale)
}
func (w *EbitengineWindow) MinimumWindowSize() image.Point {
	c := w.active()
	cfg := c.currentConfig()
	cell := measureNativeCell(w.terminalFaces(c), w.dp(float64(cfg.Terminal.LineHeight)))
	return windowGridSize(cfg, w.scale, cell, cfg.Startup.WindowMinColumns, cfg.Startup.WindowMinRows)
}

func fittedTerminalGrid(r image.Rectangle, cw, lh int) image.Rectangle {
	width, height := r.Dx()/cw*cw, r.Dy()/lh*lh
	if width == 0 || height == 0 {
		return r
	}
	left, top := (r.Dx()-width)/2, (r.Dy()-height)/2
	return image.Rect(r.Min.X+left, r.Min.Y+top, r.Min.X+left+width, r.Min.Y+top+height)
}

// Legacy Gio headless captures have no native window/font resolver. Their
// fallback canvas still follows the grid config; live captures use frameSize.
func fallbackWindowSize(cfg goconfig.AppConfig) image.Point {
	return windowGridSize(cfg, 1, nativeCellMetrics{width: max(1, int(cfg.Terminal.FontSize*.6)), height: max(1, int(cfg.Terminal.LineHeight))}, cfg.Startup.WindowColumns, cfg.Startup.WindowRows)
}
