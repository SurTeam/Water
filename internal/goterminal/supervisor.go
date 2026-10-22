package goterminal

import (
	"errors"
	"os"

	"github.com/SurTeam/Water/internal/goprotocol"
	"github.com/SurTeam/Water/internal/gosupervisor"
	"github.com/google/uuid"
)

// Supervisor owns the PTYs of this registry's terminals. A server that restarts
// re-attaches to the same terminals, so programs survive an upgrade.
type Supervisor struct {
	client *gosupervisor.Client
	socket string
}

// AttachSupervisor connects the registry to a running supervisor socket.
func (r *Registry) AttachSupervisor(socket string) error {
	client, err := gosupervisor.Dial(socket)
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.supervisor = &Supervisor{client: client, socket: socket}
	r.mu.Unlock()
	return nil
}

// DetachSupervisor drops the connection but leaves supervised programs running.
// A later server instance re-attaches by terminal id and replays retained output.
func (r *Registry) DetachSupervisor() {
	r.mu.Lock()
	supervisor := r.supervisor
	r.supervisor = nil
	terms := make([]*Terminal, 0, len(r.terms))
	for _, t := range r.terms {
		if t.supervised != nil {
			terms = append(terms, t)
		}
	}
	r.mu.Unlock()
	if supervisor != nil {
		_ = supervisor.client.Close()
	}
	for _, t := range terms {
		_ = t.Close() // closes only this server's mirror; the supervisor keeps the PTY
	}
}

// SupervisorSocket reports the attached supervisor, if any.
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
func (r *Registry) SpawnSupervisedPane(pane uuid.UUID, program string, args []string, size goprotocol.TerminalSize, cwd string, resume bool) (*Terminal, bool, error) {
	if !resume {
		r.mu.RLock()
		existing := r.terms[pane]
		r.mu.RUnlock()
		if existing != nil {
			r.Remove(pane)
		}
	}
	return r.SpawnSupervised(pane, program, args, size, cwd, resume)
}

// SpawnSupervised starts a terminal in the supervisor and mirrors its output into
// this registry's replay. Resuming an existing id re-attaches instead of
// restarting the program.
func (r *Registry) SpawnSupervised(id uuid.UUID, program string, args []string, size goprotocol.TerminalSize, cwd string, resume bool) (*Terminal, bool, error) {
	r.mu.RLock()
	supervisor := r.supervisor
	existing := r.terms[id]
	r.mu.RUnlock()
	if supervisor == nil {
		return nil, false, errors.New("terminal registry has no supervisor")
	}
	// A second spawn for a live supervised terminal must return the existing
	// mirror, otherwise the replacement would hide the publisher and the pane
	// would look empty while the program keeps running.
	if resume && existing != nil && existing.supervised != nil && !existing.isExited() {
		return existing, true, nil
	}
	size = size.Normalized()
	pid, resumed, err := supervisor.client.Spawn(gosupervisor.SpawnSpec{
		ID:      id,
		Program: program,
		Args:    args,
		Cwd:     cwd,
		Env:     append(os.Environ(), "TERM=xterm-256color", "COLORTERM=truecolor"),
		Size:    struct{ Lines, Columns uint16 }{uint16(size.Lines), uint16(size.Columns)},
	})
	if err != nil {
		return nil, false, err
	}
	_ = pid

	t := &Terminal{
		registry:        r,
		ID:              id,
		size:            size,
		scrollbackLines: r.scrollbackLines,
		subs:            make(map[uint64]*subscriber),
		closed:          make(chan struct{}),
		readerDone:      make(chan struct{}),
		resizeRequests:  make(chan resizeRequest),
		supervised:      supervisor.client,
		program:         program,
	}
	attach, events, err := supervisor.client.Attach(id, 0)
	if err != nil {
		return nil, false, err
	}
	if attach.PID > 0 {
		t.pid = attach.PID
	}
	r.mu.Lock()
	r.terms[id] = t
	r.mu.Unlock()
	go t.supervisedLoop(events, attach.Exited, attach.Code)
	return t, resumed, nil
}

// supervisedLoop mirrors supervisor events into the terminal's replay and
// subscribers. Ordering is preserved by the supervisor, so this loop only
// translates events.
func (t *Terminal) supervisedLoop(events <-chan gosupervisor.Event, exited bool, code int32) {
	defer close(t.readerDone)
	for ev := range events {
		switch ev.Kind {
		case gosupervisor.EventOutput:
			if len(ev.Output) > 0 {
				t.publish(goprotocol.TerminalEvent{Kind: goprotocol.OutputEvent, Size: t.Size(), Data: ev.Output})
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
			t.registry.retire(t.ID)
			return
		}
	}
	// The supervisor connection dropped without an exit event: the program is
	// still running, so keep the terminal and let a reconnect re-attach.
	if exited {
		exit := code
		t.publish(goprotocol.TerminalEvent{Kind: goprotocol.ExitEvent, Code: &exit})
		t.registry.retire(t.ID)
	}
}

// Write forwards input to the supervisor.
func (t *Terminal) supervisedWrite(data []byte) error {
	return t.supervised.Write(t.ID, data)
}

// Resize forwards geometry to the supervisor, which orders it against output.
func (t *Terminal) supervisedResize(size goprotocol.TerminalSize, cellWidth, cellHeight int) error {
	size = size.Normalized()
	if err := t.supervised.Resize(t.ID, uint16(size.Lines), uint16(size.Columns), uint16(cellWidth), uint16(cellHeight)); err != nil {
		return err
	}
	t.mu.Lock()
	t.size = size
	t.mu.Unlock()
	return nil
}

func (t *Terminal) supervisedClose() error {
	return t.supervised.CloseTerminal(t.ID)
}
