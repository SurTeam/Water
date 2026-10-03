package goconfig

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestTerminalGeometryMigrationUsesOneCanonicalSetting(t *testing.T) {
	for _, tc := range []struct {
		data          string
		columns, rows int
	}{
		{`{"terminal":{"default_columns":101,"default_lines":31}}`, 101, 31},
		{`{"terminal":{"default_columns":101,"default_lines":31},"startup":{"window_columns":107,"window_rows":33}}`, 107, 33},
	} {
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, []byte(tc.data), 0600); err != nil {
			t.Fatal(err)
		}
		cfg, err := Load(path)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Startup.WindowColumns != tc.columns || cfg.Startup.WindowRows != tc.rows || cfg.Terminal.DefaultColumns != tc.columns || cfg.Terminal.DefaultLines != tc.rows {
			t.Fatalf("geometry mismatch: %+v / %+v", cfg.Startup, cfg.Terminal)
		}
		if err := Save(path, cfg); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var saved struct {
			Terminal map[string]any `json:"terminal"`
		}
		if err := json.Unmarshal(data, &saved); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"default_columns", "default_lines"} {
			if _, exists := saved.Terminal[name]; exists {
				t.Fatalf("duplicate persisted: %s", name)
			}
		}
	}
}
