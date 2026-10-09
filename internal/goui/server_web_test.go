package goui

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/SurTeam/Water/internal/goclient"
	"github.com/SurTeam/Water/internal/goconfig"
	"github.com/SurTeam/Water/internal/goprotocol"
	"github.com/SurTeam/Water/internal/goserver"
	"github.com/google/uuid"
)

func webPanelFixture(t *testing.T, remote string) (*WorkspaceClient, *goserver.Server, string) {
	t.Helper()
	configPath := filepath.Join(t.TempDir(), "config.json")
	socket := filepath.Join("/tmp", "water-web-panel-"+uuid.NewString()+".sock")
	cfg := goconfig.Default()
	if err := goconfig.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	s := goserver.NewWithConfig(socket, cfg)
	s.ConfigPath = configPath
	done := make(chan error, 1)
	go func() { done <- s.ListenAndServe() }()
	t.Cleanup(func() {
		_ = s.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(time.Second):
			t.Error("server cleanup timed out")
		}
	})
	client := goclient.New(socket)
	var session *goclient.Session
	deadline := time.Now().Add(2 * time.Second)
	for {
		var err error
		session, err = client.OpenSession()
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Cleanup(func() { _ = session.Close() })
	c := NewWorkspaceClientWithConnection(session, nil, cfg, remote)
	c.openSettings()
	c.settings.group = 5
	return c, s, configPath
}
func panelAction(t *testing.T, c *WorkspaceClient, action string) {
	t.Helper()
	c.layoutMu.Lock()
	c.serverAction(action)
	c.layoutMu.Unlock()
	deadline := time.Now().Add(2 * time.Second)
	for {
		state := c.ServerPanelState()
		if !state.Busy {
			if state.Message != "Server operation completed" {
				t.Fatal(state.Message)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("panel operation timed out: %s", action)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
func TestWebPanelConfiguresOwningRemoteServer(t *testing.T) {
	local, _, localPath := webPanelFixture(t, "")
	remote, _, remotePath := webPanelFixture(t, "ssh-alias-not-a-web-host")
	panelAction(t, remote, "web-open")
	panelAction(t, remote, "web-settings")
	remote.layoutMu.Lock()
	for i := range remote.settings.fields {
		f := &remote.settings.fields[i]
		if f.group != "Web" {
			continue
		}
		if f.name == "PublicURL" {
			t.Fatal("obsolete URL field is still visible")
		}
		switch f.name {
		case "ListenAddress":
			f.editor.SetText("remote.example.ts.net")
		case "ListenPort":
			f.editor.SetText("8443")
		case "TLS":
			f.toggle.Value = true
		case "TLSCertFile":
			f.editor.SetText("/remote/cert.pem")
		case "TLSKeyFile":
			f.editor.SetText("/remote/key.pem")
		}
	}
	remote.layoutMu.Unlock()
	panelAction(t, remote, "web-configure")
	cfg, err := goconfig.Load(remotePath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Web.Origin() != "https://remote.example.ts.net:8443" {
		t.Fatal("remote config did not persist")
	}
	untouched, err := goconfig.Load(localPath)
	if err != nil {
		t.Fatal(err)
	}
	if untouched.Web.PublicURL != "" || local.currentConfig().Web.PublicURL != "" || remote.currentConfig().Web.PublicURL != "" {
		t.Fatal("remote Web settings leaked into local GUI preferences")
	}
	original, _ := os.ReadFile(localPath)
	if bytes.Contains(original, []byte("remote.example")) {
		t.Fatal("remote endpoint written into local file")
	}
}
func TestWebPairingSecretsExcludedFromUISnapshotAndCancelledOnSwitch(t *testing.T) {
	first, _, _ := webPanelFixture(t, "")
	second, _, _ := webPanelFixture(t, "remote")
	secret := "PAIRING_SECRET_MUST_NOT_APPEAR_IN_SNAPSHOT"
	first.server.webVisible = true
	first.server.pairing = &goprotocol.WebPairing{ID: uuid.NewString(), URL: "https://example/#pair=" + secret, ExpiresAt: time.Now().Add(time.Minute)}
	raw, err := json.Marshal(first.ServerPanelState())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(secret)) {
		t.Fatal("pairing secret leaked into generic GUI snapshot")
	}
	m := NewMultiWorkspaceClient(nil)
	firstID, secondID := uuid.New(), uuid.New()
	if err := m.AddConnection(ConnectionEntry{ID: firstID, Kind: "local"}, first, nil, true); err != nil {
		t.Fatal(err)
	}
	if err := m.AddConnection(ConnectionEntry{ID: secondID, Kind: "remote"}, second, nil, false); err != nil {
		t.Fatal(err)
	}
	if !m.ActivateConnection(secondID) {
		t.Fatal("switch failed")
	}
	if first.ServerPanelState().PairingExpiresAt != nil {
		t.Fatal("old connection invitation remained visible")
	}
}
