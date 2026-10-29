//go:build !windows

package gosupervisor

import (
	"errors"
	"io"
	"os"
	"syscall"
	"time"
)

func (t *terminal) rawReadLoop(out chan<- []byte, free <-chan []byte, done <-chan struct{}) {
	defer close(out)
	raw, err := t.ptmx.SyscallConn()
	if err != nil {
		return
	}
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
		var buffer []byte
		select {
		case <-done:
			return
		case buffer = <-free:
		default:
			buffer = make([]byte, readBlock)
		}
		buffer = buffer[:readBlock]
		n := 0
		var readErr error
		var started time.Time
		waiting := false
		err := raw.Read(func(fd uintptr) bool {
			for n < len(buffer) {
				count, callErr := syscall.Read(int(fd), buffer[n:])
				if count > 0 {
					n += count
					if started.IsZero() {
						started = time.Now()
					}
				}
				if errors.Is(callErr, syscall.EINTR) {
					continue
				}
				if errors.Is(callErr, syscall.EAGAIN) || errors.Is(callErr, syscall.EWOULDBLOCK) {
					if n == 0 {
						return false
					}
					if n <= echoFlushMaxBytes && time.Since(time.Unix(0, t.lastInput.Load())) < echoFlushWindow {
						return true
					}
					deadline := time.Now().Add(batchIdle)
					if maximum := started.Add(batchMaxAge); maximum.Before(deadline) {
						deadline = maximum
					}
					_ = t.ptmx.SetReadDeadline(deadline)
					waiting = true
					return false
				}
				if callErr != nil {
					readErr = callErr
					return true
				}
				if count == 0 {
					readErr = io.EOF
					return true
				}
				if time.Since(started) >= batchMaxAge {
					return true
				}
			}
			return true
		})
		if waiting {
			_ = t.ptmx.SetReadDeadline(time.Time{})
		}
		if n > 0 {
			select {
			case out <- buffer[:n]:
			case <-done:
				return
			}
		}
		if err != nil || readErr != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) {
				continue
			}
			return
		}
	}
}

// announceResize publishes the geometry change in stream order.
