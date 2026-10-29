package goplatform

import (
	"errors"
	"golang.org/x/sys/windows"
)

func ConnectionRefused(err error) bool { return errors.Is(err, windows.WSAECONNREFUSED) }
