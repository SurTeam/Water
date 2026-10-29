package goconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWindowsDefaultsAndConfigReplacement(t *testing.T) {
	t.Setenv("WATER_SHELL", "")
	t.Setenv("SHELL", "/bin/invalid-unix-shell")
	cfg := Default()
	name := strings.ToLower(filepath.Base(cfg.Shell.Program))
	if name != "pwsh.exe" && name != "powershell.exe" && name != "cmd.exe" {
		t.Fatalf("native shell = %q", cfg.Shell.Program)
	}
	if cfg.Shortcuts.Paste != "ctrl-shift-v" || cfg.Shortcuts.CopyOrInterrupt != "ctrl-shift-c" {
		t.Fatalf("Windows clipboard shortcuts: %#v", cfg.Shortcuts)
	}
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	cfg.UI.Language = "en"
	if err := Save(path, cfg); err != nil {
		t.Fatalf("replace existing config: %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.UI.Language != "en" {
		t.Fatal("replacement did not persist")
	}
	root, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(StandardPath("dev"), root+string(filepath.Separator)) {
		t.Fatalf("config outside user config dir: %q", StandardPath("dev"))
	}
}
