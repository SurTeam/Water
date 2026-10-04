package goupdate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

type State struct {
	Phase      string `json:"phase"`
	Current    string `json:"current_version"`
	Version    string `json:"available_version,omitempty"`
	Downloaded int64  `json:"downloaded_bytes"`
	Total      int64  `json:"total_bytes"`
	Error      string `json:"error,omitempty"`
}

type Manager struct {
	mu                        sync.Mutex
	state                     State
	variant, executable       string
	args                      []string
	candidate                 *Candidate
	plan                      *Plan
	ctx                       context.Context
	cancel                    context.CancelFunc
	busy, closed, transferred bool
	invalidate                func()
}

func NewManager(version, variant string, args []string, invalidate func()) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	executable, _ := os.Executable()
	executable, _ = filepath.EvalSymlinks(executable)
	m := &Manager{state: State{Phase: "idle", Current: version}, variant: variant, executable: executable, args: append([]string(nil), args...), ctx: ctx, cancel: cancel, invalidate: invalidate}
	if message := os.Getenv("WATER_UPDATE_ERROR"); message != "" {
		m.state.Phase = "error"
		m.state.Error = message
		_ = os.Unsetenv("WATER_UPDATE_ERROR")
	}
	return m
}

func (m *Manager) Snapshot() State { m.mu.Lock(); defer m.mu.Unlock(); return m.state }
func (m *Manager) notify() {
	if m.invalidate != nil {
		m.invalidate()
	}
}

func (m *Manager) Check() {
	m.mu.Lock()
	if m.busy || m.closed || m.plan != nil {
		m.mu.Unlock()
		return
	}
	m.busy = true
	m.candidate = nil
	m.state.Phase = "checking"
	m.state.Error = ""
	m.state.Version = ""
	m.mu.Unlock()
	m.notify()
	go func() {
		ctx, cancel := context.WithTimeout(m.ctx, 20*time.Second)
		defer cancel()
		releases, err := fetchReleases(ctx, httpClient(), releasesURL)
		var c *Candidate
		if err == nil {
			c, err = selectRelease(releases, m.state.Current, m.variant, runtime.GOOS, runtime.GOARCH)
		}
		m.mu.Lock()
		m.busy = false
		if err != nil {
			m.state.Phase = "error"
			m.state.Error = err.Error()
		} else if c == nil {
			m.state.Phase = "current"
		} else {
			m.candidate = c
			m.state.Phase = "available"
			m.state.Version = c.Version
			m.state.Total = c.Asset.Size
		}
		m.mu.Unlock()
		m.notify()
	}()
}

func (m *Manager) Download() {
	m.mu.Lock()
	if m.busy || m.closed || m.candidate == nil || m.plan != nil {
		m.mu.Unlock()
		return
	}
	c := *m.candidate
	m.busy = true
	m.state.Phase = "downloading"
	m.state.Downloaded = 0
	m.state.Error = ""
	m.mu.Unlock()
	m.notify()
	go func() {
		ctx, cancel := context.WithTimeout(m.ctx, 10*time.Minute)
		defer cancel()
		plan, err := prepare(ctx, httpClient(), c, m.executable, m.variant, runtime.GOOS, m.args, func(n int64) { m.mu.Lock(); m.state.Downloaded = n; m.mu.Unlock(); m.notify() })
		m.mu.Lock()
		m.busy = false
		if m.closed {
			if plan != nil {
				os.RemoveAll(plan.Directory)
			}
		} else if err != nil {
			m.state.Phase = "error"
			m.state.Error = err.Error()
		} else {
			m.plan = plan
			m.state.Phase = "ready"
		}
		m.mu.Unlock()
		m.notify()
	}()
}

// Install runs on a worker. The UI may exit only after this returns nil.
func (m *Manager) Install() error {
	m.mu.Lock()
	if m.busy || m.closed || m.transferred || m.plan == nil {
		m.mu.Unlock()
		return fmt.Errorf("no verified update is ready")
	}
	plan := m.plan
	m.busy = true
	// Close must not remove a plan while the worker transfers it.
	m.transferred = true
	m.mu.Unlock()
	err := StartInstaller(plan)
	m.mu.Lock()
	m.busy = false
	if err != nil {
		m.transferred = false
		m.state.Phase = "ready"
		m.state.Error = err.Error()
	} else {
		m.state.Phase = "installing"
	}
	cleanup := err != nil && m.closed
	m.mu.Unlock()
	if cleanup {
		_ = os.RemoveAll(plan.Directory)
	}
	return err
}

func (m *Manager) Close() {
	m.mu.Lock()
	m.closed = true
	m.cancel()
	plan := m.plan
	cleanup := plan != nil && !m.transferred
	m.mu.Unlock()
	if cleanup {
		os.RemoveAll(plan.Directory)
	}
}
