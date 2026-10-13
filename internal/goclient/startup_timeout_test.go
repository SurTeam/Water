package goclient

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
	"github.com/SurTeam/Water/internal/goprotocol"
	"github.com/google/uuid"
)

// A normal server can be temporarily slow while the GUI is opening.
// Regression: the former one-second inspect deadline closed the session,
// and the subsequent server.info fallback could not revive that socket.
func TestOpenSessionAcceptsDelayedInspection(t *testing.T) {
	path := filepath.Join("/tmp", "water-inspect-"+uuid.NewString()+".sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = ln.Close()
		_ = os.Remove(path)
	}()

	finished := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			finished <- err
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(8 * time.Second))
		descriptor := Descriptor(gobuild.Variant)
		descriptor.Capabilities = goprotocol.ServerCapabilities()
		descriptor.RequiredCapabilities = []string{"terminal-stream/v1", "window-selection/v1"}
		info := goprotocol.ServerInfo{Descriptor: descriptor}
		for _, method := range []string{"server.inspect", "session.open"} {
			frame, readErr := goprotocol.ReadFrame(conn)
			if readErr != nil {
				finished <- readErr
				return
			}
			var request goprotocol.WireMessage
			if err := json.Unmarshal(frame.JSON, &request); err != nil {
				finished <- err
				return
			}
			if request.Method != method {
				finished <- fmt.Errorf("got method %q, want %q", request.Method, method)
				return
			}
			if method == "server.inspect" {
				time.Sleep(1200 * time.Millisecond)
				err = goprotocol.WriteJSON(conn, goprotocol.Success(request.RequestID, info))
			} else {
				err = goprotocol.WriteJSON(conn, goprotocol.Success(request.RequestID, map[string]any{
					"window_id":     uuid.NewString(),
					"server":        info,
					"compatibility": goprotocol.Assess(Descriptor(gobuild.Variant), info.Descriptor),
					"diagnostic":    false,
				}))
			}
			if err != nil {
				finished <- err
				return
			}
		}
		finished <- nil
		_, _ = io.Copy(io.Discard, conn)
	}()

	session, err := New(path).OpenSession()
	if err != nil {
		t.Fatalf("delayed but healthy server rejected: %v", err)
	}
	defer session.Close()
	if session.WindowID == uuid.Nil {
		t.Fatal("missing GUI window ID")
	}
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("mock server did not finish the handshake")
	}
}
