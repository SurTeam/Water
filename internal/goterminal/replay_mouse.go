package goterminal

import "bytes"

// Mouse tracking and encoding are independent, finite terminal input settings.
// Keep their final commands when dropping raw replay spans, without retaining
// a server-side screen or an unbounded list of escape sequences.
type replayMouseModes struct {
	tracking string
	encoding string
}

func (m *replayMouseModes) size() int {
	return len(m.tracking) + len(m.encoding)
}

func (m *replayMouseModes) prefix() string {
	return m.tracking + m.encoding
}

func (m *replayMouseModes) observe(span []byte) {
	if bytes.Equal(span, []byte("\x1bc")) {
		if m.tracking != "" {
			m.tracking = "\x1b[?1000l"
		}
		if m.encoding != "" {
			m.encoding = "\x1b[?1006l"
		}
		return
	}
	if len(span) < 5 || span[0] != 0x1b || span[1] != '[' || span[2] != '?' {
		return
	}
	final := span[len(span)-1]
	if final != 'h' && final != 'l' {
		return
	}
	for _, param := range bytes.Split(span[3:len(span)-1], []byte{';'}) {
		switch string(param) {
		case "9", "1000", "1002", "1003":
			// Setting any tracking protocol replaces the previous one;
			// resetting any of them disables tracking altogether.
			m.tracking = "\x1b[?" + string(param) + string(final)
		case "1006", "1016":
			m.encoding = "\x1b[?" + string(param) + string(final)
		}
	}
}
