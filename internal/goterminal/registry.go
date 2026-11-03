package goterminal

import (
	"errors"
	"github.com/SurTeam/Water/internal/gopty"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/SurTeam/Water/internal/goprotocol"
	"github.com/google/uuid"
)

const (
	defaultScrollbackLines = 2000
	readBlockBytes         = 128 * 1024
	rawReadQueueCapacity   = 64
	idleReadCacheBlocks    = 2
	inputQueueCapacity     = 64
	inputQueueBytes        = 1 << 20
	outputBatchIdle        = time.Millisecond
	outputBatchMaxAge      = 5 * time.Millisecond
	subscriberQueueEvents  = 64
	subscriberQueueBytes   = 1 << 20
	resizeQueueCapacity    = 8
	resizeRequestTimeout   = time.Second
	// A tiny read right after input is almost certainly the echo the user is
	// waiting for; flush it immediately and keep larger reads batched.
	echoFlushWindow     = 100 * time.Millisecond
	echoFlushMaxBytes   = 64
	replayEventOverhead = 64
	exitedTerminalLimit = 32
	exitedReplayLimit   = 8 * 1024 * 1024
	coalesceReplayBytes = 32 * 1024
)

type Terminal struct {
	ID uuid.UUID

	registry *Registry

	cmd        *exec.Cmd
	ptmx       *gopty.Session
	supervisor *supervisedLink
	// supervisorKey is the pane id the supervisor holds the PTY under. It survives
	// server restarts, while ID identifies this terminal instance to clients.
	supervisorKey         string
	pid                   int
	program               string
	args                  []string // launch arguments, independent of local process ownership
	shellIntegrationDir   string
	cellWidth, cellHeight int        // owned by the ordered PTY reader
	executeMu             sync.Mutex // one foreground execution per pane
	writeMu               sync.Mutex
	lastInput             atomic.Int64 // UnixNano of the most recent input write
	input                 chan inputRequest
	inputBytes            atomic.Int64
	inputError            error

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

type inputRequest struct {
	data []byte
}

var (
	ErrInputQueueFull = errors.New("terminal input queue is full")
	ErrTerminalClosed = errors.New("terminal is closed")
	ErrResizeTimeout  = errors.New("terminal resize timed out")
)

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
	ptmx, err := gopty.Start(cmd, &gopty.Size{Rows: uint16(size.Lines), Cols: uint16(size.Columns)})
	if err != nil {
		return nil, err
	}
	t := &Terminal{
		registry:            r,
		shellIntegrationDir: integrationDir,
		ID:                  uuid.New(),
		cmd:                 cmd,
		ptmx:                ptmx,
		pid:                 ptmx.PID(),
		program:             program,
		args:                append([]string(nil), args...),
		size:                size,
		scrollbackLines:     r.scrollbackLines,
		subs:                make(map[uint64]*subscriber),
		closed:              make(chan struct{}),
		readerDone:          make(chan struct{}),
		resizeRequests:      make(chan resizeRequest, resizeQueueCapacity),
		rawFlushRequests:    make(chan rawFlushRequest, 1),
		input:               make(chan inputRequest, inputQueueCapacity),
	}
	r.mu.Lock()
	r.terms[t.ID] = t
	r.mu.Unlock()
	started = true
	go t.readLoop()
	go t.waitLoop()
	go t.inputLoop()
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
	if t.supervisor != nil {
		return t.supervisedWrite(data)
	}
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	if t.ptmx == nil {
		return io.ErrClosedPipe
	}
	defer t.ptmx.SetWriteDeadline(time.Time{})
	for len(data) > 0 {
		select {
		case <-t.closed:
			return ErrTerminalClosed
		default:
		}
		if err := t.ptmx.SetWriteDeadline(time.Now().Add(250 * time.Millisecond)); err != nil {
			return err
		}
		written, err := t.ptmx.Write(data)
		data = data[written:]
		if err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) {
				continue
			}
			return err
		}
		if written == 0 {
			return io.ErrNoProgress
		}
	}
	t.lastInput.Store(time.Now().UnixNano())
	return nil
}

// EnqueueInput accepts interactive bytes without performing PTY I/O on the
// caller's goroutine. Each terminal owns a bounded queue and one writer, so a
// program that stops reading stdin cannot stall its session's other panes.
func (t *Terminal) EnqueueInput(data []byte) error {
	if len(data) == 0 {
		return nil
	}
	t.mu.RLock()
	inputError := t.inputError
	t.mu.RUnlock()
	if inputError != nil {
		return inputError
	}
	if t.input == nil {
		return t.Write(data)
	}
	select {
	case <-t.closed:
		return ErrTerminalClosed
	default:
	}
	for {
		queued := t.inputBytes.Load()
		if queued+int64(len(data)) > inputQueueBytes {
			return ErrInputQueueFull
		}
		if t.inputBytes.CompareAndSwap(queued, queued+int64(len(data))) {
			break
		}
	}
	req := inputRequest{data: append([]byte(nil), data...)}
	select {
	case t.input <- req:
		return nil
	default:
		t.inputBytes.Add(-int64(len(req.data)))
		return ErrInputQueueFull
	}
}

func (t *Terminal) inputLoop() {
	for {
		select {
		case <-t.closed:
			return
		case req := <-t.input:
			t.inputBytes.Add(-int64(len(req.data)))
			if err := t.Write(req.data); err != nil {
				t.mu.Lock()
				t.inputError = err
				t.mu.Unlock()
				return
			}
		}
	}
}

type resizeRequest struct {
	size                  goprotocol.TerminalSize
	cellWidth, cellHeight int
	done                  chan error
	canceled              *atomic.Bool
}

type rawFlushRequest struct {
	ready  chan struct{}
	resume chan struct{}
}

func (t *Terminal) Resize(size goprotocol.TerminalSize) error {
	return t.ResizeWithCells(size, 0, 0)
}

func (t *Terminal) ResizeWithCells(size goprotocol.TerminalSize, cellWidth, cellHeight int) error {
	if t.supervisor != nil {
		return t.supervisedResize(size, cellWidth, cellHeight)
	}
	req := resizeRequest{size: size.Normalized(), cellWidth: cellWidth, cellHeight: cellHeight, done: make(chan error, 1), canceled: &atomic.Bool{}}
	timer := time.NewTimer(resizeRequestTimeout)
	defer timer.Stop()
	select {
	case t.resizeRequests <- req:
	case <-t.readerDone:
		return io.ErrClosedPipe
	case <-t.closed:
		return io.ErrClosedPipe
	case <-timer.C:
		return ErrResizeTimeout
	}
	select {
	case err := <-req.done:
		return err
	case <-t.readerDone:
		return io.ErrClosedPipe
	case <-t.closed:
		return io.ErrClosedPipe
	case <-timer.C:
		req.canceled.Store(true)
		return ErrResizeTimeout
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
	mu       sync.Mutex
	queue    []goprotocol.TerminalEvent
	bytes    int
	wake     chan struct{}
	space    chan struct{}
	events   chan goprotocol.TerminalEvent
	done     chan struct{}
	overflow atomic.Bool
	closed   atomic.Bool
	once     sync.Once
}

func (t *Terminal) Subscribe() (<-chan goprotocol.TerminalEvent, <-chan struct{}, func()) {
	events, done, cancel, _ := t.SubscribeWithOverflow()
	return events, done, cancel
}

func (t *Terminal) SubscribeWithOverflow() (<-chan goprotocol.TerminalEvent, <-chan struct{}, func(), func() bool) {
	t.mu.Lock()
	t.nextSub++
	id := t.nextSub
	sub := &subscriber{
		events: make(chan goprotocol.TerminalEvent, subscriberQueueEvents),
		wake:   make(chan struct{}, 1),
		space:  make(chan struct{}, 1),
		done:   make(chan struct{}),
	}
	t.subs[id] = sub
	t.mu.Unlock()
	go sub.deliver(t)

	cancel := func() {
		sub.close(t, id, false)
	}
	return sub.events, sub.done, cancel, sub.overflow.Load
}

func (s *subscriber) close(t *Terminal, id uint64, overflow bool) {
	s.once.Do(func() {
		s.closed.Store(true)
		if overflow {
			s.overflow.Store(true)
		}
		close(s.done)
		t.mu.Lock()
		delete(t.subs, id)
		t.mu.Unlock()
		s.mu.Lock()
		s.queue = nil
		s.bytes = 0
		s.mu.Unlock()
	})
}

func (s *subscriber) enqueue(t *Terminal, id uint64, ev goprotocol.TerminalEvent) bool {
	n := retainedEventBytes(ev)
	if n > subscriberQueueBytes {
		s.close(t, id, true)
		return false
	}
	for {
		s.mu.Lock()
		if s.closed.Load() {
			s.mu.Unlock()
			return false
		}
		if len(s.queue) >= subscriberQueueEvents || s.bytes+n > subscriberQueueBytes {
			s.mu.Unlock()
			select {
			case <-s.space:
				continue
			case <-s.done:
				return false
			case <-t.closed:
				return false
			}
		}
		s.queue = append(s.queue, ev)
		s.bytes += n
		s.mu.Unlock()
		select {
		case s.wake <- struct{}{}:
		default:
		}
		return true
	}
}

// deliver drains the bounded queue in sequence order. Closing an overflowing
// subscription ends only that pane's stream; the terminal and other panes
// remain live and the client reattaches from its last sequence.
func (s *subscriber) deliver(t *Terminal) {
	defer close(s.events)
	for {
		s.mu.Lock()
		if len(s.queue) == 0 {
			closed := s.closed.Load()
			s.mu.Unlock()
			if closed {
				return
			}
			select {
			case <-s.wake:
				continue
			case <-s.done:
				return
			}
		}
		ev := s.queue[0]
		s.queue[0] = goprotocol.TerminalEvent{}
		s.queue = s.queue[1:]
		s.bytes -= retainedEventBytes(ev)
		s.mu.Unlock()
		select {
		case s.space <- struct{}{}:
		default:
		}
		select {
		case s.events <- ev:
		case <-s.done:
			return
		}
	}
}

func (t *Terminal) Close() error {
	if t.supervisor != nil {
		var err error
		t.once.Do(func() {
			close(t.closed)
			err = t.supervisedClose()
			t.supervisor.closeLinks()
		})
		return err
	}
	var err error
	t.once.Do(func() {
		close(t.closed)
		err = t.ptmx.Kill()
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
			if req.canceled.Load() {
				close(barrier.resume)
				continue
			}
			err := t.ptmx.Resize(&gopty.Size{
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
			if !req.canceled.Load() {
				req.done <- err
			}
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

func (t *Terminal) waitLoop() {
	_ = t.ptmx.Wait()
	if t.shellIntegrationDir != "" {
		_ = os.RemoveAll(t.shellIntegrationDir)
	}
	// Preserve stream ordering: all bytes
	// readable from the PTY must be sequenced before the authoritative Exit.
	<-t.readerDone
	v := int32(t.ptmx.ExitCode())
	code := &v
	t.publish(goprotocol.TerminalEvent{Kind: goprotocol.ExitEvent, Code: code})
	_ = t.Close()
	t.registry.retire(t.ID)
}

func (t *Terminal) publish(ev goprotocol.TerminalEvent) {
	ev.Seq = t.seq.Add(1)
	t.mu.Lock()
	t.storeLocked(ev)
	t.trimReplayLocked()
	subs := make([]struct {
		id  uint64
		sub *subscriber
	}, 0, len(t.subs))
	for id, sub := range t.subs {
		subs = append(subs, struct {
			id  uint64
			sub *subscriber
		}{id, sub})
	}
	t.mu.Unlock()

	for _, sub := range subs {
		sub.sub.enqueue(t, sub.id, ev)
	}
}

func retainedEventBytes(ev goprotocol.TerminalEvent) int {
	n := replayEventOverhead
	if ev.Kind == goprotocol.OutputEvent {
		n += len(ev.Data)
	}
	return n
}
