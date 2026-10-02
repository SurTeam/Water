package goserver_test

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SurTeam/Water/internal/goclient"
	"github.com/SurTeam/Water/internal/goprotocol"
	"github.com/SurTeam/Water/internal/goserver"
	"github.com/google/uuid"
)

func TestServerClientTerminalRoundTrip(t *testing.T) {
	socket := filepath.Join("/tmp", "water-go-test-"+uuid.New().String()+".sock")
	srv := goserver.New(socket)
	done := make(chan error, 1)
	go func() { done <- srv.ListenAndServe() }()
	defer func() {
		_ = srv.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("server did not stop")
		}
	}()

	client := goclient.New(socket)
	deadline := time.Now().Add(2 * time.Second)
	for {
		var pong map[string]any
		if err := client.Call("ping", map[string]any{}, &pong); err == nil {
			break
		}
		if time.Now().After(deadline) {
			select {
			case serveErr := <-done:
				t.Fatalf("server failed to start: %v", serveErr)
			default:
				t.Fatal("server never became ready")
			}
		}
		time.Sleep(10 * time.Millisecond)
	}

	var spawned struct {
		Type       string    `json:"type"`
		TerminalID uuid.UUID `json:"terminal_id"`
	}
	if err := client.Dispatch(map[string]any{
		"type":    "terminal.spawn",
		"program": "/bin/sh",
		"args":    []string{"-c", "printf 'hello-water\\n'"},
		"columns": 80,
		"lines":   24,
	}, &spawned); err != nil {
		t.Fatal(err)
	}
	if spawned.TerminalID == uuid.Nil {
		t.Fatal("missing terminal id")
	}

	var exited struct {
		TerminalID uuid.UUID                       `json:"terminal_id"`
		Process    any                             `json:"process"`
		Events     []goprotocol.WireTerminalEvent  `json:"events"`
	}
	if err := client.Call("terminal.wait_exit", map[string]any{
		"terminal_id": spawned.TerminalID,
		"timeout_ms":  2000,
	}, &exited); err != nil {
		t.Fatal(err)
	}

	var output strings.Builder
	for _, event := range exited.Events {
		if event.Type != "output" {
			continue
		}
		data, err := base64.StdEncoding.DecodeString(event.Bytes)
		if err != nil {
			t.Fatal(err)
		}
		output.Write(data)
	}
	if !strings.Contains(output.String(), "hello-water") {
		t.Fatalf("missing terminal output: %q", output.String())
	}

	var state struct {
		StateRevision uint64 `json:"state_revision"`
		Workspaces []any `json:"workspaces"`
	}
	if err := client.Call("state.dump", map[string]any{}, &state); err != nil {
		t.Fatal(err)
	}
	if state.StateRevision == 0 || len(state.Workspaces) == 0 {
		t.Fatalf("unexpected model state: %#v", state)
	}
}


func TestUIForwardingAndEventList(t *testing.T) {
	socket := filepath.Join("/tmp", "water-go-ui-test-"+uuid.New().String()+".sock")
	srv := goserver.New(socket)
	done := make(chan error, 1)
	go func() { done <- srv.ListenAndServe() }()
	defer func() {
		_ = srv.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("server did not stop")
		}
	}()

	client := goclient.New(socket)
	deadline := time.Now().Add(2 * time.Second)
	for {
		var pong map[string]any
		if err := client.Call("ping", map[string]any{}, &pong); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("server never became ready")
		}
		time.Sleep(10 * time.Millisecond)
	}

	gui, err := client.OpenSession()
	if err != nil {
		t.Fatal(err)
	}
	defer gui.Close()

	replyDone := make(chan error, 1)
	go func() {
		for push := range gui.Pushes {
			if push.Method != "push.ui" {
				continue
			}
			var inner struct {
				Method string `json:"method"`
			}
			if err := json.Unmarshal(push.Params, &inner); err != nil {
				replyDone <- err
				return
			}
			if inner.Method != "ui.snapshot" {
				replyDone <- fmt.Errorf("unexpected UI method %q", inner.Method)
				return
			}
			replyDone <- gui.ReplySuccess(push.RequestID, map[string]any{
				"window_count":      1,
				"has_active_window": true,
			})
			return
		}
		replyDone <- fmt.Errorf("GUI session closed before push.ui")
	}()

	var snapshot struct {
		WindowCount     int  `json:"window_count"`
		HasActiveWindow bool `json:"has_active_window"`
	}
	if err := client.Call("ui.snapshot", map[string]any{}, &snapshot); err != nil {
		t.Fatal(err)
	}
	if err := <-replyDone; err != nil {
		t.Fatal(err)
	}
	if snapshot.WindowCount != 1 || !snapshot.HasActiveWindow {
		t.Fatalf("unexpected snapshot: %#v", snapshot)
	}

	if err := client.Dispatch(map[string]any{"type": "workspace.create"}, nil); err != nil {
		t.Fatal(err)
	}
	var events []struct {
		Sequence      uint64         `json:"sequence"`
		StateRevision uint64         `json:"state_revision"`
		Kind          map[string]any `json:"kind"`
	}
	if err := client.Call("event.list", map[string]any{"after_sequence": 0}, &events); err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 {
		t.Fatal("expected application event")
	}
	last := events[len(events)-1]
	if last.Sequence == 0 || last.StateRevision == 0 {
		t.Fatalf("invalid event metadata: %#v", last)
	}
	if got, _ := last.Kind["type"].(string); got != "workspace_created" {
		t.Fatalf("unexpected event kind: %#v", last.Kind)
	}

	var metrics map[string]any
	if err := client.Call("debug.metrics", map[string]any{}, &metrics); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"state_dumps", "model_snapshot_pushes", "terminal_stream_events"} {
		if _, ok := metrics[key]; !ok {
			t.Fatalf("missing metric %q", key)
		}
	}
}


func TestRejectsIncompatibleBuildVariantBeforeDispatch(t *testing.T) {
	socket := filepath.Join("/tmp", "water-go-variant-"+uuid.New().String()+".sock")
	srv := goserver.New(socket)
	done := make(chan error, 1)
	go func() { done <- srv.ListenAndServe() }()
	defer func() {
		_ = srv.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("server did not stop")
		}
	}()

	client := goclient.New(socket)
	deadline := time.Now().Add(2 * time.Second)
	for {
		var pong map[string]any
		if err := client.Call("ping", map[string]any{}, &pong); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("server never became ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	before := stateDump(t, client).StateRevision

	conn, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	params, _ := json.Marshal(map[string]any{
		"command": map[string]any{"type": "workspace.create"},
	})
	if err := goprotocol.WriteJSON(conn, goprotocol.WireMessage{
		BuildVariant:    "release",
		ProtocolVersion: goprotocol.ProtocolVersion,
		RequestID:       77,
		Method:          "command.dispatch",
		Params:          params,
	}); err != nil {
		t.Fatal(err)
	}
	frame, err := goprotocol.ReadFrame(conn)
	if err != nil {
		t.Fatal(err)
	}
	var reply goprotocol.WireMessage
	if err := json.Unmarshal(frame.JSON, &reply); err != nil {
		t.Fatal(err)
	}
	if reply.OK == nil || *reply.OK || reply.Error == nil || reply.Error.Code != "INCOMPATIBLE_SERVER" {
		t.Fatalf("unexpected reply: %#v", reply)
	}
	after := stateDump(t, client).StateRevision
	if after != before {
		t.Fatalf("incompatible request mutated model: before=%d after=%d", before, after)
	}
}


