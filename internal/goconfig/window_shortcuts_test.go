package goconfig

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestWindowShortcutOverridesRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"shortcuts":{"hide_window":"cmd-alt-w","minimize_window":"cmd-alt-m","ignore_quit":"cmd-alt-q","focus_left":"cmd-h"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Shortcuts.HideWindow != "cmd-alt-w" || cfg.Shortcuts.MinimizeWindow != "cmd-alt-m" || cfg.Shortcuts.IgnoreQuit != "cmd-alt-q" || cfg.Shortcuts.FocusLeft != "cmd-h" {
		t.Fatalf("window overrides lost: %#v", cfg.Shortcuts)
	}
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	reloaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(reloaded.Shortcuts, cfg.Shortcuts) {
		t.Fatalf("shortcut save changed mapping: %#v", reloaded.Shortcuts)
	}
}
