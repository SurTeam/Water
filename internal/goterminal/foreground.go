package goterminal

import (
	"context"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sys/unix"
)

// ForegroundNames samples one process table batch for all PTYs. Call only on a
// background worker; the GUI and terminal output stream never wait for ps.
func (r *Registry) ForegroundNames(ctx context.Context) map[uuid.UUID]string {
	r.mu.RLock()
	terms := make([]*Terminal, 0, len(r.terms))
	for _, term := range r.terms {
		terms = append(terms, term)
	}
	r.mu.RUnlock()
	groups := map[uuid.UUID]int{}
	unique := map[int]bool{}
	var pids []string
	for _, term := range terms {
		select {
		case <-term.closed:
			continue
		default:
		}
		group := term.cmd.Process.Pid
		if raw, err := term.ptmx.SyscallConn(); err == nil {
			_ = raw.Control(func(fd uintptr) {
				if foreground, err := unix.IoctlGetInt(int(fd), unix.TIOCGPGRP); err == nil && foreground > 0 {
					group = foreground
				}
			})
		}
		groups[term.ID] = group
		if !unique[group] {
			unique[group] = true
			pids = append(pids, strconv.Itoa(group))
		}
	}
	result := map[uuid.UUID]string{}
	if len(pids) == 0 {
		return result
	}
	ctx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	output, err := exec.CommandContext(ctx, "ps", "-p", strings.Join(pids, ","), "-o", "pid=", "-o", "args=").Output()
	if err != nil {
		return result
	}
	names := map[int]string{}
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		name := strings.TrimLeft(filepath.Base(fields[1]), "-")
		// Homebrew runs a Ruby script; expose the command users launched.
		for _, arg := range fields[2:] {
			if filepath.Base(arg) == "brew.rb" {
				name = "brew"
				break
			}
		}
		if name != "" {
			names[pid] = name
		}
	}
	for id, pid := range groups {
		if name := names[pid]; name != "" {
			result[id] = name
		}
	}
	return result
}
