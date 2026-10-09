package goserver

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SurTeam/Water/internal/goconfig"
	"github.com/SurTeam/Water/internal/goprotocol"
	"github.com/coder/websocket"
	"github.com/google/uuid"
)

func webFixture(t *testing.T) (*Server, *http.Client, string) {
	return webFixtureHost(t, "127.0.0.1")
}

func webFixtureHost(t *testing.T, host string) (*Server, *http.Client, string) {
	return webFixtureConfig(t, host, "")
}
func webFixtureConfig(t *testing.T, host, configPath string) (*Server, *http.Client, string) {
	t.Helper()
	dir := t.TempDir()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Water test"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("192.0.2.1")}, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	private, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certFile, keyFile := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private}), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := goconfig.Default()
	cfg.Shell.Program = "/bin/sh"
	cfg.Shell.Args = []string{"-i"}
	reservation, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		t.Fatal(err)
	}
	port := reservation.Addr().(*net.TCPAddr).Port
	_ = reservation.Close()
	origin := "https://" + net.JoinHostPort(host, fmt.Sprint(port))
	cfg.Web = goconfig.WebConfig{PublicURL: origin, TLSCertFile: certFile, TLSKeyFile: keyFile}
	s := NewWithConfig(filepath.Join(dir, "control.sock"), cfg)
	s.ConfigPath = configPath
	if configPath != "" {
		if err := goconfig.Save(configPath, s.Config); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.web.start(); err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}, Jar: jar, Timeout: 4 * time.Second}
	t.Cleanup(func() { _ = s.Close(); client.CloseIdleConnections() })
	return s, client, origin
}
func webPost(t *testing.T, client *http.Client, origin, path string, p any) int {
	t.Helper()
	data, _ := json.Marshal(p)
	req, _ := http.NewRequest("POST", origin+path, bytes.NewReader(data))
	req.Header.Set("Origin", origin)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}
func webPair(t *testing.T, s *Server, client *http.Client, origin string) {
	t.Helper()
	p, err := s.web.createInvite()
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(p.URL)
	token := strings.TrimPrefix(u.Fragment, "pair=")
	if code := webPost(t, client, origin, "/api/pair", map[string]string{"token": token, "name": "Test browser"}); code != 200 {
		t.Fatalf("pair status %d", code)
	}
}
func webSocket(t *testing.T, client *http.Client, origin string) net.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	headers := http.Header{"Origin": []string{origin}}
	ws, _, err := websocket.Dial(ctx, strings.Replace(origin, "http", "ws", 1)+"/ws", &websocket.DialOptions{HTTPClient: client, HTTPHeader: headers})
	if err != nil {
		t.Fatal(err)
	}
	c := websocket.NetConn(context.Background(), ws, websocket.MessageBinary)
	t.Cleanup(func() { _ = c.Close() })
	return c
}
func webCall(t *testing.T, s *Server, c net.Conn, id uint64, method string, p any) goprotocol.WireMessage {
	t.Helper()
	_ = c.SetDeadline(time.Now().Add(4 * time.Second))
	params, _ := json.Marshal(p)
	if err := goprotocol.WriteJSON(c, goprotocol.WireMessage{BuildVariant: s.Build, ProtocolVersion: goprotocol.ProtocolVersion, RequestID: id, Method: method, Params: params}); err != nil {
		t.Fatal(err)
	}
	for {
		f, err := goprotocol.ReadFrame(c)
		if err != nil {
			t.Fatal(err)
		}
		if f.Terminal != nil {
			continue
		}
		var msg goprotocol.WireMessage
		if err := json.Unmarshal(f.JSON, &msg); err != nil {
			t.Fatal(err)
		}
		if msg.RequestID == id {
			return msg
		}
	}
}
func openWeb(t *testing.T, s *Server, c net.Conn) {
	t.Helper()
	descriptor := goprotocol.Descriptor{BuildVariant: s.Build, ProtocolVersion: goprotocol.ProtocolVersion, APISignature: goprotocol.APISignature, Capabilities: []string{"terminal-stream/v1", "window-selection/v1"}}
	msg := webCall(t, s, c, 1, "session.open", map[string]any{"role": "gui", "client": descriptor})
	if msg.OK == nil || !*msg.OK {
		t.Fatalf("open: %s", msg.Error)
	}
}

func TestWebPairingSecurityAndAtomicConsumption(t *testing.T) {
	s, client, origin := webFixture(t)
	response, err := client.Get(origin + "/api/session")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 401 {
		t.Fatal("anonymous session accepted")
	}
	p, err := s.web.createInvite()
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(p.URL)
	token := strings.TrimPrefix(u.Fragment, "pair=")
	// A pairing URL carries the token only in the fragment.
	if u.RawQuery != "" || u.Fragment == "" {
		t.Fatal("invitation exposed in URL query")
	}
	data, _ := json.Marshal(map[string]string{"token": token})
	req, _ := http.NewRequest("POST", origin+"/api/pair", bytes.NewReader(data))
	req.Header.Set("Origin", "https://attacker.invalid")
	response, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 403 {
		t.Fatal("cross-origin pairing accepted")
	}
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := s.web.exchangeInvite(token, "Concurrent"); results <- err }()
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("invitation consumed %d times", successes)
	}
	p, err = s.web.createInvite()
	if err != nil {
		t.Fatal(err)
	}
	u, _ = url.Parse(p.URL)
	s.web.mu.Lock()
	inv := s.web.invites[uuid.MustParse(p.ID)]
	inv.expires = time.Now().Add(-time.Second)
	s.web.invites[inv.id] = inv
	s.web.mu.Unlock()
	if _, err = s.web.exchangeInvite(strings.TrimPrefix(u.Fragment, "pair="), "Expired"); err == nil {
		t.Fatal("expired invitation accepted")
	}
	webPair(t, s, client, origin)
	response, err = client.Get(origin + "/api/session")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("authenticated session %d", response.StatusCode)
	}
	// Persistent authorization contains digests, not invitation or cookie secrets.
	store, err := os.ReadFile(s.web.authPath)
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(origin)
	for _, cookie := range client.Jar.Cookies(parsed) {
		if bytes.Contains(store, []byte(cookie.Value)) {
			t.Fatal("plaintext cookie stored")
		}
	}
	if bytes.Contains(store, []byte(token)) {
		t.Fatal("plaintext invitation stored")
	}
}

func TestWebTerminalRoundTripPermissionsAndLifecycle(t *testing.T) {
	s, client, origin := webFixture(t)
	if err := s.Initialize(true, true); err != nil {
		t.Fatal(err)
	}
	webPair(t, s, client, origin)
	c := webSocket(t, client, origin)
	beforeOpen := webCall(t, s, c, 2, "command.dispatch", map[string]any{"command": map[string]any{"type": "workspace.new"}})
	if beforeOpen.OK == nil || *beforeOpen.OK || beforeOpen.Error.Code != "FORBIDDEN" {
		t.Fatal("browser command accepted before session.open")
	}
	openWeb(t, s, c)
	for i, method := range []string{"server.shutdown", "server.web.pair.create", "server.web.stop", "recovery.prepare", "ui.keystroke"} {
		msg := webCall(t, s, c, uint64(i+10), method, map[string]any{})
		if msg.OK == nil || *msg.OK || msg.Error.Code != "FORBIDDEN" {
			t.Fatalf("browser management allowed: %s", method)
		}
	}
	msg := webCall(t, s, c, 20, "command.dispatch", map[string]any{"command": map[string]any{"type": "internal.terminal.foreground"}})
	if msg.OK == nil || *msg.OK {
		t.Fatal("internal command accepted")
	}
	id := s.model.SortedTerminalIDs()[0]
	msg = webCall(t, s, c, 21, "terminal.attach", map[string]any{"terminal_id": id})
	if msg.OK == nil || !*msg.OK {
		t.Fatal(msg.Error)
	}
	params, _ := json.Marshal(map[string]any{"command": map[string]any{"type": "terminal.send_text", "terminal_id": id, "text": "printf 'WEB_%s\\n' ROUNDTRIP\n"}})
	if err := goprotocol.WriteJSON(c, goprotocol.WireMessage{BuildVariant: s.Build, ProtocolVersion: goprotocol.ProtocolVersion, RequestID: 22, Method: "command.dispatch", Params: params}); err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	deadline := time.Now().Add(4 * time.Second)
	_ = c.SetReadDeadline(deadline)
	for !strings.Contains(output.String(), "WEB_ROUNDTRIP") {
		frame, err := goprotocol.ReadFrame(c)
		if err != nil {
			t.Fatalf("terminal output: %v; %q", err, output.String())
		}
		if frame.Terminal != nil && frame.Terminal.Kind == goprotocol.OutputEvent {
			output.Write(frame.Terminal.Data)
		}
	}
	// Even after the browser disconnects, enabled Web access keeps an embedded
	// server available for the next direct connection.
	_ = c.Close()
	for {
		s.sessionsMu.RLock()
		remaining := len(s.sessions)
		s.sessionsMu.RUnlock()
		if remaining == 0 {
			break
		}
		select {
		case <-s.sessionsChanged:
		case <-time.After(time.Second):
			t.Fatal("browser session did not close")
		}
	}
	gui := &session{id: uuid.New()}
	if !s.addSession(gui) {
		t.Fatal("GUI rejected")
	}
	if s.releaseSession(gui, true) {
		t.Fatal("last GUI release closed Web server")
	}
	waitDone := make(chan struct{})
	go func() { s.WaitForGUIRelease(); close(waitDone) }()
	select {
	case <-waitDone:
		t.Fatal("Web listener did not retain embedded server")
	case <-time.After(20 * time.Millisecond):
	}
	s.web.stop()
	select {
	case <-waitDone:
	case <-time.After(time.Second):
		t.Fatal("stopped Web listener retained server")
	}
	if s.registry.Count() != 1 {
		t.Fatal("Stop Web killed PTY")
	}
}

func TestWebRevocationAndAuthorizationRestart(t *testing.T) {
	s, client, origin := webFixture(t)
	webPair(t, s, client, origin)
	p, err := s.web.createInvite()
	if err != nil {
		t.Fatal(err)
	}
	c := webSocket(t, client, origin)
	openWeb(t, s, c)
	s.web.mu.Lock()
	device := s.web.auth.Devices[0]
	s.web.mu.Unlock()
	if err := s.web.revoke(device.ID); err != nil {
		t.Fatal(err)
	}
	_ = c.SetReadDeadline(time.Now().Add(time.Second))
	for {
		_, err := goprotocol.ReadFrame(c)
		if err != nil {
			break
		}
	}
	response, err := client.Get(origin + "/api/session")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 401 {
		t.Fatal("revoked cookie accepted")
	}
	webPair(t, s, client, origin)
	cfg := s.web.config()
	s.web.stop()
	replacement := NewWithConfig(s.SocketPath, s.Config)
	defer replacement.Close()
	replacement.web.cfg = cfg
	replacement.web.mu.Lock()
	err = replacement.web.loadAuthLocked()
	replacement.web.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(origin)
	cookies := client.Jar.Cookies(parsed)
	replacement.web.mu.Lock()
	_, ok := replacement.web.deviceLocked(cookies[0].Value)
	replacement.web.mu.Unlock()
	if !ok {
		t.Fatal("device authorization lost across restart")
	}
	u, _ := url.Parse(p.URL)
	if _, err := replacement.web.exchangeInvite(strings.TrimPrefix(u.Fragment, "pair="), "Old invitation"); err == nil {
		t.Fatal("invitation survived restart")
	}
	// Different build namespaces never share the same authorization identity.
	other := NewWithConfig(s.SocketPath, s.Config)
	other.Build = "release"
	defer other.Close()
	other.web.mu.Lock()
	err = other.web.loadAuthLocked()
	other.web.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if other.web.auth.ID == replacement.web.auth.ID {
		t.Fatal("dev/release share authorization")
	}
}

func TestWebSingleHTTPSHostnameAndStartupErrors(t *testing.T) {
	s, client, origin := webFixtureHost(t, "localhost")
	if s.web.config().Origin() != origin {
		t.Fatal("HTTPS address changed during startup")
	}
	pair, err := s.web.createInvite()
	if err != nil || !strings.HasPrefix(pair.URL, origin+"/#pair=") {
		t.Fatalf("pair=%+v err=%v", pair, err)
	}
	response, err := client.Get(origin + "/")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("hostname HTTPS status=%d", response.StatusCode)
	}
	actual := s.web.status().ListenAddress
	s.web.stop()
	occupied, err := net.Listen("tcp", actual)
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	if err := s.web.start(); err == nil || !strings.Contains(err.Error(), "port is available") {
		t.Fatalf("port conflict=%v", err)
	}
	if status := s.web.status(); status.State != "stopped" || status.Error == "" {
		t.Fatalf("failed start status=%+v", status)
	}
	_ = occupied.Close()
	cfg := s.web.config()
	// A valid certificate for a non-local address must not cause a fallback
	// to loopback or a wildcard interface. 192.0.2.1 is TEST-NET-1.
	cfg.ListenAddress = "192.0.2.1"
	if err := s.web.configure(cfg); err != nil {
		t.Fatal(err)
	}
	if err := s.web.start(); err == nil || !strings.Contains(err.Error(), "resolves to this server") {
		t.Fatalf("non-local binding=%v", err)
	}
	if s.web.status().State != "stopped" {
		t.Fatal("non-local address silently started on another interface")
	}
	cfg.ListenAddress = "127.0.0.2"
	if err := s.web.configure(cfg); err != nil {
		t.Fatal(err)
	}
	if err := s.web.start(); err == nil || !strings.Contains(err.Error(), "does not match HTTPS address") {
		t.Fatalf("certificate mismatch=%v", err)
	}
}
