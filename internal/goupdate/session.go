package goupdate

import (
	"os"
	"sync/atomic"
)

// PreserveServerEnv marks a GUI taking over servers from an updating GUI.
const PreserveServerEnv = "WATER_UPDATE_PRESERVE_SERVER"

var preserveServer atomic.Bool

func init() {
	preserveServer.Store(os.Getenv(PreserveServerEnv) == "1")
	// This belongs to this GUI only, never to child windows, servers or shells.
	_ = os.Unsetenv(PreserveServerEnv)
}

// PreserveServer stays true until the replacement GUI has successfully
// acknowledged its first live frame to the installer.
func PreserveServer() bool { return preserveServer.Load() }

// StartupAcknowledged must only be called after the startup ACK was written.
func StartupAcknowledged() { preserveServer.Store(false) }
