package goserver

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/SurTeam/Water/internal/goconfig"
	"github.com/google/uuid"
)

func TestWebAuthorizationSurvivesChangedSocket(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	s, client, origin := webFixtureConfig(t, "127.0.0.1", configPath)
	webPair(t, s, client, origin)
	id := s.web.auth.ID
	authPath := s.web.authPath
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	cfg, err := goconfig.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	replacement := NewWithConfig(filepath.Join("/tmp", "water-auth-"+uuid.NewString()+".sock"), cfg)
	replacement.ConfigPath = configPath
	t.Cleanup(func() { _ = replacement.Close() })
	if err := replacement.web.start(); err != nil {
		t.Fatal(err)
	}
	response, err := client.Get(origin + "/api/session")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK || replacement.web.auth.ID != id || replacement.web.authPath != authPath {
		t.Fatalf("persistent pairing lost: status=%d id=%s path=%s", response.StatusCode, replacement.web.auth.ID, replacement.web.authPath)
	}
	other := NewWithConfig(s.SocketPath, cfg)
	other.ConfigPath = filepath.Join(filepath.Dir(configPath), "other-config.json")
	defer other.Close()
	other.web.mu.Lock()
	err = other.web.loadAuthLocked()
	other.web.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if other.web.auth.ID == id || len(other.web.auth.Devices) != 0 {
		t.Fatal("different config file shared browser authorization")
	}
}

func TestWebLegacyAuthorizationMigratesWithoutRepairing(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	s, client, origin := webFixtureConfig(t, "127.0.0.1", configPath)
	webPair(t, s, client, origin)
	id := s.web.auth.ID
	stable := filepath.Dir(s.web.authPath)
	legacy := filepath.Join(filepath.Dir(configPath), "web-"+s.Build+"-"+webDigest(s.SocketPath))
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(stable, legacy); err != nil {
		t.Fatal(err)
	}
	cfg, err := goconfig.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	replacement := NewWithConfig(s.SocketPath, cfg)
	replacement.ConfigPath = configPath
	defer replacement.Close()
	if err := replacement.web.start(); err != nil {
		t.Fatal(err)
	}
	response, err := client.Get(origin + "/api/session")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK || replacement.web.auth.ID != id {
		t.Fatal("legacy migration invalidated paired cookie")
	}
	if _, err := os.Stat(filepath.Join(stable, "devices.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("legacy directory remains: %v", err)
	}
}

func TestWebWildcardListenerUsesLocalOrigins(t *testing.T) {
	cfg := goconfig.WebConfig{ListenAddress: "0.0.0.0", ListenPort: 8080}
	origin, allowed, err := webAccessOrigins(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("wildcard access origin: %s", origin)
	if origin == "http://0.0.0.0:8080" {
		t.Fatal("wildcard advertised as browser address")
	}
	if _, ok := allowed["http://127.0.0.1:8080"]; !ok {
		t.Fatal("loopback access missing")
	}
	if _, ok := allowed["http://evil.example:8080"]; ok {
		t.Fatal("unowned Host allowed")
	}
}
