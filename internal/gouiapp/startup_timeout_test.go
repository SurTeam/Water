package gouiapp

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/SurTeam/Water/internal/gobuild"
	"github.com/SurTeam/Water/internal/goclient"
	"github.com/SurTeam/Water/internal/goconfig"
	"github.com/SurTeam/Water/internal/goprotocol"
	"github.com/google/uuid"
)

// A failed first handshake does not mean a live socket belongs to an
// incompatible server. The second connection here is the listener probe;
// the third must be allowed to open the GUI without replacing the socket.
func TestConnectOrStartRetriesLiveServerAfterHandshakeFailure(t *testing.T) {
	path := filepath.Join("/tmp", "water-gui-retry-"+uuid.NewString()+".sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = ln.Close()
		_ = os.Remove(path)
	}()

	served := make(chan error, 1)
	go func() {
		for attempt := 0; attempt < 3; attempt++ {
			conn, err := ln.Accept()
			if err != nil {
				served <- err
				return
			}
			if attempt < 2 {
				_ = conn.Close()
				continue
			}
			_ = conn.SetDeadline(time.Now().Add(8 * time.Second))
			descriptor := goclient.Descriptor(gobuild.Variant)
			descriptor.Capabilities = goprotocol.ServerCapabilities()
			descriptor.RequiredCapabilities = []string{"terminal-stream/v1", "window-selection/v1"}
			info := goprotocol.ServerInfo{Descriptor: descriptor}
			for _, method := range []string{"server.inspect", "session.open"} {
				frame, err := goprotocol.ReadFrame(conn)
				if err != nil {
					_ = conn.Close()
					served <- err
					return
				}
				var request goprotocol.WireMessage
				if err := json.Unmarshal(frame.JSON, &request); err != nil {
					_ = conn.Close()
					served <- err
					return
				}
				if request.Method != method {
					_ = conn.Close()
					served <- fmt.Errorf("method %q, want %q", request.Method, method)
					return
				}
				var result any = info
				if method == "session.open" {
					result = map[string]any{
						"window_id":     uuid.NewString(),
						"server":        info,
						"compatibility": goprotocol.Assess(goclient.Descriptor(gobuild.Variant), descriptor),
						"diagnostic":    false,
					}
				}
				if err := goprotocol.WriteJSON(conn, goprotocol.Success(request.RequestID, result)); err != nil {
					_ = conn.Close()
					served <- err
					return
				}
			}
			served <- nil
			_, _ = io.Copy(io.Discard, conn)
			_ = conn.Close()
		}
	}()

	cfg := goconfig.Default()
	cfg.Server.AutoStart = false
	session, server, started, err := connectOrStart(path, "", cfg, gobuild.Variant)
	if err != nil {
		t.Fatalf("live server was rejected after an initial failed handshake: %v", err)
	}
	defer session.Close()
	if server != nil || started {
		t.Fatal("unexpected second server start for an owned socket")
	}
	if session.WindowID == uuid.Nil {
		t.Fatal("missing GUI window ID")
	}
	if stat, err := os.Lstat(path); err != nil || stat.Mode()&os.ModeSocket == 0 {
		t.Fatalf("live socket was replaced or removed: %v", err)
	}
	select {
	case err := <-served:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("mock server did not finish its handshake")
	}
}
