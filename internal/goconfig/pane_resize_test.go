package goconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPaneResizeDefaultsMigrationAndRoundTrip(t *testing.T) {
	defaults := Default().Shortcuts
	for _, tc := range []struct{ raw, remote, taller string }{
		{`{}`, defaults.ConnectRemote, defaults.PaneTaller},
		{`{"shortcuts":{"connect_remote":"cmd-shift-k"}}`, "cmd-alt-k", defaults.PaneTaller},
		{`{"overrides":{"shortcuts":{"connect_remote":"cmd-shift-k"}}}`, "cmd-alt-k", defaults.PaneTaller},
		{`{"shortcuts":{"connect_remote":"ctrl-r","pane_taller":"ctrl-up"}}`, "ctrl-r", "ctrl-up"},
		{`{"shortcuts":{"connect_remote":"cmd-shift-k","pane_taller":""}}`, "cmd-shift-k", ""},
	} {
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, []byte(tc.raw), 0600); err != nil {
			t.Fatal(err)
		}
		cfg, err := Load(path)
		if err != nil {
			t.Fatal(err)
		}
		s := cfg.Shortcuts
		if s.ConnectRemote != tc.remote || s.PaneTaller != tc.taller || s.PaneNarrower != defaults.PaneNarrower || s.PaneWider != defaults.PaneWider || s.PaneShorter != defaults.PaneShorter {
			t.Fatalf("incorrect defaults/migration: %+v", s)
		}
		cfg.Shortcuts.PaneNarrower = "ctrl-left"
		if err := Save(path, cfg); err != nil {
			t.Fatal(err)
		}
		loaded, err := Load(path)
		if err != nil {
			t.Fatal(err)
		}
		if loaded.Shortcuts != cfg.Shortcuts {
			t.Fatal("resize shortcuts lost on reload")
		}
	}
}
