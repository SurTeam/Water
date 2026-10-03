package govt

import (
	"bytes"
	"strings"
)

const maxOSC8Bytes = 64 * 1024

type osc8Event struct {
	params string
	uri    string
}

type osc8Parser struct {
	state uint8
	buf   []byte
}

const (
	osc8Normal uint8 = iota
	osc8Escape
	osc8Body
	osc8BodyEscape
)

func (p *osc8Parser) feed(data []byte) []osc8Event {
	if len(data) > 0 && (p.state == osc8Normal || p.state == osc8Escape) &&
		!(p.state == osc8Escape && data[0] == ']') &&
		bytes.IndexByte(data, 0x9d) < 0 && !bytes.Contains(data, []byte("\x1b]")) {
		// An ordinary CSI/SGR chunk cannot open an OSC. Keep a trailing ESC
		// pending, but avoid a state-machine dispatch for every text byte.
		p.state = osc8Normal
		if data[len(data)-1] == 0x1b {
			p.state = osc8Escape
		}
		return nil
	}
	var out []osc8Event
	for _, b := range data {
		switch p.state {
		case osc8Normal:
			switch b {
			case 0x1b:
				p.state = osc8Escape
			case 0x9d:
				p.startBody()
			}
		case osc8Escape:
			if b == ']' {
				p.startBody()
			} else if b == 0x1b {
				p.state = osc8Escape
			} else {
				p.state = osc8Normal
			}
		case osc8Body:
			switch b {
			case 0x07, 0x9c:
				if ev, ok := p.finish(); ok {
					out = append(out, ev)
				}
			case 0x1b:
				p.state = osc8BodyEscape
			default:
				p.append(b)
			}
		case osc8BodyEscape:
			if b == '\\' {
				if ev, ok := p.finish(); ok {
					out = append(out, ev)
				}
			} else {
				p.append(0x1b)
				p.append(b)
				if p.state != osc8Normal {
					p.state = osc8Body
				}
			}
		}
	}
	return out
}

func (p *osc8Parser) startBody() {
	p.state = osc8Body
	p.buf = p.buf[:0]
}

func (p *osc8Parser) append(b byte) {
	if len(p.buf) >= maxOSC8Bytes {
		p.buf = p.buf[:0]
		p.state = osc8Normal
		return
	}
	p.buf = append(p.buf, b)
}

func (p *osc8Parser) finish() (osc8Event, bool) {
	data := string(p.buf)
	p.buf = p.buf[:0]
	p.state = osc8Normal
	if !strings.HasPrefix(data, "8;") {
		return osc8Event{}, false
	}
	rest := strings.TrimPrefix(data, "8;")
	idx := strings.IndexByte(rest, ';')
	if idx < 0 {
		return osc8Event{}, false
	}
	return osc8Event{params: rest[:idx], uri: rest[idx+1:]}, true
}
