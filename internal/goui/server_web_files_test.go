package goui

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/SurTeam/Water/internal/goconfig"
)

func TestSelectedLocalTLSFilesImportToOwningServer(t *testing.T) {
	for _, remote := range []string{"", "owned-ssh-server"} {
		t.Run(remote, func(t *testing.T) {
			c, _, configPath := webPanelFixture(t, remote)
			panelAction(t, c, "web-open")
			panelAction(t, c, "web-settings")
			pairDir := t.TempDir()
			key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			template := &x509.Certificate{SerialNumber: big.NewInt(123), NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), DNSNames: []string{"localhost"}}
			der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
			if err != nil {
				t.Fatal(err)
			}
			private, _ := x509.MarshalPKCS8PrivateKey(key)
			certPath, keyPath := filepath.Join(pairDir, "desktop-certificate.pem"), filepath.Join(pairDir, "desktop-key.pem")
			os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600)
			os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private}), 0600)
			c.server.chooseFile = func(title string) (string, error) {
				if title == "Choose PEM certificate / chain" {
					return certPath, nil
				}
				return keyPath, nil
			}
			panelAction(t, c, "web-select-cert")
			panelAction(t, c, "web-select-key")
			c.layoutMu.Lock()
			for i := range c.settings.fields {
				f := &c.settings.fields[i]
				if f.group == "Web" && f.name == "TLS" {
					f.toggle.Value = true
				}
			}
			c.layoutMu.Unlock()
			panelAction(t, c, "web-configure")
			saved, err := goconfig.Load(configPath)
			if err != nil {
				t.Fatal(err)
			}
			if saved.Web.TLSCertFile == certPath || saved.Web.TLSKeyFile == keyPath {
				t.Fatal("desktop path persisted on target")
			}
			contents, err := os.ReadFile(saved.Web.TLSKeyFile)
			original, _ := os.ReadFile(keyPath)
			if err != nil || !bytes.Equal(contents, original) {
				t.Fatal("selected key not imported")
			}
			if remote != "" && c.currentConfig().Web.TLS {
				t.Fatal("remote credentials leaked into local preferences")
			}
			if len(c.server.certificatePEM) != 0 || len(c.server.privateKeyPEM) != 0 {
				t.Fatal("applied secrets remained in draft")
			}
			// A cancelled picker preserves existing saved files and current draft.
			panelAction(t, c, "web-settings")
			c.server.chooseFile = func(string) (string, error) { return "", nil }
			panelAction(t, c, "web-select-key")
			again, _ := goconfig.Load(configPath)
			if again.Web != saved.Web {
				t.Fatal("cancel changed saved config")
			}
		})
	}
}

func TestSelectedTLSFilesRejectLegacyServerWithoutImport(t *testing.T) {
	c, _, path := webPanelFixture(t, "legacy-ssh")
	before, _ := os.ReadFile(path)
	c.server.info.Capabilities = []string{"web-access/v1"}
	c.server.certificatePEM = []byte("selected certificate")
	c.server.privateKeyPEM = []byte("selected key")
	c.layoutMu.Lock()
	c.webServerAction("web-configure")
	c.layoutMu.Unlock()
	if c.ServerPanelState().Message != "Upgrade this server to import local TLS files" {
		t.Fatal(c.ServerPanelState().Message)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("legacy target config changed")
	}
}

func TestTLSFileReaderRejectsWrongFormatsAndBounds(t *testing.T) {
	for _, data := range [][]byte{[]byte("not PEM"), pem.EncodeToMemory(&pem.Block{Type: "ENCRYPTED PRIVATE KEY", Bytes: []byte("encrypted")}), bytes.Repeat([]byte("x"), maxWebTLSFile+1)} {
		path := filepath.Join(t.TempDir(), "selected.pem")
		os.WriteFile(path, data, 0600)
		if _, err := readWebTLSFile(path, true); err == nil {
			t.Fatal("invalid private key file accepted")
		}
	}
}
