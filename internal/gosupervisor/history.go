package gosupervisor

// appendHistoryLocked retains the newest historyLimit bytes without moving the
// retained prefix on every publish. The caller holds t.mu and has advanced
// t.produced by len(data). Storage grows only as output arrives.
func (t *terminal) appendHistoryLocked(data []byte) {
	if len(data) == 0 {
		return
	}
	if len(data) >= historyLimit {
		if cap(t.history) < historyLimit {
			t.history = make([]byte, historyLimit)
		} else {
			t.history = t.history[:historyLimit]
		}
		copy(t.history, data[len(data)-historyLimit:])
		t.historyHead = 0
		t.start = t.produced - historyLimit
		return
	}
	if len(t.history) < historyLimit {
		n := minInt(len(data), historyLimit-len(t.history))
		if need := len(t.history) + n; need > cap(t.history) {
			capacity := minInt(historyLimit, max(need, 2*cap(t.history)))
			next := make([]byte, len(t.history), capacity)
			copy(next, t.history)
			t.history = next
		}
		t.history = append(t.history, data[:n]...)
		data = data[n:]
	}
	if len(data) == 0 {
		return
	}
	// A full ring overwrites only incoming bytes, in at most two spans.
	n := copy(t.history[t.historyHead:], data)
	copy(t.history, data[n:])
	t.historyHead = (t.historyHead + len(data)) % historyLimit
	t.start += uint64(len(data))
}

// copyHistoryLocked returns an independent, ordered snapshot starting at an
// absolute offset within [t.start, t.produced]. The caller holds t.mu.
func (t *terminal) copyHistoryLocked(offset uint64) []byte {
	n := int(t.produced - offset)
	if n == 0 {
		return nil
	}
	out := make([]byte, n)
	head := (t.historyHead + int(offset-t.start)) % len(t.history)
	copied := copy(out, t.history[head:])
	copy(out[copied:], t.history[:head])
	return out
}
