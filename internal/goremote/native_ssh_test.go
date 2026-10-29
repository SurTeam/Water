package goremote

import (
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func TestNativeSSHConfigScopesAndExpandsIdentities(t *testing.T) {
	directory := t.TempDir()
	config := filepath.Join(directory, "ssh-config")
	content := "Host other\n HostName wrong.example\nHost fixture\n HostName 127.0.0.1\n User remote-user\n Port 2222\n IdentityFile \"~/keys/%r private\"\n IdentitiesOnly yes\nHost *\n Port 22\n"
	if err := os.WriteFile(config, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WATER_SSH_CONFIG", config)
	c, err := loadNativeConfig("fixture")
	if err != nil {
		t.Fatal(err)
	}
	if c.host != "127.0.0.1" || c.user != "remote-user" || c.port != "2222" {
		t.Fatalf("incorrect scoped config: %#v", c)
	}
	if len(c.identities) != 1 || !strings.HasSuffix(c.identities[0], filepath.Join("keys", "remote-user private")) {
		t.Fatalf("identity expansion %q", c.identities)
	}
	explicit, err := loadNativeConfig("explicit@fixture")
	if err != nil || explicit.user != "explicit" {
		t.Fatalf("explicit login: %#v %v", explicit, err)
	}
}
func testHostKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	key, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	public, err := ssh.NewPublicKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return public
}
func TestNativeSSHHostKeysPinTypeAndRejectChangedKeys(t *testing.T) {
	file := filepath.Join(t.TempDir(), "known_hosts")
	key := testHostKey(t)
	if err := os.WriteFile(file, []byte(knownhosts.Line([]string{"127.0.0.1:22"}, key)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	c := &nativeSSHConfig{host: "127.0.0.1", port: "22", values: map[string]string{"userknownhostsfile": file, "stricthostkeychecking": "yes"}}
	callback, err := c.hostKeyCallback(nil)
	if err != nil {
		t.Fatal(err)
	}
	peer := &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 22}
	if err := callback("127.0.0.1:22", peer, key); err != nil {
		t.Fatal(err)
	}
	if err := callback("127.0.0.1:22", peer, testHostKey(t)); err == nil {
		t.Fatal("changed host key accepted")
	}
	algorithms := c.knownHostAlgorithms()
	if len(algorithms) != 1 || algorithms[0] != ssh.KeyAlgoED25519 {
		t.Fatalf("trusted host-key algorithm not preferred: %v", algorithms)
	}
}
func TestNativeSSHAcceptNewDoesNotAcceptChangedKey(t *testing.T) {
	file := filepath.Join(t.TempDir(), "known_hosts")
	c := &nativeSSHConfig{host: "127.0.0.1", port: "22", values: map[string]string{"userknownhostsfile": file, "stricthostkeychecking": "accept-new"}}
	callback, err := c.hostKeyCallback(nil)
	if err != nil {
		t.Fatal(err)
	}
	peer := &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 22}
	key := testHostKey(t)
	if err := callback("127.0.0.1:22", peer, key); err != nil {
		t.Fatal(err)
	}
	if err := callback("127.0.0.1:22", peer, key); err != nil {
		t.Fatal(err)
	}
	if err := callback("127.0.0.1:22", peer, testHostKey(t)); err == nil {
		t.Fatal("accept-new accepted changed host key")
	}
}
func TestPowerShellArgumentsRemainLiteral(t *testing.T) {
	if got := psQuote("C:\\my files\\it's.exe"); got != "'C:\\my files\\it''s.exe'" {
		t.Fatal(got)
	}
	for _, output := range []string{"windows AMD64", "Linux x86_64", "Darwin arm64"} {
		if _, err := parseTarget([]byte(output)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := parseTarget([]byte("windows amd64 injected")); err == nil {
		t.Fatal("unbounded platform response accepted")
	}
}
