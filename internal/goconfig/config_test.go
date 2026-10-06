package goconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadKeepsDefaultsForPartialOverrides(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{
		"terminal":{"scrollback_lines":321,"default_columns":123},
		"shell":{"program":"/bin/sh","args":["-l"]},
		"features":{"selection":false}
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Terminal.ScrollbackLines != 321 || cfg.Terminal.DefaultColumns != 123 {
		t.Fatalf("terminal overrides not applied: %#v", cfg.Terminal)
	}
	if cfg.Terminal.DefaultLines != DefaultLines || cfg.Terminal.ReplayHistoryBytes != DefaultReplayHistoryBytes {
		t.Fatalf("terminal defaults were lost: %#v", cfg.Terminal)
	}
	if cfg.Shell.Program != "/bin/sh" || len(cfg.Shell.Args) != 1 || cfg.Shell.Args[0] != "-l" {
		t.Fatalf("shell override not applied: %#v", cfg.Shell)
	}
	if cfg.Features.Selection {
		t.Fatal("selection override not applied")
	}
	if !cfg.Features.MouseReporting || !cfg.Features.BracketedPaste {
		t.Fatalf("feature defaults were lost: %#v", cfg.Features)
	}
	if !cfg.UI.SystemNotifications || cfg.UI.AgentLongRunNotificationSeconds != 600 || cfg.UI.ShellLongRunNotificationSeconds != 300 {
		t.Fatalf("notification defaults were lost: %#v", cfg.UI)
	}
}

func TestLoadWrappedOverrides(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"overrides":{"terminal":{"replay_history_bytes":2097152}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Terminal.ReplayHistoryBytes != 2*1024*1024 {
		t.Fatalf("wrapped override not applied: %d", cfg.Terminal.ReplayHistoryBytes)
	}
	if cfg.Terminal.ScrollbackLines != DefaultScrollbackLines {
		t.Fatal("wrapped override lost defaults")
	}
}

func TestNormalizeClampsTerminalLimits(t *testing.T) {
	cfg := Default()
	cfg.Startup.WindowColumns = 9999
	cfg.Startup.WindowRows = 0
	cfg.Startup.WindowMinRows = 1
	cfg.Terminal.ScrollbackLines = 5
	cfg.Terminal.InactiveScrollbackLines = 100
	cfg.Terminal.ReplayHistoryBytes = 1
	cfg = cfg.Normalized()
	if cfg.Terminal.DefaultColumns != 512 || cfg.Terminal.DefaultLines != 1 {
		t.Fatalf("geometry not clamped: %#v", cfg.Terminal)
	}
	if cfg.Terminal.InactiveScrollbackLines != 5 {
		t.Fatalf("inactive history not bounded: %#v", cfg.Terminal)
	}
	if cfg.Terminal.ReplayHistoryBytes != MinReplayHistoryBytes {
		t.Fatalf("replay limit not clamped: %d", cfg.Terminal.ReplayHistoryBytes)
	}
}

func TestNormalizeClampsNotificationThresholds(t *testing.T) {
	cfg := Default()
	cfg.UI.AgentLongRunNotificationSeconds = 100000
	cfg.UI.ShellLongRunNotificationSeconds = -1
	cfg = cfg.Normalized()
	if cfg.UI.AgentLongRunNotificationSeconds != 86400 || cfg.UI.ShellLongRunNotificationSeconds != 0 {
		t.Fatalf("notification thresholds not clamped: %#v", cfg.UI)
	}
}

func TestLoadNotificationSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"ui":{"system_notifications":false,"agent_long_run_notification_seconds":1,"shell_long_run_notification_seconds":3}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.UI.SystemNotifications || cfg.UI.AgentLongRunNotificationSeconds != 1 || cfg.UI.ShellLongRunNotificationSeconds != 3 {
		t.Fatalf("notification settings did not load: %#v", cfg.UI)
	}
}

func TestSaveNotificationSettingsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := Default()
	cfg.UI.SystemNotifications = false
	cfg.UI.AgentLongRunNotificationSeconds = 1
	cfg.UI.ShellLongRunNotificationSeconds = 3
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.UI.SystemNotifications || loaded.UI.AgentLongRunNotificationSeconds != 1 || loaded.UI.ShellLongRunNotificationSeconds != 3 {
		t.Fatalf("notification settings did not round-trip: %#v", loaded.UI)
	}
}

func TestChangingShellProgramRecomputesDefaultArgs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"shell":{"program":"/bin/sh"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Shell.Program != "/bin/sh" {
		t.Fatalf("program=%q", cfg.Shell.Program)
	}
	if len(cfg.Shell.Args) != 0 {
		t.Fatalf("sh inherited stale args: %#v", cfg.Shell.Args)
	}
}
