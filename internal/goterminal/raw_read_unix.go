//go:build !windows

package goterminal

import (
	"errors"
	"github.com/SurTeam/Water/internal/gometrics"
	"io"
	"os"
	"syscall"
	"time"
)

func (t *Terminal) rawReadLoop(out chan<- []byte, free <-chan []byte) {
	defer close(out)
	raw, err := t.ptmx.SyscallConn()
	if err != nil {
		return
	}
	var buf []byte
	pause := func(request rawFlushRequest) bool {
		_ = t.ptmx.SetReadDeadline(time.Time{})
		close(request.ready)
		select {
		case <-request.resume:
			return true
		case <-t.closed:
			return false
		}
	}
	for {
		select {
		case request := <-t.rawFlushRequests:
			if !pause(request) {
				return
			}
		default:
		}
		if buf == nil {
			select {
			case buf = <-free:
			case request := <-t.rawFlushRequests:
				if !pause(request) {
					return
				}
				continue
			case <-t.closed:
				return
			default:
				buf = make([]byte, readBlockBytes)
			}
		}
		buf = buf[:readBlockBytes]

		n := 0
		var readErr error
		// Drain available fragments into one reader-owned block. A partial
		// block can wait for readiness up to the idle/max-age deadline, avoiding
		// a channel handoff/timer reset for every ~1 KiB Darwin PTY fragment.
		var burstStarted time.Time
		waitingForData := false
		err := raw.Read(func(fd uintptr) bool {
			for n < len(buf) {
				gometrics.PTYReadCalls.Add(1)
				count, callErr := syscall.Read(int(fd), buf[n:])
				if count > 0 {
					n += count
					gometrics.PTYBytesRead.Add(uint64(count))
					if burstStarted.IsZero() {
						burstStarted = time.Now()
					}
				}
				if errors.Is(callErr, syscall.EINTR) {
					continue
				}
				if errors.Is(callErr, syscall.EAGAIN) || errors.Is(callErr, syscall.EWOULDBLOCK) {
					gometrics.PTYReadWouldBlock.Add(1)
					if n == 0 {
						return false
					}
					if n <= echoFlushMaxBytes && time.Since(time.Unix(0, t.lastInput.Load())) < echoFlushWindow {
						return true
					}
					// Keep this partial block while the poller waits for readiness.
					// Bound that wait so a prompt or resize cannot get stuck behind
					// an unfinished block, without spinning on an empty PTY.
					deadline := time.Now().Add(outputBatchIdle)
					if maximum := burstStarted.Add(outputBatchMaxAge); maximum.Before(deadline) {
						deadline = maximum
					}
					_ = t.ptmx.SetReadDeadline(deadline)
					waitingForData = true
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
				if time.Since(burstStarted) >= outputBatchMaxAge {
					return true
				}
			}
			return true
		})
		if waitingForData {
			_ = t.ptmx.SetReadDeadline(time.Time{})
		}
		if n > 0 {
			out <- buf[:n]
			buf = nil
		}
		if err != nil || readErr != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) {
				continue
			}
			// Darwin/Linux PTYs commonly surface EIO after child exit.
			return
		}
	}
}
