package goterminal

import (
	"bytes"

	"github.com/SurTeam/Water/internal/goprotocol"
)

// Stored replay is what a later attach uses to rebuild the GUI. Live
// subscribers still receive every PTY byte. Primary-buffer lines are kept up
// to the configured scrollback plus the viewport. Alternate-screen output is
// kept only while that screen is active, and it is never counted as scrollback,
// so leaving the TUI restores the normal terminal history.

func (t *Terminal) storeLocked(ev goprotocol.TerminalEvent) {
	switch ev.Kind {
	case goprotocol.OutputEvent:
		t.ingestOutputLocked(ev.Data, ev.Size, ev.Seq)
	default:
		t.flushScanHoldLocked(ev.Size, ev.Seq)
		t.replay = append(t.replay, ev)
	}
}

func (t *Terminal) ingestOutputLocked(data []byte, size goprotocol.TerminalSize, seq uint64) {
	if len(t.scanHold) > 0 {
		combined := make([]byte, 0, len(t.scanHold)+len(data))
		combined = append(combined, t.scanHold...)
		combined = append(combined, data...)
		t.scanHold = nil
		data = combined
	}
	for len(data) > 0 {
		esc := bytes.IndexByte(data, 0x1b)
		if esc < 0 {
			t.writeStreamLocked(data, size, seq)
			return
		}
		if esc > 0 {
			t.writeStreamLocked(data[:esc], size, seq)
			data = data[esc:]
		}
		n, kind, complete := classifyEscape(data)
		if !complete {
			if len(data) > 64 {
				t.writeStreamLocked(data[:1], size, seq)
				data = data[1:]
				continue
			}
			t.scanHold = append([]byte(nil), data...)
			return
		}
		switch kind {
		case escapeAltEnter:
			if !t.altScreen {
				t.altScreen = true
				t.writePrimaryLocked(data[:n], size, seq)
			} else {
				t.writeAltLocked(data[:n], seq)
			}
		case escapeAltLeave:
			if t.altScreen {
				t.altReplay = nil
				t.altScreen = false
			}
			t.writePrimaryLocked(data[:n], size, seq)
		default:
			t.writeStreamLocked(data[:n], size, seq)
		}
		data = data[n:]
	}
}

func (t *Terminal) writeStreamLocked(data []byte, size goprotocol.TerminalSize, seq uint64) {
	if t.altScreen {
		t.writeAltLocked(data, seq)
		return
	}
	t.writePrimaryLocked(data, size, seq)
}

func (t *Terminal) writePrimaryLocked(data []byte, size goprotocol.TerminalSize, seq uint64) {
	if len(data) == 0 {
		return
	}
	t.notePrimarySegmentsLocked(data)
	t.outputNewlines += bytes.Count(data, []byte{'\n'})
	if n := len(t.replay); n > 0 {
		last := &t.replay[n-1]
		if last.Kind == goprotocol.OutputEvent && last.Size == size && len(last.Data) < coalesceReplayBytes {
			last.Data = append(last.Data, data...)
			last.Seq = seq
			return
		}
	}
	t.replay = append(t.replay, goprotocol.TerminalEvent{
		Kind: goprotocol.OutputEvent,
		Seq:  seq,
		Size: size,
		Data: append([]byte(nil), data...),
	})
}

func (t *Terminal) writeAltLocked(data []byte, seq uint64) {
	if len(data) == 0 {
		return
	}
	t.altReplay = append(t.altReplay, data...)
	t.altSeq = seq
}

func (t *Terminal) flushScanHoldLocked(size goprotocol.TerminalSize, seq uint64) {
	if len(t.scanHold) == 0 {
		return
	}
	data := t.scanHold
	t.scanHold = nil
	t.writeStreamLocked(data, size, seq)
}

func (t *Terminal) trimReplayLocked() {
	cols, rows := t.size.Columns, t.size.Lines
	if cols < 2 {
		cols = 80
	}
	if rows < 1 {
		rows = 24
	}
	scrollback := t.scrollbackLines
	if scrollback < 1 {
		scrollback = defaultScrollbackLines
	}
	target := scrollback + rows
	for t.outputNewlines > target && t.dropOnePrimaryLineLocked() {
	}
	limit := screenReplayLimit(cols, rows)
	t.shrinkPrimaryRedrawsLocked(limit)
	if t.altScreen {
		t.altReplay = capEscapeBytes(t.altReplay, limit)
	}
	t.accountLocked()
	if cap(t.replay) > 8+2*len(t.replay) {
		next := make([]goprotocol.TerminalEvent, len(t.replay))
		copy(next, t.replay)
		t.replay = next
	}
}

func (t *Terminal) dropOnePrimaryLineLocked() bool {
	for len(t.replay) > 0 {
		ev := t.replay[0]
		if ev.Kind == goprotocol.ExitEvent {
			return false
		}
		if ev.Kind != goprotocol.OutputEvent {
			t.replay[0] = goprotocol.TerminalEvent{}
			t.replay = t.replay[1:]
			continue
		}
		i := bytes.IndexByte(ev.Data, '\n')
		if i < 0 {
			t.replay[0] = goprotocol.TerminalEvent{}
			t.replay = t.replay[1:]
			continue
		}
		ev.Data = dropReplayPrefix(ev.Data, i+1)
		t.outputNewlines--
		if len(ev.Data) == 0 {
			t.replay[0] = goprotocol.TerminalEvent{}
			t.replay = t.replay[1:]
		} else {
			t.replay[0] = ev
		}
		return true
	}
	return false
}

func dropReplayPrefix(data []byte, n int) []byte {
	if n >= len(data) {
		return nil
	}
	next := data[n:]
	if n > len(next) && n > 64*1024 {
		return append([]byte(nil), next...)
	}
	return next
}

// Only the trailing line and newly appended lines can become oversized. Keep
// this check proportional to new bytes instead of repeatedly scanning retained
// scrollback, which can contain many megabytes of short, escaped lines.
func (t *Terminal) notePrimarySegmentsLocked(data []byte) {
	for len(data) > 0 {
		i := bytes.IndexByte(data, '\n')
		segment := data
		if i >= 0 {
			segment = data[:i]
		}
		t.primarySegmentBytes += len(segment)
		t.primarySegmentEscape = t.primarySegmentEscape || bytes.IndexByte(segment, 0x1b) >= 0
		if t.primarySegmentBytes > t.primaryRedrawLimit && t.primarySegmentEscape {
			t.primaryRedrawCheck = true
		}
		if i < 0 {
			return
		}
		t.primarySegmentBytes = 0
		t.primarySegmentEscape = false
		data = data[i+1:]
	}
}

// Compaction can replace the trailing segment. Rebuild just that segment's
// metadata; a limit change also uses this after rechecking existing history.
func (t *Terminal) resetPrimarySegmentLocked() {
	t.primarySegmentBytes = 0
	t.primarySegmentEscape = false
	for i := len(t.replay) - 1; i >= 0; i-- {
		ev := t.replay[i]
		if ev.Kind != goprotocol.OutputEvent {
			continue
		}
		data := ev.Data
		newline := bytes.LastIndexByte(data, '\n')
		if newline >= 0 {
			data = data[newline+1:]
		}
		t.primarySegmentBytes += len(data)
		t.primarySegmentEscape = t.primarySegmentEscape || bytes.IndexByte(data, 0x1b) >= 0
		if newline >= 0 {
			return
		}
	}
}

func (t *Terminal) shrinkPrimaryRedrawsLocked(limit int) {
	if t.primaryRedrawLimit == limit && !t.primaryRedrawCheck {
		return
	}
	t.primaryRedrawLimit = limit
	t.primaryRedrawCheck = false
	defer t.resetPrimarySegmentLocked()

	total := 0
	var lastSeq uint64
	var lastSize goprotocol.TerminalSize
	hasEscape := false
	for _, ev := range t.replay {
		if ev.Kind != goprotocol.OutputEvent {
			continue
		}
		total += len(ev.Data)
		lastSeq = ev.Seq
		lastSize = ev.Size
		if !hasEscape && bytes.Contains(ev.Data, []byte{0x1b}) {
			hasEscape = true
		}
	}
	// Plain scrollback lines are kept whole. Once escaped redraws push the
	// primary buffer past one screen, compact each oversized escape segment
	// without dropping the newline-delimited lines around it.
	if !hasEscape || total <= limit || lastSeq == 0 {
		return
	}
	var buf bytes.Buffer
	buf.Grow(min(total, limit*2))
	for _, ev := range t.replay {
		if ev.Kind == goprotocol.OutputEvent {
			buf.Write(ev.Data)
		}
	}
	compacted := compactRedrawSegments(buf.Bytes(), limit)
	if len(compacted) == total {
		return
	}
	replaced := false
	next := make([]goprotocol.TerminalEvent, 0, len(t.replay))
	for _, ev := range t.replay {
		if ev.Kind != goprotocol.OutputEvent {
			next = append(next, ev)
			continue
		}
		if replaced {
			continue
		}
		ev.Data = compacted
		ev.Seq = lastSeq
		if lastSize.Columns > 0 {
			ev.Size = lastSize
		}
		next = append(next, ev)
		replaced = true
	}
	t.replay = next
}

func (t *Terminal) accountLocked() {
	n := 0
	for _, ev := range t.replay {
		n += retainedEventBytes(ev)
	}
	if len(t.altReplay) > 0 {
		n += replayEventOverhead + len(t.altReplay)
	}
	t.replayBytes = n
}

func screenReplayLimit(cols, rows int) int {
	limit := rows * cols * 32
	if limit < 32*1024 {
		limit = 32 * 1024
	}
	if limit > 256*1024 {
		limit = 256 * 1024
	}
	return limit
}

func compactRedrawSegments(data []byte, limit int) []byte {
	var b bytes.Buffer
	rest := data
	changed := false
	for {
		i := bytes.IndexByte(rest, '\n')
		seg := rest
		newline := false
		if i >= 0 {
			seg = rest[:i]
			newline = true
		}
		next := seg
		if len(seg) > limit && bytes.Contains(seg, []byte{0x1b}) {
			next = capEscapeBytes(seg, limit)
			changed = true
		}
		b.Write(next)
		if !newline {
			break
		}
		b.WriteByte('\n')
		rest = rest[i+1:]
	}
	if !changed {
		return data
	}
	return b.Bytes()
}

type escapeKind int

const (
	escapeOther escapeKind = iota
	escapeAltEnter
	escapeAltLeave
)

func classifyEscape(data []byte) (n int, kind escapeKind, complete bool) {
	if len(data) == 0 || data[0] != 0x1b {
		return 0, escapeOther, false
	}
	if len(data) == 1 {
		return 1, escapeOther, false
	}
	if data[1] != '[' {
		return 2, escapeOther, true
	}
	i := 2
	for i < len(data) {
		if data[i] >= 0x40 && data[i] <= 0x7e {
			seq := data[:i+1]
			return i + 1, altScreenKind(seq), true
		}
		i++
		if i-2 > 64 {
			return 2, escapeOther, true
		}
	}
	return len(data), escapeOther, false
}

func altScreenKind(seq []byte) escapeKind {
	if len(seq) < 4 || seq[0] != 0x1b || seq[1] != '[' || seq[2] != '?' {
		return escapeOther
	}
	final := seq[len(seq)-1]
	if final != 'h' && final != 'l' {
		return escapeOther
	}
	params := string(seq[3 : len(seq)-1])
	if !hasAltMode(params) {
		return escapeOther
	}
	if final == 'h' {
		return escapeAltEnter
	}
	return escapeAltLeave
}

func hasAltMode(params string) bool {
	for _, param := range bytes.Split([]byte(params), []byte{';'}) {
		switch string(param) {
		case "47", "1047", "1049":
			return true
		}
	}
	return false
}

func capEscapeBytes(data []byte, limit int) []byte {
	if len(data) == 0 || limit < 1 {
		return nil
	}
	// Both cursors advance by complete spans. Each span is parsed at most
	// twice, without building a table proportional to the escape count.
	start, end := 0, 0
	for end < len(data) {
		next, complete := replaySpanEnd(data, end)
		if !complete {
			break
		}
		if next-end > limit {
			// An oversized span cannot belong to any retained suffix.
			start = next
		}
		end = next
		for end-start > limit {
			start, _ = replaySpanEnd(data, start)
		}
	}
	if end == len(data) && len(data) <= limit {
		return data
	}
	if start == end {
		return nil
	}
	// Detach the capped suffix from the oversized input allocation.
	return append([]byte(nil), data[start:end]...)
}

// replaySpanEnd matches the complete-span boundaries used by the cap:
// a whole run of plain bytes or one complete escape sequence.
func replaySpanEnd(data []byte, start int) (int, bool) {
	if data[start] == 0x1b {
		return skipEscape(data, start)
	}
	if i := bytes.IndexByte(data[start:], 0x1b); i >= 0 {
		return start + i, true
	}
	return len(data), true
}

func skipEscape(data []byte, i int) (int, bool) {
	if i >= len(data) || data[i] != 0x1b {
		return i, false
	}
	if i+1 >= len(data) {
		return len(data), false
	}
	switch data[i+1] {
	case '[':
		j := i + 2
		for j < len(data) {
			if data[j] >= 0x40 && data[j] <= 0x7e {
				return j + 1, true
			}
			j++
			if j-i > 64 {
				return i + 2, true
			}
		}
		return len(data), false
	case ']':
		j := i + 2
		for j < len(data) {
			if data[j] == 0x07 {
				return j + 1, true
			}
			if data[j] == 0x1b && j+1 < len(data) && data[j+1] == '\\' {
				return j + 2, true
			}
			j++
		}
		return len(data), false
	default:
		return i + 2, true
	}
}
