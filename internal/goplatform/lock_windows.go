package goplatform

import (
	"golang.org/x/sys/windows"
	"os"
)

func LockFile(file *os.File, nonblocking bool) error {
	flags := uint32(windows.LOCKFILE_EXCLUSIVE_LOCK)
	if nonblocking {
		flags |= windows.LOCKFILE_FAIL_IMMEDIATELY
	}
	return windows.LockFileEx(windows.Handle(file.Fd()), flags, 0, 1, 0, &windows.Overlapped{})
}
func UnlockFile(file *os.File) error {
	return windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, &windows.Overlapped{})
}
