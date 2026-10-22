package goserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/SurTeam/Water/internal/goprotocol"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

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

func TestAgentTestSamplingDoesNotHoldCommandLock(t *testing.T) {
	s := New(filepath.Join(t.TempDir(), "water.sock"))
	defer s.Close()
	if _, err := s.executeCommand("workspace.new", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	s.sampleForegroundProcess = func(ctx context.Context, id uuid.UUID) (goagent.Process, error) {
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > time.Second {
			t.Error("missing bounded sampling context")
		}
		close(started)
		select {
		case <-release:
			return goagent.Process{Name: "owned"}, nil
		case <-ctx.Done():
			return goagent.Process{}, ctx.Err()
		}
	}
	done := make(chan *goprotocol.RPCError, 1)
	go func() { _, err := s.executeCommand("agents.test", json.RawMessage(`{}`)); done <- err }()
	select {
	case <-started:
	case err := <-done:
		t.Fatalf("test failed before sampling: %v", err)
	case <-time.After(time.Second):
		t.Fatal("sampler never started")
	}
	responsive := make(chan *goprotocol.RPCError, 1)
	go func() { _, err := s.executeCommand("workspace.new", json.RawMessage(`{}`)); responsive <- err }()
	select {
	case err := <-responsive:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("unrelated command blocked behind sampling")
	}
	unblock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("sampling did not finish")
	}
}

func TestAgentConfigureSuppliesSamplingDeadline(t *testing.T) {
	s := New(filepath.Join(t.TempDir(), "water.sock"))
	defer s.Close()
	s.sampleForeground = func(ctx context.Context) (map[uuid.UUID]goagent.Process, error) {
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > time.Second {
			t.Error("configure sampler has no bounded deadline")
		}
		return nil, context.DeadlineExceeded
	}
	if _, err := s.executeCommand("agents.configure", json.RawMessage(`{"config":{}}`)); err == nil {
		t.Fatal("sampling deadline ignored")
	}
}
