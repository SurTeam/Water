package goprotocol

import "fmt"

// Descriptor is deliberately independent of the application RPC version. Only
// read-only inspection and diagnostic GUI sessions may use it across versions.
type Descriptor struct {
	BuildVariant         string   `json:"build_variant"`
	Version              string   `json:"version"`
	ProtocolVersion      uint32   `json:"protocol_version"`
	APISignature         string   `json:"api_signature"`
	ServerRevision       string   `json:"server_revision,omitempty"`
	Capabilities         []string `json:"capabilities,omitempty"`
	RequiredCapabilities []string `json:"required_capabilities,omitempty"`
}

type ServerInfo struct {
	Descriptor
	ServerVersion  string `json:"server_version"`
	ServerPID      int    `json:"server_pid"`
	InstanceID     string `json:"instance_id,omitempty"`
	SocketPath     string `json:"socket_path"`
	UISessions     int    `json:"ui_sessions"`
	RecoverySchema int    `json:"recovery_schema,omitempty"`
}

type Compatibility struct {
	Compatible         bool     `json:"compatible"`
	Legacy             bool     `json:"legacy"`
	RestartRecommended bool     `json:"restart_recommended"`
	Reason             string   `json:"reason"`
	MissingServer      []string `json:"missing_server"`
	MissingClient      []string `json:"missing_client"`
}

func ServerCapabilities() []string {
	return []string{"workspace/v1", "terminal-stream/v1", "window-selection/v1", "recovery/v1", "server-inspect/v1"}
}
func ClientCapabilities() []string {
	return []string{"terminal-stream/v1", "window-selection/v1", "ui-control/v1"}
}
func HasCapability(values []string, wanted string) bool {
	for _, v := range values {
		if v == wanted {
			return true
		}
	}
	return false
}

// Assess reports both directions, and never treats product version as an API
// contract. Old servers have unknown capabilities, so only the established
// protocol/signature baseline can be used; recovery must remain disabled.
func Assess(client, server Descriptor) Compatibility {
	c := Compatibility{MissingServer: []string{}, MissingClient: []string{}}
	if client.BuildVariant != server.BuildVariant {
		c.Reason = "dev/release variant mismatch"
		return c
	}
	if client.ProtocolVersion != server.ProtocolVersion {
		c.Reason = "protocol version mismatch"
		return c
	}
	if client.APISignature != server.APISignature {
		c.Reason = "API signature mismatch"
		return c
	}
	c.Legacy = len(server.Capabilities) == 0
	provided := server.Capabilities
	if c.Legacy && server.APISignature == APISignature {
		provided = []string{"workspace/v1", "terminal-stream/v1", "window-selection/v1"}
	}
	for _, v := range client.RequiredCapabilities {
		if !HasCapability(provided, v) {
			c.MissingServer = append(c.MissingServer, v)
		}
	}
	for _, v := range server.RequiredCapabilities {
		if !HasCapability(client.Capabilities, v) {
			c.MissingClient = append(c.MissingClient, v)
		}
	}
	if len(c.MissingServer)+len(c.MissingClient) > 0 {
		c.Reason = fmt.Sprintf("missing capabilities: server=%v GUI=%v", c.MissingServer, c.MissingClient)
		return c
	}
	c.Compatible = true
	c.RestartRecommended = server.ServerRevision == "" || client.ServerRevision != server.ServerRevision
	if c.Legacy {
		c.Reason = "Legacy server: capability and recovery support are unknown"
	} else if c.RestartRecommended {
		c.Reason = "Server changed. Restart to apply the server update."
	} else {
		c.Reason = "GUI and server support each other"
	}
	return c
}
