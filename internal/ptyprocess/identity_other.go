//go:build !darwin && !linux

package ptyprocess

import "syscall"

// Without a precise identity API, retain only the owned-child fallback.
func identity(pid int) (string, error) { return "", syscall.ENOSYS }
