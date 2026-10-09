package goui

import (
	"github.com/SurTeam/Water/internal/goconfig"
	"github.com/SurTeam/Water/internal/goprotocol"
	"golang.org/x/image/font/gofont/goregular"
	"image"
	"testing"
)

func TestWebModalIsolatesSettingsAndEscapeReturns(t *testing.T) {
	cfg := goconfig.Default()
	c := NewWorkspaceClientWithConnection(nil, nil, cfg, "")
	c.openSettings()
	c.settings.group = 5
	c.server.info.Capabilities = []string{goprotocol.WebCapability, "recovery/v1"}
	w := &EbitengineWindow{scale: 2, views: map[*WorkspaceClient]*nativeView{}, size: image.Pt(1700, 1176), fonts: &nativeFonts{ui: fontSource(goregular.TTF)}}
	r := image.Rect(0, 0, w.dp(680), w.dp(492))
	w.drawServerSettings(c, nil, r, w.dp(112), r.Max.Y-w.dp(72))
	found := false
	for _, h := range c.hitRegions {
		if h.Label == "server:web-open" {
			found = true
		}
		if h.Label == "server:web-start" || h.Label == "server:web-settings" {
			t.Fatal("inline Web controls remain on Server page")
		}
	}
	if !found {
		t.Fatal("Web service entry missing")
	}
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
