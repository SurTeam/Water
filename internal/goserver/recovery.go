package goserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/SurTeam/Water/internal/gomodel"
	"github.com/SurTeam/Water/internal/goprotocol"
	"github.com/SurTeam/Water/internal/goterminal"
	"github.com/google/uuid"
)

type recoveryLease struct {
	token    uuid.UUID
	owner    uuid.UUID
	revision uint64
	expires  time.Time
}

type RecoveryPrepared struct {
	Token  uuid.UUID              `json:"token"`
	Layout gomodel.RecoveryLayout `json:"layout"`
}

func (s *Server) descriptor() goprotocol.Descriptor {
	return goprotocol.Descriptor{BuildVariant: s.Build, Version: s.Version, ProtocolVersion: goprotocol.ProtocolVersion, APISignature: goprotocol.APISignature, ServerRevision: s.Revision, Capabilities: goprotocol.ServerCapabilities(), RequiredCapabilities: []string{"terminal-stream/v1", "window-selection/v1"}}
}
func (s *Server) serverInfo() map[string]any {
	// Preserve the established server.info fields and windows list.
	raw, _ := json.Marshal(goprotocol.ServerInfo{Descriptor: s.descriptor(), ServerVersion: s.Version, ServerPID: os.Getpid(), InstanceID: s.InstanceID.String(), SocketPath: s.SocketPath, UISessions: s.uiSessionCount(), RecoverySchema: gomodel.RecoverySchema})
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	out["windows"] = s.windowSessions()
	out["web"] = s.web.status()
	return out
}

func (s *Server) recoveryRequest(ss *session, msg goprotocol.WireMessage) error {
	s.commandMu.Lock()
	defer s.commandMu.Unlock()
	var p struct {
		Token            uuid.UUID `json:"token"`
		ExpectedInstance string    `json:"expected_instance"`
	}
	if len(msg.Params) > 0 {
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			return err
		}
	}
	if msg.Method == "recovery.prepare" {
		if p.ExpectedInstance != "" && p.ExpectedInstance != s.InstanceID.String() {
			return errors.New("server instance changed; inspect it before preparing recovery")
		}
		if s.recovery != nil && time.Now().Before(s.recovery.expires) {
			return errors.New("another recovery operation is in progress")
		}
		if s.uiSessionCount() > 1 {
			return errors.New("close other Water windows connected to this server before restarting")
		}
		deadline := time.Now().Add(10 * time.Second)
		d := s.model.ExportLayout(s.InstanceID.String())
		var fill func(*gomodel.RecoveryNode) error
		fill = func(n *gomodel.RecoveryNode) error {
			if n.PaneID == uuid.Nil {
				if err := fill(n.First); err != nil {
					return err
				}
				return fill(n.Second)
			}
			if !n.Terminal {
				return nil
			}
			if time.Now().After(deadline) {
				return errors.New("current directory export timed out; server was not stopped")
			}
			id, ok := s.model.TerminalForPane(n.PaneID)
			if !ok {
				return errors.New("terminal changed during recovery export")
			}
			term, ok := s.registry.Get(id)
			if !ok {
				return errors.New("terminal unavailable during export")
			}
			n.CWD = term.CurrentDirectory()
			if !filepath.IsAbs(n.CWD) {
				return fmt.Errorf("cannot determine current directory for pane %s; server was not stopped", n.PaneID)
			}
			return nil
		}
		for _, w := range d.Workspaces {
			for _, t := range w.Tabs {
				if err := fill(t.Tree); err != nil {
					return err
				}
			}
		}
		if err := d.Validate(); err != nil {
			return err
		}
		if s.model.Revision() != d.Revision {
			return errors.New("layout changed during recovery export; retry")
		}
		token := uuid.New()
		s.recovery = &recoveryLease{token: token, owner: ss.id, revision: d.Revision, expires: time.Now().Add(30 * time.Second)}
		return ss.write(goprotocol.Success(msg.RequestID, RecoveryPrepared{Token: token, Layout: d}))
	}
	lease := s.recovery
	if lease == nil || lease.token != p.Token || lease.owner != ss.id {
		return errors.New("invalid recovery token")
	}
	if msg.Method == "recovery.cancel" {
		s.recovery = nil
		return ss.write(goprotocol.Success(msg.RequestID, map[string]bool{"cancelled": true}))
	}
	if time.Now().After(lease.expires) {
		s.recovery = nil
		return errors.New("recovery token expired; export again")
	}
	if s.uiSessionCount() > 1 || s.model.Revision() != lease.revision {
		s.recovery = nil
		return errors.New("server layout or windows changed; server was not stopped")
	}
	if err := ss.write(goprotocol.Success(msg.RequestID, map[string]bool{"ack": true})); err != nil {
		return err
	}
	go s.Close()
	return nil
}

// Shells are prepared before committing the model, so a missing directory or
// spawn failure leaves the replacement server empty and the artifact retryable.
func (s *Server) restoreLayout(d gomodel.RecoveryLayout) error {
	if s.lastRecovery == d.ID && d.ID != uuid.Nil {
		return nil
	}
	if len(s.model.Dump().Workspaces) > 0 {
		return errors.New("recovery requires an empty server; existing workspaces were left untouched")
	}
	prepared, panes, err := gomodel.RecoveryModel(d)
	if err != nil {
		return err
	}
	type spawned struct {
		term *goterminal.Terminal
		pane uuid.UUID
		meta gomodel.TerminalMeta
	}
	var terms []spawned
	committed := false
	defer func() {
		if !committed {
			for _, t := range terms {
				s.registry.Remove(t.term.ID)
			}
		}
	}()
	for _, pane := range panes {
		n := pane.Node
		if !n.Terminal {
			continue
		}
		stat, err := os.Stat(n.CWD)
		if err != nil || !stat.IsDir() {
			return fmt.Errorf("directory unavailable for pane %s: %s", pane.ID, n.CWD)
		}
		program := s.Config.Shell.Program
		args := append([]string(nil), s.Config.Shell.Args...)
		t, err := s.registry.SpawnWithDir(program, args, n.Size, n.CWD)
		if err != nil {
			return fmt.Errorf("restore pane %s: %w", pane.ID, err)
		}
		meta := gomodel.TerminalMeta{TerminalID: t.ID, SessionID: uuid.New(), Program: program, Args: args, Size: t.Size(), ProcessName: strings.TrimLeft(filepath.Base(program), "-"), CWD: n.CWD, AgentLabel: n.AgentLabel}
		terms = append(terms, spawned{t, pane.ID, meta})
		if err := prepared.InstallTerminal(pane.ID, meta); err != nil {
			return err
		}
	}
	if err := s.model.AdoptRecovery(prepared); err != nil {
		return err
	}
	committed = true
	s.lastRecovery = d.ID
	for _, t := range terms {
		_, _ = s.watchTerminal(t.term, t.pane, t.meta)
	}
	return nil
}
