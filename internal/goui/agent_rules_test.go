package goui

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/SurTeam/Water/internal/goagent"
	"github.com/SurTeam/Water/internal/goconfig"
)

func rulePanel(t *testing.T) *settingsPanel {
	t.Helper()
	c := NewWorkspaceClientWithConnection(nil, nil, goconfig.Default(), "")
	t.Cleanup(c.Close)
	c.openSettings()
	c.settings.agents.loaded = true
	c.settings.agentFields(goagent.Codex)
	return &c.settings
}
func ruleJSON(s *settingsPanel) string {
	for _, f := range s.fields {
		if f.group == "Agents" && f.name == "Rules" {
			return f.editor.Text()
		}
	}
	return ""
}
func ruleValue(s *settingsPanel, value string) {
	for i := range s.fields {
		if s.fields[i].group == "AgentRules" {
			s.fields[i].editor.SetText(value)
		}
	}
}

func TestAgentRuleEditorEscapesAndPreservesLiteralInput(t *testing.T) {
	s := rulePanel(t)
	s.openAgentRules()
	s.agentRuleAction("add-rule")
	value := "  /tmp/\"quoted\"\\path;$(touch SENTINEL)`echo x`\t  "
	ruleValue(s, value)
	s.agentRuleAction("apply")
	if s.agents.rulesEditor != nil {
		t.Fatal("valid draft was not applied")
	}
	var rules []goagent.Rule
	if err := json.Unmarshal([]byte(ruleJSON(s)), &rules); err != nil {
		t.Fatal(err)
	}
	if got := rules[len(rules)-1].All[0].Value; got != value {
		t.Fatalf("value changed: %q", got)
	}
	// Compile and diagnose treat shell metacharacters as literal match data.
	p := goagent.Builtins()[0]
	p.Rules = []goagent.Rule{{All: []goagent.Condition{{Field: "argument", Op: "exact", Value: value}}}}
	registry, err := goagent.Compile(goagent.Config{Overrides: []goagent.Override{{ID: p.ID, Rules: &p.Rules}}})
	if err != nil {
		t.Fatal(err)
	}
	if registry.Diagnose(goagent.Process{Name: "unrelated", Args: []string{value}}).Selected == nil {
		t.Fatal("literal hostile value did not match")
	}
	if registry.Diagnose(goagent.Process{Name: "unrelated", Args: []string{"echo x"}}).Selected != nil {
		t.Fatal("value was interpreted instead of matched literally")
	}
}

func TestAgentRuleEditorCancelAndValidation(t *testing.T) {
	s := rulePanel(t)
	before := ruleJSON(s)
	s.openAgentRules()
	s.agentRuleAction("add-rule")
	s.agentRuleAction("apply")
	if s.agents.rulesEditor == nil || s.agents.rulesEditor.message == "" || ruleJSON(s) != before {
		t.Fatal("empty condition must fail without overwriting JSON")
	}
	ruleValue(s, "discard me")
	s.agentRuleAction("cancel")
	if ruleJSON(s) != before || s.agentDirty() {
		t.Fatal("cancel mutated the settings draft")
	}
	for _, f := range s.fields {
		if f.group == "AgentRules" {
			t.Fatal("modal field leaked after close")
		}
	}
}

func TestAgentRuleEditorRetainsOtherRulesAndConditions(t *testing.T) {
	s := rulePanel(t)
	initial, err := parseAgentRules(ruleJSON(s))
	if err != nil {
		t.Fatal(err)
	}
	s.openAgentRules()
	s.agentRuleAction("add-rule")
	ruleValue(s, "node")
	s.agentRuleAction("add-condition")
	ruleValue(s, "/opt/agent.js")
	s.agentRuleAction("previous-condition")
	s.agentRuleAction("apply")
	got, err := parseAgentRules(ruleJSON(s))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got[:len(initial)], initial) {
		t.Fatal("existing rules changed")
	}
	last := got[len(got)-1].All
	if len(last) != 2 || last[0].Value != "node" || last[1].Value != "/opt/agent.js" {
		t.Fatalf("conditions lost: %#v", last)
	}
	s.openAgentRules()
	for len(s.agents.rulesEditor.rules) > 0 {
		s.agentRuleAction("remove-rule")
	}
	s.agentRuleAction("apply")
	if ruleJSON(s) != "[]" {
		t.Fatalf("empty rules=%q", ruleJSON(s))
	}
}

func TestAgentRuleEditorRejectsLossyJSONAndInvalidGlob(t *testing.T) {
	for _, text := range []string{
		`[{"all":[{"field":"argument","op":"exact","value":"x","typo":true}]}]`,
		`[] []`,
		`[{"all":[]}]`,
		`[{"all":[{"field":"argument","op":"glob","value":"["}]}]`,
	} {
		if _, err := parseAgentRules(text); err == nil {
			t.Fatalf("invalid rules accepted: %s", text)
		}
	}
}
