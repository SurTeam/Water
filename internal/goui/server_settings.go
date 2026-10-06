package goui

import (
	"errors"
	"sync"

	"github.com/SurTeam/Water/internal/gobuild"
	"github.com/SurTeam/Water/internal/goclient"
	"github.com/SurTeam/Water/internal/goprotocol"
)

type serverPanel struct {
	mu            sync.Mutex
	info          goprotocol.ServerInfo
	compatibility goprotocol.Compatibility
	busy          bool
	message       string
	recoveryPath  string
	operate       func(string) (goprotocol.ServerInfo, string, error)
}

type ServerPanelState struct {
	Client        goprotocol.Descriptor    `json:"client"`
	Server        goprotocol.ServerInfo    `json:"server"`
	Compatibility goprotocol.Compatibility `json:"compatibility"`
	Busy          bool                     `json:"busy"`
	Message       string                   `json:"message"`
	RecoveryPath  string                   `json:"recovery_path"`
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
	return ServerPanelState{Client: goclient.Descriptor(gobuild.Variant), Server: c.server.info, Compatibility: c.server.compatibility, Busy: c.server.busy, Message: c.server.message, RecoveryPath: c.server.recoveryPath}
}
func (c *WorkspaceClient) setServerOperator(operator func(string) (goprotocol.ServerInfo, string, error)) {
	c.server.mu.Lock()
	c.server.operate = operator
	c.server.mu.Unlock()
}

// Called under layoutMu. Work always happens off the Ebitengine frame loop.
func (c *WorkspaceClient) serverAction(action string) {
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
