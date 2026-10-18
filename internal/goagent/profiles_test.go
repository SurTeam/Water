package goagent

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/google/uuid"
)

func TestProfilesOverrideInheritanceAndRecognition(t *testing.T) {
	cfg := Config{}
	base := Builtins()[0]
	base.Name = "My Claude"
	base.Enabled = false
	cfg.Put(base)
	if len(cfg.Overrides) != 1 || cfg.Overrides[0].Rules != nil {
		t.Fatal("untouched builtin rules must inherit")
	}
	p := Profile{ID: Kind(uuid.NewString()), Name: "My Node Agent", Enabled: true, Priority: 50, Color: "#72d6ab", OSC: DefaultOSC(), Interrupt: "sigint", Exit: "sigterm", Rules: []Rule{{All: []Condition{{"executable", "glob", "node*"}, {"path", "contains", "/my-agent/"}}}, {All: []Condition{{"executable", "exact", "python3"}, {"argument", "exact", "my_agent"}}}}}
	cfg.Put(p)
	registry, err := Compile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		process Process
		want    Kind
	}{
		{Process{Name: "node", Args: []string{"node", "/tools/my-agent/cli.js"}}, p.ID},
		{Process{Name: "node", Args: []string{"node", "/tools/other/cli.js"}}, ""},
		{Process{Name: "echo", Args: []string{"echo", "/tools/my-agent/cli.js"}}, ""},
		{Process{Name: "python3", Args: []string{"python3", "-m", "my_agent"}}, p.ID},
		{Process{Name: "claude"}, ""},
		{Process{Name: "codex"}, Codex},
	} {
		got := registry.Detect(tc.process)
		if tc.want == "" {
			if got != nil {
				t.Fatalf("unexpected match %#v", got)
			}
		} else if got == nil || got.Kind != tc.want {
			t.Fatalf("match %#v want %s", got, tc.want)
		}
	}
	// Multiple matching rules produce one candidate; high priority beats defaults.
	p.Rules = []Rule{{All: []Condition{{"name", "exact", "codex"}}}, {All: []Condition{{"executable", "exact", "codex"}}}}
	cfg.Put(p)
	registry, _ = Compile(cfg)
	d := registry.Diagnose(Process{Name: "codex"})
	if d.Selected.ID != p.ID || len(d.Matches) != 2 || d.Matches[0].Rule != 0 {
		t.Fatalf("diagnostic=%#v", d)
	}
	cfg.Remove(ClaudeCode)
	if !reflect.DeepEqual(Profiles(cfg)[0], Builtins()[0]) {
		t.Fatal("restore did not remove override")
	}
	cfg.Remove(p.ID)
	if len(cfg.Custom) != 0 {
		t.Fatal("custom removal failed")
	}
}

func TestProfilesRejectInvalidDefinitions(t *testing.T) {
	base := Builtins()[0]
	base.ID = Kind(uuid.NewString())
	for _, mutate := range []func(*Profile){
		func(p *Profile) { p.ID = "my-agent" }, func(p *Profile) { p.Name = "" }, func(p *Profile) { p.Color = "#zzzzzz" }, func(p *Profile) { p.Interrupt = "script" }, func(p *Profile) { p.Exit = "kill-shell" }, func(p *Profile) { p.OSC = []string{"Waiting"} }, func(p *Profile) { p.Rules = []Rule{{}} }, func(p *Profile) { p.Rules = []Rule{{All: []Condition{{"path", "glob", "["}}}} },
	} {
		p := base
		mutate(&p)
		if _, err := Compile(Config{Custom: []Profile{p}}); err == nil {
			t.Fatalf("invalid profile accepted: %#v", p)
		}
	}
	if _, err := Compile(Config{Custom: []Profile{base, base}}); err == nil {
		t.Fatal("duplicate ID accepted")
	}
	if _, err := Compile(Config{Overrides: []Override{{ID: "missing"}}}); err == nil {
		t.Fatal("unknown builtin override accepted")
	}
}

func TestParseConfigRejectsUnrelatedOrMisspelledFiles(t *testing.T) {
	for _, input := range []string{`null`, `[]`, `{"agents":{}}`, `{"customs":[]}`, `{} {}`, `{"overrides":[{"id":"codex","enabeld":false}]}`} {
		if _, err := ParseConfig([]byte(input)); err == nil {
			t.Fatalf("invalid config accepted: %s", input)
		}
	}
	if _, err := ParseConfig([]byte(`{"overrides":[],"custom":[]}`)); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeMetadataOmitsRecognitionRules(t *testing.T) {
	registry, err := Compile(Config{})
	if err != nil {
		t.Fatal(err)
	}
	process := Process{Name: "codex", PID: 123, PGID: 123, Started: "known run"}
	agent := registry.Detect(process)
	if agent == nil || len(agent.Profile.Rules) != 0 || agent.Profile.Name != "Codex" || agent.Profile.Interrupt != "ctrl-c" || agent.Run.PID != 123 {
		t.Fatalf("runtime metadata=%#v", agent)
	}
	if len(registry.Diagnose(process).Selected.Rules) == 0 {
		t.Fatal("diagnostics lost full recognition definition")
	}
}

func TestRetiredBuiltinOverridesRemainDormantAndRoundTrip(t *testing.T) {
	for _, id := range []Kind{GeminiCLI, Aider, CursorAgent, Amp, Crush, Goose, QwenCode, Droid, Grok} {
		t.Run(string(id), func(t *testing.T) {
			input := []byte(`{"overrides":[{"id":"` + string(id) + `","name":"My retired agent","enabled":false,"priority":17,"color":"#123456","rules":[{"all":[{"field":"name","op":"exact","value":"codex"}]}],"scrollback_on_clear_screen":false,"status_source":"process","osc":["Idle","Running","Error","Paused","Completed"],"interrupt":"sigint","exit":"ctrl-d","long_run_seconds":42}]}`)
			cfg, err := ParseConfig(input)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(Profiles(cfg), Builtins()) {
				t.Fatal("retired override changed active profiles")
			}
			registry, err := Compile(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if got := registry.Detect(Process{Name: "codex"}); got == nil || got.Kind != Codex {
				t.Fatalf("dormant rule affected recognition: %#v", got)
			}
			if got := registry.Detect(Process{Name: string(id)}); got != nil {
				t.Fatalf("retired builtin recognized: %#v", got)
			}
			// Editing a current builtin must retain the dormant definition in full.
			edited := Builtins()[0]
			edited.Name = "My Claude"
			cfg.Put(edited)
			encoded, err := json.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			roundTrip, err := ParseConfig(encoded)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(cfg, roundTrip) {
				t.Fatal("round trip lost legacy fields")
			}
			if _, err := Compile(Config{Overrides: []Override{cfg.Overrides[0], cfg.Overrides[0]}}); err == nil {
				t.Fatal("duplicate retired override accepted")
			}
			cfg.Remove(id)
			for _, override := range cfg.Overrides {
				if override.ID == id {
					t.Fatal("retired override was not removable")
				}
			}
		})
	}
}
