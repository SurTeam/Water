package goui

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"github.com/SurTeam/Water/internal/goagent"
	"github.com/google/uuid"
)

type agentPanelResult struct {
	revision   uint64
	instance   uuid.UUID
	config     *goagent.Config
	diagnostic *goagent.Diagnostic
	message    string
	err        error
}
type agentPanel struct {
	revision        uint64
	instance        uuid.UUID
	config, initial goagent.Config
	selected        goagent.Kind
	loaded, busy    bool
	result          chan agentPanelResult
	message         string
	diagnostic      *goagent.Diagnostic
	page            int
	rulesEditor     *agentRulesEditor
}

func agentMetadata(raw map[string]any) (*goagent.Profile, *goagent.Process) {
	var profile *goagent.Profile
	var run *goagent.Process
	if value := raw["profile"]; value != nil {
		data, _ := json.Marshal(value)
		_ = json.Unmarshal(data, &profile)
	}
	if value := raw["run"]; value != nil {
		data, _ := json.Marshal(value)
		_ = json.Unmarshal(data, &run)
	}
	return profile, run
}
func agentMappedStatus(profile *goagent.Profile, state int) string {
	if profile != nil && len(profile.OSC) == 5 && state >= 0 && state < 5 {
		return profile.OSC[state]
	}
	return agentProgressStatus(state)
}
func (s *settingsPanel) agentProfile() (goagent.Profile, error) {
	var p goagent.Profile
	for _, candidate := range goagent.Profiles(s.agents.config) {
		if candidate.ID == s.agents.selected {
			p = candidate
			break
		}
	}
	if p.ID == "" {
		return p, errors.New("select an Agent")
	}
	for _, f := range s.fields {
		if f.group != "Agents" {
			continue
		}
		value := strings.TrimSpace(f.editor.Text())
		switch f.name {
		case "Name":
			p.Name = value
		case "Enabled":
			p.Enabled = f.toggle.Value
		case "Priority":
			n, err := strconv.Atoi(value)
			if err != nil {
				return p, errors.New("priority must be an integer")
			}
			p.Priority = n
		case "Color":
			p.Color = value
		case "Rules":
			if err := json.Unmarshal([]byte(value), &p.Rules); err != nil {
				return p, fmt.Errorf("recognition rules: %w", err)
			}
		case "StatusSource":
			p.StatusSource = value
		case "OSC":
			if value == "" {
				p.OSC = goagent.DefaultOSC()
			} else {
				p.OSC = strings.Split(value, ",")
				for i := range p.OSC {
					p.OSC[i] = strings.TrimSpace(p.OSC[i])
				}
			}
		case "Interrupt":
			p.Interrupt = value
		case "Exit":
			p.Exit = value
		case "PreserveClear":
			p.ScrollbackOnClearScreen = f.toggle.Value
		case "LongRunSeconds":
			n, err := strconv.Atoi(value)
			if err != nil {
				return p, errors.New("long-run threshold must be an integer")
			}
			p.LongRunSeconds = n
		}
	}
	return p, goagent.Validate(p)
}
func (s *settingsPanel) agentDirty() bool {
	if !s.agents.loaded {
		return false
	}
	cfg := goagent.Clone(s.agents.config)
	if s.agents.selected != "" {
		p, err := s.agentProfile()
		if err != nil {
			return true
		}
		cfg.Put(p)
	}
	// Empty builtin overrides have no semantic difference.
	return !reflect.DeepEqual(goagent.Profiles(cfg), goagent.Profiles(s.agents.initial))
}
func (s *settingsPanel) stageAgent() error {
	if s.agents.selected == "" {
		return nil
	}
	p, err := s.agentProfile()
	if err == nil {
		s.agents.config.Put(p)
	}
	return err
}
func (s *settingsPanel) agentFields(id goagent.Kind) {
	fields := s.fields[:0]
	for _, f := range s.fields {
		if f.group != "Agents" {
			fields = append(fields, f)
		}
	}
	s.fields = fields
	s.agents.selected = id
	s.focus = -1
	var p goagent.Profile
	for _, a := range goagent.Profiles(s.agents.config) {
		if a.ID == id {
			p = a
			break
		}
	}
	if p.ID == "" {
		return
	}
	rules, _ := json.Marshal(p.Rules)
	values := []struct {
		name, label, value string
		boolean            bool
		checked            bool
	}{
		{"Name", "Display name", p.Name, false, false},
		{"Enabled", "Enabled", "", true, p.Enabled},
		{"Priority", "Recognition priority", strconv.Itoa(p.Priority), false, false},
		{"Color", "Color (#RRGGBB or inherit)", p.Color, false, false},
		{"Rules", "Recognition rules (JSON)", string(rules), false, false},
		{"StatusSource", "Status source", p.StatusSource, false, false},
		{"OSC", "OSC states 0,1,2,3,4", strings.Join(p.OSC, ","), false, false},
		{"Interrupt", "Interrupt preset", p.Interrupt, false, false},
		{"Exit", "Exit preset", p.Exit, false, false},
		{"PreserveClear", "Keep history on clear", "", true, p.ScrollbackOnClearScreen},
		{"LongRunSeconds", "Long-run seconds (0 = global)", strconv.Itoa(p.LongRunSeconds), false, false},
	}
	for _, value := range values {
		f := settingsField{group: "Agents", name: value.name, label: value.label, kind: reflect.String}
		f.editor.SingleLine = true
		f.editor.SetText(value.value)
		if value.boolean {
			f.kind = reflect.Bool
			f.toggle.Value = value.checked
		}
		s.fields = append(s.fields, f)
	}
}
func (c *WorkspaceClient) agentWork(work func() agentPanelResult) {
	a := &c.settings.agents
	if a.busy {
		return
	}
	a.busy = true
	a.message = "Working…"
	result := make(chan agentPanelResult, 1)
	a.result = result
	go func() {
		result <- work()
		if c.invalidate != nil {
			c.invalidate()
		}
	}()
}
func (c *WorkspaceClient) loadAgentSettings() {
	if c.settings.agents.loaded || c.settings.agents.busy {
		return
	}
	c.agentWork(func() agentPanelResult {
		if c.session == nil {
			return agentPanelResult{err: errors.New("Agent settings require a connected server")}
		}
		var out struct {
			Config   goagent.Config `json:"config"`
			Revision uint64         `json:"revision"`
			Instance uuid.UUID      `json:"instance_id"`
		}
		err := c.session.Dispatch(map[string]any{"type": "agents.inspect"}, &out)
		return agentPanelResult{config: &out.Config, revision: out.Revision, instance: out.Instance, err: err}
	})
}
func (c *WorkspaceClient) pollAgentSettings() {
	a := &c.settings.agents
	if a.result == nil {
		return
	}
	select {
	case result := <-a.result:
		a.result = nil
		a.busy = false
		if result.err != nil {
			a.message = result.err.Error()
			return
		}
		if result.config != nil {
			a.revision, a.instance = result.revision, result.instance
			a.config = goagent.Clone(*result.config)
			a.initial = goagent.Clone(a.config)
			a.loaded = true
			id := a.selected
			if id == "" {
				id = goagent.Profiles(a.config)[0].ID
			}
			c.settings.agentFields(id)
		}
		if result.diagnostic != nil {
			a.diagnostic = result.diagnostic
		}
		a.message = result.message
	default:
	}
}
func (c *WorkspaceClient) agentSettingsAction(action string) {
	s := &c.settings
	a := &s.agents
	if a.busy {
		return
	}
	if action == "reload" {
		if s.agentDirty() {
			a.message = "Save or discard changes before reloading"
			return
		}
		a.loaded = false
		c.loadAgentSettings()
		return
	}
	if action == "interrupt" || action == "exit" {
		c.controlActiveAgent(action)
		return
	}
	if !a.loaded {
		c.loadAgentSettings()
		return
	}
	if action == "restore" && !goagent.IsBuiltin(a.selected) {
		a.message = "Custom Agents do not have builtin defaults"
		return
	}
	if action == "restore" || action == "delete" {
		if action == "delete" && goagent.IsBuiltin(a.selected) {
			a.message = "Disable a builtin Agent, or restore its defaults"
			return
		}
		id := a.selected
		a.config.Remove(id)
		if action == "delete" {
			id = goagent.Profiles(a.config)[0].ID
		}
		s.agentFields(id)
		a.message = "Changes are not saved"
		return
	}
	if err := s.stageAgent(); err != nil {
		a.message = err.Error()
		return
	}
	switch {
	case strings.HasPrefix(action, "select:"):
		s.agentFields(goagent.Kind(strings.TrimPrefix(action, "select:")))
	case action == "previous" || action == "next":
		profiles := goagent.Profiles(a.config)
		i := 0
		for j, p := range profiles {
			if p.ID == a.selected {
				i = j
				break
			}
		}
		if action == "previous" {
			i = (i + len(profiles) - 1) % len(profiles)
		} else {
			i = (i + 1) % len(profiles)
		}
		s.agentFields(profiles[i].ID)
	case action == "new" || action == "duplicate":
		p := goagent.Profile{Name: "My Agent", Enabled: true, OSC: goagent.DefaultOSC(), StatusSource: "auto", Interrupt: "ctrl-c", Exit: "sigterm", ScrollbackOnClearScreen: true, Rules: []goagent.Rule{{All: []goagent.Condition{{Field: "executable", Op: "exact", Value: "my-agent"}}}}}
		if action == "duplicate" {
			var err error
			p, err = s.agentProfile()
			if err != nil {
				a.message = err.Error()
				return
			}
			p.Name += " copy"
		}
		p.ID = goagent.Kind(uuid.NewString())
		a.config.Put(p)
		s.agentFields(p.ID)
		a.message = "Changes are not saved"
	case action == "save":
		cfg := goagent.Clone(a.config)
		revision, instance := a.revision, a.instance
		c.agentWork(func() agentPanelResult {
			var out struct {
				Config   goagent.Config `json:"config"`
				Revision uint64         `json:"revision"`
				Instance uuid.UUID      `json:"instance_id"`
			}
			err := c.session.Dispatch(map[string]any{"type": "agents.configure", "config": cfg, "expected_revision": revision, "expected_instance": instance}, &out)
			return agentPanelResult{config: &out.Config, revision: out.Revision, instance: out.Instance, message: "Agent settings saved", err: err}
		})
	case action == "test":
		cfg := goagent.Clone(a.config)
		c.mu.RLock()
		pane := c.state.FocusedPane
		c.mu.RUnlock()
		c.agentWork(func() agentPanelResult {
			var out goagent.Diagnostic
			err := c.session.Dispatch(map[string]any{"type": "agents.test", "config": cfg, "pane_id": pane}, &out)
			message := "No recognition rule matched"
			if out.Selected != nil {
				message = "Matched: " + out.Selected.Name
			}
			return agentPanelResult{diagnostic: &out, message: message, err: err}
		})
	}
}
func (c *WorkspaceClient) controlActiveAgent(action string) {
	c.mu.RLock()
	state := c.state
	c.mu.RUnlock()
	if state.FocusedPane == nil {
		c.settings.agents.message = "No focused pane"
		return
	}
	for _, raw := range state.Agents {
		a, ok := raw.(map[string]any)
		if !ok || nativeUUID(a["pane_id"]) != *state.FocusedPane {
			continue
		}
		kind := goagent.Kind(fmt.Sprint(a["kind"]))
		if kind != c.settings.agents.selected {
			c.settings.agents.message = "Select the focused terminal’s Agent before controlling it"
			return
		}
		_, run := agentMetadata(a)
		if run == nil {
			c.settings.agents.message = "Waiting for foreground run identity"
			return
		}
		pane := *state.FocusedPane
		c.agentWork(func() agentPanelResult {
			err := c.session.Dispatch(map[string]any{"type": "agent.control", "pane_id": pane, "expected": run, "action": action, "expected_agent_id": kind}, nil)
			return agentPanelResult{message: "Agent action completed", err: err}
		})
		return
	}
	c.settings.agents.message = "No recognized Agent in the focused pane"
}
