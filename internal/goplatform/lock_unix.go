//go:build !windows

package goplatform

import (
	"golang.org/x/sys/unix"
	"os"
)

func LockFile(file *os.File, nonblocking bool) error {
	flags := unix.LOCK_EX
	if nonblocking {
		flags |= unix.LOCK_NB
	}
	return unix.Flock(int(file.Fd()), flags)
}
func UnlockFile(file *os.File) error { return unix.Flock(int(file.Fd()), unix.LOCK_UN) }
