package goserver

import (
	"context"
	"encoding/json"
	"github.com/SurTeam/Water/internal/goterminal"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

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

func TestCreationFailureRestoresLayoutAndSelection(t *testing.T) {
	for _, command := range []string{"tab.new", "tab.new_in_workspace", "pane.split"} {
		t.Run(command, func(t *testing.T) {
			cfg := goconfig.Default()
			cfg.Shell.Program = filepath.Join(t.TempDir(), "missing-shell")
			s := NewWithConfig(filepath.Join(t.TempDir(), "server.sock"), cfg)
			t.Cleanup(func() { _ = s.Close() })
			wid := s.model.CreateWorkspace("target")
			_, pane, _ := s.model.CreateTab(wid, "source", false)
			other, _ := s.model.SplitPane(pane, "down")
			_ = s.model.FocusPane(other)
			_, _, _ = s.model.CreateTab(wid, "selected", false)
			active := s.model.CreateWorkspace("active elsewhere")
			_, _, _ = s.model.CreateTab(active, "active", false)
			_ = s.model.ActivateWorkspace(active)
			before := s.model.Dump()
			raw, _ := json.Marshal(map[string]any{"workspace_id": wid, "pane_id": pane, "direction": "right"})
			if _, err := s.executeCommand(command, raw); err == nil || err.Code != "TERMINAL_SPAWN_FAILED" {
				t.Fatalf("spawn error = %v", err)
			}
			after := s.model.Dump()
			before.StateRevision, after.StateRevision = 0, 0
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("failed creation changed layout/selection:\nbefore: %+v\nafter: %+v", before, after)
			}
		})
	}
}

func TestChineseDirectoryInheritedByTabAndSplit(t *testing.T) {
	t.Setenv("LC_ALL", "C")
	t.Setenv("LANG", "C")
	cfg := goconfig.Default()
	cfg.Shell.Program, cfg.Shell.Args = "/bin/sh", []string{"-i"}
	s := NewWithConfig(filepath.Join(t.TempDir(), "server.sock"), cfg)
	t.Cleanup(func() { _ = s.Close() })
	directory := filepath.Join(t.TempDir(), "进度", "paper with spaces")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	directory, _ = filepath.EvalSymlinks(directory)
	wid := s.model.CreateWorkspace("Chinese")
	_, source, _ := s.model.CreateTab(wid, "source", false)
	if _, err := s.spawnInPane(source, "", nil, s.defaultTerminalSize(), t.TempDir()); err != nil {
		t.Fatal(err)
	}
	tid, _ := s.model.TerminalForPane(source)
	term, _ := s.registry.Get(tid)
	deadline := time.Now().Add(2 * time.Second)
	for len(term.Replay()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	// Change the parent shell directory, leaving its spawn metadata unchanged.
	if err := term.Write([]byte("cd '" + strings.ReplaceAll(directory, "'", "'\"'\"'") + "'\n")); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(2 * time.Second)
	for term.CurrentDirectory() != directory {
		if time.Now().After(deadline) {
			t.Fatal("source shell did not change directory")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Metadata deliberately differs: successful inheritance must use the live cwd.
	for _, command := range []string{"tab.new", "pane.split"} {
		raw, _ := json.Marshal(map[string]any{"workspace_id": wid, "pane_id": source, "direction": "right"})
		result, err := s.executeCommand(command, raw)
		if err != nil {
			t.Fatal(err)
		}
		var pane uuid.UUID
		if command == "pane.split" {
			pane = result.(map[string]any)["pane_id"].(uuid.UUID)
		} else {
			pane, _ = s.model.WorkspaceActivePane(wid)
		}
		if got := s.model.PaneCWD(pane); got != directory {
			t.Fatalf("%s cwd = %q, want %q", command, got, directory)
		}
		id, ok := s.model.TerminalForPane(pane)
		if !ok {
			t.Fatal("created empty pane")
		}
		child, _ := s.registry.Get(id)
		deadline = time.Now().Add(2 * time.Second)
		for len(child.Replay()) == 0 && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		execResult, execErr := child.Execute(ctx, goterminal.ExecuteOptions{Command: "pwd; printf 'WATER_SHELL_OK\n'"})
		cancel()
		if execErr != nil || !strings.Contains(execResult.Text, "WATER_SHELL_OK") || execResult.ExitCode == nil || *execResult.ExitCode != 0 {
			t.Fatalf("shell unusable: %+v, %v", execResult, execErr)
		}
	}
}

func TestPaneDirectoryFallsBackWhenProcessUnavailable(t *testing.T) {
	cfg := goconfig.Default()
	cfg.Shell.Program, cfg.Shell.Args = "/bin/sh", []string{"-i"}
	s := NewWithConfig(filepath.Join(t.TempDir(), "server.sock"), cfg)
	t.Cleanup(func() { _ = s.Close() })
	wid := s.model.CreateWorkspace("fallback")
	_, pane, _ := s.model.CreateTab(wid, "source", false)
	directory := t.TempDir()
	if _, err := s.spawnInPane(pane, "", nil, s.defaultTerminalSize(), directory); err != nil {
		t.Fatal(err)
	}
	id, _ := s.model.TerminalForPane(pane)
	// Retain metadata while simulating a missing registry/process lookup.
	s.registry.Remove(id)
	if got := s.currentPaneDirectory(pane, ""); got != directory {
		t.Fatalf("fallback cwd = %q, want %q", got, directory)
	}
	reported := t.TempDir()
	if got := s.currentPaneDirectory(pane, reported); got != reported {
		t.Fatalf("reported fallback = %q, want %q", got, reported)
	}
}
