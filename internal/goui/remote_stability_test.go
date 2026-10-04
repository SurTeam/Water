package goui

import (
	"github.com/SurTeam/Water/internal/goclient"
	"github.com/SurTeam/Water/internal/goconfig"
	"github.com/SurTeam/Water/internal/goremote"
	"github.com/google/uuid"
	"os"
	"testing"
	"time"
)

// Opt-in loopback sshd integration; scripts/local-ssh-smoke.py owns the service.
func TestSSHManagedReconnectPreservesPaneAndConnectionIdentity(t *testing.T) {
	destination := os.Getenv("WATER_TEST_SSH_DESTINATION")
	if destination == "" {
		t.Skip("owned local sshd is not configured")
	}
	manager := NewMultiWorkspaceClient(nil)
	defer manager.Close()
	manager.SetRemoteConnector(func(destination string) (ConnectionEntry, *WorkspaceClient, func(), error) {
		tunnel, err := goremote.Connect(destination)
		if err != nil {
			return ConnectionEntry{}, nil, nil, err
		}
		session, err := tunnel.Client().OpenSession()
		if err != nil {
			_ = tunnel.Close()
			return ConnectionEntry{}, nil, nil, err
		}
		view := NewWorkspaceClientWithConnection(session, nil, goconfig.Default(), destination)
		return ConnectionEntry{ID: uuid.New(), Name: destination, Kind: "remote", Status: "connected", Destination: destination, SocketPath: tunnel.LocalSocket(), RemoteSocketPath: tunnel.RemoteSocket()}, view, func() { _ = session.Close(); _ = tunnel.Close() }, nil
	})
	if err := manager.ConnectRemote(destination); err != nil {
		t.Fatal(err)
	}
	if err := manager.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	manager.Run()
	id := manager.ActiveConnectionID()
	manager.mu.RLock()
	old := manager.connections[id]
	manager.mu.RUnlock()
	old.view.mu.RLock()
	before := old.view.state
	old.view.mu.RUnlock()
	if before.FocusedPane == nil {
		t.Fatal("remote has no pane")
	}
	old.view.layoutMu.Lock()
	old.view.openSettings()
	old.view.settings.fields[0].editor.SetText("pending edit")
	old.view.layoutMu.Unlock()
	// Simulate a failure state only. Do not touch the network or the SSH service.
	manager.markViewDisconnected(id, old.view)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		manager.mu.RLock()
		current := manager.connections[id]
		manager.mu.RUnlock()
		if current != old && current.entry.Status == "connected" {
			current.view.layoutMu.Lock()
			settingsPreserved := current.view.settings.visible && len(current.view.settings.fields) > 0 && current.view.settings.fields[0].editor.Text() == "pending edit"
			current.view.layoutMu.Unlock()
			if !settingsPreserved {
				time.Sleep(10 * time.Millisecond)
				continue
			}
			current.view.mu.RLock()
			after := current.view.state
			current.view.mu.RUnlock()
			if after.FocusedPane == nil || *after.FocusedPane != *before.FocusedPane {
				t.Fatal("reconnect lost pane identity")
			}
			if manager.ActiveConnectionID() != id || len(manager.ConnectionEntries()) != 1 {
				t.Fatal("reconnect changed connection selection")
			}
			if err := goclient.New(current.entry.SocketPath).CallTimeout("server.info", nil, nil, time.Second); err != nil {
				t.Fatal(err)
			}
			t.Log("real loopback SSH session reattached after simulated failure; connection and pane UUIDs preserved")
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("simulated disconnect did not reconnect")
}
