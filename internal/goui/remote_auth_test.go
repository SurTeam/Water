package goui

import (
	"github.com/SurTeam/Water/internal/goconfig"
	"github.com/google/uuid"
	"strings"
	"testing"
)

func TestDiagnosticLinesRetainsCompleteMultilineUnicode(t *testing.T) {
	value := "Warning: 密钥 failure\n" + strings.Repeat("long-unbroken-diagnostic", 30) + "\nlast detail"
	lines := diagnosticLines(value, func(s string) bool { return len([]rune(s)) <= 17 })
	if strings.Join(lines, "") != strings.ReplaceAll(value, "\n", "") {
		t.Fatal("diagnostic text was lost")
	}
	for _, line := range lines {
		if len([]rune(line)) > 17 {
			t.Fatal("line exceeds available width")
		}
	}
	if lines[len(lines)-1] != "last detail" {
		t.Fatal("final diagnostic omitted")
	}
}

func TestRemoteScrollAcceptsTrackpadDeltas(t *testing.T) {
	for _, delta := range []float64{.2, 1, 40} {
		if remoteScrollSteps(delta) <= 0 || remoteScrollSteps(-delta) >= 0 {
			t.Fatalf("delta %v ignored", delta)
		}
	}
}

func TestConnectSuccessKeepsWarningsOnActivatedView(t *testing.T) {
	manager := NewMultiWorkspaceClient(nil)
	defer manager.Close()
	source := NewWorkspaceClientWithConnection(nil, nil, goconfig.Default(), "")
	if err := manager.AddConnection(ConnectionEntry{ID: uuid.New(), Kind: "local"}, source, nil, true); err != nil {
		t.Fatal(err)
	}
	destination := NewWorkspaceClientWithConnection(nil, nil, goconfig.Default(), "host")
	remoteID := uuid.New()
	manager.SetInteractiveRemoteConnector(func(string) (ConnectionEntry, *WorkspaceClient, func(), error) {
		destination.ShowSSHWarnings("successful SSH warning")
		return ConnectionEntry{ID: remoteID, Kind: "remote", Destination: "host"}, destination, nil, nil
	})
	if err := manager.ConnectRemote("host"); err != nil {
		t.Fatal(err)
	}
	// connectRemote completes by hiding the originating view, after AddConnection
	// has activated the newly created view. Its warning modal must survive.
	source.connectionMu.Lock()
	source.remoteFormVisible = false
	source.connectionMu.Unlock()
	if manager.ActiveConnectionID() != remoteID {
		t.Fatal("new connection not activated")
	}
	destination.connectionMu.RLock()
	defer destination.connectionMu.RUnlock()
	if !destination.remoteFormVisible || destination.remoteError != "successful SSH warning" {
		t.Fatal("success completion hid SSH warnings")
	}
}
