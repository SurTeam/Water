package goui

import (
	"errors"
	"testing"

	"github.com/SurTeam/Water/internal/goclient"
	"github.com/SurTeam/Water/internal/goconfig"
	"github.com/SurTeam/Water/internal/goprotocol"
	"github.com/google/uuid"
)

func TestRestartOutdatedLocalServersAfterUpdate(t *testing.T) {
	manager := NewMultiWorkspaceClient(nil)
	defer manager.Close()

	localID := uuid.MustParse("00000000-0000-0000-0000-000000000011")
	remoteID := uuid.MustParse("00000000-0000-0000-0000-000000000012")
	currentID := uuid.MustParse("00000000-0000-0000-0000-000000000013")

	outdatedLocal := NewWorkspaceClientWithConnection(nil, nil, goconfig.Default(), "")
	outdatedLocal.session = &goclient.Session{
		Server: goprotocol.ServerInfo{
			Descriptor: goprotocol.Descriptor{
				Capabilities: goprotocol.ServerCapabilities(),
			},
		},
		Compatibility: goprotocol.Compatibility{Compatible: true, RestartRecommended: true},
	}
	outdatedRemote := NewWorkspaceClientWithConnection(nil, nil, goconfig.Default(), "alice@host")
	outdatedRemote.session = &goclient.Session{
		Server: goprotocol.ServerInfo{
			Descriptor: goprotocol.Descriptor{
				Capabilities: goprotocol.ServerCapabilities(),
			},
		},
		Compatibility: goprotocol.Compatibility{Compatible: true, RestartRecommended: true},
	}
	currentLocal := NewWorkspaceClientWithConnection(nil, nil, goconfig.Default(), "")
	currentLocal.session = &goclient.Session{
		Server: goprotocol.ServerInfo{
			Descriptor: goprotocol.Descriptor{
				Capabilities: goprotocol.ServerCapabilities(),
			},
		},
		Compatibility: goprotocol.Compatibility{Compatible: true, RestartRecommended: false},
	}

	if err := manager.AddConnection(ConnectionEntry{ID: localID, Name: "Local", Kind: "local"}, outdatedLocal, nil, true); err != nil {
		t.Fatal(err)
	}
	if err := manager.AddConnection(ConnectionEntry{ID: remoteID, Name: "Remote", Kind: "remote", Destination: "alice@host"}, outdatedRemote, nil, false); err != nil {
		t.Fatal(err)
	}
	if err := manager.AddConnection(ConnectionEntry{ID: currentID, Name: "Current", Kind: "local"}, currentLocal, nil, false); err != nil {
		t.Fatal(err)
	}

	actions := make([]string, 0, 2)
	manager.SetServerOperator(func(entry ConnectionEntry, session *goclient.Session, action string) (ConnectionEntry, *WorkspaceClient, func(), string, error) {
		actions = append(actions, entry.Kind+":"+action+":"+entry.ID.String())
		if entry.ID != localID || action != "restart" {
			return entry, nil, nil, "", errors.New("unexpected server operation")
		}
		return entry, nil, nil, "", nil
	})

	manager.RestartOutdatedLocalServersAfterUpdate()
	if len(actions) != 1 || actions[0] != "local:restart:"+localID.String() {
		t.Fatalf("actions = %v", actions)
	}
}
