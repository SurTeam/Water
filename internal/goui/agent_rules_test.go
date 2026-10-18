package goui

import (
	"encoding/json"
	"reflect"
	"strconv"
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
	data, _ := json.Marshal(s.agents.rules)
	return string(data)
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
	initial := cloneAgentRules(s.agents.rules)
	s.openAgentRules()
	s.agentRuleAction("add-rule")
	ruleValue(s, "node")
	s.agentRuleAction("add-condition")
	ruleValue(s, "/opt/agent.js")
	s.agentRuleAction("previous-condition")
	s.agentRuleAction("apply")
	got := s.agents.rules
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

func TestAgentRuleEditorRejectsInvalidConditions(t *testing.T) {
	for _, rule := range []goagent.Rule{
		{},
		{All: []goagent.Condition{{Field: "unknown", Op: "exact", Value: "x"}}},
		{All: []goagent.Condition{{Field: "argument", Op: "glob", Value: "["}}},
	} {
		if err := validateAgentRules([]goagent.Rule{rule}); err == nil {
			t.Fatalf("invalid rule accepted: %#v", rule)
		}
	}
}

func TestAgentRulesRowIsNotATextFocusTarget(t *testing.T) {
	s := rulePanel(t)
	s.group = 6
	for _, row := range settingsRows(s) {
		if row.field >= 0 && s.fields[row.field].name == "Rules" {
			t.Fatal("rules row exposed to text focus navigation")
		}
	}
	for _, f := range s.fields {
		if f.group == "Agents" && f.name == "Rules" && f.editor.Text() != "" {
			t.Fatal("raw JSON retained in an editor")
		}
	}
}

func TestAgentRuleConditionPagesReserveEditorAndFooter(t *testing.T) {
	// overlayPanel leaves 96 logical pixels outside the panel.
	for _, height := range []int{504, 584, 680} {
		for selected := 0; selected < 8; selected++ {
			start, end := agentRuleConditionPage(height, 8, selected)
			if start > selected || end <= selected || start < 0 || end > 8 {
				t.Fatalf("height %d selection %d outside [%d,%d)", height, selected, start, end)
			}
			// Heading and pager: 190; editor and message: 178;
			// footer begins 48 pixels above the panel bottom.
			if bottom := 190 + (end-start)*22 + 178; bottom > height-48 {
				t.Fatalf("height %d: content bottom %d overlaps footer %d", height, bottom, height-48)
			}
		}
	}
}

func TestAgentRuleEditorNavigatesAllEightConditions(t *testing.T) {
	s := rulePanel(t)
	s.openAgentRules()
	s.agentRuleAction("add-rule")
	for i := 1; i < 8; i++ {
		s.agentRuleAction("add-condition")
	}
	for i := 7; i >= 0; i-- {
		if s.agents.rulesEditor.condition != i {
			t.Fatalf("previous selected %d, want %d", s.agents.rulesEditor.condition, i)
		}
		s.agentRuleAction("previous-condition")
	}
	for i := 0; i < 8; i++ {
		if s.agents.rulesEditor.condition != i {
			t.Fatalf("next selected %d, want %d", s.agents.rulesEditor.condition, i)
		}
		s.agentRuleAction("next-condition")
	}
	for _, i := range []int{2, 6, 0, 7} {
		s.agentRuleAction("condition:" + strconv.Itoa(i))
		if s.agents.rulesEditor.condition != i {
			t.Fatalf("direct selection failed for %d", i)
		}
	}
}
