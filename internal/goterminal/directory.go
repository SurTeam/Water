package goterminal

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// CurrentDirectory is queried on the server command worker when creating a
// terminal, never on the GUI thread. Prefer the foreground job, then its shell.
func (t *Terminal) CurrentDirectory() string {
	pid := t.cmd.Process.Pid
	foreground := pid
	if raw, err := t.ptmx.SyscallConn(); err == nil {
		_ = raw.Control(func(fd uintptr) {
			if group, err := unix.IoctlGetInt(int(fd), unix.TIOCGPGRP); err == nil && group > 0 {
				foreground = group
			}
		})
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for _, candidate := range []int{foreground, pid} {
		var dir string
		if runtime.GOOS == "linux" {
			dir, _ = os.Readlink("/proc/" + strconv.Itoa(candidate) + "/cwd")
		} else if runtime.GOOS == "darwin" {
			out, err := exec.CommandContext(ctx, "/usr/sbin/lsof", "-a", "-p", strconv.Itoa(candidate), "-d", "cwd", "-Fn").Output()
			if err == nil {
				for _, line := range strings.Split(string(out), "\n") {
					if strings.HasPrefix(line, "n/") {
						dir = line[1:]
						break
					}
				}
			}
		}
		if filepath.IsAbs(dir) {
			return dir
		}
		if candidate == pid {
			break
		}
	}
	return ""
}
