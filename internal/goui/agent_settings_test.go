package goui

import (
	"github.com/SurTeam/Water/internal/goconfig"
	"reflect"
	"testing"
)

func TestSettingsCancelRequiresExplicitDiscardAndDefaultsStayDirty(t *testing.T) {
	c := NewWorkspaceClientWithConnection(nil, nil, goconfig.Default(), "")
	c.openSettings()
	for i := range c.settings.fields {
		if c.settings.fields[i].name == "SidebarWidth" {
			c.settings.fields[i].editor.SetText("260")
		}
	}
	c.settings.cancelChanges()
	if !c.settings.visible || !c.settings.confirmDiscard {
		t.Fatal("dirty settings closed without confirmation")
	}
	c.settings.cancelChanges()
	if c.settings.visible {
		t.Fatal("explicit discard did not close")
	}
	c.openSettings()
	c.settings.cancelChanges()
	if c.settings.visible {
		t.Fatal("clean settings did not cancel")
	}
}

func TestSidebarChoiceValidation(t *testing.T) {
	c := NewWorkspaceClientWithConnection(nil, nil, goconfig.Default(), "")
	c.openSettings()
	for i := range c.settings.fields {
		if c.settings.fields[i].name == "SidebarAgentMode" {
			c.settings.fields[i].editor.SetText("invalid")
		}
	}
	if _, err := c.settings.parse(); err == nil {
		t.Fatal("invalid sidebar mode accepted")
	}
}

func TestSettingsSavedPanelStaysOpenWithFreshBaseline(t *testing.T) {
	c := NewWorkspaceClientWithConnection(nil, nil, goconfig.Default(), "")
	c.openSettings()
	c.settings.group = 1
	c.settings.saving = true
	c.settings.result = make(chan settingsSave, 1)
	cfg := goconfig.Default()
	cfg.UI.TitlebarHeight = 12
	cfg.UI.TabHeight = 8
	c.settings.result <- settingsSave{cfg: cfg}
	c.pollSettingsSave()
	if !c.settings.visible || c.settings.saving || c.settings.group != 1 || !reflect.DeepEqual(c.settings.initial, c.settings.values()) {
		t.Fatal("save must retain the panel and reset the normalized baseline")
	}
	if c.currentConfig().UI.TitlebarHeight != 12 || titlebarHeight(cfg.UI) != 12 {
		t.Fatal("compact titlebar height was expanded")
	}
	c.settings.cancelChanges()
	if c.settings.visible || c.settings.confirmDiscard {
		t.Fatal("saved settings must close without a discard prompt")
	}
}

func TestCompactChromeDimensions(t *testing.T) {
	cfg := goconfig.Default()
	cfg.UI.TitlebarHeight, cfg.UI.TabHeight, cfg.UI.TabFontSize = 12, 8, 6
	cfg.UI.SidebarHostHeaderHeight, cfg.UI.SidebarHeaderHeight, cfg.UI.SidebarAgentRowHeight = 8, 8, 8
	cfg = cfg.Normalized()
	if cfg.UI.TitlebarHeight != 12 || cfg.UI.TabHeight != 8 || cfg.UI.TabFontSize != 6 || titlebarHeight(cfg.UI) != 12 {
		t.Fatal("compact dimensions were enlarged by normalization or font size")
	}
	cfg.UI.TabHeight = 40
	cfg = cfg.Normalized()
	if cfg.UI.TabHeight != 12 || cfg.UI.TitlebarHeight != 12 {
		t.Fatal("tabs must fit inside the independently configured titlebar")
	}
}

func TestSettingsShortcutConfirmsUnsavedChanges(t *testing.T) {
	c := NewWorkspaceClientWithConnection(nil, nil, goconfig.Default(), "")
	c.dispatchShortcut("cmd-,")
	for i := range c.settings.fields {
		if c.settings.fields[i].name == "TitlebarHeight" {
			c.settings.fields[i].editor.SetText("20")
		}
	}
	c.dispatchShortcut("cmd-,")
	if !c.settings.visible || !c.settings.confirmDiscard {
		t.Fatal("shortcut discarded dirty settings without confirmation")
	}
	c.dispatchShortcut("cmd-,")
	if c.settings.visible {
		t.Fatal("confirmed shortcut did not close settings")
	}
}
