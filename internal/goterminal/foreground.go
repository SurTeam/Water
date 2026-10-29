package goterminal

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/SurTeam/Water/internal/goagent"
	"github.com/google/uuid"
)

func (t *Terminal) ForegroundGroup() (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	return t.ForegroundGroupContext(ctx)
}

func (t *Terminal) ForegroundGroupContext(ctx context.Context) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if t.supervised != nil {
		// The supervisor owns the PTY master, so only it can read TIOCGPGRP.
		return t.supervised.ForegroundContext(ctx, t.supervisedKeyUUID())
	}
	group, err := t.ptmx.ForegroundGroup()
	if err != nil {
		return 0, err
	}
	if group <= 1 {
		return 0, errors.New("no live foreground group")
	}
	return group, nil
}

// ForegroundProcesses keeps process identity and arguments intact for server
// recognition. Call only on a worker; process queries have a bounded deadline.
func (r *Registry) ForegroundProcesses(ctx context.Context) map[uuid.UUID]goagent.Process {
	result, _ := r.SampleForegroundProcesses(ctx)
	return result
}

// SampleForegroundProcesses reports sampling failures so configuration changes
// can be rejected before persisting a registry that could not be applied.
func (r *Registry) SampleForegroundProcesses(ctx context.Context) (map[uuid.UUID]goagent.Process, error) {
	return r.foregroundProcesses(ctx)
}
func (r *Registry) ForegroundProcess(ctx context.Context, id uuid.UUID) (goagent.Process, error) {
	r.mu.RLock()
	t := r.terms[id]
	r.mu.RUnlock()
	if t == nil {
		return goagent.Process{}, errors.New("terminal not found")
	}
	processes, err := sampleTerminals(ctx, []*Terminal{t})
	if err != nil {
		return goagent.Process{}, err
	}
	if process, ok := processes[id]; ok {
		return process, nil
	}
	return goagent.Process{}, errors.New("no live foreground process could be sampled for this terminal")
}
func (r *Registry) foregroundProcesses(ctx context.Context) (map[uuid.UUID]goagent.Process, error) {
	r.mu.RLock()
	terms := make([]*Terminal, 0, len(r.terms))
	for _, t := range r.terms {
		terms = append(terms, t)
	}
	r.mu.RUnlock()
	return sampleTerminals(ctx, terms)
}
func sampleTerminals(ctx context.Context, terms []*Terminal) (map[uuid.UUID]goagent.Process, error) {
	return sampleTerminalsWithQuery(ctx, terms, (*Terminal).ForegroundGroupContext)
}

func sampleTerminalsWithQuery(ctx context.Context, terms []*Terminal, query func(*Terminal, context.Context) (int, error)) (map[uuid.UUID]goagent.Process, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("foreground sampling failed: %w", err)
	}
	groups := map[uuid.UUID]int{}
	unique := map[int]bool{}
	var pids []int
	for _, t := range terms {
		select {
		case <-t.closed:
			continue
		default:
		}
		group, err := query(t, ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil, fmt.Errorf("foreground group sampling failed: %w", ctx.Err())
			}
			continue
		}
		groups[t.ID] = group
		if !unique[group] {
			unique[group] = true
			pids = append(pids, group)
		}
	}
	result := map[uuid.UUID]goagent.Process{}
	if err := ctx.Err(); err != nil {
		return result, fmt.Errorf("foreground sampling failed: %w", err)
	}
	if len(pids) == 0 {
		return result, nil
	}
	processes, err := sampleProcessGroups(ctx, pids)
	if err != nil {
		return result, err
	}
	for id, group := range groups {
		if p, ok := processes[group]; ok {
			result[id] = p
		}
	}
	return result, nil
}

func parseForegroundOutput(output []byte, queryErr, contextErr error) (map[int]goagent.Process, error) {
	if contextErr != nil {
		return nil, fmt.Errorf("foreground ps sampling failed: %w", contextErr)
	}
	if queryErr != nil && len(output) == 0 {
		return nil, fmt.Errorf("foreground ps sampling failed: %w", queryErr)
	}
	// A disappearing PTY group may make ps return a nonzero exit status while
	// still returning complete records for other groups. Keep those records.
	processes := map[int]goagent.Process{}
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 8 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		pgid, err := strconv.Atoi(fields[1])
		if err != nil || pgid != pid {
			continue
		}
		processes[pid] = goagent.Process{PID: pid, PGID: pgid, Started: strings.Join(fields[2:7], " "), Name: strings.TrimLeft(filepath.Base(fields[7]), "-"), Args: fields[7:]}
	}
	return processes, nil
}

// Retained for diagnostic callers. Recognition consumes ForegroundProcesses.
func (r *Registry) ForegroundNames(ctx context.Context) map[uuid.UUID]string {
	result := map[uuid.UUID]string{}
	for id, p := range r.ForegroundProcesses(ctx) {
		name := p.Name
		if agent := goagent.Detect(p.Name, p.Args); agent != nil {
			for _, d := range goagent.Builtins() {
				if d.ID == agent.Kind {
					for _, rule := range d.Rules {
						if rule.All[0].Field == "name" {
							name = rule.All[0].Value
							break
						}
					}
					break
				}
			}
		}
		result[id] = name
	}
	return result
}
