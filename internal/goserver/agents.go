package goserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/SurTeam/Water/internal/goagent"
	"github.com/SurTeam/Water/internal/goconfig"
	"github.com/google/uuid"
)

func (s *Server) applyForeground(id uuid.UUID, p goagent.Process) bool {
	return s.model.SetTerminalForegroundAgent(id, p.Name, s.agentRegistry.Detect(p))
}

func (s *Server) agentCommand(kind string, raw json.RawMessage) (any, error) {
	switch kind {
	case "agents.inspect":
		return map[string]any{"config": s.agentConfig, "profiles": goagent.Profiles(s.agentConfig), "config_path": s.ConfigPath, "revision": s.agentRevision, "instance_id": s.InstanceID}, nil
	case "agents.configure":
		var c struct {
			Config           json.RawMessage `json:"config"`
			ExpectedRevision *uint64         `json:"expected_revision,omitempty"`
			ExpectedInstance *uuid.UUID      `json:"expected_instance,omitempty"`
		}
		if err := json.Unmarshal(raw, &c); err != nil {
			return nil, err
		}
		if c.ExpectedRevision != nil && *c.ExpectedRevision != s.agentRevision || c.ExpectedInstance != nil && *c.ExpectedInstance != s.InstanceID {
			return nil, errors.New("Agent settings changed on this server; cancel and reopen to reload")
		}
		cfg, err := goagent.ParseConfig(c.Config)
		if err != nil {
			return nil, err
		}
		registry, err := goagent.Compile(cfg)
		if err != nil {
			return nil, err
		}
		processes, err := s.registry.SampleForegroundProcesses(context.Background())
		if err != nil {
			return nil, err
		}
		if s.ConfigPath != "" {
			if err := goconfig.SaveAgents(s.ConfigPath, cfg); err != nil {
				return nil, err
			}
		}
		s.agentConfig = goagent.Clone(cfg)
		s.agentRegistry = registry
		s.agentRevision++
		for id, p := range processes {
			s.applyForeground(id, p)
		}
		return map[string]any{"config": s.agentConfig, "profiles": goagent.Profiles(s.agentConfig), "revision": s.agentRevision, "instance_id": s.InstanceID}, nil
	case "agents.test":
		var c struct {
			Config     *goagent.Config `json:"config,omitempty"`
			PaneID     *uuid.UUID      `json:"pane_id,omitempty"`
			TerminalID *uuid.UUID      `json:"terminal_id,omitempty"`
		}
		if err := json.Unmarshal(raw, &c); err != nil {
			return nil, err
		}
		registry := s.agentRegistry
		if c.Config != nil {
			var err error
			registry, err = goagent.Compile(*c.Config)
			if err != nil {
				return nil, err
			}
		}
		id, err := s.resolveTerminal(c.TerminalID, c.PaneID)
		if err != nil {
			return nil, err
		}
		p, err := s.registry.ForegroundProcess(context.Background(), id)
		if err != nil {
			return nil, err
		}
		return registry.Diagnose(p), nil
	case "agent.control":
		var c struct {
			PaneID        *uuid.UUID       `json:"pane_id,omitempty"`
			TerminalID    *uuid.UUID       `json:"terminal_id,omitempty"`
			Expected      *goagent.Process `json:"expected"`
			Action        string           `json:"action"`
			ExpectedAgent goagent.Kind     `json:"expected_agent_id"`
		}
		if err := json.Unmarshal(raw, &c); err != nil {
			return nil, err
		}
		if c.Expected == nil {
			return nil, errors.New("expected foreground run identity is required")
		}
		id, err := s.resolveTerminal(c.TerminalID, c.PaneID)
		if err != nil {
			return nil, err
		}
		agent := s.agentRegistry.Detect(*c.Expected)
		if agent == nil {
			return nil, errors.New("expected run is not an enabled Agent")
		}
		if c.ExpectedAgent == "" || agent.Kind != c.ExpectedAgent {
			return nil, errors.New("Agent classification changed; refresh and retry")
		}
		var action string
		switch c.Action {
		case "interrupt":
			action = agent.Profile.Interrupt
		case "exit":
			action = agent.Profile.Exit
		case "force-stop":
			action = "sigkill"
		default:
			return nil, fmt.Errorf("unknown Agent action %q", c.Action)
		}
		if err := s.registry.ControlForeground(context.Background(), id, *c.Expected, action); err != nil {
			return nil, err
		}
		return map[string]any{"terminal_id": id, "action": c.Action, "preset": action}, nil
	}
	return nil, errors.New("unknown Agent command")
}
