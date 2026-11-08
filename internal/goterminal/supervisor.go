package goterminal

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/SurTeam/Water/internal/goprotocol"
	"github.com/SurTeam/Water/internal/gosupervisor"
	"github.com/google/uuid"
)

// Supervisor owns the PTYs of this registry's terminals. A server that restarts
// re-attaches to the same terminals, so programs survive an upgrade.
type Supervisor struct {
	socket    string
	legacy    string
	legacyIDs map[string]struct{}
}

// AttachSupervisor connects the registry to a running supervisor socket.
func (r *Registry) AttachSupervisor(socket string) error {
	return r.AttachSupervisors(socket, "", nil)
}

// AttachSupervisors sends new shells to current. Programs already running under
// legacy keep that supervisor; only a recovery resume of those ids goes there.
func (r *Registry) AttachSupervisors(current, legacy string, legacyIDs []string) error {
	client, err := gosupervisor.Dial(current)
	if err != nil {
		return err
	}
	_ = client.Close()
	ids := make(map[string]struct{}, len(legacyIDs))
	for _, id := range legacyIDs {
		ids[id] = struct{}{}
	}
	r.mu.Lock()
	r.supervisor = &Supervisor{socket: current, legacy: legacy, legacyIDs: ids}
	r.mu.Unlock()
	return nil
}

// DetachSupervisor drops the connection but leaves supervised programs running.
// A later server instance re-attaches by terminal id and replays retained output.
func (r *Registry) DetachSupervisor() {
	r.mu.Lock()
	r.supervisor = nil
	terms := make([]*Terminal, 0, len(r.terms))
	for _, t := range r.terms {
		if t.supervisor != nil {
			terms = append(terms, t)
		}
	}
	r.mu.Unlock()
	for _, t := range terms {
		t.detachSupervised()
	}
}

// SupervisorSocket reports the attached supervisor, if any.
func (s *Supervisor) socketFor(key string, resume bool) string {
	if resume && s.legacy != "" {
		if _, ok := s.legacyIDs[key]; ok {
			return s.legacy
		}
	}
	return s.socket
}

func (r *Registry) SupervisorSocket() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.supervisor == nil {
		return ""
	}
	return r.supervisor.socket
}

// SpawnSupervisedPane starts a pane's terminal in the supervisor under the pane
// id. resume is set only by recovery: it keeps the program a previous server left
// running instead of replacing it. An ordinary spawn always replaces the pane's
// terminal, even though the supervisor key stays the same.
func (r *Registry) SpawnSupervisedPane(pane uuid.UUID, program string, args []string, size goprotocol.TerminalSize, cwd string, resume bool, inputModes ...string) (*Terminal, bool, error) {
	// The supervisor keeps the PTY under the pane id so a later server can resume
	// it, but each terminal instance gets its own id. Clients attach by instance
	// id, so a replacement is visible to them instead of silently reusing a
	// subscription to the retired stream.
	if !resume {
		// Retire the previous instance first: dropping it from the registry makes
		// its late Exit a no-op instead of a pane close.
		r.mu.RLock()
		var previous uuid.UUID
		for id, candidate := range r.terms {
			if candidate.supervisorKey == pane.String() {
				previous = id
				break
			}
		}
		r.mu.RUnlock()
		if previous != uuid.Nil {
			r.Remove(previous)
		}
	}
	return r.SpawnSupervised(pane, program, args, size, cwd, resume, inputModes...)
}

// SpawnSupervised starts a terminal in the supervisor under key and mirrors its
// output into this registry's replay. The returned Terminal has its own instance
// id; only the supervisor key survives a restart.
func (r *Registry) SpawnSupervised(key uuid.UUID, program string, args []string, size goprotocol.TerminalSize, cwd string, resume bool, inputModes ...string) (*Terminal, bool, error) {
	keyString := key.String()
	r.mu.RLock()
	supervisor := r.supervisor
	var existing *Terminal
	for _, candidate := range r.terms {
		if candidate.supervisorKey == keyString {
			existing = candidate
			break
		}
	}
	r.mu.RUnlock()
	if supervisor == nil {
		return nil, false, errors.New("terminal registry has no supervisor")
	}
	// A second spawn for a live supervised terminal must return the existing
	// mirror, otherwise the replacement would hide the publisher and the pane
	// would look empty while the program keeps running.
	if resume && existing != nil && existing.supervisor != nil && !existing.isExited() {
		return existing, true, nil
	}
	size = size.Normalized()
	link := newSupervisedLink(supervisor.socketFor(keyString, resume), key)
	_, resumed, err := link.spawn(gosupervisor.SpawnSpec{
		ID:      key,
		Program: program,
		Args:    args,
		Cwd:     cwd,
		Env:     append(os.Environ(), "TERM=xterm-256color", "COLORTERM=truecolor"),
		Size:    struct{ Lines, Columns uint16 }{uint16(size.Lines), uint16(size.Columns)},
		Replace: !resume,
	})
	if err != nil {
		link.closeLinks()
		return nil, false, err
	}

	id := uuid.New()
	t := &Terminal{
		registry:        r,
		ID:              id,
		supervisorKey:   keyString,
		size:            size,
		scrollbackLines: r.scrollbackLines,
		subs:            make(map[uint64]*subscriber),
		closed:          make(chan struct{}),
		readerDone:      make(chan struct{}),
		resizeRequests:  make(chan resizeRequest),
		input:           make(chan inputRequest, inputQueueCapacity),
		supervisor:      link,
		program:         program,
		args:            append([]string(nil), args...),
	}
	_, attach, events, err := link.attach(0)
	if err != nil {
		if !resumed {
			_ = link.closeTerminal()
		}
		link.closeLinks()
		return nil, false, err
	}
	if attach.PID > 0 {
		t.pid = attach.PID
	}
	r.mu.Lock()
	r.terms[id] = t
	r.mu.Unlock()
	// Older supervisors cannot checkpoint discarded mode commands. Seed only a
	// resumed, truncated stream without a supervisor checkpoint, before its tail
	// so retained resets and subsequent live output still take precedence.
	if len(inputModes) > 0 {
		if modes := legacyRecoveryInputModes(attach, resumed, inputModes[0]); modes != "" {
			t.publish(goprotocol.TerminalEvent{Kind: goprotocol.OutputEvent, Size: size, Data: []byte(modes)})
		}
	}

	if len(attach.Restore) > 0 {
		t.publish(goprotocol.TerminalEvent{Kind: goprotocol.OutputEvent, Size: size, Data: attach.Restore})
	}
	go t.supervisedLoop(link, events, attach.Offset, attach.Exited, attach.Code)
	go t.inputLoop()
	return t, resumed, nil
}

// supervisedLoop mirrors supervisor events into the terminal's replay and
// subscribers. Ordering is preserved by the supervisor, so this loop only
// translates events.
func (t *Terminal) supervisedLoop(link *supervisedLink, events <-chan gosupervisor.Event, offset uint64, exited bool, code int32) {
	defer close(t.readerDone)
	backoff := 50 * time.Millisecond
	for {
		for ev := range events {
			switch ev.Kind {
			case gosupervisor.EventOutput:
				if len(ev.Output) > 0 {
					t.publish(goprotocol.TerminalEvent{Kind: goprotocol.OutputEvent, Size: t.Size(), Data: ev.Output})
					offset = ev.Offset + uint64(len(ev.Output))
				}
			case gosupervisor.EventResize:
				size := goprotocol.TerminalSize{Lines: int(ev.Lines), Columns: int(ev.Columns)}
				t.mu.Lock()
				t.size = size
				t.mu.Unlock()
				t.publish(goprotocol.TerminalEvent{Kind: goprotocol.ResizeEvent, Size: size})
			case gosupervisor.EventExit:
				exit := ev.ExitCode
				t.publish(goprotocol.TerminalEvent{Kind: goprotocol.ExitEvent, Code: &exit})
				t.registry.retireIf(t.ID, t)
				return
			}
		}
		// The supervisor closed only this output subscription because its
		// bounded queue overflowed. The program still owns the PTY, so resume
		// from the last byte this server consumed instead of silently ending
		// the pane's stream.
		if exited {
			exit := code
			t.publish(goprotocol.TerminalEvent{Kind: goprotocol.ExitEvent, Code: &exit})
			t.registry.retireIf(t.ID, t)
			return
		}
		select {
		case <-t.closed:
			return
		default:
		}
		_, attach, next, err := link.attach(offset)
		if err != nil {
			select {
			case <-t.closed:
				return
			case <-time.After(backoff):
			}
			if backoff < 5*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = 50 * time.Millisecond
		if len(attach.Restore) > 0 {
			t.publish(goprotocol.TerminalEvent{Kind: goprotocol.OutputEvent, Size: t.Size(), Data: attach.Restore})
		}
		offset = attach.Offset
		events = next
		exited, code = attach.Exited, attach.Code
	}
}

// Write forwards input to the supervisor.
func (t *Terminal) supervisedWrite(data []byte) error {
	return t.supervisor.write(data)
}

// Resize forwards geometry to the supervisor, which orders it against output.
func (t *Terminal) supervisedResize(size goprotocol.TerminalSize, cellWidth, cellHeight int) error {
	size = size.Normalized()
	if err := t.supervisor.resize(size, cellWidth, cellHeight); err != nil {
		return err
	}
	t.mu.Lock()
	t.size = size
	t.mu.Unlock()
	return nil
}

func (t *Terminal) supervisedClose() error {
	return t.supervisor.closeTerminal()
}

func (t *Terminal) detachSupervised() {
	t.once.Do(func() {
		close(t.closed)
		t.supervisor.closeLinks()
	})
}

// supervisedLink owns the independent supervisor connections for one pane.
// Output, input and control are isolated so a blocked writer for this pane's
// screen or stdin cannot delay closing/resizing it or any other pane.
type supervisedLink struct {
	socket string
	key    uuid.UUID

	mu          sync.Mutex
	control     *gosupervisor.Client
	input       *gosupervisor.Client
	output      *gosupervisor.Client
	closed      bool
	binaryInput atomic.Bool
}

func newSupervisedLink(socket string, key uuid.UUID) *supervisedLink {
	return &supervisedLink{socket: socket, key: key}
}

func (l *supervisedLink) controlClient() (*gosupervisor.Client, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil, ErrTerminalClosed
	}
	if l.control != nil {
		return l.control, nil
	}
	client, err := gosupervisor.Dial(l.socket)
	if err != nil {
		return nil, err
	}
	l.control = client
	return client, nil
}

func (l *supervisedLink) outputClient() (*gosupervisor.Client, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil, ErrTerminalClosed
	}
	if l.output != nil {
		return l.output, nil
	}
	client, err := gosupervisor.Dial(l.socket)
	if err != nil {
		return nil, err
	}
	l.output = client
	return client, nil
}

func (l *supervisedLink) inputClient() (*gosupervisor.Client, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil, ErrTerminalClosed
	}
	if l.input != nil {
		return l.input, nil
	}
	client, err := gosupervisor.Dial(l.socket)
	if err != nil {
		return nil, err
	}
	l.input = client
	return client, nil
}

func (l *supervisedLink) dropInput(client *gosupervisor.Client) {
	l.mu.Lock()
	if l.input == client {
		l.input = nil
	}
	l.mu.Unlock()
	_ = client.Close()
}

func (l *supervisedLink) dropControl(client *gosupervisor.Client) {
	l.mu.Lock()
	if l.control == client {
		l.control = nil
	}
	l.mu.Unlock()
	_ = client.Close()
}

func (l *supervisedLink) dropOutput(client *gosupervisor.Client) {
	l.mu.Lock()
	if l.output == client {
		l.output = nil
	}
	l.mu.Unlock()
	_ = client.Close()
}

func (l *supervisedLink) isClosed() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.closed
}

func (l *supervisedLink) closeLinks() {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return
	}
	l.closed = true
	control, input, output := l.control, l.input, l.output
	l.control, l.input, l.output = nil, nil, nil
	l.mu.Unlock()
	if control != nil {
		_ = control.Close()
	}
	if output != nil {
		_ = output.Close()
	}
	if input != nil {
		_ = input.Close()
	}
}

// spawn is intentionally not retried: a lost reply may mean a new process
// already exists, and repeating a replacement would create an orphan.
func (l *supervisedLink) spawn(spec gosupervisor.SpawnSpec) (int, bool, error) {
	client, err := l.controlClient()
	if err != nil {
		return 0, false, err
	}
	pid, resumed, err := client.Spawn(spec)
	if err != nil {
		l.dropControl(client)
	}
	return pid, resumed, err
}

// write is at-most-once. The supervisor does not acknowledge input frames, so
// retrying after a broken frame could duplicate bytes already queued. A program
// that stops reading stdin is handled by the supervisor's bounded input queue.
func (l *supervisedLink) write(data []byte) error {
	client, err := l.inputClient()
	if err != nil {
		return err
	}
	if l.binaryInput.Load() {
		err = client.WriteBinaryInput(l.key, data)
	} else {
		err = client.Write(l.key, data)
	}
	if err != nil {
		l.dropInput(client)
		if l.isClosed() {
			return ErrTerminalClosed
		}
	}
	return err
}

func (l *supervisedLink) resize(size goprotocol.TerminalSize, cellWidth, cellHeight int) error {
	var last error
	for range 2 {
		client, err := l.controlClient()
		if err != nil {
			return err
		}
		if err = client.Resize(l.key, uint16(size.Lines), uint16(size.Columns), uint16(cellWidth), uint16(cellHeight)); err == nil {
			return nil
		}
		last = err
		l.dropControl(client)
		if l.isClosed() {
			return ErrTerminalClosed
		}
	}
	return last
}

func (l *supervisedLink) foreground(ctx context.Context) (int, error) {
	var last error
	for range 2 {
		client, err := l.controlClient()
		if err != nil {
			return 0, err
		}
		group, err := client.ForegroundContext(ctx, l.key)
		if err == nil {
			return group, nil
		}
		last = err
		l.dropControl(client)
		if l.isClosed() || ctx.Err() != nil {
			return 0, last
		}
	}
	return 0, last
}

func (l *supervisedLink) closeTerminal() error {
	client, err := l.controlClient()
	if err != nil {
		return err
	}
	if err = client.CloseTerminal(l.key); err != nil {
		l.dropControl(client)
	}
	return err
}

func (l *supervisedLink) attach(offset uint64) (*gosupervisor.Client, gosupervisor.Attach, <-chan gosupervisor.Event, error) {
	var last error
	for range 2 {
		client, err := l.outputClient()
		if err != nil {
			return nil, gosupervisor.Attach{}, nil, err
		}
		attach, events, err := client.AttachDedicated(l.key, offset)
		if err == nil {
			l.binaryInput.Store(attach.BinaryInput)
			return client, attach, events, nil
		}
		last = err
		l.dropOutput(client)
		if l.isClosed() {
			return nil, gosupervisor.Attach{}, nil, ErrTerminalClosed
		}
	}
	return nil, gosupervisor.Attach{}, nil, last
}

// Accept only finite terminal mode commands from a recovery document.
func validatedRecoveryInputModes(input string) string {
	if len(input) > 128 {
		return ""
	}
	out := ""
	for len(input) > 0 {
		matched := false
		for _, seq := range []string{"\x1b[?1049h", "\x1b[?9h", "\x1b[?1000h", "\x1b[?1002h", "\x1b[?1003h", "\x1b[?1006h", "\x1b[?1016h"} {
			if strings.HasPrefix(input, seq) {
				out += seq
				input = input[len(seq):]
				matched = true
				break
			}
		}
		if !matched {
			return ""
		}
	}
	return out
}

func legacyRecoveryInputModes(attach gosupervisor.Attach, resumed bool, modes string) string {
	if attach.RestoreSupported || !resumed || !attach.Truncated || len(attach.Restore) != 0 {
		return ""
	}
	return validatedRecoveryInputModes(modes)
}
