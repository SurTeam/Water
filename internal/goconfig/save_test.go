package goconfig

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSavePreservesEnvelopeAndUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	original := []byte(`{"version":9,"overrides":{"terminal":{"future_option":true,"font_size":12},"future_section":{"enabled":true}}}`)
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Terminal.FontSize = 19
	cfg.Shortcuts.SplitDown = "ctrl--"
	cfg.Theme.ANSIColors = []string{"#123456"}
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Terminal.FontSize != 19 || loaded.Shortcuts.SplitDown != "ctrl--" || loaded.Theme.ANSIColors[0] != "#123456" {
		t.Fatal("settings lost on reload")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err = json.Unmarshal(data, &root); err != nil {
		t.Fatal(err)
	}
	overrides := root["overrides"].(map[string]any)
	if root["version"] != float64(9) || overrides["future_section"] == nil || overrides["terminal"].(map[string]any)["future_option"] != true {
		t.Fatal("unknown configuration was discarded")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatalf("permissions: %v", info.Mode())
	}
}

func TestSaveRefusesMalformedExistingConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	original := []byte(`{"overrides":`)
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := Save(path, Default()); err == nil {
		t.Fatal("malformed config overwritten")
	}
	data, _ := os.ReadFile(path)
	if string(data) != string(original) {
		t.Fatal("failed save changed existing file")
	}
}

func TestUIFontNamesSupportRustAndLegacyGoConfigs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	for _, input := range []string{`{"ui":{"ui_font_size":17}}`, `{"ui":{"font_size":17,"font_family":"monospace"}}`} {
		if err := os.WriteFile(path, []byte(input), 0600); err != nil {
			t.Fatal(err)
		}
		cfg, err := Load(path)
		if err != nil || cfg.UI.UIFontSize != 17 {
			t.Fatalf("UI font load: %v %#v", err, cfg.UI)
		}
		cfg.UI.UIFontSize = 20
		if err = Save(path, cfg); err != nil {
			t.Fatal(err)
		}
		loaded, err := Load(path)
		if err != nil || loaded.UI.UIFontSize != 20 {
			t.Fatal("saved font did not survive reload")
		}
		data, _ := os.ReadFile(path)
		var root map[string]any
		_ = json.Unmarshal(data, &root)
		if root["ui"].(map[string]any)["font_size"] != float64(20) {
			t.Fatal("Rust-compatible font key absent")
		}
	}
}
