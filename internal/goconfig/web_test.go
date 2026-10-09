package goconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWebConfigurationOverrideValidationAndSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	data := `{"future":true,"web":{"listen_address":"127.0.0.1:8443"},"overrides":{"web":{"enabled":false,"listen_address":"100.64.0.1:8443","public_url":" https://water.example.ts.net:8443/ ","tls_cert_file":"/cert.pem","tls_key_file":"/key.pem","future_web":42}}}`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Web.ListenAddress != "water.example.ts.net" || cfg.Web.ListenPort != 8443 || !cfg.Web.TLS || cfg.Web.PublicURL != "" {
		t.Fatalf("legacy migration: %+v", cfg.Web)
	}
	cfg.Web.Enabled = true
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil || loaded.Web != cfg.Web {
		t.Fatalf("save/load: %+v err=%v", loaded.Web, err)
	}
	saved, _ := os.ReadFile(path)
	if strings.Contains(string(saved), `"public_url"`) || !strings.Contains(string(saved), `"listen_port": 8443`) || !strings.Contains(string(saved), `"future_web": 42`) {
		t.Fatalf("unexpected saved config: %s", saved)
	}
	address, err := loaded.Web.BindAddress()
	if err != nil || address != "water.example.ts.net:8443" {
		t.Fatalf("binding=%q err=%v", address, err)
	}
	for _, origin := range []string{"ftp://example.com", "https://user:secret@example.com", "https://example.com/path", "https://example.com#token", "https://example.com:0", "https://example.com:65536", "https://example.com:", "https://example.com:abc", "https://example.com?", "https://239.1.2.3:8443"} {
		bad := WebConfig{PublicURL: origin, TLSCertFile: "/cert", TLSKeyFile: "/key"}
		if bad.Validate() == nil {
			t.Fatalf("invalid legacy origin accepted: %s", origin)
		}
	}
}

func TestWebHTTPSOriginMatchesBrowserNormalization(t *testing.T) {
	for _, test := range []struct{ input, origin string }{
		{" http://Water.Example.ts.net:80/ ", "http://water.example.ts.net"},
		{"http://192.168.15.177:060453", "http://192.168.15.177:60453"},
		{" https://Water.Example.ts.net:443/ ", "https://water.example.ts.net"},
		{"https://Water.Example.ts.net:08443", "https://water.example.ts.net:8443"},
		{"https://[FD7A:115C:A1E0:0:0:0:0:2]:443", "https://[fd7a:115c:a1e0::2]"},
	} {
		cfg := (WebConfig{PublicURL: test.input}).Normalized()
		if cfg.Origin() != test.origin || cfg.PublicURL != "" {
			t.Fatalf("input=%q cfg=%+v actual=%s", test.input, cfg, cfg.Origin())
		}
	}
}

func TestWebSeparateListenerValidation(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "0.0.0.0", "::", "::1", "water.example.ts.net"} {
		cfg := WebConfig{ListenAddress: host, ListenPort: 8080}
		if err := cfg.Validate(); err != nil {
			t.Fatalf("%s: %v", host, err)
		}
	}
	for _, port := range []int{-1, 0, 65536} {
		if (WebConfig{ListenAddress: "127.0.0.1", ListenPort: port}).Validate() == nil {
			t.Fatalf("invalid port accepted: %d", port)
		}
	}
	for _, host := range []string{"http://127.0.0.1", "127.0.0.1:8080", "user@host", "host/path", "239.1.2.3"} {
		if (WebConfig{ListenAddress: host, ListenPort: 8080}).Validate() == nil {
			t.Fatalf("invalid host accepted: %s", host)
		}
	}
	cfg := (WebConfig{}).Normalized()
	if cfg.ListenAddress != "127.0.0.1" || cfg.ListenPort != 8080 || cfg.TLS {
		t.Fatalf("defaults=%+v", cfg)
	}
}
