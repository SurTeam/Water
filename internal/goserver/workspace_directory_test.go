package goserver

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/SurTeam/Water/internal/goconfig"
	"github.com/google/uuid"
)

func TestWorkspaceNewDirectory(t *testing.T) {
	cfg := goconfig.Default()
	s := NewWithConfig(filepath.Join(t.TempDir(), "server.sock"), cfg)
	t.Cleanup(func() { _ = s.Close() })
	directory := t.TempDir()
	raw, _ := json.Marshal(map[string]any{"cwd": directory})
	var previous uuid.UUID
	for range 2 {
		result, err := s.executeCommand("workspace.new", raw)
		if err != nil {
			t.Fatal(err)
		}
		id := result.(map[string]any)["workspace_id"].(uuid.UUID)
		if id == previous {
			t.Fatal("folder request reused a workspace")
		}
		if cwd := s.model.WorkspaceActiveCWD(id); cwd != directory {
			t.Fatalf("workspace cwd = %q, want %q", cwd, directory)
		}
		previous = id
	}
	for _, directory := range []string{"relative", filepath.Join(t.TempDir(), "missing")} {
		raw, _ := json.Marshal(map[string]any{"cwd": directory})
		if _, err := s.executeCommand("workspace.new", raw); err == nil {
			t.Fatalf("invalid cwd %q accepted", directory)
		}
	}
}
