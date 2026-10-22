package govt

// ContentCursor identifies the cursor in retained physical rows. TrimmedLines
// makes a primary-buffer row stable while scrollback is evicted.
type ContentCursor struct {
	Row          int
	TotalRows    int
	TrimmedLines uint64
	AltScreen    bool
}

// ReadContentCursor observes the active buffer without publishing a frame.
func (e *Emulator) ReadContentCursor() ContentCursor {
	e.mu.RLock()
	defer e.mu.RUnlock()
	b := e.term.Buffer()
	return ContentCursor{Row: b.YBase + b.Y, TotalRows: b.Lines.Length(), TrimmedLines: e.trimmedLines, AltScreen: e.term.IsAltBufferActive()}
}
