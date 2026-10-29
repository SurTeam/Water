//go:build !windows

// Package gopty owns a terminal device and its child process tree.
package gopty

import (
	"github.com/SurTeam/Water/internal/ptyprocess"
	"github.com/creack/pty"
	"golang.org/x/sys/unix"
	"os"
	"os/exec"
	"syscall"
	"time"
)

type Session struct {
	*os.File
	cmd   *exec.Cmd
	owner *ptyprocess.Session
}

func Start(cmd *exec.Cmd, size *Size) (*Session, error) {
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: size.Rows, Cols: size.Cols, X: size.X, Y: size.Y})
	if err != nil {
		return nil, err
	}
	owner := ptyprocess.Capture(cmd.Process)
	poll, err := makePollablePTY(f)
	if err != nil {
		f.Close()
		owner.Kill()
		cmd.Wait()
		return nil, err
	}
	return &Session{File: poll, cmd: cmd, owner: owner}, nil
}
func (s *Session) Resize(size *Size) error {
	return pty.Setsize(s.File, &pty.Winsize{Rows: size.Rows, Cols: size.Cols, X: size.X, Y: size.Y})
}
func (s *Session) Kill() error { return s.owner.Kill() }
func (s *Session) Wait() error { return s.cmd.Wait() }
func (s *Session) PID() int    { return s.cmd.Process.Pid }
func (s *Session) ExitCode() int {
	if s.cmd.ProcessState == nil {
		return -1
	}
	return s.cmd.ProcessState.ExitCode()
}
func (s *Session) ForegroundGroup() (int, error) {
	raw, err := s.SyscallConn()
	if err != nil {
		return 0, err
	}
	var group int
	var query error
	err = raw.Control(func(fd uintptr) { group, query = unix.IoctlGetInt(int(fd), unix.TIOCGPGRP) })
	if err != nil {
		return 0, err
	}
	return group, query
}
func makePollablePTY(original *os.File) (*os.File, error) {
	raw, err := original.SyscallConn()
	if err != nil {
		return nil, err
	}
	fd := -1
	var duplicateErr error
	err = raw.Control(func(originalFD uintptr) {
		syscall.ForkLock.RLock()
		fd, duplicateErr = syscall.Dup(int(originalFD))
		if duplicateErr == nil {
			syscall.CloseOnExec(fd)
		}
		syscall.ForkLock.RUnlock()
	})
	if err != nil {
		return nil, err
	}
	if duplicateErr != nil {
		return nil, duplicateErr
	}
	if err := syscall.SetNonblock(fd, true); err != nil {
		_ = syscall.Close(fd)
		return nil, err
	}
	file := os.NewFile(uintptr(fd), original.Name())
	// Verify poller registration before handing the file to the read loop.
	if err := file.SetReadDeadline(time.Time{}); err != nil {
		_ = file.Close()
		return nil, err
	}
	_ = original.Close()
	return file, nil
}
