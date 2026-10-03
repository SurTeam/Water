package govt

import (
	"fmt"
	"github.com/SurTeam/Water/internal/xterm"
)

// WindowMetrics are physical pixels from the client owning this terminal.
type WindowMetrics struct {
	Width, Height, ScreenWidth, ScreenHeight int
	X, Y, TextX, TextY                       int
	Iconified                                bool
}

func (e *Emulator) SetWindowMetrics(metrics WindowMetrics) {
	e.windowMetrics.Store(&metrics)
}

func (e *Emulator) reportWindow(kind xterm.WindowsOptionsReportType) {
	cw, ch := max(1, e.graphics.cellWidth), max(1, e.graphics.cellHeight)
	m := e.windowMetrics.Load()
	var response string
	switch kind {
	case xterm.GetWinSizePixels:
		response = fmt.Sprintf("\x1b[4;%d;%dt", e.term.Rows()*ch, e.term.Cols()*cw)
	case xterm.GetCellSizePixels:
		response = fmt.Sprintf("\x1b[6;%d;%dt", ch, cw)
	case xterm.GetWindowSizePixels:
		if m != nil {
			response = fmt.Sprintf("\x1b[4;%d;%dt", m.Height, m.Width)
		}
	case xterm.GetScreenSizePixels:
		if m != nil {
			response = fmt.Sprintf("\x1b[5;%d;%dt", m.ScreenHeight, m.ScreenWidth)
		}
	case xterm.GetScreenSizeChars:
		if m != nil {
			response = fmt.Sprintf("\x1b[9;%d;%dt", m.ScreenHeight/ch, m.ScreenWidth/cw)
		}
	case xterm.GetWindowPosition:
		if m != nil {
			response = fmt.Sprintf("\x1b[3;%d;%dt", m.X&65535, m.Y&65535)
		}
	case xterm.GetTextAreaPosition:
		if m != nil {
			response = fmt.Sprintf("\x1b[3;%d;%dt", m.TextX&65535, m.TextY&65535)
		}
	case xterm.GetWindowState:
		if m != nil {
			state := 1
			if m.Iconified {
				state = 2
			}
			response = fmt.Sprintf("\x1b[%dt", state)
		}
	}
	e.enqueueResponse([]byte(response))
}
