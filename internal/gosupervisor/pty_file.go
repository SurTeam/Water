package gosupervisor

import (
	"os"
	"syscall"
	"time"
)

// makePollablePTY rewraps the PTY master as a nonblocking file registered with the
// runtime poller. Without it, a blocking Read parks an OS thread and a concurrent
// SyscallConn().Control call (used for TIOCGPGRP) waits for that read to finish.
func makePollablePTY(original *os.File) (*os.File, error) {
	raw, err := original.SyscallConn()
	if err != nil {
		return nil, err
	}
	fd := -1
	var duplicateErr error
	err = raw.Control(func(originalFD uintptr) {
		syscall.ForkLock.RLock()
		fd, duplicateErr = syscall.Dup(int(originalFD))
		if duplicateErr == nil {
			syscall.CloseOnExec(fd)
		}
		syscall.ForkLock.RUnlock()
	})
	if err != nil {
		return nil, err
	}
	if duplicateErr != nil {
		return nil, duplicateErr
	}
	if err := syscall.SetNonblock(fd, true); err != nil {
		_ = syscall.Close(fd)
		return nil, err
	}
	file := os.NewFile(uintptr(fd), original.Name())
	if err := file.SetReadDeadline(time.Time{}); err != nil {
		_ = file.Close()
		return nil, err
	}
	_ = original.Close()
	return file, nil
}
