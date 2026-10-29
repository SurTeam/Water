//go:build windows

package goterminal

import (
	"errors"
	"syscall"
)

func signalProcessGroup(pid int, sig syscall.Signal) error {
	return errors.New("POSIX process group signaling is unavailable on Windows")
}
