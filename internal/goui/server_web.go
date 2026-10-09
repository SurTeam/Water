package goui

import (
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/SurTeam/Water/internal/goconfig"
	"github.com/SurTeam/Water/internal/goprotocol"
	"github.com/skip2/go-qrcode"
)

// No layout lock is required. The invitation belongs to this connection, never
// whichever connection happens to be active when the worker completes.
func (c *WorkspaceClient) cancelWebPairing() {
	c.server.mu.Lock()
	pair := c.server.pairing
	c.server.pairGeneration++
	c.server.pairing = nil
	c.server.pairQR = nil
	c.server.mu.Unlock()
	if pair != nil && c.session != nil {
		go func() {
			_ = c.session.CallTimeout("server.web.pair.cancel", map[string]any{"id": pair.ID}, nil, time.Second)
		}()
	}
}

func (c *WorkspaceClient) closeWebService() {
	c.cancelWebPairing()
	c.server.mu.Lock()
	c.server.webVisible = false
	c.server.webConfigVisible = false
	c.server.devicesVisible = false
	c.server.mu.Unlock()
}

func (c *WorkspaceClient) webServerAction(action string) {
	if action == "web-select-cert" || action == "web-select-key" {
		c.selectWebTLSFile(action)
		return
	}
	if action == "web-dismiss" {
		c.closeWebService()
		c.setWebConfigFields(c.currentConfig().Web)
		c.settings.focus = -1
		return
	}
	if action == "web-close" {
		c.settings.focus = -1
		c.cancelWebPairing()
		c.server.mu.Lock()
		c.server.webConfigVisible = false
		c.server.devicesVisible = false
		c.server.capabilitiesVisible = false
		c.server.mu.Unlock()
		return
	}
	if action == "web-capabilities" {
		c.cancelWebPairing()
		c.server.mu.Lock()
		c.server.capabilitiesVisible = true
		c.server.webConfigVisible = false
		c.server.devicesVisible = false
		c.server.mu.Unlock()
		return
	}
	c.server.mu.Lock()
	if c.server.busy {
		c.server.mu.Unlock()
		return
	}
	if action == "web-open" {
		c.server.webVisible = true
		c.server.webConfigVisible = false
		c.server.devicesVisible = false
		c.server.capabilitiesVisible = false
		c.settings.focus = -1
	}
	c.server.busy = true
	c.server.message = "Working…"
	c.server.mu.Unlock()
	var cfg goconfig.WebConfig
	var certificate, privateKey []byte
	if action == "web-configure" {
		c.server.mu.Lock()
		certificate, privateKey = c.server.certificatePEM, c.server.privateKeyPEM
		importSupported := goprotocol.HasCapability(c.server.info.Capabilities, goprotocol.WebTLSImportCapability)
		c.server.mu.Unlock()
		if len(certificate) != 0 && !importSupported {
			c.server.mu.Lock()
			c.server.busy = false
			c.server.message = "Upgrade this server to import local TLS files"
			c.server.mu.Unlock()
			return
		}
		if (len(certificate) == 0) != (len(privateKey) == 0) {
			c.server.mu.Lock()
			c.server.busy = false
			c.server.message = "Select both files on this computer before applying"
			c.server.mu.Unlock()
			return
		}
		parsed, err := c.settings.parseWeb()
		if err != nil {
			c.server.mu.Lock()
			c.server.busy = false
			c.server.message = err.Error()
			c.server.mu.Unlock()
			return
		}
		cfg = parsed
	}
	c.cancelWebPairing()
	c.server.mu.Lock()
	pairGeneration := c.server.pairGeneration
	c.server.mu.Unlock()
	go func() {
		var err error
		if c.session == nil {
			err = errors.New("server session is unavailable")
		} else {
			switch {
			case action == "web-start":
				err = c.session.CallTimeout("server.web.start", nil, nil, 10*time.Second)
			case action == "web-stop":
				err = c.session.CallTimeout("server.web.stop", nil, nil, 10*time.Second)
			case action == "web-configure":
				request := struct {
					goconfig.WebConfig
					CertificatePEM []byte `json:"certificate_pem,omitempty"`
					PrivateKeyPEM  []byte `json:"private_key_pem,omitempty"`
				}{cfg, certificate, privateKey}
				var response struct {
					Config goconfig.WebConfig `json:"config"`
				}
				err = c.session.CallTimeout("server.web.configure", request, &response, 10*time.Second)
				if err == nil && len(certificate) != 0 {
					cfg = response.Config
				}
			case action == "web-open" || action == "web-settings":
				var response struct {
					Config goconfig.WebConfig `json:"config"`
				}
				err = c.session.CallTimeout("server.web.inspect", nil, &response, 3*time.Second)
				if err == nil {
					cfg = response.Config
					c.layoutMu.Lock()
					c.server.mu.Lock()
					visible := c.server.webVisible && c.server.pairGeneration == pairGeneration
					c.server.webEnabled = cfg.Enabled
					if visible {
						c.server.webConfigVisible = action == "web-settings"
						c.server.devicesVisible = false
					}
					c.server.mu.Unlock()
					if visible {
						c.setWebConfigFields(cfg)
					}
					c.layoutMu.Unlock()
				}
			case action == "web-autostart":
				var response struct {
					Config goconfig.WebConfig `json:"config"`
				}
				err = c.session.CallTimeout("server.web.inspect", nil, &response, 3*time.Second)
				if err == nil {
					cfg = response.Config
					cfg.Enabled = !cfg.Enabled
					err = c.session.CallTimeout("server.web.configure", cfg, nil, 10*time.Second)
				}
			case action == "web-pair":
				var pair goprotocol.WebPairing
				err = c.session.CallTimeout("server.web.pair.create", nil, &pair, 3*time.Second)
				if err == nil {
					code, qrErr := qrcode.New(pair.URL, qrcode.Medium)
					if qrErr != nil {
						err = qrErr
						_ = c.session.CallTimeout("server.web.pair.cancel", map[string]any{"id": pair.ID}, nil, time.Second)
					} else {
						img := code.Image(256)
						// If the panel was closed while the request was in flight, cancel the
						// invitation instead of leaving an invisible authorization token alive.
						c.layoutMu.Lock()
						c.server.mu.Lock()
						visible := c.settings.visible && c.server.webVisible
						c.server.mu.Unlock()
						c.layoutMu.Unlock()
						if !visible {
							_ = c.session.CallTimeout("server.web.pair.cancel", map[string]any{"id": pair.ID}, nil, time.Second)
						} else {
							c.server.mu.Lock()
							valid := c.server.pairGeneration == pairGeneration
							if valid {
								c.server.pairing = &pair
								c.server.pairQR = img
							}
							c.server.mu.Unlock()
							if valid {
								go c.watchWebPairing(pair.ID)
							} else {
								_ = c.session.CallTimeout("server.web.pair.cancel", map[string]any{"id": pair.ID}, nil, time.Second)
							}
						}
					}
				}
			case action == "web-devices":
				var devices []goprotocol.WebDevice
				err = c.session.CallTimeout("server.web.devices.list", nil, &devices, 3*time.Second)
				if err == nil {
					c.server.mu.Lock()
					c.server.devices = devices
					c.server.devicesVisible = true
					c.server.webConfigVisible = false
					c.server.mu.Unlock()
				}
			case strings.HasPrefix(action, "web-revoke:"):
				err = c.session.CallTimeout("server.web.devices.revoke", map[string]any{"id": strings.TrimPrefix(action, "web-revoke:")}, nil, 3*time.Second)
				if err == nil {
					var devices []goprotocol.WebDevice
					err = c.session.CallTimeout("server.web.devices.list", nil, &devices, 3*time.Second)
					c.server.mu.Lock()
					c.server.devices = devices
					c.server.mu.Unlock()
				}
			default:
				err = fmt.Errorf("unknown Web operation %q", action)
			}
		}
		if err == nil && (action == "web-configure" || action == "web-autostart") {
			c.layoutMu.Lock()
			if c.remoteDestination == "" {
				if c.settingsStore != nil {
					for {
						old := c.settingsStore.value.Load()
						next := *old
						next.Web = cfg
						if c.settingsStore.value.CompareAndSwap(old, &next) {
							if c.settings.base == old {
								c.settings.base = &next
							}
							break
						}
					}
				} else {
					c.config.Web = cfg
				}
			}
			c.server.mu.Lock()
			visible := c.server.webVisible
			c.server.webEnabled = cfg.Enabled
			c.server.mu.Unlock()
			if visible {
				c.setWebConfigFields(cfg)
				c.settings.focus = -1
			}
			c.layoutMu.Unlock()
		}
		var info goprotocol.ServerInfo
		if c.session != nil {
			_ = c.session.CallTimeout("server.inspect", nil, &info, time.Second)
		}
		c.server.mu.Lock()
		c.server.busy = false
		if info.ProtocolVersion != 0 {
			c.server.info = info
		}
		if err != nil {
			c.server.message = err.Error()
		} else {
			c.server.message = "Server operation completed"
			if action == "web-configure" {
				c.server.webConfigVisible = false
			}
		}
		c.server.mu.Unlock()
		if c.invalidate != nil {
			c.invalidate()
		}
	}()
}

// Called with layoutMu held; target-server fields must not leak into the
// local Startup preferences when the user leaves the Server panel.
func (c *WorkspaceClient) setWebConfigFields(cfg goconfig.WebConfig) {
	c.server.mu.Lock()
	c.server.certificatePEM = nil
	c.server.privateKeyPEM = nil
	c.server.mu.Unlock()
	values := reflect.ValueOf(cfg)
	for i := range c.settings.fields {
		f := &c.settings.fields[i]
		if f.group != "Web" {
			continue
		}
		v := values.FieldByName(f.name)
		if v.Kind() == reflect.Bool {
			f.toggle.Value = v.Bool()
		} else if f.kind == reflect.Int {
			f.editor.SetText(strconv.FormatInt(v.Int(), 10))
		} else {
			f.editor.SetText(v.String())
		}
	}
	valuesNow := c.settings.values()
	for i, f := range c.settings.fields {
		if f.group == "Web" && i < len(c.settings.initial) {
			c.settings.initial[i] = valuesNow[i]
		}
	}
}

// Poll a concrete invitation state; stop as soon as the panel cancels it.
func (c *WorkspaceClient) watchWebPairing(id string) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for range ticker.C {
		c.server.mu.Lock()
		current := c.server.pairing != nil && c.server.pairing.ID == id
		c.server.mu.Unlock()
		if !current {
			return
		}
		var result struct {
			Pending bool `json:"pending"`
		}
		err := c.session.CallTimeout("server.web.pair.status", map[string]any{"id": id}, &result, time.Second)
		if err != nil || !result.Pending {
			c.server.mu.Lock()
			if c.server.pairing != nil && c.server.pairing.ID == id {
				c.server.pairing = nil
				c.server.pairQR = nil
				if err != nil {
					c.server.message = err.Error()
				} else {
					c.server.message = "Pairing invitation is no longer active"
				}
			}
			c.server.mu.Unlock()
			if c.invalidate != nil {
				c.invalidate()
			}
			return
		}
	}
}
