package goremote

import (
	"io"
	"os"
)

func openNativeAgent() (io.ReadWriteCloser, error) {
	return os.OpenFile(`\\.\pipe\openssh-ssh-agent`, os.O_RDWR, 0)
}
