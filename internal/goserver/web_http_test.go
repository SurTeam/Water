package goserver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SurTeam/Water/internal/goconfig"
	"github.com/SurTeam/Water/internal/goprotocol"
)

func TestWebHTTPPairTerminalOriginAndLogout(t *testing.T) {
	dir := t.TempDir()
	reservation, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	origin := "http://" + reservation.Addr().String()
	_ = reservation.Close()
	cfg := goconfig.Default()
	cfg.Shell.Program, cfg.Shell.Args = "/bin/sh", []string{"-i"}
	// Existing certificate settings must not block an explicitly chosen HTTP URL.
	cfg.Web = goconfig.WebConfig{PublicURL: origin, TLSCertFile: "/nonexistent/cert", TLSKeyFile: "/nonexistent/key"}
	s := NewWithConfig(filepath.Join(dir, "control.sock"), cfg)
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Initialize(true, true); err != nil {
		t.Fatal(err)
	}
	if err := s.web.start(); err != nil {
		t.Fatal(err)
	}
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Timeout: 4 * time.Second}
	t.Cleanup(client.CloseIdleConnections)
	response, err := client.Get(origin + "/")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 200 || response.Header.Get("Strict-Transport-Security") != "" {
		t.Fatalf("HTTP response: status=%d headers=%v", response.StatusCode, response.Header)
	}
	pair, err := s.web.createInvite()
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(pair.URL)
	body := fmt.Sprintf(`{"token":%q,"name":"HTTP browser"}`, strings.TrimPrefix(u.Fragment, "pair="))
	req, _ := http.NewRequest("POST", origin+"/api/pair", strings.NewReader(body))
	req.Header.Set("Origin", "http://attacker.invalid")
	response, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 403 {
		t.Fatal("HTTP cross-origin pairing accepted")
	}
	req, _ = http.NewRequest("POST", origin+"/api/pair", strings.NewReader(body))
	req.Header.Set("Origin", origin)
	response, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	cookies := response.Cookies()
	if response.StatusCode != 200 || len(cookies) != 1 {
		t.Fatalf("pair response=%d cookies=%v", response.StatusCode, cookies)
	}
	cookie := cookies[0]
	if cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || strings.HasPrefix(cookie.Name, "__Host-") {
		t.Fatalf("invalid HTTP cookie flags: %v", cookie)
	}
	response, err = client.Get(origin + "/api/session")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal("HTTP authorization cookie not retained")
	}
	c := webSocket(t, client, origin)
	openWeb(t, s, c)
	blocked := webCall(t, s, c, 2, "server.web.start", nil)
	if blocked.OK == nil || *blocked.OK {
		t.Fatal("HTTP browser obtained server administration")
	}
	id := s.model.SortedTerminalIDs()[0]
	attached := webCall(t, s, c, 3, "terminal.attach", map[string]any{"terminal_id": id})
	if attached.OK == nil || !*attached.OK {
		t.Fatal(attached.Error)
	}
	params, _ := json.Marshal(map[string]any{"command": map[string]any{"type": "terminal.send_text", "terminal_id": id, "text": "printf 'HTTP_%s\\n' FLOW_OK\n"}})
	if err := goprotocol.WriteJSON(c, goprotocol.WireMessage{BuildVariant: s.Build, ProtocolVersion: goprotocol.ProtocolVersion, RequestID: 4, Method: "command.dispatch", Params: params}); err != nil {
		t.Fatal(err)
	}
	_ = c.SetReadDeadline(time.Now().Add(4 * time.Second))
	var output bytes.Buffer
	for !strings.Contains(output.String(), "HTTP_FLOW_OK") {
		frame, err := goprotocol.ReadFrame(c)
		if err != nil {
			t.Fatalf("HTTP terminal: %v output=%q", err, output.String())
		}
		if frame.Terminal != nil && frame.Terminal.Kind == goprotocol.OutputEvent {
			output.Write(frame.Terminal.Data)
		}
	}
	if code := webPost(t, client, origin, "/api/logout", nil); code != 204 {
		t.Fatalf("HTTP logout=%d", code)
	}
	response, err = client.Get(origin + "/api/session")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.StatusCode != 401 {
		t.Fatal("HTTP session survived logout")
	}
	parsed, _ := url.Parse(origin)
	if len(jar.Cookies(parsed)) != 0 {
		t.Fatal("HTTP logout cookie was not cleared")
	}
}
