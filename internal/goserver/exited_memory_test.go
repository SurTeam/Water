package goserver_test

import (
	"encoding/base64"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SurTeam/Water/internal/goclient"
	"github.com/SurTeam/Water/internal/goprotocol"
	"github.com/SurTeam/Water/internal/goserver"
	"github.com/google/uuid"
)

func TestExitedTerminalsHaveBoundedQueryableHistory(t *testing.T) {
	socket := filepath.Join("/tmp", "water-exit-test-"+uuid.NewString()+".sock")
	srv := goserver.New(socket)
	done := make(chan error, 1)
	go func() { done <- srv.ListenAndServe() }()
	defer func() {
		_ = srv.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(2 * time.Second):
			t.Error("isolated server did not stop")
		}
	}()
	client := goclient.New(socket)
	wait := func(check func() bool) {
		t.Helper()
		timer := time.NewTimer(3 * time.Second)
		defer timer.Stop()
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		for {
			if check() {
				return
			}
			select {
			case <-ticker.C:
			case <-timer.C:
				t.Fatal("isolated server condition timed out")
			}
		}
	}
	wait(func() bool { return client.CallTimeout("ping", nil, nil, time.Second) == nil })

	var first uuid.UUID
	for i := 0; i < 40; i++ {
		marker := fmt.Sprintf("exit-output-%d", i)
		var spawned struct {
			TerminalID uuid.UUID `json:"terminal_id"`
		}
		if err := client.Dispatch(map[string]any{
			"type": "terminal.spawn", "program": "/bin/sh",
			"args": []string{"-c", "printf " + marker}, "columns": 80, "lines": 24,
		}, &spawned); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = spawned.TerminalID
		}
		var exited struct {
			Events []goprotocol.WireTerminalEvent `json:"events"`
		}
		if err := client.CallTimeout("terminal.wait_exit", map[string]any{
			"terminal_id": spawned.TerminalID, "timeout_ms": 2000,
		}, &exited, 3*time.Second); err != nil {
			t.Fatal(err)
		}
		var text strings.Builder
		for _, event := range exited.Events {
			if event.Type == "output" {
				data, err := base64.StdEncoding.DecodeString(event.Bytes)
				if err != nil {
					t.Fatal(err)
				}
				text.Write(data)
			}
		}
		if text.String() != marker || len(exited.Events) == 0 || exited.Events[len(exited.Events)-1].Type != "exit" {
			t.Fatalf("exit %d lost output or authoritative Exit", i)
		}
		var memory struct {
			Active int `json:"terminal_count"`
			Exited int `json:"exited_terminal_count"`
			Bytes  int `json:"exited_replay_bytes"`
			Panes  int `json:"surface_count"`
		}
		wait(func() bool {
			if err := client.CallTimeout("debug.memory", nil, &memory, time.Second); err != nil {
				t.Fatal(err)
			}
			return memory.Active == 0 && memory.Panes == 0 && memory.Exited == min(i+1, 32)
		})
		if memory.Bytes > 8*1024*1024 {
			t.Fatalf("exit history exceeded byte budget: %d", memory.Bytes)
		}
		if err := client.CallTimeout("terminal.snapshot", map[string]any{"terminal_id": spawned.TerminalID}, nil, time.Second); err != nil {
			t.Fatalf("recent exit unavailable: %v", err)
		}
	}
	if err := client.CallTimeout("terminal.snapshot", map[string]any{"terminal_id": first}, nil, time.Second); err == nil {
		t.Fatal("old exit was not evicted")
	}
}
