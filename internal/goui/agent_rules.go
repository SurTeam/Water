package goui

import (
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"

	"github.com/SurTeam/Water/internal/goagent"
)

// A private draft: only Apply writes serialized rules into the settings field.
type agentRulesEditor struct {
	rules           []goagent.Rule
	rule, condition int
	message         string
	previousFocus   int
}

func parseAgentRules(text string) ([]goagent.Rule, error) {
	var rules []goagent.Rule
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&rules); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("Expected one JSON rules array")
	}
	if err := validateAgentRules(rules); err != nil {
		return nil, err
	}
	return rules, nil
}

func validateAgentRules(rules []goagent.Rule) error {
	p := goagent.Builtins()[0]
	p.Rules = rules
	return goagent.Validate(p)
}

func (s *settingsPanel) openAgentRules() {
	for _, f := range s.fields {
		if f.group != "Agents" || f.name != "Rules" {
			continue
		}
		rules, err := parseAgentRules(f.editor.Text())
		if err != nil {
			s.agents.message = "Recognition rules: " + err.Error()
			return
		}
		s.agents.rulesEditor = &agentRulesEditor{rules: rules, previousFocus: s.focus}
		s.loadAgentRuleValue()
		return
	}
}

func (s *settingsPanel) loadAgentRuleValue() {
	fields := s.fields[:0]
	for _, f := range s.fields {
		if f.group != "AgentRules" {
			fields = append(fields, f)
		}
	}
	s.fields = fields
	s.focus = -1
	e := s.agents.rulesEditor
	if e == nil || len(e.rules) == 0 {
		return
	}
	e.rule = min(max(0, e.rule), len(e.rules)-1)
	all := e.rules[e.rule].All
	e.condition = min(max(0, e.condition), len(all)-1)
	f := settingsField{group: "AgentRules", name: "Value", kind: reflect.String}
	f.editor.SingleLine = true
	f.editor.SetText(all[e.condition].Value)
	s.fields = append(s.fields, f)
	s.focus = len(s.fields) - 1
}

func (s *settingsPanel) stageAgentRuleValue() {
	e := s.agents.rulesEditor
	if e == nil || len(e.rules) == 0 {
		return
	}
	for _, f := range s.fields {
		if f.group == "AgentRules" {
			e.rules[e.rule].All[e.condition].Value = f.editor.Text()
		}
	}
}

func (s *settingsPanel) closeAgentRules() {
	e := s.agents.rulesEditor
	if e == nil {
		return
	}
	fields := s.fields[:0]
	for _, f := range s.fields {
		if f.group != "AgentRules" {
			fields = append(fields, f)
		}
	}
	s.fields = fields
	s.focus = e.previousFocus
	s.agents.rulesEditor = nil
}

func (s *settingsPanel) agentRuleAction(action string) {
	e := s.agents.rulesEditor
	if e == nil {
		return
	}
	if action == "cancel" {
		s.closeAgentRules()
		return
	}
	s.stageAgentRuleValue()
	e.message = ""
	switch action {
	case "apply":
		if err := validateAgentRules(e.rules); err != nil {
			e.message = err.Error()
			return
		}
		rules := e.rules
		if rules == nil {
			rules = []goagent.Rule{}
		}
		data, err := json.Marshal(rules)
		if err != nil {
			e.message = err.Error()
			return
		}
		for i := range s.fields {
			f := &s.fields[i]
			if f.group == "Agents" && f.name == "Rules" {
				f.editor.SetText(string(data))
			}
		}
		s.closeAgentRules()
		s.agents.message = "Changes are not saved"
		return
	case "add-rule":
		if len(e.rules) >= 64 {
			e.message = "At most 64 rules"
			return
		}
		e.rules = append(e.rules, goagent.Rule{All: []goagent.Condition{{Field: "executable", Op: "exact"}}})
		e.rule, e.condition = len(e.rules)-1, 0
	case "remove-rule":
		if len(e.rules) > 0 {
			e.rules = append(e.rules[:e.rule], e.rules[e.rule+1:]...)
			e.rule = max(0, e.rule-1)
			e.condition = 0
		}
	case "previous-rule", "next-rule":
		delta := 1
		if action == "previous-rule" {
			delta = -1
		}
		e.rule = min(max(0, e.rule+delta), len(e.rules)-1)
		e.condition = 0
	default:
		if len(e.rules) == 0 {
			return
		}
		rule := &e.rules[e.rule]
		switch action {
		case "add-condition":
			if len(rule.All) >= 8 {
				e.message = "At most 8 conditions per rule"
				return
			}
			rule.All = append(rule.All, goagent.Condition{Field: "argument", Op: "exact"})
			e.condition = len(rule.All) - 1
		case "remove-condition":
			if len(rule.All) == 1 {
				e.message = "Each rule needs at least one condition"
				return
			}
			rule.All = append(rule.All[:e.condition], rule.All[e.condition+1:]...)
			e.condition = max(0, e.condition-1)
		case "previous-condition", "next-condition":
			delta := 1
			if action == "previous-condition" {
				delta = -1
			}
			e.condition = min(max(0, e.condition+delta), len(rule.All)-1)
		case "field", "op":
			condition := &rule.All[e.condition]
			choices, target := []string{"executable", "name", "path", "argument"}, &condition.Field
			if action == "op" {
				choices, target = []string{"exact", "contains", "glob"}, &condition.Op
			}
			for i, choice := range choices {
				if *target == choice {
					*target = choices[(i+1)%len(choices)]
					break
				}
			}
		}
	}
	s.loadAgentRuleValue()
}
