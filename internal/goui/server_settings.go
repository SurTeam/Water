package goui

import (
	"errors"
	"github.com/hajimehoshi/ebiten/v2"
	"image"
	"strings"
	"sync"
	"time"

	"github.com/SurTeam/Water/internal/gobuild"
	"github.com/SurTeam/Water/internal/goclient"
	"github.com/SurTeam/Water/internal/goprotocol"
)

type serverPanel struct {
	mu                  sync.Mutex
	info                goprotocol.ServerInfo
	compatibility       goprotocol.Compatibility
	busy                bool
	message             string
	recoveryPath        string
	operate             func(string) (goprotocol.ServerInfo, string, error)
	webVisible          bool
	webEnabled          bool
	webConfigVisible    bool
	chooseFile          func(string) (string, error)
	certificatePEM      []byte
	privateKeyPEM       []byte
	pairing             *goprotocol.WebPairing
	pairGeneration      uint64
	capabilitiesVisible bool
	pairQR              image.Image
	pairTexture         *ebiten.Image
	pairTextureSource   image.Image
	devices             []goprotocol.WebDevice
	devicesVisible      bool
}

type ServerPanelState struct {
	Client              goprotocol.Descriptor    `json:"client"`
	Server              goprotocol.ServerInfo    `json:"server"`
	Compatibility       goprotocol.Compatibility `json:"compatibility"`
	Busy                bool                     `json:"busy"`
	Message             string                   `json:"message"`
	RecoveryPath        string                   `json:"recovery_path"`
	WebVisible          bool                     `json:"web_visible"`
	WebEnabled          bool                     `json:"web_enabled"`
	WebConfigVisible    bool                     `json:"web_config_visible"`
	PairingExpiresAt    *time.Time               `json:"pairing_expires_at,omitempty"`
	PairQR              image.Image              `json:"-"`
	Devices             []goprotocol.WebDevice   `json:"web_devices,omitempty"`
	DevicesVisible      bool                     `json:"web_devices_visible"`
	CapabilitiesVisible bool                     `json:"capabilities_visible"`
}

func (c *WorkspaceClient) SetServerRecoveryError(path string, err error) {
	c.recoveryBlocked = true
	c.server.mu.Lock()
	c.server.recoveryPath = path
	c.server.message = err.Error()
	c.server.mu.Unlock()
	c.layoutMu.Lock()
	c.openSettings()
	c.settings.group = 5
	c.settings.focus = -1
	c.layoutMu.Unlock()
}

func (c *WorkspaceClient) ServerPanelState() ServerPanelState {
	c.server.mu.Lock()
	defer c.server.mu.Unlock()
	state := ServerPanelState{Client: goclient.Descriptor(gobuild.Variant), Server: c.server.info, Compatibility: c.server.compatibility, Busy: c.server.busy, Message: c.server.message, RecoveryPath: c.server.recoveryPath, WebVisible: c.server.webVisible, WebEnabled: c.server.webEnabled, WebConfigVisible: c.server.webConfigVisible, Devices: c.server.devices, DevicesVisible: c.server.devicesVisible, CapabilitiesVisible: c.server.capabilitiesVisible}
	if c.server.pairing != nil {
		expires := c.server.pairing.ExpiresAt
		state.PairingExpiresAt = &expires
		if time.Now().Before(expires) {
			state.PairQR = c.server.pairQR
		}
	}
	return state
}
func (c *WorkspaceClient) setServerOperator(operator func(string) (goprotocol.ServerInfo, string, error)) {
	c.server.mu.Lock()
	c.server.operate = operator
	c.server.mu.Unlock()
}

// Called under layoutMu. Work always happens off the Ebitengine frame loop.
func (c *WorkspaceClient) serverAction(action string) {
	if strings.HasPrefix(action, "web-") {
		c.webServerAction(action)
		return
	}
	if action == "inspect" {
		c.cancelWebPairing()
	}
	if action == "restart" && !c.settings.serverConfirm {
		c.settings.serverConfirm = true
		return
	}
	if action == "cancel-restart" {
		c.settings.serverConfirm = false
		return
	}
	c.settings.serverConfirm = false
	c.server.mu.Lock()
	if c.server.busy {
		c.server.mu.Unlock()
		return
	}
	operator := c.server.operate
	c.server.busy = true
	c.server.message = "Working…"
	c.server.mu.Unlock()
	go func() {
		var info goprotocol.ServerInfo
		var path string
		var err error
		if operator == nil {
			err = errors.New("server operations are unavailable")
		} else {
			info, path, err = operator(action)
		}
		c.server.mu.Lock()
		c.server.busy = false
		if info.ProtocolVersion != 0 {
			c.server.info = info
			c.server.compatibility = goprotocol.Assess(goclient.Descriptor(gobuild.Variant), info.Descriptor)
		}
		if path != "" {
			c.server.recoveryPath = path
		}
		if err != nil {
			c.server.message = err.Error()
		} else {
			c.server.message = "Server operation completed"
		}
		c.server.mu.Unlock()
		if c.invalidate != nil {
			c.invalidate()
		}
	}()
}
