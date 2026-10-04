package goconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCursorSettingsPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"terminal":{"font_size":18}}`), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Terminal.CursorStyle != "block" || !cfg.Terminal.CursorBlink {
		t.Fatal("legacy config lost cursor defaults")
	}
	for _, style := range []string{"block", "bar", "underline"} {
		for _, blink := range []bool{true, false} {
			cfg.Terminal.CursorStyle, cfg.Terminal.CursorBlink = style, blink
			if err := Save(path, cfg); err != nil {
				t.Fatal(err)
			}
			next, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			if next.Terminal.CursorStyle != style || next.Terminal.CursorBlink != blink {
				t.Fatalf("round trip: %+v", next.Terminal)
			}
		}
	}
	cfg.Terminal.CursorStyle = "invalid"
	if cfg.Normalized().Terminal.CursorStyle != "block" {
		t.Fatal("invalid cursor style not normalized")
	}
}
