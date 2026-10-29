//go:build windows

package gosupervisor

import (
	"errors"
	"os"
	"time"
)

func (t *terminal) rawReadLoop(out chan<- []byte, free <-chan []byte, done <-chan struct{}) {
	defer close(out)
	for {
		select {
		case req := <-t.rawFlushRequests:
			t.ptmx.SetReadDeadline(time.Time{})
			close(req.ready)
			select {
			case <-req.resume:
			case <-done:
				return
			}
		default:
		}
		var buf []byte
		select {
		case buf = <-free:
		default:
			buf = make([]byte, readBlock)
		}
		n, err := t.ptmx.Read(buf[:readBlock])
		if n > 0 {
			select {
			case out <- buf[:n]:
			case <-done:
				return
			}
		}
		if err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) {
				continue
			}
			return
		}
	}
}
