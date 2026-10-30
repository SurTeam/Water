//go:build !windows && !darwin

package goplatform

import (
	"os/exec"
	"syscall"
)

func Detach(command *exec.Cmd) { command.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }
