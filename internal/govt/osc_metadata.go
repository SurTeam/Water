package govt

import (
	"encoding/base64"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/SurTeam/Water/internal/xterm"
)

const maxClipboardBytes = 1024 * 1024

func (e *Emulator) registerOSCMetadata() {
	e.term.RegisterEscHandler(xterm.FunctionIdentifier{Final: 'c'}, func() bool {
		if e.progress.Load() != 0 {
			e.progress.Store(1) // RIS withdraws a previously reported indicator.
		}
		return false // Preserve the terminal's normal hard-reset handler.
	})
	e.term.RegisterOSCHandler(9, func(data string) bool {
		if len(data) > 64 {
			return true
		}
		fields := strings.Split(data, ";")
		if len(fields) < 2 || len(fields) > 3 || fields[0] != "4" || len(fields[1]) != 1 || fields[1][0] < '0' || fields[1][0] > '4' {
			return true
		}
		if len(fields) == 3 {
			if fields[2] == "" || strings.IndexFunc(fields[2], func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
				return true
			}
			if _, err := strconv.ParseUint(fields[2], 10, 32); err != nil {
				return true
			}
		}
		e.progress.Store(uint32(fields[1][0]-'0') + 1)
		return true
	})
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

// ProgressState is client-local metadata, also reconstructed by bounded replay.
// State 4 means paused/warning, not necessarily an approval request.
func (e *Emulator) ProgressState() (state int, reported bool) {
	value := e.progress.Load()
	return int(value) - 1, value != 0
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
