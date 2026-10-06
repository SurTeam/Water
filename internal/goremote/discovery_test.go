package goremote

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SurTeam/Water/internal/gobuild"
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
