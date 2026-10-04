package goserver

import (
	"errors"
	"fmt"
	"net"
	"os"
	"syscall"
	"time"
)

// Keep the lock file in place: unlinking it would let a third process lock a
// different inode while another starter still holds the original lock.
func listenOwnedSocket(path string) (net.Listener, func(), error) {
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, nil, err
	}
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil, nil, fmt.Errorf("socket %s already has an owner: %w", path, err)
	}
	release := func() { _ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN); _ = lock.Close() }
	if info, statErr := os.Lstat(path); statErr == nil {
		if info.Mode()&os.ModeSocket == 0 {
			release()
			return nil, nil, fmt.Errorf("refusing to replace non-socket %s", path)
		}
		conn, dialErr := net.DialTimeout("unix", path, 250*time.Millisecond)
		if dialErr == nil {
			conn.Close()
			release()
			return nil, nil, fmt.Errorf("socket %s is already listening", path)
		}
		if !errors.Is(dialErr, syscall.ECONNREFUSED) {
			release()
			return nil, nil, dialErr
		}
		if err = os.Remove(path); err != nil {
			release()
			return nil, nil, err
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		release()
		return nil, nil, statErr
	}
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		release()
		return nil, nil, err
	}
	ln.SetUnlinkOnClose(false)
	owned, err := os.Lstat(path)
	if err != nil {
		ln.Close()
		release()
		return nil, nil, err
	}
	cleanup := func() {
		_ = ln.Close()
		if current, err := os.Lstat(path); err == nil && os.SameFile(owned, current) {
			_ = os.Remove(path)
		}
		release()
	}
	if err = os.Chmod(path, 0o600); err != nil {
		cleanup()
		return nil, nil, err
	}
	return ln, cleanup, nil
}
