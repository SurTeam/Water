package goui

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/SurTeam/Water/internal/goagent"
	"github.com/SurTeam/Water/internal/goconfig"
	"github.com/google/uuid"
)

func TestAgentRuleForProcessCopiesNameAndArgumentsLiterally(t *testing.T) {
	for _, executable := range []string{"custom-runner", "python", "node", "sh"} {
		p := goagent.Process{PID: 1234, Started: "transient", Name: executable,
			Args: []string{"/opt/bin/" + executable, "--unknown-option", "-c", `a;$(touch sentinel)\"quoted\"\\path`, " space preserved "}}
		rule, err := agentRuleForProcess(p)
		if err != nil {
			t.Fatal(err)
		}
		want := []goagent.Condition{{Field: "executable", Op: "exact", Value: executable}}
		for _, arg := range p.Args[1:] {
			want = append(want, goagent.Condition{Field: "argument", Op: "exact", Value: arg})
		}
		if !reflect.DeepEqual(rule.All, want) {
			t.Fatalf("%s was interpreted or modified: %#v", executable, rule)
		}
		profile := goagent.Builtins()[0]
		profile.Rules = []goagent.Rule{rule}
		registry, err := goagent.Compile(goagent.Config{Overrides: []goagent.Override{{ID: profile.ID, Rules: &profile.Rules}}})
		if err != nil {
			t.Fatal(err)
		}
		if registry.Diagnose(p).Selected == nil {
			t.Fatal("generated rule does not match source")
		}
	}
}

func TestAgentRuleForProcessValidatesWithoutTruncation(t *testing.T) {
	for _, p := range []goagent.Process{
		{},
		{Name: "agent", Args: []string{"agent", ""}},
		{Name: "agent", Args: []string{"agent", strings.Repeat("x", 513)}},
		{Name: "agent", Args: []string{"agent", "a", "b", "c", "d", "e", "f", "g", "h"}},
	} {
		if _, err := agentRuleForProcess(p); err == nil {
			t.Fatalf("invalid sample silently accepted: %#v", p)
		}
	}
	rule, err := agentRuleForProcess(goagent.Process{Name: "agent"})
	if err != nil || len(rule.All) != 1 || rule.All[0].Value != "agent" {
		t.Fatalf("process without args: %#v %v", rule, err)
	}
}

func sampleClient(t *testing.T) *WorkspaceClient {
	t.Helper()
	c := NewWorkspaceClientWithConnection(nil, nil, goconfig.Default(), "")
	t.Cleanup(c.Close)
	c.openSettings()
	c.settings.agents.loaded = true
	c.settings.agentFields(goagent.Codex)
	pane := uuid.New()
	c.state.FocusedPane = &pane
	c.settings.openAgentRules()
	return c
}

func TestAgentRuleSampleReplacesOnlySelectedRule(t *testing.T) {
	c := sampleClient(t)
	s := &c.settings
	before := cloneAgentRules(s.agents.rules)
	s.agentRuleAction("add-rule")
	ruleValue(s, "manual draft")
	e := s.agents.rulesEditor
	e.sampling = true
	process := goagent.Process{Name: "arbitrary", Args: []string{"arbitrary", "--flag", "/tmp/entry"}}
	c.applyAgentRuleSample(agentPanelResult{rulesEditor: e, samplePane: *c.state.FocusedPane, diagnostic: &goagent.Diagnostic{Process: process}})
	want, _ := agentRuleForProcess(process)
	if e.sampling || !reflect.DeepEqual(e.rules[len(e.rules)-1], want) || !reflect.DeepEqual(e.rules[:len(before)], before) {
		t.Fatal("autofill replaced the wrong rules")
	}
	if !reflect.DeepEqual(s.agents.rules, before) {
		t.Fatal("autofill prematurely applied to parent draft")
	}
	s.agentRuleAction("cancel")
	if !reflect.DeepEqual(s.agents.rules, before) {
		t.Fatal("cancel committed sampled rule")
	}
	s.openAgentRules()
	for len(s.agents.rulesEditor.rules) > 0 {
		s.agentRuleAction("remove-rule")
	}
	e = s.agents.rulesEditor
	c.applyAgentRuleSample(agentPanelResult{rulesEditor: e, samplePane: *c.state.FocusedPane, diagnostic: &goagent.Diagnostic{Process: process}})
	if len(e.rules) != 1 || !reflect.DeepEqual(e.rules[0], want) {
		t.Fatal("empty draft was not filled")
	}
}

func TestAgentRuleSampleFailurePaneChangeAndStaleReply(t *testing.T) {
	c := sampleClient(t)
	s := &c.settings
	e := s.agents.rulesEditor
	before := cloneAgentRules(e.rules)
	result := agentPanelResult{rulesEditor: e, samplePane: *c.state.FocusedPane, diagnostic: &goagent.Diagnostic{Process: goagent.Process{Name: "sampled"}}}
	result.err = errors.New("sampling failed")
	e.sampling = true
	c.applyAgentRuleSample(result)
	if e.sampling || e.message != "sampling failed" || !reflect.DeepEqual(e.rules, before) {
		t.Fatal("failed sample changed draft")
	}
	result.err = nil
	result.samplePane = uuid.New()
	c.applyAgentRuleSample(result)
	if !reflect.DeepEqual(e.rules, before) || e.message != "The focused pane changed; sample again" {
		t.Fatal("changed pane sample was applied")
	}
	result.samplePane = *c.state.FocusedPane
	s.closeAgentRules()
	s.openAgentRules()
	next := s.agents.rulesEditor
	s.agents.busy = true
	s.agents.result = make(chan agentPanelResult, 1)
	s.agents.result <- result
	c.pollAgentSettings()
	if s.agents.busy || next == e || !reflect.DeepEqual(next.rules, before) || next.message != "" {
		t.Fatal("late reply mutated new modal")
	}
}

func TestAgentRuleSampleRequiresConnectedPane(t *testing.T) {
	c := sampleClient(t)
	before := cloneAgentRules(c.settings.agents.rulesEditor.rules)
	c.sampleAgentRule()
	e := c.settings.agents.rulesEditor
	if e.sampling || c.settings.agents.busy || e.message != "A connected terminal pane is required" || !reflect.DeepEqual(e.rules, before) {
		t.Fatal("unavailable connection changed draft")
	}
}
