package goserver_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/SurTeam/Water/internal/goagent"
	"github.com/SurTeam/Water/internal/goconfig"
	"github.com/SurTeam/Water/internal/goserver"
	"github.com/google/uuid"
)

func TestAgentProfilesThroughDispatcherAndOwnedForegroundControl(t *testing.T) {
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
	if err = client.Dispatch(map[string]any{"type": "agent.control", "terminal_id": first, "expected": d.Process, "expected_agent_id": goagent.Codex, "action": "interrupt"}, nil); err == nil {
		t.Fatal("another Agent's policy was accepted for this run")
	}
	stale := d.Process
	stale.Started = "other run"
	if err = client.Dispatch(map[string]any{"type": "agent.control", "expected_agent_id": p.ID, "terminal_id": first, "expected": stale, "action": "exit"}, nil); err == nil {
		t.Fatal("stale process identity was accepted")
	}
	if err = client.Dispatch(map[string]any{"type": "agent.control", "expected_agent_id": p.ID, "terminal_id": second, "expected": d.Process, "action": "interrupt"}, nil); err == nil {
		t.Fatal("another terminal's run identity was accepted")
	}
	if err = client.Dispatch(map[string]any{"type": "agent.control", "expected_agent_id": p.ID, "terminal_id": first, "expected": d.Process, "action": "interrupt"}, nil); err != nil {
		t.Fatal(err)
	}
	var exited any
	if err = client.Call("terminal.wait_exit", map[string]any{"terminal_id": first, "timeout_ms": 2000}, &exited); err != nil {
		t.Fatal(err)
	}
	if err = client.Dispatch(map[string]any{"type": "agents.test", "terminal_id": second}, &d); err != nil || d.Selected == nil {
		t.Fatal("unrelated terminal was interrupted")
	}
	if err = client.Dispatch(map[string]any{"type": "agent.control", "expected_agent_id": p.ID, "terminal_id": second, "expected": d.Process, "action": "exit"}, nil); err != nil {
		t.Fatal(err)
	}
	if err = client.Call("terminal.wait_exit", map[string]any{"terminal_id": second, "timeout_ms": 2000}, &exited); err != nil {
		t.Fatal(err)
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
