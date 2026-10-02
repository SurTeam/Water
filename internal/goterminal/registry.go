package goterminal

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"syscall"

	"github.com/SurTeam/Water/internal/goprotocol"
	"github.com/creack/pty"
	"github.com/google/uuid"
)

const (
	defaultReplayBytes = 8 * 1024 * 1024
	readBlockBytes     = 64 * 1024
	replayEventOverhead = 64
)

type Terminal struct {
	ID uuid.UUID

	cmd  *exec.Cmd
	ptmx *os.File

	mu          sync.RWMutex
	size        goprotocol.TerminalSize
	replay      []goprotocol.TerminalEvent
	replayBytes int
	replayLimit int
	subs        map[uint64]*subscriber
	nextSub     uint64

	seq    atomic.Uint64
	closed     chan struct{}
	readerDone chan struct{}
	once       sync.Once
}

type Registry struct {
	mu          sync.RWMutex
	terms       map[uuid.UUID]*Terminal
	replayLimit int
}

func NewRegistry() *Registry {
	return NewRegistryWithReplayLimit(defaultReplayBytes)
}

func NewRegistryWithReplayLimit(limit int) *Registry {
	if limit < 1024*1024 {
		limit = 1024 * 1024
	}
	if limit > defaultReplayBytes {
		limit = defaultReplayBytes
	}
	return &Registry{
		terms:       make(map[uuid.UUID]*Terminal),
		replayLimit: limit,
	}
}

func (r *Registry) Spawn(program string, args []string, size goprotocol.TerminalSize) (*Terminal, error) {
	return r.SpawnWithDir(program, args, size, "")
}

func (r *Registry) SpawnWithDir(program string, args []string, size goprotocol.TerminalSize, cwd string) (*Terminal, error) {
	if program == "" {
		return nil, errors.New("program is required")
	}
	size = size.Normalized()
	cmd := exec.Command(program, args...)
	if cwd != "" {
		cmd.Dir = cwd
	}
	cmd.Env = append(os.Environ(), "TERM=xterm-256color", "COLORTERM=truecolor")
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: uint16(size.Lines), Cols: uint16(size.Columns)})
	if err != nil {
		return nil, err
	}
	t := &Terminal{
		ID:          uuid.New(),
		cmd:         cmd,
		ptmx:        ptmx,
		size:        size,
		replayLimit: r.replayLimit,
		subs:        make(map[uint64]*subscriber),
		closed:      make(chan struct{}),
		readerDone:  make(chan struct{}),
	}
	r.mu.Lock()
	r.terms[t.ID] = t
	r.mu.Unlock()
	go t.readLoop()
	go t.waitLoop()
	return t, nil
}

func (r *Registry) Get(id uuid.UUID) (*Terminal, bool) {
	r.mu.RLock()
	t, ok := r.terms[id]
	r.mu.RUnlock()
	return t, ok
}

func (r *Registry) CloseAll() {
	r.mu.RLock()
	terms := make([]*Terminal, 0, len(r.terms))
	for _, t := range r.terms {
		terms = append(terms, t)
	}
	r.mu.RUnlock()
	for _, t := range terms {
		_ = t.Close()
	}
}

func (r *Registry) Remove(id uuid.UUID) {
	r.mu.Lock()
	t := r.terms[id]
	delete(r.terms, id)
	r.mu.Unlock()
	if t != nil {
		_ = t.Close()
	}
}

func (r *Registry) Count() int {
	r.mu.RLock()
	n := len(r.terms)
	r.mu.RUnlock()
	return n
}

func (r *Registry) RetainedReplayBytes() int {
	r.mu.RLock()
	terms := make([]*Terminal, 0, len(r.terms))
	for _, term := range r.terms {
		terms = append(terms, term)
	}
	r.mu.RUnlock()
	total := 0
	for _, term := range terms {
		term.mu.RLock()
		total += term.replayBytes
		term.mu.RUnlock()
	}
	return total
}

func (t *Terminal) Size() goprotocol.TerminalSize {
	t.mu.RLock()
	s := t.size
	t.mu.RUnlock()
	return s
}

func (t *Terminal) Write(data []byte) error {
	_, err := t.ptmx.Write(data)
	return err
}

func (t *Terminal) Resize(size goprotocol.TerminalSize) error {
	size = size.Normalized()
	if err := pty.Setsize(t.ptmx, &pty.Winsize{Rows: uint16(size.Lines), Cols: uint16(size.Columns)}); err != nil {
		return err
	}
	t.mu.Lock()
	t.size = size
	t.mu.Unlock()
	t.publish(goprotocol.TerminalEvent{Kind: goprotocol.ResizeEvent, Size: size})
	return nil
}

func (t *Terminal) Replay() []goprotocol.TerminalEvent {
	t.mu.RLock()
	out := make([]goprotocol.TerminalEvent, len(t.replay))
	copy(out, t.replay)
	t.mu.RUnlock()
	return out
}

type subscriber struct {
	events chan goprotocol.TerminalEvent
	done   chan struct{}
	once   sync.Once
}

func (t *Terminal) Subscribe() (<-chan goprotocol.TerminalEvent, <-chan struct{}, func()) {
	t.mu.Lock()
	t.nextSub++
	id := t.nextSub
	sub := &subscriber{
		events: make(chan goprotocol.TerminalEvent, 64),
		done:   make(chan struct{}),
	}
	t.subs[id] = sub
	t.mu.Unlock()

	cancel := func() {
		sub.once.Do(func() {
			close(sub.done)
			t.mu.Lock()
			delete(t.subs, id)
			t.mu.Unlock()
		})
	}
	return sub.events, sub.done, cancel
}

func (t *Terminal) Close() error {
	var err error
	t.once.Do(func() {
		close(t.closed)
		if t.cmd.Process != nil {
			// creack/pty starts the child in its own session/process group.
			// Signal the whole group so pane/tab closure cannot leave shell
			// descendants running in the background.
			pid := t.cmd.Process.Pid
			if killErr := syscall.Kill(-pid, syscall.SIGHUP); killErr != nil && !errors.Is(killErr, syscall.ESRCH) {
				err = t.cmd.Process.Signal(syscall.SIGHUP)
			}
		}
		_ = t.ptmx.Close()
	})
	return err
}

func (t *Terminal) readLoop() {
	defer close(t.readerDone)
	buf := make([]byte, readBlockBytes)
	for {
		n, err := t.ptmx.Read(buf)
		if n > 0 {
			data := append([]byte(nil), buf[:n]...)
			t.publish(goprotocol.TerminalEvent{
				Kind: goprotocol.OutputEvent,
				Size: t.Size(),
				Data: data,
			})
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				// Darwin/Linux PTYs commonly surface EIO after child exit.
			}
			return
		}
	}
}

func (t *Terminal) waitLoop() {
	err := t.cmd.Wait()
	// Preserve the same stream-order invariant as the Rust worker: all bytes
	// readable from the PTY must be sequenced before the authoritative Exit.
	<-t.readerDone
	var code *int32
	if t.cmd.ProcessState != nil {
		v := int32(t.cmd.ProcessState.ExitCode())
		code = &v
	} else if err == nil {
		v := int32(0)
		code = &v
	}
	t.publish(goprotocol.TerminalEvent{Kind: goprotocol.ExitEvent, Code: code})
	_ = t.Close()
}

func (t *Terminal) publish(ev goprotocol.TerminalEvent) {
	ev.Seq = t.seq.Add(1)
	t.mu.Lock()
	t.replayBytes += retainedEventBytes(ev)
	t.replay = append(t.replay, ev)
	for t.replayBytes > t.replayLimit && len(t.replay) > 1 {
		old := t.replay[0]
		t.replayBytes -= retainedEventBytes(old)
		t.replay[0] = goprotocol.TerminalEvent{}
		t.replay = t.replay[1:]
	}
	subs := make([]*subscriber, 0, len(t.subs))
	for _, sub := range t.subs {
		subs = append(subs, sub)
	}
	t.mu.Unlock()

	// Match the Rust worker semantics: the 64-event subscriber queue is a
	// bounded backpressure boundary. Never silently drop a terminal event.
	for _, sub := range subs {
		select {
		case sub.events <- ev:
		case <-sub.done:
		}
	}
}


func retainedEventBytes(ev goprotocol.TerminalEvent) int {
	n := replayEventOverhead
	if ev.Kind == goprotocol.OutputEvent {
		n += len(ev.Data)
	}
	return n
}
