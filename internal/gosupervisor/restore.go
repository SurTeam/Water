package gosupervisor

import (
	"strconv"
	"strings"
)

// historyRestore records only finite input modes and parser state at the raw
// history boundary. It is not a screen snapshot and never changes byte offsets.
type historyRestore struct {
	state   byte
	osc     bool
	pending string
	modes   [8]int
}

func (r *historyRestore) observe(data []byte) {
	for _, b := range data {
		switch r.state {
		case 0:
			if b == 0x1b {
				r.state = 'e'
			}
		case 'e':
			if b < 0x20 && b != 0x18 && b != 0x1a && b != 0x1b || b == 0x7f {
				continue
			}
			switch b {
			case '[':
				r.state, r.pending = 'c', "\x1b["
			case ']':
				r.state = 's'
				r.osc = true
			case 'P', 'X', '^', '_':
				r.state = 's'
				r.osc = false
			case 'c':
				*r = historyRestore{}
			case '=':
				r.modes[7] = 66
				r.state = 0
			case '>':
				r.modes[7] = 0
				r.state = 0
			case 0x1b:
			default:
				r.state = 0
			}
		case 'c':
			if b < 0x20 && b != 0x18 && b != 0x1a && b != 0x1b || b == 0x7f {
				continue
			}
			if b == 0x1b {
				r.state, r.pending = 'e', ""
				continue
			}
			if b == 0x18 || b == 0x1a {
				r.state, r.pending = 0, ""
				continue
			}
			if len(r.pending) < 128 {
				r.pending += string(b)
			} else {
				r.pending = ""
			}
			if b >= 0x40 && b <= 0x7e {
				r.command(r.pending)
				r.state, r.pending = 0, ""
			}
		case 's':
			if (b == 0x07 && r.osc) || b == 0x18 || b == 0x1a {
				r.state = 0
			} else if b == 0x1b {
				r.state = 'e'
			}
		}
	}
}

func (r *historyRestore) command(seq string) {
	if len(seq) < 5 || !strings.HasPrefix(seq, "\x1b[?") {
		return
	}
	final := seq[len(seq)-1]
	if final != 'h' && final != 'l' {
		return
	}
	for _, p := range strings.Split(seq[3:len(seq)-1], ";") {
		n, err := strconv.Atoi(p)
		if err != nil {
			continue
		}
		slot := -1
		switch n {
		case 47, 1047, 1049:
			slot = 0
		case 9, 1000, 1002, 1003:
			slot = 1
		case 1006, 1016:
			slot = 2
		case 1004:
			slot = 3
		case 2004:
			slot = 4
		case 1:
			slot = 5
		case 6:
			slot = 6
		case 66:
			slot = 7
		}
		if slot >= 0 {
			if final == 'h' {
				r.modes[slot] = n
			} else {
				r.modes[slot] = 0
			}
		}
	}
}

func (r *historyRestore) bytes() []byte {
	var out strings.Builder
	for _, n := range r.modes {
		if n != 0 {
			out.WriteString("\x1b[?" + strconv.Itoa(n) + "h")
		}
	}
	switch r.state {
	case 'e':
		out.WriteByte(0x1b)
	case 'c':
		out.WriteString(r.pending)
	case 's':
		if r.osc {
			out.WriteString("\x1b]")
		} else {
			out.WriteString("\x1bP")
		} // discard an incomplete control string safely
	}
	return []byte(out.String())
}
