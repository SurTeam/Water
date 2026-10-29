//go:build !windows

package goremote

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestUnixSSHForwardingFixture(t *testing.T) {
	server := os.Getenv("WATER_UNIX_SSH_NATIVE_SERVER")
	if server == "" {
		t.Skip("requires an explicitly built native server")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 fixture unavailable")
	}
	directory, err := os.MkdirTemp("/tmp", "water-old-ssh-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(directory)
	fixture, err := filepath.Abs("../../scripts/fixtures/server-ssh.py")
	if err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(directory, "ssh")
	if err := os.WriteFile(wrapper, []byte("#!/bin/sh\nexec "+shellQuote(python)+" "+shellQuote(fixture)+" \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(directory, "config.json")
	if err := os.WriteFile(config, []byte(`{"startup":{"initial_workspace":false},"web":{"enabled":false}}`), 0600); err != nil {
		t.Fatal(err)
	}
	program := filepath.Join(directory, "server")
	if err := os.WriteFile(program, []byte("#!/bin/sh\nexec "+shellQuote(server)+" --config "+shellQuote(config)+" \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WATER_SSH_TRANSPORT", "")
	t.Setenv("WATER_SSH_PROGRAM", wrapper)
	t.Setenv("WATER_SSH_FIXTURE_DIR", directory)
	t.Setenv("WATER_REMOTE_SERVER_COMMAND", program)
	t.Setenv("WATER_REMOTE_CONTROL_SOCKET", filepath.Join(directory, "r.sock"))
	tunnel, err := Connect("owned-unix-fixture")
	if err != nil {
		t.Fatal(err)
	}
	defer tunnel.Close()
	if tunnel.portable != nil {
		t.Fatal("Unix forwarding unexpectedly switched transport")
	}
	defer tunnel.Client().Call("server.shutdown", map[string]any{}, nil)
	if _, err := tunnel.Client().Inspect(3 * time.Second); err != nil {
		t.Fatal(err)
	}
	if err := tunnel.Client().Call("server.shutdown", map[string]any{}, nil); err != nil {
		t.Fatal(err)
	}
	if err := WaitForShutdown(tunnel.Destination(), tunnel.RemoteSocket(), 5*time.Second); err != nil {
		t.Fatal(err)
	}
}
