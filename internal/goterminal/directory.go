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
	pid := t.processID()
	if pid <= 0 {
		return ""
	}
	foreground := pid
	if t.ptmx != nil {
		if raw, err := t.ptmx.SyscallConn(); err == nil {
			_ = raw.Control(func(fd uintptr) {
				if group, err := unix.IoctlGetInt(int(fd), unix.TIOCGPGRP); err == nil && group > 0 {
					foreground = group
				}
			})
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	for _, candidate := range []int{foreground, pid} {
		var dir string
		if runtime.GOOS == "linux" {
			dir, _ = os.Readlink("/proc/" + strconv.Itoa(candidate) + "/cwd")
		} else if runtime.GOOS == "darwin" {
			cmd := exec.CommandContext(ctx, "/usr/sbin/lsof", "-b", "-w", "-a", "-p", strconv.Itoa(candidate), "-d", "cwd", "-Fn")
			// lsof escapes non-ASCII bytes under the C locale (common for GUI
			// launches). Those display escapes are not a filesystem path.
			cmd.Env = append(os.Environ(), "LC_ALL=en_US.UTF-8")
			out, err := cmd.Output()
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
			if info, err := os.Stat(dir); err == nil && info.IsDir() {
				return dir
			}
		}
		if candidate == pid || ctx.Err() != nil {
			break
		}
	}
	return ""
}
