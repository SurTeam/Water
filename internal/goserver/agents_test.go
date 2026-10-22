package goserver_test

import (
	"fmt"
	"github.com/SurTeam/Water/internal/gosupervisor"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/SurTeam/Water/internal/goagent"
	"github.com/SurTeam/Water/internal/goconfig"
	"github.com/SurTeam/Water/internal/goserver"
	"github.com/google/uuid"
)

func TestAgentProfilesThroughDispatcherAndRecognition(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "server.json")
	desktop := filepath.Join(dir, "desktop.json")
	cfg := recoveryConfig(t)
	cfg.UI.SidebarWidth = 257
	if err := goconfig.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(desktop, []byte(`{"unchanged":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	_, client := recoveryServer(t, cfg, func(s *goserver.Server) { s.ConfigPath = path })
	program := filepath.Join(dir, "water-own-agent")
	if err := os.Symlink("/bin/sleep", program); err != nil {
		t.Fatal(err)
	}
	spawn := func() uuid.UUID {
		t.Helper()
		if err := client.Dispatch(map[string]any{"type": "workspace.new"}, nil); err != nil {
			t.Fatal(err)
		}
		var out struct {
			TerminalID uuid.UUID `json:"terminal_id"`
		}
		if err := client.Dispatch(map[string]any{"type": "terminal.spawn", "program": program, "args": []string{"30"}}, &out); err != nil {
			t.Fatal(err)
		}
		return out.TerminalID
	}
	first, second := spawn(), spawn()
	for _, id := range []uuid.UUID{first, second} {
		deadline := time.Now().Add(2 * time.Second)
		for {
			var sampled goagent.Diagnostic
			err := client.Dispatch(map[string]any{"type": "agents.test", "terminal_id": id}, &sampled)
			if err == nil && sampled.Process.Name == "water-own-agent" {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("owned fixture did not become foreground: %v %#v", err, sampled)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	p := goagent.Profile{ID: goagent.Kind(uuid.NewString()), Name: "Owned custom Agent", Enabled: true, Priority: 50, Color: "#12ab34", OSC: []string{"Completed", "Running", "Error", "Running", "Paused"}, Interrupt: "sigint", Exit: "sigterm", Rules: []goagent.Rule{{All: []goagent.Condition{{Field: "executable", Op: "exact", Value: "water-own-agent"}}}}}
	agents := goagent.Config{}
	agents.Put(p)
	configure := func(c goagent.Config) {
		t.Helper()
		if err := client.Dispatch(map[string]any{"type": "agents.configure", "config": c}, nil); err != nil {
			t.Fatal(err)
		}
	}
	configure(agents)
	dump := stateDump(t, client)
	if len(dump.Agents) != 2 {
		t.Fatalf("live terminals not reclassified: %d", len(dump.Agents))
	}
	for _, raw := range dump.Agents {
		a := raw.(map[string]any)
		if a["label"] != p.Name || a["scrollback_on_clear_screen"] != false || a["profile"] == nil || a["run"] == nil {
			t.Fatalf("Agent metadata missing: %#v", a)
		}
	}
	saved, err := goconfig.Load(path)
	if err != nil || saved.UI.SidebarWidth != 257 || len(saved.Agents.Custom) != 1 {
		t.Fatal("target config persistence lost fields")
	}
	before, _ := os.ReadFile(desktop)
	if string(before) != `{"unchanged":true}` {
		t.Fatal("other connection configuration changed")
	}
	// Test a draft configuration without persisting or altering current identity.
	draft := goagent.Clone(agents)
	draft.Custom[0].Enabled = false
	var d goagent.Diagnostic
	if err = client.Dispatch(map[string]any{"type": "agents.test", "terminal_id": first, "config": draft}, &d); err != nil || d.Selected != nil {
		t.Fatalf("draft recognition test: %v %#v", err, d)
	}
	if len(stateDump(t, client).Agents) != 2 {
		t.Fatal("recognition test mutated saved runtime")
	}
	configure(draft)
	if len(stateDump(t, client).Agents) != 0 {
		t.Fatal("disable did not reclassify current terminals")
	}
	configure(agents)
	if err = client.Dispatch(map[string]any{"type": "agents.test", "terminal_id": first}, &d); err != nil || d.Selected == nil {
		t.Fatalf("real foreground diagnosis failed: %v", err)
	}
	for _, action := range []string{"interrupt", "exit", "force-stop"} {
		if err = client.Dispatch(map[string]any{"type": "agent.control", "expected_agent_id": p.ID, "terminal_id": first, "expected": d.Process, "action": action}, nil); err == nil {
			t.Fatalf("removed Agent control %q was accepted", action)
		}
	}
	for _, id := range []uuid.UUID{first, second} {
		if err = client.Dispatch(map[string]any{"type": "agents.test", "terminal_id": id}, &d); err != nil || d.Selected == nil {
			t.Fatalf("recognition failed after rejected controls: %v", err)
		}
	}
	// Invalid config must leave both saved and runtime definitions intact.
	invalid := goagent.Clone(agents)
	invalid.Custom[0].Exit = "arbitrary-script"
	if err = client.Dispatch(map[string]any{"type": "agents.configure", "config": invalid}, nil); err == nil {
		t.Fatal("invalid policy accepted")
	}
	var info struct {
		Config   goagent.Config `json:"config"`
		Revision uint64         `json:"revision"`
		Instance uuid.UUID      `json:"instance_id"`
	}
	if err = client.Dispatch(map[string]any{"type": "agents.inspect"}, &info); err != nil || info.Config.Custom[0].Exit != "sigterm" {
		t.Fatal("failed configure replaced runtime")
	}
	if err = client.Dispatch(map[string]any{"type": "agents.configure", "config": agents, "expected_revision": info.Revision - 1, "expected_instance": info.Instance}, nil); err == nil {
		t.Fatal("stale configuration revision was accepted")
	}
	if err = client.Dispatch(map[string]any{"type": "agents.configure", "config": agents, "expected_revision": info.Revision, "expected_instance": uuid.New()}, nil); err == nil {
		t.Fatal("another server's configuration token was accepted")
	}
	if err = client.Dispatch(map[string]any{"type": "agents.configure"}, nil); err == nil {
		t.Fatal("missing config silently reset definitions")
	}

}

func TestSupervisorAgentSamplingConcurrentWithMonitor(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "water-agent-chain-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	supervisor := gosupervisor.NewServer()
	socket := filepath.Join(dir, "pty.sock")
	if err := supervisor.Listen(socket); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = supervisor.Close() })
	cfg := recoveryConfig(t)
	profile := goagent.Profile{ID: goagent.Kind(uuid.NewString()), Name: "Owned supervisor Agent", Interrupt: "none", Exit: "none", Enabled: true, Priority: 50, Rules: []goagent.Rule{{All: []goagent.Condition{{Field: "executable", Op: "exact", Value: "water-chain-agent"}}}}}
	cfg.Agents.Put(profile)
	if _, err := goagent.Compile(cfg.Agents); err != nil {
		t.Fatal(err)
	}
	server, client := recoveryServer(t, cfg, func(s *goserver.Server) {
		if err := s.UseSupervisor(socket); err != nil {
			t.Fatal(err)
		}
	})
	program := filepath.Join(dir, "water-chain-agent")
	if err := os.Symlink("/bin/sleep", program); err != nil {
		t.Fatal(err)
	}
	ids := make([]uuid.UUID, 3)
	for i := range ids {
		if err := client.Dispatch(map[string]any{"type": "workspace.new"}, nil); err != nil {
			t.Fatal(err)
		}
		var spawned struct {
			TerminalID uuid.UUID `json:"terminal_id"`
		}
		if err := client.Dispatch(map[string]any{"type": "terminal.spawn", "program": program, "args": []string{"30"}}, &spawned); err != nil {
			t.Fatal(err)
		}
		ids[i] = spawned.TerminalID
	}

	// Classification traverses supervisor -> sampler -> dispatcher -> model.
	// Concurrent samples below span multiple background monitor ticks.
	deadline := time.Now().Add(3 * time.Second)
	for {
		dump := stateDump(t, client)
		ready := len(dump.Agents) == len(ids)
		for _, raw := range dump.Agents {
			agent := raw.(map[string]any)
			run, ok := agent["run"].(map[string]any)
			if !ok || run["pid"] == nil || run["started"] == "" || agent["label"] != profile.Name {
				ready = false
			}
		}
		if ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("monitor did not classify owned supervisor Agents: %#v", dump.Agents)
		}
		time.Sleep(10 * time.Millisecond)
	}
	start := make(chan struct{})
	failures := make(chan error, len(ids))
	for _, id := range ids {
		go func(id uuid.UUID) {
			<-start
			ticker := time.NewTicker(20 * time.Millisecond)
			defer ticker.Stop()
			for i := 0; i < 40; i++ {
				<-ticker.C
				var diagnostic goagent.Diagnostic
				if err := client.Dispatch(map[string]any{"type": "agents.test", "terminal_id": id}, &diagnostic); err != nil {
					failures <- err
					return
				}
				if diagnostic.Process.Name != "water-chain-agent" || diagnostic.Selected == nil || diagnostic.Selected.ID != profile.ID {
					failures <- fmt.Errorf("wrong foreground reply: %#v", diagnostic)
					return
				}
			}
			failures <- nil
		}(id)
	}
	close(start)
	for range ids {
		select {
		case err := <-failures:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(8 * time.Second):
			t.Fatal("concurrent supervised sampling stalled")
		}
	}
	t.Logf("isolated supervisor=%s server=%s; 120 concurrent target samples passed with monitor active", socket, server.InstanceID)
}
