package goui

import (
	"image"
	"testing"

	"github.com/SurTeam/Water/internal/goconfig"
	"github.com/SurTeam/Water/internal/goprotocol"
	"golang.org/x/image/font/gofont/goregular"
)

func TestWebModalIsolatesSettingsAndEscapeReturns(t *testing.T) {
	cfg := goconfig.Default()
	c := NewWorkspaceClientWithConnection(nil, nil, cfg, "")
	c.openSettings()
	c.settings.group = 5
	c.server.info.Capabilities = []string{goprotocol.WebCapability, "recovery/v1"}
	w := &EbitengineWindow{scale: 2, views: map[*WorkspaceClient]*nativeView{}, size: image.Pt(1700, 1176), fonts: &nativeFonts{ui: fontSource(goregular.TTF)}}
	r := image.Rect(0, 0, w.dp(680), w.dp(492))

	check := func() {
		c.hitRegions = c.hitRegions[:0]
		w.drawServerSettings(c, nil, r, w.dp(112), r.Max.Y-w.dp(72))
		found := false
		var labels []string
		for _, h := range c.hitRegions {
			labels = append(labels, h.Label)
			if h.Label == "server:web-open" {
				found = true
			}
			if h.Label == "server:web-start" || h.Label == "server:web-settings" {
				t.Fatalf("inline Web control %q remains on Server page; all: %v", h.Label, labels)
			}
		}
		if !found {
			t.Fatalf("Web service entry missing; all: %v", labels)
		}
	}
	check()

	// The entry also sits under the capability list so related content stays
	// together and the page below is free for content output.
	c.server.mu.Lock()
	c.server.capabilitiesVisible = true
	c.server.mu.Unlock()
	check()
	c.server.mu.Lock()
	c.server.capabilitiesVisible = false
	c.server.mu.Unlock()

	c.server.webVisible = true
	w.drawWebService(c, nil)
	for _, h := range c.hitRegions {
		if h.Label == "server:restart" || h.Label == "server:web-open" || h.Label == "cancel" {
			t.Fatalf("underlying input leaked into modal: %s", h.Label)
		}
	}
	c.settings.focus = -1
	if !w.key(c, "escape") {
		t.Fatal("Escape not handled")
	}
	if c.ServerPanelState().WebVisible || !c.settings.visible {
		t.Fatal("Escape should close Web modal and retain Settings")
	}
}

// TestWebServiceEntrySitsUnderCapabilityList verifies the Web service button
// renders directly under the capability list (no inline Web status controls
// between them) so related content stays together and the page below is free
// for content output.
func TestWebServiceEntrySitsUnderCapabilityList(t *testing.T) {
	cfg := goconfig.Default()
	c := NewWorkspaceClientWithConnection(nil, nil, cfg, "")
	c.openSettings()
	c.settings.group = 5
	c.server.info.Capabilities = []string{goprotocol.WebCapability, "recovery/v1"}
	w := &EbitengineWindow{scale: 2, views: map[*WorkspaceClient]*nativeView{}, size: image.Pt(1700, 1176), fonts: &nativeFonts{ui: fontSource(goregular.TTF)}}
	r := image.Rect(0, 0, w.dp(680), w.dp(492))
	c.hitRegions = c.hitRegions[:0]
	w.drawServerSettings(c, nil, r, w.dp(112), r.Max.Y-w.dp(72))
	var labels []string
	openIdx := -1
	for i, h := range c.hitRegions {
		labels = append(labels, h.Label)
		switch h.Label {
		case "server:web-open":
			openIdx = i
		case "server:web-start", "server:web-settings", "server:web-devices", "server:web-autostart":
			t.Fatalf("inline Web control %q remains on the Server page; all: %v", h.Label, labels)
		}
	}
	if openIdx == -1 {
		t.Fatalf("Web service entry missing; all: %v", labels)
	}
	// The entry must directly follow the Capabilities header button.
	if openIdx != 1 || c.hitRegions[0].Label != "server:web-capabilities" {
		t.Fatalf("Web service entry not directly under the capability list: got index %d after %q", openIdx, c.hitRegions[0].Label)
	}
	// Below the entry, the page must be free of content other than the
	// footer actions, leaving the space for content output.
	for _, h := range c.hitRegions[openIdx+1:] {
		switch h.Label {
		case "server:web-close", "server:inspect", "server:backup", "server:restart", "server:restore", "cancel":
		default:
			t.Fatalf("unexpected content below the Web service entry: %q", h.Label)
		}
	}
}

func TestWebAutostartPanelPersistsPreference(t *testing.T) {
	c, _, path := webPanelFixture(t, "")
	initial := c.currentConfig()
	store := &SettingsStore{}
	store.value.Store(&initial)
	c.settingsStore = store
	c.settings.base = &initial
	panelAction(t, c, "web-open")
	panelAction(t, c, "web-settings")
	c.layoutMu.Lock()
	for i := range c.settings.fields {
		f := &c.settings.fields[i]
		if f.group == "Web" && f.name == "ListenAddress" {
			f.editor.SetText("127.0.0.1")
		}
		if f.group == "Web" && f.name == "ListenPort" {
			f.editor.SetText("8080")
		}
	}
	c.layoutMu.Unlock()
	panelAction(t, c, "web-configure")
	panelAction(t, c, "web-autostart")
	cfg, err := goconfig.Load(path)
	if err != nil || !cfg.Web.Enabled || cfg.Web.Origin() != "http://127.0.0.1:8080" {
		t.Fatalf("saved config=%+v err=%v", cfg.Web, err)
	}
	if c.settings.base != store.value.Load() {
		t.Fatal("Web save left the underlying Settings base stale")
	}
	if !c.ServerPanelState().WebEnabled {
		t.Fatal("autostart state not projected")
	}
	c.layoutMu.Lock()
	c.webServerAction("web-dismiss")
	c.layoutMu.Unlock()
	if c.ServerPanelState().WebVisible || !c.settings.visible {
		t.Fatal("close did not return to Settings")
	}
}
