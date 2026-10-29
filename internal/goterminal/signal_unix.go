//go:build !windows

package goterminal

import "syscall"

func signalProcessGroup(pid int, sig syscall.Signal) error { return syscall.Kill(-pid, sig) }
