package goclient

import (
	"time"

	"github.com/SurTeam/Water/internal/gobuild"
	"github.com/SurTeam/Water/internal/goprotocol"
)

func Descriptor(variant string) goprotocol.Descriptor {
	return goprotocol.Descriptor{BuildVariant: variant, Version: gobuild.Version, ProtocolVersion: goprotocol.ProtocolVersion, APISignature: goprotocol.APISignature, ServerRevision: gobuild.ServerRevision, Capabilities: goprotocol.ClientCapabilities(), RequiredCapabilities: []string{"workspace/v1", "terminal-stream/v1", "window-selection/v1"}}
}

func (c *Client) Inspect(timeout time.Duration) (goprotocol.ServerInfo, error) {
	var info goprotocol.ServerInfo
	err := c.CallTimeout("server.inspect", nil, &info, timeout)
	if err != nil {
		err = c.CallTimeout("server.info", nil, &info, timeout)
	}
	return info, err
}
