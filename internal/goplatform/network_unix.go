//go:build !windows

package goplatform

import (
	"errors"
	"syscall"
)

func ConnectionRefused(err error) bool { return errors.Is(err, syscall.ECONNREFUSED) }
