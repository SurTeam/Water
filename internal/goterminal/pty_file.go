package goterminal

import (
	"os"
	"syscall"
	"time"
)

// pty returns a blocking os.NewFile on Darwin. Rewrap a nonblocking descriptor
// so reads use the runtime poller instead of parking an OS thread per PTY read.
// Dup keeps ownership unambiguous: the two os.File finalizers never share an fd.
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
	// Verify poller registration before handing the file to the read loop.
	if err := file.SetReadDeadline(time.Time{}); err != nil {
		_ = file.Close()
		return nil, err
	}
	_ = original.Close()
	return file, nil
}
