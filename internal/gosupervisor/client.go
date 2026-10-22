package gosupervisor

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Client is one server's connection to the supervisor. A server may drop this
// connection and reconnect; terminals and their programs keep running.
type Client struct {
	conn      net.Conn
	writeOnce sync.Once
	writeGate chan struct{} // serializes waiter registration and frame writes

	pendingMu sync.Mutex
	pending   [slotCount][]*pendingReply

	eventsMu    sync.Mutex
	events      map[string]chan Event
	generations map[string]uint64
	closed      chan struct{}
	once        sync.Once
	failure     error
}

// Event is one supervisor event for a terminal.
type Event struct {
	Kind     EventKind
	Output   []byte
	Offset   uint64
	Lines    uint16
	Columns  uint16
	ExitCode int32
}

type EventKind uint8

const (
	EventOutput EventKind = iota + 1
	EventResize
	EventExit
)

// SpawnSpec describes a terminal the server wants the supervisor to own.
type SpawnSpec struct {
	ID      uuid.UUID
	Program string
	Args    []string
	Cwd     string
	Env     []string
	Size    struct{ Lines, Columns uint16 }
	Cell    struct{ Width, Height uint16 }
	Replace bool
}

// Attach describes retained history for a resumed terminal.
type Attach struct {
	PID       int
	Offset    uint64
	Available uint64
	Truncated bool
	Exited    bool
	Code      int32
}

// Dial connects to a supervisor socket.
func Dial(socket string) (*Client, error) {
	conn, err := net.DialTimeout("unix", socket, 2*time.Second)
	if err != nil {
		return nil, err
	}
	// The output path is the hot path: ask for generous socket buffers so a
	// burst from a fast program is not throttled by the default 8 KiB window.
	if unix, ok := conn.(*net.UnixConn); ok {
		_ = unix.SetReadBuffer(1 << 20)
		_ = unix.SetWriteBuffer(1 << 20)
	}
	c := &Client{conn: conn, events: make(map[string]chan Event), generations: make(map[string]uint64), closed: make(chan struct{})}
	go c.readLoop()
	return c, nil
}

// Close drops this server's connection. Terminals stay alive in the supervisor.
func (c *Client) Close() error {
	c.shutdown()
	return nil
}

// shutdown closes the connection exactly once; the reader also calls it when the
// peer goes away, so both paths share one owner of c.closed.
func (c *Client) shutdown() {
	c.once.Do(func() {
		close(c.closed)
		_ = c.conn.Close()
	})
}

const requestTimeout = 10 * time.Second

type pendingReply struct {
	id string
	ch chan any
}

// request registers the waiter while holding the same gate as frame writes.
// Queue order therefore always agrees with wire order, even for immediate replies.
func (c *Client) request(ctx context.Context, kind byte, value any, slot int, id string) (any, error) {
	ch, err := c.sendRequest(ctx, kind, value, slot, id)
	if err != nil {
		return nil, err
	}
	select {
	case value := <-ch:
		return value, nil
	case <-ctx.Done():
		// Keep this FIFO entry as a tombstone: the old protocol has no request IDs,
		// so removing it would let a late reply satisfy the next request.
		return nil, ctx.Err()
	case <-c.closed:
		return nil, c.connectionError()
	}
}

func (c *Client) connectionError() error {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	if c.failure != nil {
		return c.failure
	}
	return errors.New("supervisor connection closed")
}

func (c *Client) fail(err error) {
	c.pendingMu.Lock()
	if c.failure == nil {
		c.failure = err
	}
	c.pendingMu.Unlock()
	c.shutdown()
}

func (c *Client) sendRequest(ctx context.Context, kind byte, value any, slot int, id string) (chan any, error) {
	c.writeOnce.Do(func() { c.writeGate = make(chan struct{}, 1) })
	select {
	case c.writeGate <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.closed:
		return nil, c.connectionError()
	}
	defer func() { <-c.writeGate }()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case <-c.closed:
		return nil, c.connectionError()
	default:
	}
	deadline, _ := ctx.Deadline()
	if err := c.conn.SetWriteDeadline(deadline); err != nil {
		c.fail(err)
		return nil, err
	}
	// Interrupt an in-flight write on cancellation. Stop and join the callback
	// before clearing the deadline, so it cannot affect the next writer.
	interrupted := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { _ = c.conn.SetWriteDeadline(time.Now()); close(interrupted) })
	var ch chan any
	if slot >= 0 {
		ch = make(chan any, 1)
		c.pendingMu.Lock()
		c.pending[slot] = append(c.pending[slot], &pendingReply{id: id, ch: ch})
		c.pendingMu.Unlock()
	}
	err := writeJSON(c.conn, kind, value)
	if !stop() {
		<-interrupted
	}
	_ = c.conn.SetWriteDeadline(time.Time{})
	if err != nil {
		// A partial frame makes this stream unusable; closing also releases waiters.
		c.fail(err)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if !deadline.IsZero() && !time.Now().Before(deadline) {
			return nil, context.DeadlineExceeded
		}
	}
	return ch, err
}

func (c *Client) send(kind byte, value any) error {
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	_, err := c.sendRequest(ctx, kind, value, -1, "")
	return err
}

func (c *Client) call(kind byte, value any, slot int, id string) (any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	return c.request(ctx, kind, value, slot, id)
}

// Spawn creates or resumes a terminal. Resumed is true when a live terminal with
// the same id already exists, which is how an upgraded server keeps programs.
func (c *Client) Spawn(spec SpawnSpec) (pid int, resumed bool, err error) {
	req := spawnRequest{
		ID:      spec.ID.String(),
		Program: spec.Program,
		Args:    spec.Args,
		Cwd:     spec.Cwd,
		Env:     spec.Env,
		Lines:   spec.Size.Lines,
		Columns: spec.Size.Columns,
		CellW:   spec.Cell.Width,
		CellH:   spec.Cell.Height,
		Replace: spec.Replace,
	}
	value, err := c.call(kindSpawn, req, spawnedSlot, req.ID)
	resp, _ := value.(spawnedResponse)
	if err != nil {
		return 0, false, err
	}
	if resp.Error != "" {
		return 0, false, errors.New(resp.Error)
	}
	return resp.PID, resp.Resumed, nil
}

// Attach subscribes to a terminal's output, replaying retained history first.
func (c *Client) Attach(id uuid.UUID, offset uint64) (Attach, <-chan Event, error) {
	key := id.String()
	events := make(chan Event, 256)
	c.eventsMu.Lock()
	c.events[key] = events
	c.eventsMu.Unlock()
	value, err := c.call(kindAttach, attachRequest{ID: key, Offset: offset}, attachedSlot, key)
	resp, _ := value.(attachedResponse)
	if err != nil {
		c.dropEvents(key)
		return Attach{}, nil, err
	}
	if resp.Error != "" {
		c.dropEvents(key)
		return Attach{}, nil, errors.New(resp.Error)
	}
	return Attach{PID: resp.PID, Offset: resp.Offset, Available: resp.Available, Truncated: resp.Truncated, Exited: resp.Exited, Code: resp.Code}, events, nil
}

// Write sends terminal input.
func (c *Client) Write(id uuid.UUID, data []byte) error {
	if len(data) == 0 {
		return nil
	}
	return c.send(kindWrite, writeRequest{ID: id.String(), Data: data})
}

// Resize applies a new geometry; the supervisor orders it against output.
func (c *Client) Resize(id uuid.UUID, lines, columns, cellW, cellH uint16) error {
	return c.send(kindResize, resizeRequest{ID: id.String(), Lines: lines, Columns: columns, CellW: cellW, CellH: cellH})
}

// CloseTerminal terminates a terminal's process group.
func (c *Client) CloseTerminal(id uuid.UUID) error {
	return c.send(kindClose, terminalRef{ID: id.String()})
}

// Foreground reports the PTY's foreground process group for a terminal.
func (c *Client) Foreground(id uuid.UUID) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	return c.ForegroundContext(ctx, id)
}

// ForegroundContext bounds both writing the query and waiting for its reply.
func (c *Client) ForegroundContext(ctx context.Context, id uuid.UUID) (int, error) {
	value, err := c.request(ctx, kindForeground, terminalRef{ID: id.String()}, foregroundSlot, id.String())
	if err != nil {
		return 0, err
	}
	resp := value.(foregroundResponse)
	if resp.Error != "" {
		return 0, errors.New(resp.Error)
	}
	return resp.Group, nil
}

// List reports terminals the supervisor still owns.
func (c *Client) List() ([]ListedTerminal, error) {
	value, err := c.call(kindList, struct{}{}, listedSlot, "")
	resp, _ := value.(listedResponse)
	if err != nil {
		return nil, err
	}
	out := make([]ListedTerminal, 0, len(resp.Terminals))
	for _, t := range resp.Terminals {
		out = append(out, ListedTerminal{ID: t.ID, PID: t.PID, Exited: t.Exited})
	}
	return out, nil
}

type ListedTerminal struct {
	ID     string
	PID    int
	Exited bool
}

// closeEventsLocked closes a terminal's channel exactly once. Callers hold
// eventsMu, and the entry is removed before closing so no path can close twice.
func (c *Client) closeEventsLocked(key string) {
	if ch, ok := c.events[key]; ok {
		delete(c.events, key)
		delete(c.generations, key)
		close(ch)
	}
}

func (c *Client) dropEvents(key string) {
	c.eventsMu.Lock()
	c.closeEventsLocked(key)
	c.eventsMu.Unlock()
}

// readLoop is the only reader of the connection. Responses are delivered in
// request order, so a single pending queue matches them without correlation ids.
func (c *Client) readLoop() {
	defer func() {
		c.eventsMu.Lock()
		for key := range c.events {
			c.closeEventsLocked(key)
		}
		c.eventsMu.Unlock()
		c.shutdown()
	}()
	for {
		kind, payload, err := readFrame(c.conn)
		if err != nil {
			c.pendingMu.Lock()
			c.failure = err
			c.pendingMu.Unlock()
			return
		}
		switch kind {
		case kindSpawned:
			var resp spawnedResponse
			if err := unmarshal(payload, &resp); err != nil {
				return
			}
			c.deliver(spawnedSlot, resp)
		case kindAttached:
			var resp attachedResponse
			if err := unmarshal(payload, &resp); err != nil {
				return
			}
			c.deliver(attachedSlot, resp)
		case kindListed:
			var resp listedResponse
			if err := unmarshal(payload, &resp); err != nil {
				return
			}
			c.deliver(listedSlot, resp)
		case kindForegrounded:
			var resp foregroundResponse
			if err := unmarshal(payload, &resp); err != nil {
				return
			}
			c.deliver(foregroundSlot, resp)
		case kindFailure:
			var resp failureResponse
			if err := unmarshal(payload, &resp); err != nil {
				return
			}
			c.deliver(failureSlot, resp)
		case kindOutput:
			header, data, err := parseOutput(payload)
			if err != nil {
				return
			}
			c.publish(header, data)
		case kindResized:
			var ev resizedEvent
			if err := unmarshal(payload, &ev); err != nil {
				return
			}
			c.publishEvent(ev.ID, ev.Generation, Event{Kind: EventResize, Lines: ev.Lines, Columns: ev.Columns})
		case kindExited:
			var ev exitedEvent
			if err := unmarshal(payload, &ev); err != nil {
				return
			}
			c.publishEvent(ev.ID, ev.Generation, Event{Kind: EventExit, ExitCode: ev.Code})
		}
	}
}

// publish forwards output without blocking the reader. A slow server consumer
// loses the connection instead of stalling the supervisor's other terminals; the
// server then re-attaches from its last offset.
func (c *Client) publish(header outputHeader, data []byte) {
	id, err := uuid.FromBytes(header.id[:])
	if err != nil {
		return
	}
	key := id.String()
	c.eventsMu.Lock()
	ch := c.events[key]
	current := c.generations[key]
	c.eventsMu.Unlock()
	if ch == nil || header.generation != current {
		// A replacement reuses the supervisor key, so frames from the retired
		// generation must not leak into the successor's stream.
		return
	}
	copied := append([]byte(nil), data...)
	select {
	case ch <- Event{Kind: EventOutput, Output: copied, Offset: header.offset}:
	default:
		c.eventsMu.Lock()
		if current, ok := c.events[key]; ok && current == ch {
			c.closeEventsLocked(key)
		}
		c.eventsMu.Unlock()
	}
}

func (c *Client) publishEvent(id string, generation uint64, event Event) {
	c.eventsMu.Lock()
	ch := c.events[id]
	current := c.generations[id]
	c.eventsMu.Unlock()
	if ch == nil || generation != current {
		return
	}
	select {
	case ch <- event:
	default:
	}
}

const (
	spawnedSlot = iota
	attachedSlot
	listedSlot
	foregroundSlot
	failureSlot
	slotCount
)

// deliver hands a reply to the oldest waiting caller of that kind. Replies arrive
// in request order on one connection, so a FIFO per kind matches them without
// correlation ids while staying correct for concurrent callers.
func (c *Client) deliver(slot int, value any) {
	c.pendingMu.Lock()
	if len(c.pending[slot]) == 0 {
		c.pendingMu.Unlock()
		return
	}
	waiter := c.pending[slot][0]
	c.pending[slot][0] = nil
	c.pending[slot] = c.pending[slot][1:]
	c.pendingMu.Unlock()
	var id string
	switch resp := value.(type) {
	case spawnedResponse:
		id = resp.ID
	case attachedResponse:
		id = resp.ID
	case foregroundResponse:
		id = resp.ID
	}
	if waiter.id != id {
		c.fail(fmt.Errorf("supervisor reply terminal %q, want %q", id, waiter.id))
		return
	}
	if resp, ok := value.(attachedResponse); ok {
		// Set the generation before processing the replay frames following this reply.
		c.eventsMu.Lock()
		if _, ok := c.events[resp.ID]; ok {
			c.generations[resp.ID] = resp.Generation
		}
		c.eventsMu.Unlock()
	}
	waiter.ch <- value
}
