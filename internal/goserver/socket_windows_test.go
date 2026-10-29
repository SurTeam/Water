package goserver

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestWindowsOwnedSocketLifecycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.sock")
	listener, cleanup, err := listenOwnedSocket(path)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if other, release, err := listenOwnedSocket(path); err == nil {
		other.Close()
		release()
		t.Fatal("second listener acquired owned socket")
	}
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	listener.Close()
	cleanup()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("owned endpoint remains: %v", err)
	}
	// A cleanly stopped listener must be replaceable.
	_, cleanup2, err := listenOwnedSocket(path)
	if err != nil {
		t.Fatal(err)
	}
	cleanup2()
}

func TestWindowsOwnedSocketStaleAndForeignPaths(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	listener.SetUnlinkOnClose(false)
	listener.Close()
	_, cleanup, err := listenOwnedSocket(path)
	if err != nil {
		t.Fatalf("replace stale endpoint: %v", err)
	}
	cleanup()
	if err := os.WriteFile(path, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if other, release, err := listenOwnedSocket(path); err == nil {
		other.Close()
		release()
		t.Fatal("replaced regular file")
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "keep" {
		t.Fatalf("foreign path changed: %s: %v", data, err)
	}
}
