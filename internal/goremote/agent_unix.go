//go:build !windows

package goremote

import (
	"io"
	"net"
	"os"
	"time"
)

func openNativeAgent() (io.ReadWriteCloser, error) {
	return net.DialTimeout("unix", os.Getenv("SSH_AUTH_SOCK"), time.Second)
}
