package ptyprocess

import (
	"fmt"
	"golang.org/x/sys/unix"
)

func identity(pid int) (string, error) {
	info, err := unix.SysctlKinfoProcSlice("kern.proc.pid", pid)
	if err != nil {
		return "", err
	}
	if len(info) != 1 || int(info[0].Proc.P_pid) != pid {
		return "", unix.ESRCH
	}
	p := info[0].Proc
	return fmt.Sprintf("%d.%06d", p.P_starttime.Sec, p.P_starttime.Usec), nil
}
