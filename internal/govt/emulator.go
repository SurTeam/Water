package govt

import (
	"sync"

	xterm "github.com/gitpod-io/xterm-go"
)

type Emulator struct {
	mu   sync.RWMutex
	term *xterm.Terminal
}

func New(cols, rows, scrollback int) *Emulator {
	if cols < 2 {
		cols = 80
	}
	if rows < 1 {
		rows = 24
	}
	if scrollback < 1 {
		scrollback = 2000
	}
	return &Emulator{
		term: xterm.New(
			xterm.WithCols(cols),
			xterm.WithRows(rows),
			xterm.WithScrollback(scrollback),
		),
	}
}

func (e *Emulator) Close() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.term.Dispose()
}

func (e *Emulator) Write(p []byte) {
	e.mu.Lock()
	_, _ = e.term.Write(p)
	e.mu.Unlock()
}

func (e *Emulator) Resize(cols, rows int) {
	e.mu.Lock()
	e.term.Resize(cols, rows)
	e.mu.Unlock()
}

func (e *Emulator) Scroll(lines int) {
	e.mu.Lock()
	e.term.ScrollLines(lines)
	e.mu.Unlock()
}

func (e *Emulator) Text() string {
	e.mu.RLock()
	s := e.term.String()
	e.mu.RUnlock()
	return s
}

func (e *Emulator) Cursor() (int, int) {
	e.mu.RLock()
	x, y := e.term.CursorX(), e.term.CursorY()
	e.mu.RUnlock()
	return x, y
}
