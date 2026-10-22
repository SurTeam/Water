package goui

import (
	"testing"

	"github.com/SurTeam/Water/internal/gobuild"
	"github.com/SurTeam/Water/internal/goconfig"
	"github.com/SurTeam/Water/internal/goprotocol"
)

func outdatedRemote(t *testing.T, destination string, capabilities []string) *WorkspaceClient {
	t.Helper()
	c := NewWorkspaceClientWithConnection(nil, nil, goconfig.Default(), destination)
	c.server.info = goprotocol.ServerInfo{
		Descriptor: goprotocol.Descriptor{
			BuildVariant:    gobuild.Variant,
			Version:         "0.4.20",
			ProtocolVersion: goprotocol.ProtocolVersion,
			APISignature:    goprotocol.APISignature,
			ServerRevision:  "older-build",
			Capabilities:    capabilities,
		},
		ServerVersion: "0.4.20",
	}
	c.server.compatibility = goprotocol.Assess(clientDescriptor(), c.server.info.Descriptor)
	return c
}

func clientDescriptor() goprotocol.Descriptor {
	return goprotocol.Descriptor{
		BuildVariant:    gobuild.Variant,
		Version:         gobuild.Version,
		ProtocolVersion: goprotocol.ProtocolVersion,
		APISignature:    goprotocol.APISignature,
		ServerRevision:  gobuild.ServerRevision,
	}
}

func TestRemoteServerRestartPromptOnlyForOutdatedRemoteServers(t *testing.T) {
	remote := outdatedRemote(t, "alice@host", goprotocol.ServerCapabilities())
	if !remote.PromptServerRestartIfOutdated() {
		t.Fatal("outdated remote server did not prompt")
	}
	state := remote.ServerRestartPrompt()
	if state == nil || state["destination"] != "alice@host" || state["server_version"] != "0.4.20" || state["legacy"] != false {
		t.Fatalf("unexpected prompt %v", state)
	}
	if state["client_version"] != gobuild.Version {
		t.Fatalf("client version %v", state["client_version"])
	}

	local := outdatedRemote(t, "", goprotocol.ServerCapabilities())
	if local.PromptServerRestartIfOutdated() || local.ServerRestartPrompt() != nil {
		t.Fatal("local server must keep using the Server panel")
	}

	current := outdatedRemote(t, "alice@host", goprotocol.ServerCapabilities())
	current.server.compatibility = goprotocol.Assess(clientDescriptor(), clientDescriptor())
	if current.PromptServerRestartIfOutdated() {
		t.Fatal("current remote server must not prompt")
	}
}

func TestRemoteServerRestartPromptLegacyServerOffersNoAutomaticRestart(t *testing.T) {
	c := outdatedRemote(t, "alice@host", nil)
	if !c.PromptServerRestartIfOutdated() {
		t.Fatal("legacy remote server did not prompt")
	}
	if state := c.ServerRestartPrompt(); state == nil || state["legacy"] != true {
		t.Fatalf("legacy prompt %v", state)
	}
	restarted := false
	c.setServerOperator(func(action string) (goprotocol.ServerInfo, string, error) {
		restarted = true
		return goprotocol.ServerInfo{}, "", nil
	})
	c.layoutMu.Lock()
	c.confirmServerRestart()
	c.layoutMu.Unlock()
	if restarted {
		t.Fatal("legacy server must not attempt an automatic restart")
	}
	if c.ServerRestartPrompt() != nil {
		t.Fatal("legacy prompt stayed open")
	}
}

func TestRemoteServerRestartPromptConfirmRestartsServer(t *testing.T) {
	c := outdatedRemote(t, "alice@host", goprotocol.ServerCapabilities())
	if !c.PromptServerRestartIfOutdated() {
		t.Fatal("outdated remote server did not prompt")
	}
	actions := make(chan string, 1)
	c.setServerOperator(func(action string) (goprotocol.ServerInfo, string, error) {
		actions <- action
		return goprotocol.ServerInfo{}, "", nil
	})
	c.layoutMu.Lock()
	handled := c.handleServerRestartKey("enter")
	c.layoutMu.Unlock()
	if !handled {
		t.Fatal("prompt did not consume the key")
	}
	if action := <-actions; action != "restart" {
		t.Fatalf("restarted with %q", action)
	}
	if c.ServerRestartPrompt() != nil {
		t.Fatal("prompt stayed open after confirming")
	}
}

func TestRemoteServerRestartPromptEscapeKeepsServer(t *testing.T) {
	c := outdatedRemote(t, "alice@host", goprotocol.ServerCapabilities())
	if !c.PromptServerRestartIfOutdated() {
		t.Fatal("outdated remote server did not prompt")
	}
	acted := make(chan string, 1)
	c.setServerOperator(func(action string) (goprotocol.ServerInfo, string, error) {
		acted <- action
		return goprotocol.ServerInfo{}, "", nil
	})
	c.layoutMu.Lock()
	c.handleServerRestartKey("escape")
	c.layoutMu.Unlock()
	if c.ServerRestartPrompt() != nil {
		t.Fatal("prompt stayed open after escape")
	}
	select {
	case action := <-acted:
		t.Fatalf("dismissing the prompt still ran %q", action)
	default:
	}
}

func TestSidelinedServerNoticeNeverRestarts(t *testing.T) {
	c := NewWorkspaceClientWithConnection(nil, nil, goconfig.Default(), "alice@host")
	acted := make(chan string, 1)
	c.setServerOperator(func(action string) (goprotocol.ServerInfo, string, error) {
		acted <- action
		return goprotocol.ServerInfo{}, "", nil
	})
	c.ShowSidelinedServer("/tmp/water-go-dev-1-abc.sock", "0.3.9", gobuild.Version)
	state := c.ServerRestartPrompt()
	if state == nil || state["sidelined"] != true || state["sidelined_socket"] != "/tmp/water-go-dev-1-abc.sock" {
		t.Fatalf("unexpected notice %v", state)
	}
	c.layoutMu.Lock()
	c.handleServerRestartKey("enter")
	c.layoutMu.Unlock()
	select {
	case action := <-acted:
		t.Fatalf("sidelined notice restarted the server with %q", action)
	default:
	}
	if c.ServerRestartPrompt() != nil {
		t.Fatal("notice stayed open")
	}
}
