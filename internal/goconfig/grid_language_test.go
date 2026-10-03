package goconfig

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestGridLanguageConfigMigrationAndSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"startup":{"window_width":1100,"window_height":760,"window_columns":103,"window_rows":37},"ui":{"language":"zh-CN"},"future":{"keep":true}}`), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Startup.WindowColumns != 103 || cfg.Startup.WindowRows != 37 || cfg.UI.Language != "zh-Hans" || cfg.Startup.WindowWidth != 0 {
		t.Fatalf("migration: %+v", cfg)
	}
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved map[string]json.RawMessage
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	var startup map[string]any
	if err := json.Unmarshal(saved["startup"], &startup); err != nil {
		t.Fatal(err)
	}
	if _, exists := startup["window_width"]; exists {
		t.Fatal("saved obsolete pixels")
	}
	if string(saved["future"]) == "" {
		t.Fatal("lost unknown setting")
	}
	reloaded, err := Load(path)
	if err != nil || !reflect.DeepEqual(reloaded.Startup, cfg.Startup) || reloaded.UI != cfg.UI {
		t.Fatalf("reload lost grid/locale: %v", err)
	}
}
