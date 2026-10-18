package goui

import (
	"reflect"
	"strconv"
	"strings"

	"github.com/SurTeam/Water/internal/goagent"
)

// A private typed draft: only Apply replaces the panel's recognition rules.
type agentRulesEditor struct {
	rules           []goagent.Rule
	rule, condition int
	message         string
	previousFocus   int
	sampling        bool
}

// agentRuleConditionPage reserves room for the heading, paging controls,
// condition editor, validation message, and footer. Dimensions are logical pixels.
func agentRuleConditionPage(panelHeight, count, selected int) (start, end int) {
	capacity := max(1, (panelHeight-416)/22)
	selected = min(max(0, selected), max(0, count-1))
	start = selected / capacity * capacity
	return start, min(count, start+capacity)
}

func cloneAgentRules(rules []goagent.Rule) []goagent.Rule {
	if rules == nil {
		return nil
	}
	copy := make([]goagent.Rule, len(rules))
	for i, rule := range rules {
		copy[i].All = append([]goagent.Condition(nil), rule.All...)
	}
	return copy
}

func validateAgentRules(rules []goagent.Rule) error {
	p := goagent.Builtins()[0]
	p.Rules = rules
	return goagent.Validate(p)
}

func (s *settingsPanel) openAgentRules() {
	if !s.agents.loaded || s.agents.selected == "" || s.agents.rulesEditor != nil {
		return
	}
	s.agents.rulesEditor = &agentRulesEditor{rules: cloneAgentRules(s.agents.rules), previousFocus: s.focus}
	s.loadAgentRuleValue()
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
	if e.sampling {
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
		s.agents.rules = cloneAgentRules(e.rules)
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
		if strings.HasPrefix(action, "condition:") {
			index, err := strconv.Atoi(strings.TrimPrefix(action, "condition:"))
			if err == nil && len(e.rules) > 0 && index >= 0 && index < len(e.rules[e.rule].All) {
				e.condition = index
				s.loadAgentRuleValue()
			}
			return
		}
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

func agentRuleFieldLabel(field string) string {
	switch field {
	case "executable":
		return "Program name"
	case "name":
		return "Process or launcher name"
	case "path":
		return "Program or script path"
	case "argument":
		return "Command argument"
	}
	return field
}
func agentRuleOpLabel(op string) string {
	switch op {
	case "exact":
		return "equals"
	case "contains":
		return "contains text"
	case "glob":
		return "matches wildcard"
	}
	return op
}
