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
	readBlockBytes     = 128 * 1024
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
	subs        map[uint64]chan goprotocol.TerminalEvent
	nextSub     uint64

	seq    atomic.Uint64
	closed chan struct{}
	once   sync.Once
}

type Registry struct {
	mu    sync.RWMutex
	terms map[uuid.UUID]*Terminal
}

func NewRegistry() *Registry {
	return &Registry{terms: make(map[uuid.UUID]*Terminal)}
}

func (r *Registry) Spawn(program string, args []string, size goprotocol.TerminalSize) (*Terminal, error) {
	if program == "" {
		return nil, errors.New("program is required")
	}
	size = size.Normalized()
	cmd := exec.Command(program, args...)
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
		replayLimit: defaultReplayBytes,
		subs:        make(map[uint64]chan goprotocol.TerminalEvent),
		closed:      make(chan struct{}),
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

func (t *Terminal) Subscribe() (<-chan goprotocol.TerminalEvent, func()) {
	t.mu.Lock()
	t.nextSub++
	id := t.nextSub
	ch := make(chan goprotocol.TerminalEvent, 64)
	t.subs[id] = ch
	t.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			t.mu.Lock()
			if c, ok := t.subs[id]; ok {
				delete(t.subs, id)
				close(c)
			}
			t.mu.Unlock()
		})
	}
	return ch, cancel
}

func (t *Terminal) Close() error {
	var err error
	t.once.Do(func() {
		close(t.closed)
		if t.cmd.Process != nil {
			err = t.cmd.Process.Signal(syscall.SIGHUP)
		}
		_ = t.ptmx.Close()
	})
	return err
}

func (t *Terminal) readLoop() {
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
	if ev.Kind == goprotocol.OutputEvent {
		t.replayBytes += len(ev.Data)
	}
	t.replay = append(t.replay, ev)
	for t.replayBytes > t.replayLimit && len(t.replay) > 1 {
		old := t.replay[0]
		if old.Kind == goprotocol.OutputEvent {
			t.replayBytes -= len(old.Data)
		}
		t.replay[0] = goprotocol.TerminalEvent{}
		t.replay = t.replay[1:]
	}
	for _, ch := range t.subs {
		select {
		case ch <- ev:
		default:
			// Never backpressure the PTY on a slow client.
		}
	}
	t.mu.Unlock()
}
