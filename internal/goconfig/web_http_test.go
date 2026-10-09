package goconfig

import (
	"path/filepath"
	"testing"
)

func TestWebHTTPAutostartWithoutCertificate(t *testing.T) {
	cfg := Default()
	cfg.Web = (WebConfig{Enabled: true, PublicURL: "http://192.168.15.177:60453"}).Normalized()
	if err := cfg.Web.Validate(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil || loaded.Web != cfg.Web {
		t.Fatalf("HTTP autostart load: config=%+v err=%v", loaded.Web, err)
	}
	cfg.Web.TLS = true
	if err := cfg.Web.Validate(); err == nil {
		t.Fatal("HTTPS accepted without certificate and key")
	}
}
