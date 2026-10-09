package goconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfiguredLoadPathUsesEnvironment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "isolated config.json")
	if err := os.WriteFile(path, []byte(`{"web":{"listen_address":"127.0.0.2","listen_port":18080}}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WATER_CONFIG", path)
	if got := ConfiguredLoadPath("dev"); got != path {
		t.Fatalf("configured path=%q, want %q", got, path)
	}
	cfg, loadedPath, err := LoadDefault("dev")
	if err != nil {
		t.Fatal(err)
	}
	if loadedPath != path || cfg.Web.ListenAddress != "127.0.0.2" || cfg.Web.ListenPort != 18080 {
		t.Fatalf("wrong configuration loaded: path=%q web=%+v", loadedPath, cfg.Web)
	}
}

func TestConfiguredLoadPathFallsBackToVariantDefault(t *testing.T) {
	t.Setenv("WATER_CONFIG", "")
	for _, variant := range []string{"dev", "release"} {
		if got, want := ConfiguredLoadPath(variant), DefaultLoadPath(variant); got != want {
			t.Fatalf("%s path=%q, want %q", variant, got, want)
		}
	}
}
