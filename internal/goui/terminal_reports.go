package goui

import (
	"github.com/SurTeam/Water/internal/govt"
	"github.com/hajimehoshi/ebiten/v2"
	"golang.design/x/clipboard"
	"image"
)

func (w *EbitengineWindow) updateTerminalMetrics(term *terminalClient, rect image.Rectangle) {
	x, y := ebiten.WindowPosition()
	m := govt.WindowMetrics{Width: w.size.X, Height: w.size.Y, X: w.dp(float64(x)), Y: w.dp(float64(y)), Iconified: ebiten.IsWindowMinimized()}
	m.TextX, m.TextY = m.X+rect.Min.X, m.Y+rect.Min.Y
	if monitor := ebiten.Monitor(); monitor != nil {
		sw, sh := monitor.Size()
		m.ScreenWidth, m.ScreenHeight = w.dp(float64(sw)), w.dp(float64(sh))
	}
	term.emu.SetWindowMetrics(m)
}

func (w *EbitengineWindow) processTerminalReports() {
	w.multi.mu.RLock()
	defer w.multi.mu.RUnlock()
	for _, connection := range w.multi.connections {
		c := connection.view
		c.oscClipboardMu.Lock()
		data := c.oscClipboardWrite
		c.oscClipboardWrite = nil
		c.oscClipboardMu.Unlock()
		if data != nil && w.clipboardReady {
			clipboard.Write(clipboard.FmtText, data)
		}
	}
}
