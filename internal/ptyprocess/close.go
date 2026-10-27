// Package ptyprocess cleans up processes owned by a PTY session.
package ptyprocess

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

type process struct {
	pid, parent int
	started     string
}

// Session retains the root's identity before the caller starts waiting on it.
// A historical PID alone must never authorize discovery or group signaling.
type Session struct {
	child   *os.Process
	started string
}

// Capture must be called immediately after Start, before any goroutine calls
// Wait. Until Wait reaps it, even an exited child retains its PID and identity.
// If native identity inspection fails, cleanup is restricted to Process.Kill.
func Capture(child *os.Process) *Session {
	s := &Session{child: child}
	if child != nil && child.Pid > 1 && child.Pid != os.Getpid() {
		s.started, _ = identity(child.Pid)
	}
	return s
}

func (s *Session) ownsRoot() bool {
	if s == nil || s.child == nil || s.started == "" || s.child.Signal(syscall.Signal(0)) != nil {
		return false
	}
	token, err := identity(s.child.Pid)
	return err == nil && token == s.started
}

// Kill forcefully closes a session created by creack/pty. Discover jobs before
// killing the shell: killing it first loses ancestry for detached descendants.
// Enumeration is bounded; failure still kills the original group and child.
// If the original root has been reaped or replaced, fail closed: a reused PID
// must never authorize enumerating or signaling an unrelated session.
func (s *Session) Kill() error {
	if s == nil || s.child == nil || s.child.Pid <= 1 || s.child.Pid == os.Getpid() {
		return nil
	}
	child := s.child
	killChild := func() error {
		err := child.Kill()
		if errors.Is(err, os.ErrProcessDone) || errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}
	pid := child.Pid
	own, _ := unix.Getsid(0)
	if pid == own {
		return nil
	}
	if !s.ownsRoot() {
		return killChild()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
	defer cancel()
	first, err := snapshot(ctx)
	if err == nil && s.ownsRoot() {
		selected := make(map[int]process)
		for _, p := range first {
			if ctx.Err() != nil {
				break
			}
			sid, e := unix.Getsid(p.pid)
			if e == nil && sid == pid {
				selected[p.pid] = p
			}
		}
		// Include descendants that called setsid, but never unrelated sessions.
		for changed := true; changed && ctx.Err() == nil; {
			changed = false
			for _, p := range first {
				if _, ok := selected[p.pid]; ok {
					continue
				}
				if _, ok := selected[p.parent]; ok {
					selected[p.pid] = p
					changed = true
				}
			}
		}
		// Capture native start identities, then recheck immediately before each
		// signal. Darwin exposes microseconds; Linux exposes boot clock ticks.
		identities := make(map[int]string)
		for pid := range selected {
			if ctx.Err() != nil {
				break
			}
			if token, e := identity(pid); e == nil {
				identities[pid] = token
			}
		}
		// Recheck ancestry as well: a process may have exited or been reparented.
		if current, e := snapshot(ctx); e == nil && s.ownsRoot() {
			for _, p := range current {
				if ctx.Err() != nil || !s.ownsRoot() {
					break
				}
				original, ok := selected[p.pid]
				if !ok || original != p || p.pid <= 1 || p.pid == os.Getpid() || p.pid == pid {
					continue
				}
				token, e := identity(p.pid)
				if originalToken, exists := identities[p.pid]; e == nil && exists && token == originalToken {
					_ = syscall.Kill(p.pid, syscall.SIGKILL)
				}
			}
		}
	}
	// Only signal a numeric group while its leader still belongs to the PTY
	// session. Process.Kill handles an already-reaped direct child safely.
	if sid, e := unix.Getsid(pid); e == nil && sid == pid && s.ownsRoot() {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
	}
	return killChild()
}

type boundedOutput struct{ bytes.Buffer }

func (b *boundedOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 4<<20 {
		return 0, errors.New("process list exceeds limit")
	}
	return b.Buffer.Write(p)
}
func snapshot(ctx context.Context) ([]process, error) {
	cmd := exec.CommandContext(ctx, "ps", "-axo", "pid=,ppid=,lstart=")
	cmd.WaitDelay = 100 * time.Millisecond
	var output boundedOutput
	cmd.Stdout = &output
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	var out []process
	for _, line := range strings.Split(output.String(), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 7 {
			continue
		}
		pid, e := strconv.Atoi(fields[0])
		if e != nil {
			continue
		}
		parent, e := strconv.Atoi(fields[1])
		if e != nil {
			continue
		}
		out = append(out, process{pid, parent, strings.Join(fields[2:], " ")})
	}
	return out, nil
}
