package goui

import (
	"image"

	"github.com/SurTeam/Water/internal/gobuild"
	"github.com/SurTeam/Water/internal/goclient"
	"github.com/SurTeam/Water/internal/goprotocol"
	"github.com/google/uuid"
	"github.com/hajimehoshi/ebiten/v2"
)

// serverRestartPrompt asks the user to replace an older remote Water server with
// the bundled build. A remote server outlives the GUI that deployed it, so a
// compatible but older server would otherwise be reused silently and keep
// serving the previous version. Owned by layoutMu like other modal state.
type serverRestartPrompt struct {
	// Sidelined prompts only report a preserved incompatible server; restart
	// prompts offer to replace an outdated one.
	Sidelined       bool
	SidelinedSocket string
	Destination     string
	ServerVersion   string
	ServerRevision  string
	ClientVersion   string
	// A server without recovery support cannot export the current layout, so it
	// has to be stopped on the remote host instead of being replaced in place.
	Legacy bool
}

func remoteServerVersion(info goprotocol.ServerInfo) string {
	if value := info.ServerVersion; value != "" {
		return value
	}
	return info.Version
}

// PromptServerRestartIfOutdated shows the restart prompt when a reused remote
// server is not the bundled build. Local servers keep using the Server panel.
func (c *WorkspaceClient) PromptServerRestartIfOutdated() bool {
	c.server.mu.Lock()
	compatibility, info := c.server.compatibility, c.server.info
	c.server.mu.Unlock()
	if c.remoteDestination == "" || !compatibility.RestartRecommended {
		return false
	}
	client := goclient.Descriptor(gobuild.Variant)
	c.layoutMu.Lock()
	if c.serverRestart == nil {
		c.serverRestart = &serverRestartPrompt{
			Destination:    c.remoteDestination,
			ServerVersion:  remoteServerVersion(info),
			ServerRevision: info.ServerRevision,
			ClientVersion:  client.Version,
			Legacy:         !goprotocol.HasCapability(info.Capabilities, "recovery/v1"),
		}
	}
	c.layoutMu.Unlock()
	if c.invalidate != nil {
		c.invalidate()
	}
	return true
}

// ShowSidelinedServer reports that an incompatible server was left running while
// this build started beside it. It is informational, never an error.
func (c *WorkspaceClient) ShowSidelinedServer(socket, serverVersion, clientVersion string) {
	c.layoutMu.Lock()
	if c.serverRestart == nil {
		c.serverRestart = &serverRestartPrompt{
			Sidelined:       true,
			SidelinedSocket: socket,
			Destination:     c.remoteDestination,
			ServerVersion:   serverVersion,
			ClientVersion:   clientVersion,
		}
	}
	c.layoutMu.Unlock()
	if c.invalidate != nil {
		c.invalidate()
	}
}

// ServerRestartPrompt reports the pending prompt for snapshots and automation.
func (c *WorkspaceClient) ServerRestartPrompt() map[string]any {
	c.layoutMu.Lock()
	prompt := c.serverRestart
	c.layoutMu.Unlock()
	return serverRestartPromptState(prompt)
}

func serverRestartPromptState(prompt *serverRestartPrompt) map[string]any {
	if prompt == nil {
		return nil
	}
	return map[string]any{
		"sidelined":        prompt.Sidelined,
		"sidelined_socket": prompt.SidelinedSocket,
		"destination":      prompt.Destination,
		"server_version":   prompt.ServerVersion,
		"server_revision":  prompt.ServerRevision,
		"client_version":   prompt.ClientVersion,
		"legacy":           prompt.Legacy,
	}
}

// confirmServerRestart replaces the outdated server, keeping the layout through
// the existing recovery restart operation. Called under layoutMu.
func (c *WorkspaceClient) confirmServerRestart() {
	prompt := c.serverRestart
	c.serverRestart = nil
	if prompt == nil || prompt.Legacy || prompt.Sidelined {
		return
	}
	c.performServerAction("restart")
}

// dismissServerRestart closes the prompt without touching the server. Called
// under layoutMu.
func (c *WorkspaceClient) dismissServerRestart() {
	if c.serverRestart == nil {
		return
	}
	c.serverRestart = nil
	if c.invalidate != nil {
		c.invalidate()
	}
}

// handleServerRestartKey consumes keys while the modal is visible. Called under
// layoutMu.
func (c *WorkspaceClient) handleServerRestartKey(spec string) bool {
	switch spec {
	case "escape":
		c.dismissServerRestart()
	case "enter", "return":
		c.confirmServerRestart()
	}
	return true
}

func (w *EbitengineWindow) drawServerRestart(c *WorkspaceClient, dst *ebiten.Image) {
	prompt := c.serverRestart
	if prompt == nil {
		return
	}
	r := w.overlay(c, dst, w.dp(560), w.dp(250))
	fg := configColor(c.currentConfig().Theme.UIForeground, 0xe6eaea)
	if prompt.Sidelined {
		w.label(dst, c, image.Rect(r.Min.X+w.dp(24), r.Min.Y+w.dp(24), r.Max.X-w.dp(24), r.Max.Y), c.tr("Incompatible server kept running"), 20, fg, true)
		w.label(dst, c, image.Rect(r.Min.X+w.dp(24), r.Min.Y+w.dp(68), r.Max.X-w.dp(24), r.Max.Y), c.trf("Water server %s is incompatible with this GUI %s. It keeps running at %s; a new server was started beside it with an empty workspace.", prompt.ServerVersion, prompt.ClientVersion, prompt.SidelinedSocket), 12, fg, false)
		w.button(dst, c, image.Rect(r.Min.X+w.dp(24), r.Max.Y-w.dp(58), r.Min.X+w.dp(120), r.Max.Y-w.dp(24)), "Close", hitServerRestartCancel, uuid.Nil, true)
		return
	}
	w.label(dst, c, image.Rect(r.Min.X+w.dp(24), r.Min.Y+w.dp(24), r.Max.X-w.dp(24), r.Max.Y), c.tr("Remote server is out of date"), 20, fg, true)
	w.label(dst, c, image.Rect(r.Min.X+w.dp(24), r.Min.Y+w.dp(68), r.Max.X-w.dp(24), r.Max.Y), c.trf("Remote Water server %s is older than this GUI %s.", prompt.ServerVersion, prompt.ClientVersion), 13, fg, false)
	detail := c.tr("Restart it with the latest server now? Running tasks will end.")
	confirm := "Restart with latest"
	if prompt.Legacy {
		detail = c.tr("Stop the old server on the remote host, then reconnect to use the latest version.")
		confirm = "Close"
	}
	w.label(dst, c, image.Rect(r.Min.X+w.dp(24), r.Min.Y+w.dp(104), r.Max.X-w.dp(24), r.Max.Y), detail, 12, fg, false)
	if prompt.Legacy {
		w.button(dst, c, image.Rect(r.Min.X+w.dp(24), r.Max.Y-w.dp(58), r.Min.X+w.dp(120), r.Max.Y-w.dp(24)), confirm, hitServerRestartCancel, uuid.Nil, true)
		return
	}
	w.button(dst, c, image.Rect(r.Min.X+w.dp(24), r.Max.Y-w.dp(58), r.Min.X+w.dp(230), r.Max.Y-w.dp(24)), confirm, hitServerRestartConfirm, uuid.Nil, true)
	w.button(dst, c, image.Rect(r.Min.X+w.dp(246), r.Max.Y-w.dp(58), r.Max.X-w.dp(24), r.Max.Y-w.dp(24)), "Later", hitServerRestartCancel, uuid.Nil, false)
}
