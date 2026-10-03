package goclient

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCallTimeoutBoundsUnresponsiveServer(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "water-rpc.")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "server.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	finish := make(chan struct{})
	defer close(finish)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		<-finish
	}()
	start := time.Now()
	err = New(path).CallTimeout("server.shutdown", nil, nil, 30*time.Millisecond)
	var timeout net.Error
	if !errors.As(err, &timeout) || !timeout.Timeout() {
		t.Fatalf("expected timeout, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("shutdown RPC took %v", elapsed)
	}
}
