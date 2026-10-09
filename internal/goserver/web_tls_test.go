package goserver

import (
	"bytes"
	"crypto/tls"
	"os"
	"path/filepath"
	"testing"

	"github.com/SurTeam/Water/internal/goconfig"
)

func TestWebTLSImportValidatesBeforeSavingAndRestartsWithPair(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	server, client, origin := webFixtureConfig(t, "127.0.0.1", path)
	old := server.web.config()
	certificate, _ := os.ReadFile(old.TLSCertFile)
	key, _ := os.ReadFile(old.TLSKeyFile)
	server.web.stop()
	other, _, _ := webFixture(t)
	mismatchedKey, _ := os.ReadFile(other.web.config().TLSKeyFile)
	before, _ := os.ReadFile(path)
	for _, pair := range [][2][]byte{{certificate, nil}, {nil, key}, {certificate, mismatchedKey}, {certificate, certificate}, {bytes.Repeat([]byte("x"), maxWebTLSFile+1), key}} {
		if err := server.web.configureFiles(old, pair[0], pair[1]); err == nil {
			t.Fatal("invalid import accepted")
		}
		after, _ := os.ReadFile(path)
		if !bytes.Equal(before, after) {
			t.Fatal("invalid import changed saved config")
		}
	}
	old.TLSCertFile, old.TLSKeyFile = "/desktop/not-on-server.crt", "/desktop/not-on-server.key"
	if err := server.web.configureFiles(old, certificate, key); err != nil {
		t.Fatal(err)
	}
	saved, err := goconfig.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Web.TLSCertFile == old.TLSCertFile || saved.Web.TLSKeyFile == old.TLSKeyFile {
		t.Fatal("desktop paths persisted")
	}
	for _, name := range []string{saved.Web.TLSCertFile, saved.Web.TLSKeyFile} {
		info, err := os.Stat(name)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("TLS asset permissions: %v %v", info, err)
		}
	}
	if _, err = tls.LoadX509KeyPair(saved.Web.TLSCertFile, saved.Web.TLSKeyFile); err != nil {
		t.Fatal(err)
	}
	if err = server.web.start(); err != nil {
		t.Fatal(err)
	}
	response, err := client.Get(origin + "/api/session")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 401 {
		t.Fatalf("authenticated endpoint status=%d", response.StatusCode)
	}
}

func TestWebTLSImportCannotReplaceRunningCredentials(t *testing.T) {
	server, _, _ := webFixture(t)
	cfg := server.web.config()
	certificate, _ := os.ReadFile(cfg.TLSCertFile)
	key, _ := os.ReadFile(cfg.TLSKeyFile)
	if err := server.web.configureFiles(cfg, certificate, key); err == nil {
		t.Fatal("running credential replacement accepted")
	}
	entries, err := os.ReadDir(server.SocketPath + ".tls-" + server.Build)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatal("failed import left private pair directory")
	}
	if server.web.config() != cfg {
		t.Fatal("running configuration changed")
	}
}
