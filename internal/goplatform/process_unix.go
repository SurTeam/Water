//go:build !windows && !darwin

package goplatform

import (
	"os/exec"
	"syscall"
)

func RunningBinaryReplaced(int, string) (bool, error) { return false, nil }

func Detach(command *exec.Cmd) { command.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }
