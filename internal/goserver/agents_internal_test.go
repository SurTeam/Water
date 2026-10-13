package goserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/SurTeam/Water/internal/goagent"
	"github.com/SurTeam/Water/internal/goconfig"
	"github.com/google/uuid"
)

func TestAgentConfigureSamplingFailurePreservesSavedRegistry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := goconfig.Default()
	if err := goconfig.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	s := NewWithConfig(filepath.Join(t.TempDir(), "water.sock"), cfg)
	defer s.Close()
	s.ConfigPath = path
	before := goagent.Clone(s.agentConfig)
	revision := s.agentRevision
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Inject the failure at the sampler boundary, independent of platform or PATH.
	// Run the real dispatcher so this checks both runtime and persistence ordering.
	called := false
	s.sampleForeground = func(context.Context) (map[uuid.UUID]goagent.Process, error) {
		called = true
		return nil, errors.New("owned sampling failure")
	}
	draft := goagent.Clone(before)
	profile := goagent.Builtins()[0]
	profile.Enabled = false
	draft.Put(profile)
	raw, err := json.Marshal(map[string]any{"type": "agents.configure", "config": draft})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.executeCommand("agents.configure", raw); err == nil || !called {
		t.Fatal("sampling failure reported a successful save or sampler was bypassed")
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if s.agentRevision != revision || !reflect.DeepEqual(before, s.agentConfig) || !bytes.Equal(original, saved) {
		t.Fatal("failed sampling changed configuration or revision")
	}
}
