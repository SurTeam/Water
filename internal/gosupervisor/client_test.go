package gosupervisor

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func pipeClient(t *testing.T) (*Client, net.Conn) {
	t.Helper()
	local, peer := net.Pipe()
	c := &Client{conn: local, events: make(map[string]chan Event), generations: make(map[string]uint64), closed: make(chan struct{})}
	go c.readLoop()
	t.Cleanup(func() { c.Close(); peer.Close() })
	return c, peer
}

// replyBeforeWriteReturns forces the reader to dispatch the response before the
// request's final Write returns. The third Read starts only after dispatch.
type replyBeforeWriteReturns struct {
	net.Conn
	mu            sync.Mutex
	reads, writes int
	dispatched    chan struct{}
}

func (c *replyBeforeWriteReturns) Read(p []byte) (int, error) {
	c.mu.Lock()
	c.reads++
	if c.reads == 3 {
		close(c.dispatched)
	}
	c.mu.Unlock()
	return c.Conn.Read(p)
}
func (c *replyBeforeWriteReturns) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	c.mu.Lock()
	c.writes++
	second := c.writes == 2
	c.mu.Unlock()
	if second && err == nil {
		<-c.dispatched
	}
	return n, err
}

func TestImmediateRepliesAreRegisteredBeforeWrite(t *testing.T) {
	for _, method := range []string{"spawn", "attach", "list", "foreground"} {
		t.Run(method, func(t *testing.T) {
			local, peer := net.Pipe()
			defer peer.Close()
			conn := &replyBeforeWriteReturns{Conn: local, dispatched: make(chan struct{})}
			c := &Client{conn: conn, events: make(map[string]chan Event), generations: make(map[string]uint64), closed: make(chan struct{})}
			defer c.Close()
			go c.readLoop()
			id := uuid.New()
			go func() {
				kind, _, err := readFrame(peer)
				if err != nil {
					return
				}
				switch kind {
				case kindSpawn:
					_ = writeJSON(peer, kindSpawned, spawnedResponse{ID: id.String(), PID: 42})
				case kindAttach:
					_ = writeJSON(peer, kindAttached, attachedResponse{ID: id.String(), PID: 42})
				case kindList:
					_ = writeJSON(peer, kindListed, listedResponse{})
				case kindForeground:
					_ = writeJSON(peer, kindForegrounded, foregroundResponse{ID: id.String(), Group: 42})
				}
			}()
			done := make(chan error, 1)
			go func() {
				var err error
				switch method {
				case "spawn":
					_, _, err = c.Spawn(SpawnSpec{ID: id})
				case "attach":
					_, _, err = c.Attach(id, 0)
				case "list":
					_, err = c.List()
				case "foreground":
					_, err = c.Foreground(id)
				}
				done <- err
			}()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("immediate reply was lost")
			}
		})
	}
}

func TestConcurrentForegroundDistinctTerminals(t *testing.T) {
	c, peer := pipeClient(t)
	const count = 64
	ids := make([]uuid.UUID, count)
	groups := make(map[string]int, count)
	for i := range ids {
		ids[i] = uuid.New()
		groups[ids[i].String()] = i + 10
	}
	go func() {
		for range count {
			_, payload, err := readFrame(peer)
			if err != nil {
				return
			}
			var req terminalRef
			if unmarshal(payload, &req) != nil {
				return
			}
			if writeJSON(peer, kindForegrounded, foregroundResponse{ID: req.ID, Group: groups[req.ID]}) != nil {
				return
			}
		}
	}()
	errs := make(chan error, count)
	for i, id := range ids {
		go func() {
			group, err := c.Foreground(id)
			if err == nil && group != i+10 {
				err = errors.New("reply belonged to another terminal")
			}
			errs <- err
		}()
	}
	for range count {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
}

func TestCanceledForegroundConsumesLateReply(t *testing.T) {
	c, peer := pipeClient(t)
	first, second := uuid.New(), uuid.New()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := c.ForegroundContext(ctx, first); done <- err }()
	if _, _, err := readFrame(peer); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	go func() {
		_, _, err := readFrame(peer)
		if err != nil {
			return
		}
		_ = writeJSON(peer, kindForegrounded, foregroundResponse{ID: first.String(), Group: 11})
		_ = writeJSON(peer, kindForegrounded, foregroundResponse{ID: second.String(), Group: 22})
	}()
	group, err := c.Foreground(second)
	if err != nil || group != 22 {
		t.Fatalf("group=%d error=%v", group, err)
	}
}

func TestForegroundCancellationInterruptsBlockedWrite(t *testing.T) {
	c, _ := pipeClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := c.ForegroundContext(ctx, uuid.New())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
	select {
	case <-c.closed:
	default:
		t.Fatal("partial frame connection remained open")
	}
}

func TestForegroundRejectsWrongTerminal(t *testing.T) {
	c, peer := pipeClient(t)
	go func() {
		_, _, _ = readFrame(peer)
		_ = writeJSON(peer, kindForegrounded, foregroundResponse{ID: uuid.New().String(), Group: 22})
	}()
	if _, err := c.Foreground(uuid.New()); err == nil {
		t.Fatal("accepted wrong terminal reply")
	}
}
