package goterminal

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/SurTeam/Water/internal/gometrics"
	"github.com/SurTeam/Water/internal/goprotocol"
	"github.com/SurTeam/Water/internal/gosupervisor"
	"github.com/creack/pty"
	"github.com/google/uuid"
)

const (
	defaultScrollbackLines = 2000
	readBlockBytes         = 128 * 1024
	rawReadQueueCapacity   = 64
	idleReadCacheBlocks    = 2
	outputBatchIdle        = time.Millisecond
	outputBatchMaxAge      = 5 * time.Millisecond
	replayEventOverhead    = 64
	exitedTerminalLimit    = 32
	exitedReplayLimit      = 8 * 1024 * 1024
	coalesceReplayBytes    = 32 * 1024
)

type Terminal struct {
	ID uuid.UUID

	registry *Registry

	cmd        *exec.Cmd
	ptmx       *os.File
	supervised *gosupervisor.Client
	// supervisorKey is the pane id the supervisor holds the PTY under. It survives
	// server restarts, while ID identifies this terminal instance to clients.
	supervisorKey         string
	pid                   int
	program               string
	args                  []string // launch arguments, independent of local process ownership
	shellIntegrationDir   string
	cellWidth, cellHeight int        // owned by the ordered PTY reader
	executeMu             sync.Mutex // one foreground execution per pane

	mu              sync.RWMutex
	size            goprotocol.TerminalSize
	replay          []goprotocol.TerminalEvent
	replayBytes     int
	scrollbackLines int
	outputNewlines  int
	altScreen       bool
	altReplay       []byte
	altSeq          uint64
	scanHold        []byte

	// Incremental eligibility for primary replay compaction, owned by mu.
	primarySegmentBytes  int
	primarySegmentEscape bool
	primaryRedrawCheck   bool
	primaryRedrawLimit   int

	subs    map[uint64]*subscriber
	nextSub uint64

	seq              atomic.Uint64
	closed           chan struct{}
	readerDone       chan struct{}
	resizeRequests   chan resizeRequest
	rawFlushRequests chan rawFlushRequest
	once             sync.Once
}

type exitedTerminal struct {
	terminal *Terminal
	bytes    int
}

type Registry struct {
	mu              sync.RWMutex
	terms           map[uuid.UUID]*Terminal
	supervisor      *Supervisor
	scrollbackLines int
	exited          map[uuid.UUID]exitedTerminal
	exitOrder       []uuid.UUID
	exitedBytes     int
}

func NewRegistry() *Registry {
	return NewRegistryWithScrollback(defaultScrollbackLines)
}

// NewRegistryWithScrollback keeps enough primary-buffer replay to rebuild the
// GUI's scrollback. Alternate-screen redraws are stored separately and cannot
// evict that history.
func NewRegistryWithScrollback(lines int) *Registry {
	if lines < 1 {
		lines = defaultScrollbackLines
	}
	if lines > 10_000 {
		lines = 10_000
	}
	return &Registry{
		terms:           make(map[uuid.UUID]*Terminal),
		exited:          make(map[uuid.UUID]exitedTerminal),
		scrollbackLines: lines,
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
	integrationDir, err := prepareShellIntegration(cmd, program, args)
	if err != nil {
		return nil, err
	}
	started := false
	defer func() {
		if !started && integrationDir != "" {
			_ = os.RemoveAll(integrationDir)
		}
	}()
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: uint16(size.Lines), Cols: uint16(size.Columns)})
	if err != nil {
		return nil, err
	}
	pollable, err := makePollablePTY(ptmx)
	if err != nil {
		_ = ptmx.Close()
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGHUP)
		_ = cmd.Wait()
		return nil, err
	}
	ptmx = pollable
	t := &Terminal{
		registry:            r,
		shellIntegrationDir: integrationDir,
		ID:                  uuid.New(),
		cmd:                 cmd,
		ptmx:                ptmx,
		pid:                 cmd.Process.Pid,
		program:             program,
		args:                append([]string(nil), cmd.Args[1:]...),
		size:                size,
		scrollbackLines:     r.scrollbackLines,
		subs:                make(map[uint64]*subscriber),
		closed:              make(chan struct{}),
		readerDone:          make(chan struct{}),
		resizeRequests:      make(chan resizeRequest),
		rawFlushRequests:    make(chan rawFlushRequest, 1),
	}
	r.mu.Lock()
	r.terms[t.ID] = t
	r.mu.Unlock()
	started = true
	go t.readLoop()
	go t.waitLoop()
	return t, nil
}

func (r *Registry) Get(id uuid.UUID) (*Terminal, bool) {
	r.mu.RLock()
	t, ok := r.terms[id]
	if !ok {
		entry, found := r.exited[id]
		t, ok = entry.terminal, found
	}
	r.mu.RUnlock()
	return t, ok
}

// retireIf removes a terminal only when it is still the registered one. A
// supervised pane reuses its pane id for the replacement terminal, so a late
// Exit from the retired worker must not evict its successor.
func (r *Registry) retireIf(id uuid.UUID, t *Terminal) {
	r.mu.RLock()
	current := r.terms[id]
	r.mu.RUnlock()
	if current != t {
		return
	}
	r.retire(id)
}

// retire runs after the authoritative Exit has been published and the PTY has
// closed. Preserve recent replay for wait_exit/snapshot, but never keep an
// unbounded history of Terminal objects. Explicit removal must not resurrect one.
func (r *Registry) retire(id uuid.UUID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.terms[id]
	if !ok {
		return
	}
	delete(r.terms, id)
	t.mu.RLock()
	n := t.replayBytes
	t.mu.RUnlock()
	r.exited[id] = exitedTerminal{terminal: t, bytes: n}
	r.exitOrder = append(r.exitOrder, id)
	r.exitedBytes += n
	for len(r.exitOrder) > exitedTerminalLimit || r.exitedBytes > exitedReplayLimit {
		r.removeExitedLocked(r.exitOrder[0])
	}
}

func (r *Registry) removeExitedLocked(id uuid.UUID) *Terminal {
	entry, ok := r.exited[id]
	if !ok {
		return nil
	}
	delete(r.exited, id)
	r.exitedBytes -= entry.bytes
	for i, candidate := range r.exitOrder {
		if candidate == id {
			copy(r.exitOrder[i:], r.exitOrder[i+1:])
			r.exitOrder[len(r.exitOrder)-1] = uuid.Nil
			r.exitOrder = r.exitOrder[:len(r.exitOrder)-1]
			break
		}
	}
	return entry.terminal
}

func (r *Registry) CloseAll() {
	r.mu.Lock()
	terms := make([]*Terminal, 0, len(r.terms))
	for _, t := range r.terms {
		terms = append(terms, t)
	}
	clear(r.terms)
	clear(r.exited)
	r.exitOrder = nil
	r.exitedBytes = 0
	r.mu.Unlock()
	for _, t := range terms {
		_ = t.Close()
	}
}

func (r *Registry) Remove(id uuid.UUID) {
	r.mu.Lock()
	t := r.terms[id]
	delete(r.terms, id)
	if exited := r.removeExitedLocked(id); exited != nil {
		t = exited
	}
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

// ExitedRetention reports the bounded, queryable history, separate from live PTYs.
func (r *Registry) ExitedRetention() (count, replayBytes int) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.exited), r.exitedBytes
}

func (r *Registry) RetainedReplayBytes() int {
	r.mu.RLock()
	terms := make([]*Terminal, 0, len(r.terms))
	for _, term := range r.terms {
		terms = append(terms, term)
	}
	total := r.exitedBytes
	r.mu.RUnlock()
	for _, term := range terms {
		term.mu.RLock()
		total += term.replayBytes
		term.mu.RUnlock()
	}
	return total
}

// processID is the pane's process, owned locally or by the supervisor.
func (t *Terminal) processID() int {
	if t.pid > 0 {
		return t.pid
	}
	if t.cmd != nil && t.cmd.Process != nil {
		return t.cmd.Process.Pid
	}
	return 0
}

func (t *Terminal) isExited() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	for _, ev := range t.replay {
		if ev.Kind == goprotocol.ExitEvent {
			return true
		}
	}
	return false
}

func (t *Terminal) Size() goprotocol.TerminalSize {
	t.mu.RLock()
	s := t.size
	t.mu.RUnlock()
	return s
}

func (t *Terminal) Write(data []byte) error {
	if t.supervised != nil {
		return t.supervisedWrite(data)
	}
	_, err := t.ptmx.Write(data)
	return err
}

type resizeRequest struct {
	size                  goprotocol.TerminalSize
	cellWidth, cellHeight int
	done                  chan error
}

type rawFlushRequest struct {
	ready  chan struct{}
	resume chan struct{}
}

func (t *Terminal) Resize(size goprotocol.TerminalSize) error {
	return t.ResizeWithCells(size, 0, 0)
}

func (t *Terminal) ResizeWithCells(size goprotocol.TerminalSize, cellWidth, cellHeight int) error {
	if t.supervised != nil {
		return t.supervisedResize(size, cellWidth, cellHeight)
	}
	req := resizeRequest{size: size.Normalized(), cellWidth: cellWidth, cellHeight: cellHeight, done: make(chan error, 1)}
	select {
	case t.resizeRequests <- req:
	case <-t.readerDone:
		return io.ErrClosedPipe
	case <-t.closed:
		return io.ErrClosedPipe
	}
	select {
	case err := <-req.done:
		return err
	case <-t.readerDone:
		return io.ErrClosedPipe
	case <-t.closed:
		return io.ErrClosedPipe
	}
}

func (t *Terminal) Replay() []goprotocol.TerminalEvent {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make([]goprotocol.TerminalEvent, len(t.replay))
	copy(out, t.replay)
	if !t.altScreen || len(t.altReplay) == 0 {
		return out
	}
	alt := goprotocol.TerminalEvent{
		Kind: goprotocol.OutputEvent, Seq: t.altSeq, Size: t.size,
		Data: append([]byte(nil), t.altReplay...),
	}
	for i, ev := range out {
		if ev.Seq == t.altSeq && ev.Kind == goprotocol.OutputEvent {
			merged := make([]byte, len(ev.Data)+len(alt.Data))
			copy(merged, ev.Data)
			copy(merged[len(ev.Data):], alt.Data)
			out[i].Data = merged
			return out
		}
		if ev.Seq > t.altSeq {
			out = append(out, goprotocol.TerminalEvent{})
			copy(out[i+1:], out[i:])
			out[i] = alt
			return out
		}
	}
	return append(out, alt)
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
	if t.supervised != nil {
		var err error
		t.once.Do(func() {
			close(t.closed)
			err = t.supervisedClose()
		})
		return err
	}
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

	raw := make(chan []byte, rawReadQueueCapacity)
	// Queue capacity bounds backpressure, not an idle memory reservation.
	// Allocate blocks only when the reader needs them and discard burst excess.
	free := make(chan []byte, idleReadCacheBlocks)
	recycle := func(chunk []byte) {
		if cap(chunk) < readBlockBytes {
			return
		}
		select {
		case free <- chunk[:readBlockBytes]:
		default:
		}
	}
	rawReaderDone := make(chan struct{})
	go func() { defer close(rawReaderDone); t.rawReadLoop(raw, free) }()

	idleTimer := time.NewTimer(time.Hour)
	maxTimer := time.NewTimer(time.Hour)
	if !idleTimer.Stop() {
		select {
		case <-idleTimer.C:
		default:
		}
	}
	if !maxTimer.Stop() {
		select {
		case <-maxTimer.C:
		default:
		}
	}
	defer idleTimer.Stop()
	defer maxTimer.Stop()
	var idleC, maxC <-chan time.Time

	// Hold reader-owned blocks until flush, then allocate exactly the number of
	// immutable bytes retained by replay/subscribers. This avoids reserving a
	// full 128 KiB event buffer for every partially-filled micro-burst.
	pending := make([][]byte, 0, rawReadQueueCapacity)
	pendingBytes := 0

	stopIdle := func() {
		if idleC == nil {
			return
		}
		if !idleTimer.Stop() {
			select {
			case <-idleTimer.C:
			default:
			}
		}
		idleC = nil
	}
	stopMax := func() {
		if maxC == nil {
			return
		}
		if !maxTimer.Stop() {
			select {
			case <-maxTimer.C:
			default:
			}
		}
		maxC = nil
	}
	resetIdle := func() {
		if idleC != nil {
			if !idleTimer.Stop() {
				select {
				case <-idleTimer.C:
				default:
				}
			}
		}
		idleTimer.Reset(outputBatchIdle)
		idleC = idleTimer.C
	}
	startMax := func() {
		if maxC != nil {
			return
		}
		maxTimer.Reset(outputBatchMaxAge)
		maxC = maxTimer.C
	}
	returnPending := func() {
		for _, chunk := range pending {
			recycle(chunk)
		}
		clear(pending)
		pending = pending[:0]
		pendingBytes = 0
	}
	flush := func() {
		if pendingBytes == 0 {
			stopIdle()
			stopMax()
			return
		}
		data := make([]byte, pendingBytes)
		offset := 0
		for _, chunk := range pending {
			offset += copy(data[offset:], chunk)
		}
		returnPending()
		stopIdle()
		stopMax()
		t.publish(goprotocol.TerminalEvent{
			Kind: goprotocol.OutputEvent,
			Size: t.Size(),
			Data: data,
		})
	}
	appendChunk := func(chunk []byte) {
		if len(chunk) == 0 {
			recycle(chunk)
			return
		}
		if pendingBytes > 0 && pendingBytes+len(chunk) > readBlockBytes {
			flush()
		}
		if pendingBytes == 0 {
			startMax()
		}
		pending = append(pending, chunk)
		pendingBytes += len(chunk)
		if pendingBytes >= readBlockBytes {
			flush()
		} else {
			// Each useful read in a micro-burst
			// extends the idle window, while max age still bounds a continuous
			// stream to five milliseconds.
			resetIdle()
		}
	}
	drainObserved := func() {
		for {
			select {
			case chunk, ok := <-raw:
				if !ok {
					return
				}
				appendChunk(chunk)
			default:
				return
			}
		}
	}

	for {
		select {
		case chunk, ok := <-raw:
			if !ok {
				flush()
				return
			}
			appendChunk(chunk)
		case <-idleC:
			idleC = nil
			flush()
		case <-maxC:
			maxC = nil
			flush()
		case req := <-t.resizeRequests:
			// Pause the raw reader at a block boundary. A micro-burst can hold
			// bytes outside raw, and those must keep their old geometry too.
			barrier := rawFlushRequest{ready: make(chan struct{}), resume: make(chan struct{})}
			select {
			case t.rawFlushRequests <- barrier:
			case <-rawReaderDone:
			}
			_ = t.ptmx.SetReadDeadline(time.Now())
		waiting:
			for {
				select {
				case chunk, ok := <-raw:
					if !ok {
						break waiting
					}
					appendChunk(chunk)
				case <-barrier.ready:
					break waiting
				case <-rawReaderDone:
					break waiting
				case <-idleC:
					idleC = nil
					flush()
				case <-maxC:
					maxC = nil
					flush()
				}
			}
			// Serialize resize with every PTY block already observed by the
			// reader. Preserve authoritative stream
			// ordering: Output(old geometry) -> Resize -> Output(new geometry).
			drainObserved()
			flush()
			if req.cellWidth > 0 {
				t.cellWidth = min(req.cellWidth, 1024)
			}
			if req.cellHeight > 0 {
				t.cellHeight = min(req.cellHeight, 1024)
			}
			err := pty.Setsize(t.ptmx, &pty.Winsize{
				Rows: uint16(req.size.Lines),
				Cols: uint16(req.size.Columns),
				X:    uint16(min(65535, t.cellWidth*req.size.Columns)),
				Y:    uint16(min(65535, t.cellHeight*req.size.Lines)),
			})
			if err == nil {
				t.mu.Lock()
				t.size = req.size
				t.mu.Unlock()
				t.publish(goprotocol.TerminalEvent{
					Kind: goprotocol.ResizeEvent,
					Size: req.size,
				})
			}
			close(barrier.resume)
			req.done <- err
		case <-t.closed:
			// Closing ptmx wakes the raw reader. Keep draining until it closes
			// so bytes already returned by the kernel still precede Exit.
			for chunk := range raw {
				appendChunk(chunk)
			}
			flush()
			return
		}
	}
}

func (t *Terminal) rawReadLoop(out chan<- []byte, free <-chan []byte) {
	defer close(out)
	raw, err := t.ptmx.SyscallConn()
	if err != nil {
		return
	}
	var buf []byte
	pause := func(request rawFlushRequest) bool {
		_ = t.ptmx.SetReadDeadline(time.Time{})
		close(request.ready)
		select {
		case <-request.resume:
			return true
		case <-t.closed:
			return false
		}
	}
	for {
		select {
		case request := <-t.rawFlushRequests:
			if !pause(request) {
				return
			}
		default:
		}
		if buf == nil {
			select {
			case buf = <-free:
			case request := <-t.rawFlushRequests:
				if !pause(request) {
					return
				}
				continue
			case <-t.closed:
				return
			default:
				buf = make([]byte, readBlockBytes)
			}
		}
		buf = buf[:readBlockBytes]

		n := 0
		var readErr error
		// Drain available fragments into one reader-owned block. A partial
		// block can wait for readiness up to the idle/max-age deadline, avoiding
		// a channel handoff/timer reset for every ~1 KiB Darwin PTY fragment.
		var burstStarted time.Time
		waitingForData := false
		err := raw.Read(func(fd uintptr) bool {
			for n < len(buf) {
				gometrics.PTYReadCalls.Add(1)
				count, callErr := syscall.Read(int(fd), buf[n:])
				if count > 0 {
					n += count
					gometrics.PTYBytesRead.Add(uint64(count))
					if burstStarted.IsZero() {
						burstStarted = time.Now()
					}
				}
				if errors.Is(callErr, syscall.EINTR) {
					continue
				}
				if errors.Is(callErr, syscall.EAGAIN) || errors.Is(callErr, syscall.EWOULDBLOCK) {
					gometrics.PTYReadWouldBlock.Add(1)
					if n == 0 {
						return false
					}
					// Keep this partial block while the poller waits for readiness.
					// Bound that wait so a prompt or resize cannot get stuck behind
					// an unfinished block, without spinning on an empty PTY.
					deadline := time.Now().Add(outputBatchIdle)
					if maximum := burstStarted.Add(outputBatchMaxAge); maximum.Before(deadline) {
						deadline = maximum
					}
					_ = t.ptmx.SetReadDeadline(deadline)
					waitingForData = true
					return false
				}
				if callErr != nil {
					readErr = callErr
					return true
				}
				if count == 0 {
					readErr = io.EOF
					return true
				}
				if time.Since(burstStarted) >= outputBatchMaxAge {
					return true
				}
			}
			return true
		})
		if waitingForData {
			_ = t.ptmx.SetReadDeadline(time.Time{})
		}
		if n > 0 {
			out <- buf[:n]
			buf = nil
		}
		if err != nil || readErr != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) {
				continue
			}
			// Darwin/Linux PTYs commonly surface EIO after child exit.
			return
		}
	}
}

func (t *Terminal) waitLoop() {
	err := t.cmd.Wait()
	if t.shellIntegrationDir != "" {
		_ = os.RemoveAll(t.shellIntegrationDir)
	}
	// Preserve stream ordering: all bytes
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
	t.registry.retire(t.ID)
}

func (t *Terminal) publish(ev goprotocol.TerminalEvent) {
	ev.Seq = t.seq.Add(1)
	t.mu.Lock()
	t.storeLocked(ev)
	t.trimReplayLocked()
	subs := make([]*subscriber, 0, len(t.subs))
	for _, sub := range t.subs {
		subs = append(subs, sub)
	}
	t.mu.Unlock()

	// The 64-event subscriber queue is a
	// bounded backpressure boundary. Never silently drop a terminal event.
	for _, sub := range subs {
		// Preserve final/exit events for consumers that still have capacity.
		// Closing only cancels delivery that would otherwise remain blocked.
		select {
		case sub.events <- ev:
			continue
		default:
		}
		select {
		case sub.events <- ev:
		case <-sub.done:
		case <-t.closed:
			return
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
