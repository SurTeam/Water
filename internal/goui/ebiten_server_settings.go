package goui

import (
	"fmt"
	"image"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/SurTeam/Water/internal/goprotocol"
	"github.com/google/uuid"
	"github.com/hajimehoshi/ebiten/v2"
)

func (w *EbitengineWindow) drawServerSettings(c *WorkspaceClient, dst *ebiten.Image, r image.Rectangle, top, footer int) {
	w.drawServerContent(c, dst, r, top, footer, false)
}

func (w *EbitengineWindow) drawWebService(c *WorkspaceClient, dst *ebiten.Image) {
	r := w.overlayPanel(c, dst, w.dp(560), w.dp(440), true)
	w.label(dst, c, image.Rect(r.Min.X+w.dp(24), r.Min.Y+w.dp(16), r.Max.X-w.dp(24), r.Min.Y+w.dp(40)), c.tr("Web service"), 16, configColor(c.currentConfig().Theme.UIForeground, 0xe6eaea), true)
	w.drawServerContent(c, dst, r, r.Min.Y+w.dp(52), r.Max.Y-w.dp(72), true)
}

func (w *EbitengineWindow) drawServerContent(c *WorkspaceClient, dst *ebiten.Image, r image.Rectangle, top, footer int, web bool) {
	state := c.ServerPanelState()
	if picker, ok := w.platform.(interface{ ChooseFile(string) (string, error) }); ok {
		c.server.mu.Lock()
		c.server.chooseFile = picker.ChooseFile
		c.server.mu.Unlock()
	}
	if !web {
		state.WebConfigVisible = false
		state.DevicesVisible = false
	}
	if state.PairQR == nil {
		c.server.mu.Lock()
		if c.server.pairTexture != nil {
			c.server.pairTexture.Deallocate()
			c.server.pairTexture = nil
			c.server.pairTextureSource = nil
		}
		c.server.mu.Unlock()
	}
	cfg := c.currentConfig()
	fg := configColor(cfg.Theme.UIForeground, 0xe6eaea)
	label := func(y int, text string, small bool) {
		size := 11.
		if small {
			size = 10
		}
		w.label(dst, c, image.Rect(r.Min.X+w.dp(24), y, r.Max.X-w.dp(24), y+w.dp(22)), text, size, fg, false)
	}
	button := func(x, y, width int, title, action string) {
		br := image.Rect(x, y, x+width, y+w.dp(30))
		if action == "web-configure" || action == "web-start" {
			w.settingsPrimaryButton(dst, c, br, title)
		} else {
			w.button(dst, c, br, title, hitSettingsControl, uuid.Nil, false)
		}
		c.hitRegions[len(c.hitRegions)-1].Label = "server:" + action
	}
	left := r.Min.X + w.dp(24)
	connection := "Local"
	if c.remoteDestination != "" {
		connection = c.remoteDestination
	}
	label(top, c.trf("Connection: %s", connection), false)
	y := top + w.dp(34)
	if !web {
		label(top+w.dp(24), fmt.Sprintf("GUI %s · Server %s · %s · PID %d", state.Client.Version, state.Server.ServerVersion, state.Server.BuildVariant, state.Server.ServerPID), true)
		label(top+w.dp(48), c.tr(state.Compatibility.Reason), true)
		if !state.Busy {
			button(r.Max.X-w.dp(120), top, w.dp(96), "Capabilities", "web-capabilities")
		}
		y = top + w.dp(80)
	}
	if state.CapabilitiesVisible {
		label(y, c.trf("Server revision: %s", state.Server.ServerRevision), true)
		y += w.dp(24)
		label(y, c.tr("Capability · GUI / Server"), false)
		y += w.dp(26)
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
		describe := func(d goprotocol.Descriptor, key string) string {
			var parts []string
			if goprotocol.HasCapability(d.Capabilities, key) {
				parts = append(parts, c.tr("Provided"))
			}
			if goprotocol.HasCapability(d.RequiredCapabilities, key) {
				parts = append(parts, c.tr("Required"))
			}
			if len(parts) == 0 {
				return "—"
			}
			return strings.Join(parts, " / ")
		}
		for _, key := range keys {
			if y > footer-w.dp(80) {
				break
			}
			label(y, key+" · "+describe(state.Client, key)+" / "+describe(state.Server.Descriptor, key), true)
			y += w.dp(24)
		}
		if !web {
			y += w.dp(12)
			if !state.Busy {
				label(y, c.tr("Manage browser access, pairing and devices in a separate window."), true)
				button(left, y+w.dp(30), w.dp(150), "Web service", "web-open")
			}
		}
		if !state.Busy {
			button(left, footer-w.dp(72), w.dp(90), "Back", "web-close")
		}
	} else if state.WebConfigVisible {
		label(y, c.tr("Web settings belong to this server. Stop Web before applying."), true)
		y += w.dp(24)
		tls := false
		count := 0
		for i, f := range c.settings.fields {
			if f.group == "Web" && f.name == "TLS" {
				tls = f.toggle.Value
			}
			if webSettingsFieldVisible(c.settings.fields, i) {
				count++
			}
		}
		if tls {
			for _, text := range []string{
				"HTTPS needs both a PEM certificate chain (.pem/.crt) and its key.",
				"Key: unencrypted PEM (.pem/.key), RSA / EC / PKCS#8; not .p12/.pfx.",
				"Choose both files locally; Apply imports them to this server.",
			} {
				label(y, c.tr(text), true)
				y += w.dp(18)
			}
			y += w.dp(4)
		} else {
			label(y, c.tr("HTTP service: enter a listen IP/hostname and a fixed port separately."), true)
			y += w.dp(28)
		}
		step := min(w.dp(32), (footer-w.dp(30)-y)/max(1, count))
		for i := range c.settings.fields {
			f := &c.settings.fields[i]
			if !webSettingsFieldVisible(c.settings.fields, i) {
				continue
			}
			fr := image.Rect(r.Min.X+r.Dx()*40/100, y, r.Max.X-w.dp(24), y+step-w.dp(4))
			w.label(dst, c, image.Rect(left, y, fr.Min.X-w.dp(8), fr.Max.Y), localizedField(c.language(), *f), 11, fg, false)
			if f.kind == reflect.Bool {
				w.drawSettingsToggle(c, dst, fr, f.toggle.Value)
			} else {
				if f.name == "TLSCertFile" || f.name == "TLSKeyFile" {
					br := image.Rect(fr.Max.X-w.dp(76), fr.Min.Y, fr.Max.X, fr.Max.Y)
					fr.Max.X = br.Min.X - w.dp(8)
					if !state.Busy {
						w.button(dst, c, br, "Choose…", hitSettingsControl, uuid.Nil, false)
						action := "server:web-select-cert"
						if f.name == "TLSKeyFile" {
							action = "server:web-select-key"
						}
						c.hitRegions[len(c.hitRegions)-1].Label = action
					}
				}
				w.field(c, dst, fr, f.editor.Text(), c.settings.focus == i, &f.editor)
			}
			w.hit(c, fr, hitSettingsControl, uuid.Nil, "field:Web."+f.name)
			y += step
		}
	} else if state.DevicesVisible {
		label(y, c.tr("Paired browsers"), false)
		y += w.dp(26)
		maxRows := max(1, (footer-y-w.dp(76))/w.dp(34))
		scroll := w.view(c).settingsScroll
		scroll = min(max(0, scroll), max(0, len(state.Devices)-maxRows))
		w.view(c).settingsScroll = scroll
		if len(state.Devices) == 0 {
			label(y, c.tr("No paired browsers"), true)
		}
		for i := scroll; i < len(state.Devices) && i < scroll+maxRows; i++ {
			d := state.Devices[i]
			w.label(dst, c, image.Rect(left, y, r.Max.X-w.dp(120), y+w.dp(30)), d.Name, 10, fg, false)
			if !state.Busy {
				button(r.Max.X-w.dp(112), y, w.dp(88), "Revoke", "web-revoke:"+d.ID)
			}
			y += w.dp(34)
		}
		if !state.Busy && !web {
			button(left, footer-w.dp(72), w.dp(90), "Back", "web-close")
		}
	} else if !web && goprotocol.HasCapability(state.Server.Capabilities, goprotocol.WebCapability) {
		// The Web service entry sits directly under the header so related
		// content stays together and the page below is free for content output.
		y += w.dp(12)
		if !state.Busy {
			label(y, c.tr("Manage browser access, pairing and devices in a separate window."), true)
			button(left, y+w.dp(30), w.dp(150), "Web service", "web-open")
		}
	} else if web && goprotocol.HasCapability(state.Server.Capabilities, goprotocol.WebCapability) {
		// Inline controls render only inside the Web modal.
		{
			webStatus := state.Server.Web
			running := webStatus != nil && webStatus.State == "running"
			text := "Web: stopped"
			if webStatus != nil {
				text = "Web: " + webStatus.State
				if webStatus.PublicURL != "" {
					text += " · " + webStatus.PublicURL
				}
			}
			label(y, text, true)
			y += w.dp(26)
			if !state.Busy {
				action, title := "web-start", "Start Web"
				if running {
					action, title = "web-stop", "Stop Web"
				}
				button(left, y, w.dp(100), title, action)
				button(left+w.dp(108), y, w.dp(112), "Web settings", "web-settings")
				button(left+w.dp(228), y, w.dp(90), "Devices", "web-devices")
				if running {
					button(left+w.dp(326), y, w.dp(110), "Pair device", "web-pair")
				}
			}
			y += w.dp(42)
			fr := image.Rect(r.Min.X+r.Dx()*42/100, y, r.Max.X-w.dp(36), y+w.dp(34))
			w.label(dst, c, image.Rect(left, y+w.dp(3), fr.Min.X-w.dp(8), y+w.dp(22)), c.tr("Start Web with server"), 11, fg, false)
			w.drawSettingsToggle(c, dst, fr, state.WebEnabled)
			if !state.Busy {
				w.hit(c, fr, hitSettingsControl, uuid.Nil, "server:web-autostart")
			}
			y += w.dp(40)
			if state.PairingExpiresAt != nil {
				if state.PairQR != nil {
					size := min(w.dp(176), footer-y-w.dp(35))
					c.server.mu.Lock()
					if c.server.pairTextureSource != state.PairQR {
						if c.server.pairTexture != nil {
							c.server.pairTexture.Deallocate()
						}
						c.server.pairTexture = ebiten.NewImageFromImage(state.PairQR)
						c.server.pairTextureSource = state.PairQR
					}
					texture := c.server.pairTexture
					c.server.mu.Unlock()
					if dst != nil && size > 0 {
						op := &ebiten.DrawImageOptions{}
						op.GeoM.Scale(float64(size)/float64(texture.Bounds().Dx()), float64(size)/float64(texture.Bounds().Dy()))
						op.GeoM.Translate(float64(left), float64(y))
						dst.DrawImage(texture, op)
					}
					text := c.trf("Expires at %s", state.PairingExpiresAt.Local().Format("15:04:05"))
					w.label(dst, c, image.Rect(left+size+w.dp(16), y, r.Max.X-w.dp(24), y+w.dp(24)), text, 10, fg, false)
					w.label(dst, c, image.Rect(left+size+w.dp(16), y+w.dp(28), r.Max.X-w.dp(24), y+w.dp(70)), c.tr("Scan to connect directly to this server."), 10, fg, false)
					if !state.Busy {
						button(left+size+w.dp(16), y+w.dp(78), w.dp(120), "Cancel pairing", "web-close")
					}
				} else if time.Now().After(*state.PairingExpiresAt) {
					label(y, c.tr("Pairing expired. Create a new invitation."), true)
				}
			}
		}
	} else {
		label(y, c.tr("This server does not support Web access. Upgrade the server to enable it."), true)
	}
	message := state.Message
	if c.settings.serverConfirm {
		message = c.tr("Restart restores layout and directories. Running tasks will end.")
	}
	if message != "" {
		label(footer-w.dp(26), c.tr(message), true)
	}
	if !state.Busy {
		if state.WebConfigVisible {
			button(left, footer+w.dp(24), w.dp(150), "Apply Web settings", "web-configure")
			button(left+w.dp(162), footer+w.dp(24), w.dp(90), "Back", "web-close")
		} else if web && state.DevicesVisible {
			button(left, footer+w.dp(24), w.dp(90), "Back", "web-close")
		} else if web {
			button(left, footer+w.dp(24), w.dp(92), "Refresh", "inspect")
		} else if c.settings.serverConfirm {
			button(left, footer+w.dp(24), w.dp(210), "Save layout and restart", "restart")
			button(left+w.dp(222), footer+w.dp(24), w.dp(100), "Cancel", "cancel-restart")
		} else {
			button(left, footer+w.dp(24), w.dp(92), "Refresh", "inspect")
			if goprotocol.HasCapability(state.Server.Capabilities, "recovery/v1") {
				button(left+w.dp(100), footer+w.dp(24), w.dp(105), "Save layout", "backup")
				button(left+w.dp(213), footer+w.dp(24), w.dp(115), "Restart server", "restart")
				button(left+w.dp(336), footer+w.dp(24), w.dp(105), "Retry restore", "restore")
			}
		}
	}
	br := image.Rect(r.Max.X-w.dp(98), footer+w.dp(24), r.Max.X-w.dp(24), footer+w.dp(54))
	w.button(dst, c, br, "Close", hitSettingsControl, uuid.Nil, false)
	if web {
		c.hitRegions[len(c.hitRegions)-1].Label = "server:web-dismiss"
	} else {
		c.hitRegions[len(c.hitRegions)-1].Label = "cancel"
	}
}
