package goui

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/SurTeam/Water/internal/goagent"
	"github.com/google/uuid"
)

// Map sampled process data directly to supported conditions. Do not interpret
// command syntax, infer language-specific entry points, or evaluate arguments.
func agentRuleForProcess(p goagent.Process) (goagent.Rule, error) {
	if p.Name == "" {
		return goagent.Rule{}, errors.New("No usable foreground process was sampled")
	}
	rule := goagent.Rule{All: []goagent.Condition{{Field: "executable", Op: "exact", Value: filepath.Base(p.Name)}}}
	// Args[0] is the executable; its basename is already represented above.
	for i, arg := range p.Args {
		if i == 0 {
			continue
		}
		rule.All = append(rule.All, goagent.Condition{Field: "argument", Op: "exact", Value: arg})
	}
	if err := validateAgentRules([]goagent.Rule{rule}); err != nil {
		return goagent.Rule{}, err
	}
	return rule, nil
}

func (c *WorkspaceClient) sampleAgentRule() {
	s := &c.settings
	e := s.agents.rulesEditor
	if e == nil || e.sampling || s.agents.busy {
		return
	}
	c.mu.RLock()
	pane := uuid.Nil
	if c.state.FocusedPane != nil {
		pane = *c.state.FocusedPane
	}
	c.mu.RUnlock()
	if pane == uuid.Nil || c.session == nil {
		e.message = "A connected terminal pane is required"
		return
	}
	s.stageAgentRuleValue()
	e.sampling = true
	e.message = "Sampling current pane…"
	session := c.session
	c.agentWork(func() agentPanelResult {
		var out goagent.Diagnostic
		err := session.Dispatch(map[string]any{"type": "agents.test", "pane_id": pane}, &out)
		return agentPanelResult{rulesEditor: e, samplePane: pane, diagnostic: &out, err: err}
	})
}

func (c *WorkspaceClient) applyAgentRuleSample(result agentPanelResult) {
	s := &c.settings
	e := s.agents.rulesEditor
	if e == nil || e != result.rulesEditor {
		return
	}
	e.sampling = false
	c.mu.RLock()
	pane := uuid.Nil
	if c.state.FocusedPane != nil {
		pane = *c.state.FocusedPane
	}
	c.mu.RUnlock()
	if pane != result.samplePane {
		e.message = "The focused pane changed; sample again"
		return
	}
	if result.err != nil {
		e.message = result.err.Error()
		return
	}
	if result.diagnostic == nil {
		e.message = "No usable foreground process was sampled"
		return
	}
	rule, err := agentRuleForProcess(result.diagnostic.Process)
	if err != nil {
		e.message = err.Error()
		return
	}
	if len(e.rules) == 0 {
		e.rules = []goagent.Rule{rule}
		e.rule = 0
	} else {
		e.rules[e.rule] = rule
	}
	e.condition = 0
	e.message = fmt.Sprintf(c.tr("Filled from current pane: %s. Review before applying."), result.diagnostic.Process.Name)
	s.loadAgentRuleValue()
}
