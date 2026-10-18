package govt

import "strings"

// Bound the whole record as well as the ignored base64 message. Only root
// records are supported; unknown fields (including job identifiers) are ignored
// by rejecting the complete record rather than replacing the root status.
const maxProgramStatusBytes = 3072
const maxProgramStatusMessageBytes = 2732

func (e *Emulator) handleProgramStatus(data string) bool {
	if data == "?" {
		e.enqueueResponse([]byte("\x1b]7501;?\x1b\\"))
		return true
	}
	if len(data) > maxProgramStatusBytes {
		return true
	}
	var state, app, kind string
	seen := make(map[string]bool, 4)
	for _, pair := range strings.Split(data, ":") {
		key, value, ok := strings.Cut(pair, "=")
		if !ok || seen[key] {
			return true
		}
		seen[key] = true
		switch key {
		case "state":
			switch value {
			case "idle", "working", "blocked", "done", "error", "clear":
				state = value
			default:
				return true
			}
		case "app":
			if len(value) == 0 || len(value) > 32 {
				return true
			}
			for _, c := range []byte(value) {
				if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("_.+-", rune(c))) {
					return true
				}
			}
			app = value
		case "kind":
			if value != "permission" && value != "question" && value != "auth" {
				return true
			}
			kind = value
		case "msg":
			if len(value) > maxProgramStatusMessageBytes {
				return true
			}
			// The UI does not consume message text. Check the base64 alphabet
			// without allocating or decoding untrusted content.
			for _, c := range []byte(value) {
				if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '+' || c == '/' || c == '=') {
					return true
				}
			}
		default:
			return true
		}
	}
	if state == "" || kind != "" && state != "blocked" {
		return true
	}
	if state == "clear" {
		state, app = "", ""
		e.progress.Store(0)
	}
	e.programStatusState, e.programStatusApp = state, app
	return true
}

// ProgramStatus returns client-local OSC 7501 root metadata, reconstructed by
// replay. A clear report or hard reset removes the report. App is optional and
// callers must match it before attributing a report to a particular program.
func (e *Emulator) ProgramStatus() (state, app string, reported bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.programStatusState, e.programStatusApp, e.programStatusState != ""
}

// ResetAgentStatus discards metadata associated with a previous foreground run.
func (e *Emulator) ResetAgentStatus() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.programStatusState, e.programStatusApp = "", ""
	e.progress.Store(0)
}
