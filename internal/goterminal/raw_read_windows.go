//go:build windows

package goterminal

import (
	"errors"
	"os"
	"time"
)

func (t *Terminal) rawReadLoop(out chan<- []byte, free <-chan []byte) {
	defer close(out)
	pause := func(req rawFlushRequest) bool {
		t.ptmx.SetReadDeadline(time.Time{})
		close(req.ready)
		select {
		case <-req.resume:
			return true
		case <-t.closed:
			return false
		}
	}
	for {
		select {
		case req := <-t.rawFlushRequests:
			if !pause(req) {
				return
			}
		default:
		}
		var buf []byte
		select {
		case buf = <-free:
		default:
			buf = make([]byte, readBlockBytes)
		}
		n, err := t.ptmx.Read(buf[:readBlockBytes])
		if n > 0 {
			out <- buf[:n]
		}
		if err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) {
				continue
			}
			return
		}
	}
}
