package goui

import (
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/SurTeam/Water/internal/goclient"
)

// Only compatibility/in-memory transports use this mailbox. Real GUI panes
// consume the subscription's bounded event channel directly; socket delivery
// applies backpressure independently to each pane without discarding live ANSI.
const terminalQueueBytes = 1 << 20
const terminalQueueEvents = 256

type terminalWorker struct {
	mu           sync.Mutex
	queue        []goclient.TerminalPush
	bytes        int
	wake         chan struct{}
	stop         chan struct{}
	done         chan struct{}
	started      bool
	subscription *goclient.TerminalSubscription
	events       <-chan goclient.TerminalPush
	config       *bool
	reset        atomic.Bool
	err          error
}

func (t *terminalClient) startWorkerLocked(c *WorkspaceClient) {
	w := &t.worker
	if w.started {
		return
	}
	w.started = true
	w.wake = make(chan struct{}, 1)
	w.stop = make(chan struct{})
	w.done = make(chan struct{})
	go t.runOutputWorker(c)
}

func (t *terminalClient) startSubscription(c *WorkspaceClient, sub *goclient.TerminalSubscription) {
	w := &t.worker
	w.mu.Lock()
	if t.closed.Load() {
		w.mu.Unlock()
		_ = sub.Close()
		return
	}
	if w.events == sub.Events {
		w.mu.Unlock()
		return
	}
	w.subscription = sub
	w.events = sub.Events
	t.startWorkerLocked(c)
	select {
	case w.wake <- struct{}{}:
	default:
	}
	w.mu.Unlock()
}

func (t *terminalClient) enqueueEvent(c *WorkspaceClient, push goclient.TerminalPush) {
	w := &t.worker
	w.mu.Lock()
	if t.closed.Load() {
		w.mu.Unlock()
		return
	}
	t.startWorkerLocked(c)
	if len(w.queue) >= terminalQueueEvents || w.bytes+len(push.Event.Data) > terminalQueueBytes {
		w.mu.Unlock()
		t.failOutput(c, fmt.Errorf("compatibility terminal event mailbox exceeded %d events / %d bytes; isolated socket transport is required", terminalQueueEvents, terminalQueueBytes))
		return
	}
	w.queue = append(w.queue, push)
	w.bytes += len(push.Event.Data)
	select {
	case w.wake <- struct{}{}:
	default:
	}
	w.mu.Unlock()
}

func (t *terminalClient) queueConfig(enabled bool) {
	w := &t.worker
	w.mu.Lock()
	defer w.mu.Unlock()
	if t.closed.Load() {
		return
	}
	w.config = &enabled
	if w.wake != nil {
		select {
		case w.wake <- struct{}{}:
		default:
		}
	}
}

func (t *terminalClient) failOutput(c *WorkspaceClient, err error) {
	w := &t.worker
	w.mu.Lock()
	if w.err == nil {
		w.err = err
	}
	w.mu.Unlock()
	c.mu.Lock()
	c.connectionError = fmt.Sprintf("terminal %s output stopped: %v", t.id, err)
	c.mu.Unlock()
	t.stopWorker()
	if c.invalidate != nil {
		c.invalidate()
	}
}

// Cancellation releases queued payloads immediately. All emulator destruction
// and subscription closure then go through the same shutdown path.
func (t *terminalClient) stopWorker() {
	w := &t.worker
	w.mu.Lock()
	if t.closed.Swap(true) {
		w.mu.Unlock()
		return
	}
	w.queue = nil
	w.bytes = 0
	w.config = nil
	sub := w.subscription
	if w.started {
		close(w.stop)
	} else {
		w.done = make(chan struct{})
		go func() {
			if sub != nil {
				_ = sub.Close()
			}
			t.close()
			close(w.done)
		}()
	}
	w.mu.Unlock()
	if sub != nil {
		go sub.Close()
	}
}

func (t *terminalClient) runOutputWorker(c *WorkspaceClient) {
	w := &t.worker
	defer close(w.done)
	defer func() {
		w.mu.Lock()
		sub := w.subscription
		w.mu.Unlock()
		if sub != nil {
			_ = sub.Close()
		}
		t.close()
	}()
	for {
		w.mu.Lock()
		if t.closed.Load() {
			w.mu.Unlock()
			return
		}
		cfg := w.config
		w.config = nil
		events := w.events
		subscription := w.subscription
		var push *goclient.TerminalPush
		if len(w.queue) > 0 {
			value := w.queue[0]
			w.queue[0] = goclient.TerminalPush{}
			w.queue = w.queue[1:]
			w.bytes -= len(value.Event.Data)
			push = &value
		}
		w.mu.Unlock()
		if cfg != nil {
			t.setScrollbackOnClearScreen(*cfg)
		}
		if w.reset.Swap(false) {
			t.parseMu.Lock()
			t.mu.RLock()
			if t.emu != nil {
				t.emu.ResetAgentStatus()
			}
			t.mu.RUnlock()
			t.parseMu.Unlock()
		}
		if push == nil {
			select {
			case <-w.stop:
				return
			case <-w.wake:
				continue
			case value, ok := <-events:
				if !ok {
					if !t.closed.Load() {
						t.failOutput(c, fmt.Errorf("terminal subscription ended: %v", subscription.Err()))
					}
					return
				}
				push = &value
			}
		}
		if t.closed.Load() {
			return
		}
		c.applyTerminalEventFor(t, *push)
		t.dirty.Store(true)
		c.mu.RLock()
		notify := c.outputWake
		c.mu.RUnlock()
		select {
		case notify <- struct{}{}:
		default:
		}
	}
}
