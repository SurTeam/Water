package goui

import (
	"fmt"
	"image"
	"sort"
	"strings"

	"github.com/SurTeam/Water/internal/goprotocol"
	"github.com/google/uuid"
	"github.com/hajimehoshi/ebiten/v2"
)

func (w *EbitengineWindow) drawServerSettings(c *WorkspaceClient, dst *ebiten.Image, r image.Rectangle, top, footer int) {
	state := c.ServerPanelState()
	cfg := c.currentConfig()
	fg := configColor(cfg.Theme.UIForeground, 0xe6eaea)
	muted := mixColor(configColor(cfg.Theme.ChromeBackground, 0x171b20), fg, .6)
	label := func(y int, text string, small bool) {
		size := 11.
		if small {
			size = 10
		}
		w.label(dst, c, image.Rect(r.Min.X+w.dp(24), y, r.Max.X-w.dp(24), y+w.dp(22)), text, size, fg, false)
	}
	connection := "Local"
	if c.remoteDestination != "" {
		connection = c.remoteDestination
	}
	label(top, c.trf("Connection: %s", connection), false)
	label(top+w.dp(24), fmt.Sprintf("GUI %s  ·  Server %s  ·  %s", state.Client.Version, state.Server.ServerVersion, state.Server.BuildVariant), true)
	revision := state.Server.ServerRevision
	if len(revision) > 16 {
		revision = revision[:16]
	}
	if revision == "" {
		revision = c.tr("Unknown")
	}
	label(top+w.dp(48), c.trf("Server revision: %s", revision)+fmt.Sprintf("  ·  PID %d", state.Server.ServerPID), true)
	label(top+w.dp(72), c.tr(state.Compatibility.Reason), true)
	y := top + w.dp(104)
	tableLabel := func(rect image.Rectangle, text string, header bool) { w.label(dst, c, rect, text, 10, muted, header) }
	x0, x1, x2 := r.Min.X+w.dp(24), r.Min.X+r.Dx()/2, r.Min.X+r.Dx()*3/4
	tableLabel(image.Rect(x0, y, x1, y+w.dp(22)), c.tr("Capability"), true)
	tableLabel(image.Rect(x1, y, x2, y+w.dp(22)), "GUI", true)
	tableLabel(image.Rect(x2, y, r.Max.X-w.dp(24), y+w.dp(22)), "Server", true)
	values := map[string]bool{}
	for _, list := range [][]string{state.Client.Capabilities, state.Client.RequiredCapabilities, state.Server.Capabilities, state.Server.RequiredCapabilities} {
		for _, v := range list {
			values[v] = true
		}
	}
	var keys []string
	for v := range values {
		keys = append(keys, v)
	}
	sort.Strings(keys)
	describe := func(d goprotocol.Descriptor, capability string, legacy bool) string {
		var parts []string
		if goprotocol.HasCapability(d.Capabilities, capability) {
			parts = append(parts, c.tr("Provided"))
		}
		if goprotocol.HasCapability(d.RequiredCapabilities, capability) {
			parts = append(parts, c.tr("Required"))
		}
		if len(parts) == 0 {
			if legacy {
				return c.tr("Unknown")
			}
			return "—"
		}
		return strings.Join(parts, " / ")
	}
	for _, capability := range keys {
		y += w.dp(20)
		if y > footer-w.dp(80) {
			break
		}
		tableLabel(image.Rect(x0, y, x1, y+w.dp(20)), capability, false)
		tableLabel(image.Rect(x1, y, x2, y+w.dp(20)), describe(state.Client, capability, false), false)
		tableLabel(image.Rect(x2, y, r.Max.X-w.dp(24), y+w.dp(20)), describe(state.Server.Descriptor, capability, state.Compatibility.Legacy), false)
	}
	message := state.Message
	if c.settings.serverConfirm {
		message = c.tr("Restart restores layout and directories. Running tasks will end.")
	} else if state.Compatibility.Legacy {
		message = c.tr("Legacy server cannot safely export current directories. Restart recovery is unavailable.")
	}
	if message != "" {
		label(footer-w.dp(42), c.tr(message), true)
	}
	button := func(x, width int, title, action string) {
		br := image.Rect(x, footer+w.dp(24), x+width, footer+w.dp(54))
		w.button(dst, c, br, title, hitSettingsControl, uuid.Nil, false)
		c.hitRegions[len(c.hitRegions)-1].Label = "server:" + action
	}
	left := r.Min.X + w.dp(24)
	if !state.Busy {
		if c.settings.serverConfirm {
			button(left, w.dp(210), "Save layout and restart", "restart")
			button(left+w.dp(222), w.dp(100), "Cancel", "cancel-restart")
		} else {
			button(left, w.dp(104), "Refresh", "inspect")
			if goprotocol.HasCapability(state.Server.Capabilities, "recovery/v1") {
				button(left+w.dp(116), w.dp(120), "Save layout", "backup")
				button(left+w.dp(248), w.dp(110), "Restart server", "restart")
				button(left+w.dp(370), w.dp(110), "Retry restore", "restore")
			}
		}
	}
	br := image.Rect(r.Max.X-w.dp(98), footer+w.dp(24), r.Max.X-w.dp(24), footer+w.dp(54))
	w.button(dst, c, br, "Close", hitSettingsControl, uuid.Nil, false)
	c.hitRegions[len(c.hitRegions)-1].Label = "cancel"
}
