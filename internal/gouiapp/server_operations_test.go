package gouiapp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/SurTeam/Water/internal/gobuild"
	"github.com/SurTeam/Water/internal/goprotocol"
)

func TestSiblingResolutionSkipsStaleServerBeforeRestart(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	preferred := filepath.Join(dir, "water-srv-dev")
	if err := os.WriteFile(preferred, []byte("#!/bin/sh\necho 'old server has no descriptor' >&2\nexit 2\n"), 0700); err != nil {
		t.Fatal(err)
	}
	descriptor := goprotocol.Descriptor{BuildVariant: "dev", ServerRevision: gobuild.ServerRevision, ProtocolVersion: goprotocol.ProtocolVersion, APISignature: goprotocol.APISignature, Capabilities: goprotocol.ServerCapabilities()}
	raw, _ := json.Marshal(descriptor)
	current := filepath.Join(dir, "water-server")
	if err := os.WriteFile(current, []byte("#!/bin/sh\nprintf '%s\\n' '"+string(raw)+"'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	got, err := resolveSiblingServer(filepath.Join(dir, "water-test-gui"), "dev")
	if err != nil || got != current {
		t.Fatalf("matching sibling was not selected: %s err=%v", got, err)
	}
	descriptor.BuildVariant = "release"
	raw, _ = json.Marshal(descriptor)
	if err := os.WriteFile(current, []byte("#!/bin/sh\nprintf '%s\\n' '"+string(raw)+"'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveSiblingServer(filepath.Join(dir, "water-test-gui"), "dev"); err == nil {
		t.Fatal("release server accepted for dev GUI")
	}
}
