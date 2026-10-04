package goserver

import (
	"github.com/SurTeam/Water/internal/goclient"
	"github.com/google/uuid"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSocketDoesNotReplaceLiveOwner(t *testing.T) {
	path := filepath.Join("/tmp", "water-own-"+uuid.NewString()+".sock")
	t.Cleanup(func() { os.Remove(path + ".lock") })
	ln, cleanup, err := listenOwnedSocket(path)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	before, _ := os.Stat(path)
	if second, closeSecond, err := listenOwnedSocket(path); err == nil {
		second.Close()
		closeSecond()
		t.Fatal("second owner accepted")
	}
	after, err := os.Stat(path)
	if err != nil || !os.SameFile(before, after) {
		t.Fatal("live socket replaced")
	}
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	ln.Close()
}

func TestSocketRejectsExternalOwnerAndPreservesReplacement(t *testing.T) {
	path := filepath.Join("/tmp", "water-own-"+uuid.NewString()+".sock")
	t.Cleanup(func() { os.Remove(path + ".lock") })
	external, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	external.SetUnlinkOnClose(false)
	if _, _, err := listenOwnedSocket(path); err == nil {
		t.Fatal("external live owner replaced")
	}
	external.Close() // Leaves a stale socket, which the next owner may reclaim.
	_, cleanup, err := listenOwnedSocket(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	replacement, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Close()
	cleanup()
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("old cleanup removed replacement: %v", err)
	}
	conn.Close()
}

func multiwindowServer(t *testing.T) (*Server, *goclient.Client) {
	t.Helper()
	path := filepath.Join("/tmp", "water-multi-"+uuid.NewString()+".sock")
	srv := New(path)
	done := make(chan error, 1)
	go func() { done <- srv.ListenAndServe() }()
	t.Cleanup(func() {
		srv.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("server failed to stop")
		}
		os.Remove(path + ".lock")
	})
	client := goclient.New(path)
	deadline := time.Now().Add(time.Second)
	for {
		if err := client.CallTimeout("ping", map[string]any{}, nil, time.Second); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("server not ready")
		}
		time.Sleep(time.Millisecond)
	}
	return srv, client
}

func TestSharedSessionReleaseAndShutdownWithoutSocketPath(t *testing.T) {
	srv, client := multiwindowServer(t)
	a, err := client.OpenSession()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := client.OpenSession()
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if a.WindowID == uuid.Nil || a.WindowID == b.WindowID {
		t.Fatal("window identities are not unique")
	}
	if err := a.Call("session.release", map[string]any{"shutdown_if_last": true}, nil); err != nil {
		t.Fatal(err)
	}
	if err := b.Call("ping", map[string]any{}, nil); err != nil {
		t.Fatalf("closing first window killed shared server: %v", err)
	}
	if err := os.Remove(srv.SocketPath); err != nil {
		t.Fatal(err)
	}
	if err := b.Call("server.shutdown", map[string]any{}, nil); err != nil {
		t.Fatalf("persistent connection could not shut down server: %v", err)
	}
}

func TestFocusedWindowOwnsPTYResize(t *testing.T) {
	srv, client := multiwindowServer(t)
	a, err := client.OpenSession()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := client.OpenSession()
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	var spawned struct {
		TerminalID uuid.UUID `json:"terminal_id"`
	}
	if err := a.Dispatch(map[string]any{"type": "terminal.spawn", "program": "/bin/sh", "args": []string{"-c", "read line"}, "columns": 80, "lines": 24}, &spawned); err != nil {
		t.Fatal(err)
	}
	resize := func(s *goclient.Session, cols int) {
		t.Helper()
		if err := s.Dispatch(map[string]any{"type": "terminal.resize", "terminal_id": spawned.TerminalID, "columns": cols, "lines": 30}, nil); err != nil {
			t.Fatal(err)
		}
	}
	check := func(cols int) {
		t.Helper()
		term, _ := srv.registry.Get(spawned.TerminalID)
		if term.Size().Columns != cols {
			t.Fatalf("columns=%d want=%d", term.Size().Columns, cols)
		}
	}
	resize(a, 100)
	resize(b, 40)
	check(100)
	if err := b.Call("session.focus", map[string]any{"focused": true}, nil); err != nil {
		t.Fatal(err)
	}
	resize(b, 60)
	resize(a, 110)
	check(60)
}
