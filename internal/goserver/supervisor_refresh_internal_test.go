package goserver

import (
	"path/filepath"
	"testing"

	"github.com/SurTeam/Water/internal/goconfig"
	"github.com/SurTeam/Water/internal/gosupervisor"
	"github.com/google/uuid"
)

func TestRefreshSupervisorGenerationNoopWhenCurrent(t *testing.T) {
	socket := filepath.Join("/tmp", "water-go-refresh-"+uuid.NewString()+".sock")
	supervisor := gosupervisor.NewServer()
	if err := supervisor.Listen(socket); err != nil {
		t.Fatal(err)
	}
	defer supervisor.Close()

	server := NewWithConfig(filepath.Join(t.TempDir(), "server.sock"), goconfig.Default())
	if err := server.UseSupervisor(socket); err != nil {
		t.Fatal(err)
	}
	if err := server.refreshSupervisorGeneration(); err != nil {
		t.Fatal(err)
	}
	if got := SupervisorSocketForServer(server); got != socket {
		t.Fatalf("supervisor socket = %q, want %q", got, socket)
	}
	if server.legacySupervisorSocket != "" {
		t.Fatalf("unexpected legacy supervisor %q", server.legacySupervisorSocket)
	}
}
