package goui

import (
	"testing"

	"github.com/SurTeam/Water/internal/goconfig"
)

func TestWebSettingsIgnoreUnrelatedDraftErrors(t *testing.T) {
	c := NewWorkspaceClientWithConnection(nil, nil, goconfig.Default(), "")
	c.openSettings()
	for i := range c.settings.fields {
		f := &c.settings.fields[i]
		if f.group == "Terminal" && f.name == "CursorStyle" {
			f.editor.SetText("invalid-cursor")
		}
		if f.group == "Web" && f.name == "ListenAddress" {
			f.editor.SetText("0.0.0.0")
		}
	}
	if _, err := c.settings.parse(); err == nil {
		t.Fatal("unrelated invalid draft should remain invalid")
	}
	cfg, err := c.settings.parseWeb()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ListenAddress != "0.0.0.0" || cfg.ListenPort != 8080 {
		t.Fatalf("Web draft not parsed: %+v", cfg)
	}
	for i := range c.settings.fields {
		f := &c.settings.fields[i]
		if f.group == "Terminal" && f.name == "CursorStyle" && f.editor.Text() != "invalid-cursor" {
			t.Fatal("Web parsing changed another draft")
		}
		if f.group == "Web" && f.name == "ListenPort" {
			f.editor.SetText("-1")
		}
	}
	if _, err := c.settings.parseWeb(); err == nil {
		t.Fatal("invalid Web port was accepted")
	}
}
