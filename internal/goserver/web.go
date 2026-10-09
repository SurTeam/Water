package goserver

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/SurTeam/Water/internal/goconfig"
	"github.com/SurTeam/Water/internal/goprotocol"
	"github.com/coder/websocket"
	"github.com/google/uuid"
)

//go:embed webassets/*
var webAssets embed.FS

type webService struct {
	mu            sync.Mutex
	server        *Server
	cfg           goconfig.WebConfig
	publicOrigin  string
	origins       map[string]struct{}
	http          *http.Server
	listener      net.Listener
	state         string
	lastError     string
	auth          webAuthStore
	authPath      string
	invites       map[uuid.UUID]webInvite
	connections   map[*webConnection]string
	attempts      int
	attemptWindow time.Time
}

// net.Conn carries the existing length-prefixed protocol. Each binary WebSocket
// message is a stream chunk, not necessarily a complete Water frame.
type webConnection struct{ net.Conn }

func (c *webConnection) Write(p []byte) (int, error) {
	_ = c.Conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	return c.Conn.Write(p)
}

func newWebService(s *Server, cfg goconfig.WebConfig) *webService {
	return &webService{server: s, cfg: cfg.Normalized(), state: "stopped", invites: map[uuid.UUID]webInvite{}, connections: map[*webConnection]string{}}
}
func (w *webService) status() goprotocol.WebStatus {
	w.mu.Lock()
	defer w.mu.Unlock()
	address, _ := w.cfg.BindAddress()
	if w.listener != nil {
		address = w.listener.Addr().String()
	}
	return goprotocol.WebStatus{State: w.state, ListenAddress: address, PublicURL: w.originLocked(), Error: w.lastError}
}
func (w *webService) config() goconfig.WebConfig { w.mu.Lock(); defer w.mu.Unlock(); return w.cfg }
func (w *webService) configure(cfg goconfig.WebConfig) error {
	cfg = cfg.Normalized()
	if err := cfg.Validate(); err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.http != nil && (cfg.Origin() != w.cfg.Origin() || cfg.TLSCertFile != w.cfg.TLSCertFile || cfg.TLSKeyFile != w.cfg.TLSKeyFile) {
		return errors.New("stop the Web service before changing its configuration")
	}
	// ConfigPath is owned by the target server. A remote command never saves the
	// desktop's config or replaces unrelated settings from that server's file.
	if w.server.ConfigPath != "" {
		c, err := goconfig.Load(w.server.ConfigPath)
		if err != nil {
			return err
		}
		c.Web = cfg
		if err := goconfig.Save(w.server.ConfigPath, c); err != nil {
			return err
		}
	}
	w.cfg = cfg
	if w.http == nil {
		w.publicOrigin = ""
		w.origins = nil
	}
	return nil
}
func (w *webService) start() (err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	select {
	case <-w.server.closing:
		return errors.New("server is closed")
	default:
	}
	if w.http != nil {
		return nil
	}
	w.state = "starting"
	defer func() {
		if err != nil {
			w.state = "stopped"
			w.lastError = err.Error()
		}
	}()
	if err = w.cfg.Validate(); err != nil {
		return err
	}
	origin, origins, err := webAccessOrigins(w.cfg)
	if err != nil {
		return err
	}
	var tlsConfig *tls.Config
	if w.cfg.UsesTLS() {
		cert, err := tls.LoadX509KeyPair(w.cfg.TLSCertFile, w.cfg.TLSKeyFile)
		if err != nil {
			return fmt.Errorf("Web TLS: %w", err)
		}
		leaf, err := x509.ParseCertificate(cert.Certificate[0])
		if err != nil {
			return fmt.Errorf("Web certificate: %w", err)
		}
		public, _ := url.Parse(origin)
		if err := leaf.VerifyHostname(public.Hostname()); err != nil {
			return fmt.Errorf("Web certificate does not match HTTPS address: %w", err)
		}
		tlsConfig = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}
	}
	address, _ := w.cfg.BindAddress()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	network := "tcp"
	if ip := net.ParseIP(w.cfg.ListenAddress); ip != nil && ip.To4() != nil {
		network = "tcp4"
	}
	ln, err := (&net.ListenConfig{}).Listen(ctx, network, address)
	if err != nil {
		return fmt.Errorf("Web cannot bind %s; check that the Web hostname resolves to this server and the port is available: %w", address, err)
	}
	if err = w.loadAuthLocked(); err != nil {
		_ = ln.Close()
		return err
	}
	w.publicOrigin, w.origins = origin, origins
	if tlsConfig != nil {
		ln = tls.NewListener(ln, tlsConfig)
	}
	h := &http.Server{Handler: w.handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 * 1024}
	w.http = h
	w.listener = ln
	w.state = "running"
	w.lastError = ""
	w.server.webActive.Store(true)
	w.server.notifySessionsChanged()
	go func() {
		err := h.Serve(ln)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			w.mu.Lock()
			if w.http == h {
				w.lastError = err.Error()
			}
			w.mu.Unlock()
			w.stopInstance(h)
		}
	}()
	return nil
}
func (w *webService) stop() { w.stopInstance(nil) }
func (w *webService) stopInstance(expected *http.Server) {
	w.mu.Lock()
	h := w.http
	if h == nil || expected != nil && h != expected {
		w.mu.Unlock()
		return
	}
	w.state = "stopping"
	w.http = nil
	w.listener = nil
	w.invites = map[uuid.UUID]webInvite{}
	conns := make([]*webConnection, 0, len(w.connections))
	for c := range w.connections {
		conns = append(conns, c)
	}
	_ = h.Close()
	for _, c := range conns {
		_ = c.Close()
	}
	w.state = "stopped"
	w.server.webActive.Store(false)
	w.server.notifySessionsChanged()
	w.mu.Unlock()
}
func (w *webService) cookieName() string {
	prefix := "water-http-"
	if w.cfg.UsesTLS() {
		prefix = "__Host-water-"
	}
	return prefix + w.server.Build + "-" + w.auth.ID.String()
}
func (w *webService) sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	w.mu.Lock()
	expected := "http://" + r.Host
	if r.TLS != nil {
		expected = "https://" + r.Host
	}
	_, allowed := w.origins[origin]
	w.mu.Unlock()
	return allowed && origin == expected
}
func (w *webService) handler() http.Handler {
	assets, _ := fs.Sub(webAssets, "webassets")
	files := http.FileServer(http.FS(assets))
	mux := http.NewServeMux()
	mux.HandleFunc("/api/pair", w.pairHTTP)
	mux.HandleFunc("/api/session", w.sessionHTTP)
	mux.HandleFunc("/api/logout", w.logoutHTTP)
	mux.HandleFunc("/ws", w.websocketHTTP)
	mux.HandleFunc("/", func(rw http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(rw, "method not allowed", 405)
			return
		}
		if r.URL.Path != "/" && r.URL.Path != "/app.js" && r.URL.Path != "/style.css" && r.URL.Path != "/xterm.js" && r.URL.Path != "/xterm.css" && r.URL.Path != "/fit.js" && r.URL.Path != "/qr-worker.js" && r.URL.Path != "/jsqr.js" {
			http.NotFound(rw, r)
			return
		}
		files.ServeHTTP(rw, r)
	})
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
			rw.Header().Set("Strict-Transport-Security", "max-age=86400")
		}
		rw.Header().Set("Cache-Control", "no-store")
		rw.Header().Set("X-Content-Type-Options", "nosniff")
		rw.Header().Set("Referrer-Policy", "no-referrer")
		rw.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; worker-src 'self'; style-src 'self' 'unsafe-inline'; connect-src 'self'; font-src 'self'; img-src 'self' data: blob:; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		w.mu.Lock()
		_, allowed := w.origins[scheme+"://"+r.Host]
		w.mu.Unlock()
		if !allowed {
			http.Error(rw, "unexpected host", 421)
			return
		}
		mux.ServeHTTP(rw, r)
	})
}
func (w *webService) pairHTTP(rw http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(rw, "method not allowed", 405)
		return
	}
	if !w.sameOrigin(r) {
		http.Error(rw, "origin rejected", 403)
		return
	}
	w.mu.Lock()
	if time.Since(w.attemptWindow) > time.Minute {
		w.attemptWindow = time.Now()
		w.attempts = 0
	}
	w.attempts++
	limited := w.attempts > 60
	w.mu.Unlock()
	if limited {
		http.Error(rw, "pairing rate limit", 429)
		return
	}
	var p struct {
		Token string `json:"token"`
		Name  string `json:"name"`
	}
	r.Body = http.MaxBytesReader(rw, r.Body, 4096)
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		http.Error(rw, "invalid pairing request", 400)
		return
	}
	secret, err := w.exchangeInvite(p.Token, p.Name)
	if err != nil {
		http.Error(rw, err.Error(), 403)
		return
	}
	w.mu.Lock()
	name := w.cookieName()
	secure := w.cfg.UsesTLS()
	w.mu.Unlock()
	http.SetCookie(rw, &http.Cookie{Name: name, Value: secret, Path: "/", Secure: secure, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: int(webDeviceLifetime.Seconds())})
	rw.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(rw).Encode(map[string]bool{"paired": true})
}
func (w *webService) authenticate(r *http.Request) (webStoredDevice, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.http == nil {
		return webStoredDevice{}, false
	}
	cookie, err := r.Cookie(w.cookieName())
	if err != nil {
		return webStoredDevice{}, false
	}
	return w.deviceLocked(cookie.Value)
}
func (w *webService) sessionHTTP(rw http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(rw, "method not allowed", 405)
		return
	}
	device, ok := w.authenticate(r)
	if !ok {
		http.Error(rw, "pair this browser with the server", 401)
		return
	}
	rw.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(rw).Encode(map[string]any{"device": device.WebDevice, "server": w.server.serverInfo()})
}
func (w *webService) logoutHTTP(rw http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(rw, "method not allowed", 405)
		return
	}
	if !w.sameOrigin(r) {
		http.Error(rw, "origin rejected", 403)
		return
	}
	device, ok := w.authenticate(r)
	if !ok {
		http.Error(rw, "unauthorized", 401)
		return
	}
	if err := w.revoke(device.ID); err != nil {
		http.Error(rw, "could not revoke device", 500)
		return
	}
	w.mu.Lock()
	name := w.cookieName()
	secure := w.cfg.UsesTLS()
	w.mu.Unlock()
	http.SetCookie(rw, &http.Cookie{Name: name, Path: "/", Secure: secure, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	rw.WriteHeader(204)
}
func (w *webService) websocketHTTP(rw http.ResponseWriter, r *http.Request) {
	if !w.sameOrigin(r) {
		http.Error(rw, "origin rejected", 403)
		return
	}
	device, ok := w.authenticate(r)
	if !ok {
		http.Error(rw, "unauthorized", 401)
		return
	}
	// Serialize admission with stop/revoke; no untracked upgraded connections.
	w.mu.Lock()
	if w.http == nil || len(w.connections) >= 32 {
		w.mu.Unlock()
		http.Error(rw, "Web connection limit", 503)
		return
	}
	cookie, _ := r.Cookie(w.cookieName())
	if _, ok := w.deviceLocked(cookie.Value); !ok {
		w.mu.Unlock()
		http.Error(rw, "unauthorized", 401)
		return
	}
	ws, err := websocket.Accept(rw, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		w.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	c := &webConnection{Conn: websocket.NetConn(ctx, ws, websocket.MessageBinary)}
	ws.SetReadLimit(1024 * 1024)
	w.connections[c] = device.ID
	w.mu.Unlock()
	defer func() { cancel(); _ = c.Close(); w.mu.Lock(); delete(w.connections, c); w.mu.Unlock() }()
	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
	go func() {
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if !time.Now().Before(device.ExpiresAt) {
					_ = c.Close()
					return
				}
				pingCtx, done := context.WithTimeout(ctx, 5*time.Second)
				err := ws.Ping(pingCtx)
				done()
				if err != nil {
					_ = c.Close()
					return
				}
			}
		}
	}()
	w.server.handleConn(c)
}
func (w *webService) revoke(id string) error {
	if _, err := uuid.Parse(id); err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.loadAuthLocked(); err != nil {
		return err
	}
	previous := w.auth.Devices
	next := make([]webStoredDevice, 0, len(previous))
	for _, d := range previous {
		if d.ID != id {
			next = append(next, d)
		}
	}
	w.auth.Devices = next
	if err := w.saveAuthLocked(); err != nil {
		w.auth.Devices = previous
		return err
	}
	for c, device := range w.connections {
		if device == id {
			_ = c.Close()
		}
	}
	return nil
}
func (s *Server) webRequest(ss *session, msg goprotocol.WireMessage) error {
	var result any
	switch msg.Method {
	case "server.web.inspect":
		result = map[string]any{"status": s.web.status(), "config": s.web.config()}
	case "server.web.configure":
		var cfg goconfig.WebConfig
		var files struct {
			CertificatePEM []byte `json:"certificate_pem,omitempty"`
			PrivateKeyPEM  []byte `json:"private_key_pem,omitempty"`
		}
		if err := json.Unmarshal(msg.Params, &cfg); err != nil {
			return err
		}
		if err := json.Unmarshal(msg.Params, &files); err != nil {
			return err
		}
		if err := s.web.configureFiles(cfg, files.CertificatePEM, files.PrivateKeyPEM); err != nil {
			return err
		}
		result = struct {
			goprotocol.WebStatus
			Config goconfig.WebConfig `json:"config"`
		}{s.web.status(), s.web.config()}
	case "server.web.start":
		if err := s.web.start(); err != nil {
			return err
		}
		result = s.web.status()
	case "server.web.stop":
		s.web.stop()
		result = s.web.status()
	case "server.web.pair.create":
		p, err := s.web.createInvite()
		if err != nil {
			return err
		}
		result = p
	case "server.web.pair.status":
		var p struct {
			ID uuid.UUID `json:"id"`
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			return err
		}
		s.web.mu.Lock()
		inv, exists := s.web.invites[p.ID]
		pending := exists && time.Now().Before(inv.expires)
		s.web.mu.Unlock()
		result = map[string]bool{"pending": pending}
	case "server.web.pair.cancel":
		var p struct {
			ID uuid.UUID `json:"id"`
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			return err
		}
		s.web.mu.Lock()
		delete(s.web.invites, p.ID)
		s.web.mu.Unlock()
		result = map[string]bool{"cancelled": true}
	case "server.web.devices.list":
		s.web.mu.Lock()
		if err := s.web.loadAuthLocked(); err != nil {
			s.web.mu.Unlock()
			return err
		}
		list := make([]goprotocol.WebDevice, 0, len(s.web.auth.Devices))
		for _, d := range s.web.auth.Devices {
			if time.Now().Before(d.ExpiresAt) {
				list = append(list, d.WebDevice)
			}
		}
		s.web.mu.Unlock()
		result = list
	case "server.web.devices.revoke":
		var p struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			return err
		}
		if err := s.web.revoke(p.ID); err != nil {
			return err
		}
		result = map[string]bool{"revoked": true}
	default:
		return fmt.Errorf("unsupported Web operation %q", msg.Method)
	}
	return ss.write(goprotocol.Success(msg.RequestID, result))
}

// JSON replay uses decimal strings for sequence values because JavaScript
// numbers cannot represent all uint64 values. Live WT4 frames retain uint64.
func webAttachPayload(resp terminalAttachResponse) any {
	type event struct {
		goprotocol.WireTerminalEvent
		Seq string `json:"seq"`
	}
	replay := make([]event, 0, len(resp.Replay))
	for _, ev := range resp.Replay {
		replay = append(replay, event{ev, strconv.FormatUint(ev.Seq, 10)})
	}
	var first *string
	if resp.FirstSeq != nil {
		value := strconv.FormatUint(*resp.FirstSeq, 10)
		first = &value
	}
	return map[string]any{"terminal_id": resp.TerminalID, "first_seq": first, "last_seq": strconv.FormatUint(resp.LastSeq, 10), "size": resp.Size, "replay": replay}
}

func webAllowedMethod(method string) bool {
	switch method {
	case "ping", "server.inspect", "server.info", "session.open", "session.focus", "session.release", "state.dump", "event.list", "terminal.attach", "terminal.detach", "terminal.replay", "command.dispatch", "operation.get":
		return true
	}
	return false
}
func webAllowedCommand(raw json.RawMessage) bool {
	var p struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(raw, &p) != nil {
		return false
	}
	switch p.Type {
	case "workspace.new", "workspace.rename", "workspace.close", "workspace.delete", "tab.new", "tab.new_in_workspace", "tab.rename", "tab.close", "pane.split", "pane.close", "terminal.send_text", "terminal.send_bytes", "terminal.resize":
		return true
	}
	return false
}

// Keep the security boundary explicit instead of trusting role strings supplied
// by a browser. Internal commands and recovery never cross the Web adapter.
func validateWebRequest(msg goprotocol.WireMessage) error {
	if msg.OK != nil || !webAllowedMethod(msg.Method) {
		return errors.New("method unavailable to browser sessions")
	}
	if msg.Method == "command.dispatch" {
		var p struct {
			Command json.RawMessage `json:"command"`
		}
		if json.Unmarshal(msg.Params, &p) != nil || !webAllowedCommand(p.Command) {
			return errors.New("command unavailable to browser sessions")
		}
	}
	return nil
}
