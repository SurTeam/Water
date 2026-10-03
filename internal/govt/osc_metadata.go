package govt

import (
	"encoding/base64"
	"net/url"
	"strings"
	"unicode/utf8"
)

const maxClipboardBytes = 1024 * 1024

func (e *Emulator) registerOSCMetadata() {
	e.term.RegisterOSCHandler(7, func(data string) bool {
		if len(data) > 8192 {
			return true
		}
		u, err := url.Parse(data)
		if err == nil && u.Scheme == "file" && strings.HasPrefix(u.Path, "/") && u.RawQuery == "" && u.Fragment == "" && !strings.ContainsAny(u.Path, "\x00\r\n") {
			e.workingDirectoryURI = data
		}
		return true
	})
	e.term.RegisterOSCHandler(52, func(data string) bool {
		selection, encoded, ok := strings.Cut(data, ";")
		if !ok || len(selection) > 16 {
			return true
		}
		for _, r := range selection {
			if !strings.ContainsRune("cpqs01234567", r) {
				return true
			}
		}
		// Water provides the system clipboard, not X11 primary/cut buffers.
		if selection != "" && !strings.ContainsAny(selection, "cs") {
			return true
		}
		if encoded == "?" {
			// A terminal program cannot read the user's system clipboard.
			e.enqueueResponse([]byte("\x1b]52;" + selection + ";\x1b\\"))
			return true
		}
		if e.suppressResponses || len(encoded) > base64.StdEncoding.EncodedLen(maxClipboardBytes) {
			return true
		}
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err == nil && len(decoded) <= maxClipboardBytes && utf8.Valid(decoded) {
			e.clipboardMu.Lock()
			e.clipboardWrite = append([]byte{}, decoded...)
			e.clipboardMu.Unlock()
		}
		return true
	})
}

// TakeClipboardWrite coalesces bounded OSC52 writes; replay never writes.
func (e *Emulator) TakeClipboardWrite() ([]byte, bool) {
	e.clipboardMu.Lock()
	defer e.clipboardMu.Unlock()
	if e.clipboardWrite == nil {
		return nil, false
	}
	data := e.clipboardWrite
	e.clipboardWrite = nil
	return data, true
}
