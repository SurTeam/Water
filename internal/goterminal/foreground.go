package goterminal

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/SurTeam/Water/internal/goagent"
	"github.com/google/uuid"
	"golang.org/x/sys/unix"
)

func (t *Terminal) ForegroundGroup() (int, error) {
	var group int
	raw, err := t.ptmx.SyscallConn()
	if err != nil {
		return 0, err
	}
	var queryErr error
	err = raw.Control(func(fd uintptr) { group, queryErr = unix.IoctlGetInt(int(fd), unix.TIOCGPGRP) })
	if err != nil {
		return 0, err
	}
	if queryErr != nil {
		return 0, queryErr
	}
	if group <= 1 {
		return 0, errors.New("no live foreground group")
	}
	return group, nil
}

// ForegroundProcesses keeps process identity and arguments intact for server
// recognition. Call only on a worker; ps has a bounded deadline.
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
	processes, err := r.foregroundProcesses(ctx)
	if process, ok := processes[id]; ok {
		return process, nil
	}
	if err != nil {
		return goagent.Process{}, err
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
	groups := map[uuid.UUID]int{}
	unique := map[int]bool{}
	var pids []string
	for _, t := range terms {
		select {
		case <-t.closed:
			continue
		default:
		}
		group, err := t.ForegroundGroup()
		if err != nil {
			continue
		}
		groups[t.ID] = group
		if !unique[group] {
			unique[group] = true
			pids = append(pids, strconv.Itoa(group))
		}
	}
	result := map[uuid.UUID]goagent.Process{}
	if len(pids) == 0 {
		return result, nil
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "ps", "-p", strings.Join(pids, ","), "-o", "pid=", "-o", "pgid=", "-o", "lstart=", "-o", "args=").Output()
	processes, err := parseForegroundOutput(output, err, ctx.Err())
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

// Recheck both process identity and this PTY's foreground group immediately
// before delivering a control action. Never signal a cached arbitrary PID.
func (r *Registry) ControlForeground(ctx context.Context, id uuid.UUID, expected goagent.Process, action string) error {
	t, ok := r.Get(id)
	if !ok {
		return errors.New("terminal no longer exists")
	}
	actual, err := r.ForegroundProcess(ctx, id)
	if err != nil {
		return err
	}
	if expected.PID <= 1 || expected.Started == "" || !reflect.DeepEqual(actual, expected) {
		return errors.New("foreground run changed; refresh and retry")
	}
	group, err := t.ForegroundGroup()
	if err != nil || group != actual.PGID {
		return errors.New("foreground group changed")
	}
	select {
	case <-t.closed:
		return errors.New("terminal has exited")
	default:
	}
	switch action {
	case "ctrl-c":
		return t.Write([]byte{3})
	case "ctrl-d":
		return t.Write([]byte{4})
	case "sigint":
		return syscall.Kill(-group, syscall.SIGINT)
	case "sigterm":
		return syscall.Kill(-group, syscall.SIGTERM)
	case "sighup":
		return syscall.Kill(-group, syscall.SIGHUP)
	case "sigkill":
		return syscall.Kill(-group, syscall.SIGKILL)
	default:
		return errors.New("control action disabled or unavailable")
	}
}
