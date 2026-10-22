package goremote

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SurTeam/Water/internal/gobuild"
	"github.com/SurTeam/Water/internal/goprotocol"
	"github.com/google/uuid"
)

func TestRemoteSocketSurvivesProductUpgrade(t *testing.T) {
	t.Setenv("WATER_REMOTE_CONTROL_SOCKET", "")
	old := gobuild.Version
	defer func() { gobuild.Version = old }()
	before := remoteControlSocket("alice@host")
	gobuild.Version = "999.0.0"
	after := remoteControlSocket("alice@host")
	if before != after {
		t.Fatalf("product upgrade changed server endpoint: %s -> %s", before, after)
	}
	if strings.Contains(before, "-p") {
		t.Fatalf("endpoint is still protocol-scoped: %s", before)
	}
}
func TestLegacyDiscoveryCommandIsLoginShellPortable(t *testing.T) {
	// zsh aborts on an unmatched glob ("no matches found") where POSIX sh passes
	// the pattern through, so discovery must not depend on the login shell.
	destination := "alice@host-" + uuid.NewString()
	remote := legacyDiscoveryCommand(destination)
	if !strings.HasPrefix(remote, "sh -c '") {
		t.Fatalf("discovery command is not POSIX-shell wrapped: %s", remote)
	}
	for _, shell := range []string{"sh", "bash", "zsh"} {
		t.Run(shell, func(t *testing.T) {
			path, err := exec.LookPath(shell)
			if err != nil {
				t.Skipf("%s is not installed", shell)
			}
			out, err := exec.Command(path, "-c", remote).CombinedOutput()
			if err != nil {
				t.Fatalf("%s rejected discovery: %v: %s", shell, err, out)
			}
			if strings.TrimSpace(string(out)) != "" {
				t.Fatalf("unexpected discovery output %q", out)
			}
		})
	}
}

func TestLegacyDiscoveryFindsSocketThroughPosixShell(t *testing.T) {
	destination := "alice@host"
	socket := filepath.Join("/tmp", fmt.Sprintf("water-go-%s-p42-abcd-%016x.sock", buildIdentityToken(), stableID(destination)))
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Skipf("cannot create remote fixture socket: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	// Emulate a remote login shell running the requested command.
	script := filepath.Join(t.TempDir(), "ssh")
	content := "#!/bin/sh\nfor last; do :; done\nexec sh -c \"$last\"\n"
	if err := os.WriteFile(script, []byte(content), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WATER_SSH_PROGRAM", script)
	got, err := legacyRemoteSockets(destination, "owned-control")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != socket {
		t.Fatalf("unexpected paths %v, want [%s]", got, socket)
	}
}

func TestLegacyDiscoveryValidatesPaths(t *testing.T) {
	destination := "alice@host"
	valid := filepath.Join("/tmp", "water-go-"+buildIdentityToken()+"-p5-abcd-"+fmt.Sprintf("%016x", stableID(destination))+".sock")
	for _, tt := range []struct {
		name, output string
		ok           bool
	}{{"one old instance", valid + "\n", true}, {"foreign path", "/tmp/other.sock\n", false}, {"shell injection", strings.TrimSuffix(valid, ".sock") + ";touch-x.sock\n", false}} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			script := filepath.Join(dir, "ssh")
			if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s' \"$WATER_DISCOVERY_FIXTURE\"\n"), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("WATER_SSH_PROGRAM", script)
			t.Setenv("WATER_DISCOVERY_FIXTURE", tt.output)
			got, err := legacyRemoteSockets(destination, "owned-control")
			if (err == nil) != tt.ok {
				t.Fatalf("paths=%v err=%v", got, err)
			}
			if tt.ok && (len(got) != 1 || got[0] != valid) {
				t.Fatalf("unexpected paths %v", got)
			}
		})
	}
}

func TestSideEndpointIsSeparateAndStable(t *testing.T) {
	t.Setenv("WATER_REMOTE_CONTROL_SOCKET", "")
	destination := "alice@host"
	primary := remoteControlSocket(destination)
	side := sideRemoteSocket(destination)
	if side == primary {
		t.Fatalf("side endpoint reuses the stable endpoint: %s", side)
	}
	if side != sideRemoteSocket(destination) {
		t.Fatal("side endpoint is not stable across calls")
	}
	if !strings.Contains(side, fmt.Sprintf("p%d", goprotocol.ProtocolVersion)) {
		t.Fatalf("side endpoint is not protocol scoped: %s", side)
	}
	if len(side) > 100 {
		t.Fatalf("side endpoint exceeds the Unix socket path budget: %s", side)
	}
}

func TestRemoteSocketOccupiedDistinguishesMissingFromForeign(t *testing.T) {
	for _, tt := range []struct {
		name   string
		script string
		want   bool
		fails  bool
	}{
		{"owned endpoint", "#!/bin/sh\nexit 0\n", true, false},
		{"missing endpoint", "#!/bin/sh\nexit 1\n", false, false},
		{"unreachable host", "#!/bin/sh\nexit 255\n", false, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			script := filepath.Join(t.TempDir(), "ssh")
			if err := os.WriteFile(script, []byte(tt.script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("WATER_SSH_PROGRAM", script)
			occupied, err := remoteSocketOccupied("host", "control", "/tmp/water-go-dev-1-x.sock")
			if (err != nil) != tt.fails {
				t.Fatalf("err=%v occupied=%v", err, occupied)
			}
			if occupied != tt.want {
				t.Fatalf("occupied=%v want %v", occupied, tt.want)
			}
		})
	}
}
