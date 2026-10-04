package govt

import "github.com/SurTeam/Water/internal/xterm"

func (e *Emulator) SetCursorDefaults(style string, blink bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.term.SetCursorDefaults(xterm.CursorStyle(style), blink)
}
