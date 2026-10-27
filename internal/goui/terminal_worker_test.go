package goui

import (
	"context"
	"errors"
	"github.com/SurTeam/Water/internal/goconfig"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/SurTeam/Water/internal/goclient"
	"github.com/SurTeam/Water/internal/goprotocol"
	"github.com/SurTeam/Water/internal/govt"
	"github.com/google/uuid"
)

func awaitWorkerCondition(t *testing.T, fn func() bool) {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for !fn() {
		select {
		case <-deadline.C:
			t.Fatal("worker condition timed out")
		case <-tick.C:
		}
	}
}

func TestPaneOutputWorkerIsolationAndClose(t *testing.T) {
	a := &terminalClient{id: uuid.New(), emu: govt.New(20, 4, 100)}
	b := &terminalClient{id: uuid.New(), emu: govt.New(20, 4, 100)}
	events := make(chan goclient.TerminalPush, 8)
	frames := make(chan struct{}, 32)
	c := &WorkspaceClient{session: &goclient.Session{Events: events}, terminals: map[uuid.UUID]*terminalClient{a.id: a, b.id: b}, snapshotWake: make(chan struct{}, 1), invalidate: func() {
		select {
		case frames <- struct{}{}:
		default:
		}
	}}
	a.parseMu.Lock()
	runDone := make(chan struct{})
	go func() { c.Run(); close(runDone) }()
	// Also queue a snapshot of A: its blocked publication must not occupy Run.
	awaitWorkerCondition(t, func() bool { c.mu.RLock(); defer c.mu.RUnlock(); return c.outputWake != nil })
	a.dirty.Store(true)
	c.mu.RLock()
	notify := c.outputWake
	c.mu.RUnlock()
	notify <- struct{}{}
	awaitWorkerCondition(t, func() bool { return a.publishing.Load() })
	event := func(term *terminalClient, text string) {
		events <- goclient.TerminalPush{TerminalID: term.id, Event: goprotocol.TerminalEvent{Seq: 1, Kind: goprotocol.OutputEvent, Data: []byte(text)}}
	}
	event(a, "blocked")
	event(b, "ready")
	awaitWorkerCondition(t, func() bool {
		b.mu.RLock()
		defer b.mu.RUnlock()
		return len(b.snapshot.RowsData) > 0 && b.snapshot.RowsData[0].Cells[0].Text == "r"
	})
	stopped := make(chan struct{})
	go func() { a.stopWorker(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("close waited for parser")
	}
	a.worker.mu.Lock()
	queued := len(a.worker.queue)
	a.worker.mu.Unlock()
	if queued != 0 {
		t.Fatal("close retained output queue")
	}
	a.parseMu.Unlock()
	awaitWorkerCondition(t, func() bool {
		a.worker.mu.Lock()
		done := a.worker.done
		a.worker.mu.Unlock()
		if done == nil {
			return false
		}
		select {
		case <-done:
			return true
		default:
			return false
		}
	})
	a.mu.RLock()
	released := a.emu == nil
	a.mu.RUnlock()
	if !released {
		t.Fatal("worker did not release emulator")
	}
	close(events)
	select {
	case <-runDone:
	case <-time.After(time.Second):
		t.Fatal("Run did not exit")
	}
	awaitWorkerCondition(t, func() bool {
		b.worker.mu.Lock()
		done := b.worker.done
		b.worker.mu.Unlock()
		select {
		case <-done:
			return true
		default:
			return false
		}
	})
}

func TestCompatibilityOutputOverflowFailsExplicitly(t *testing.T) {
	term := &terminalClient{id: uuid.New(), emu: govt.New(20, 4, 100)}
	term.parseMu.Lock()
	c := &WorkspaceClient{}
	for i := 0; i < terminalQueueEvents+2; i++ {
		term.enqueueEvent(c, goclient.TerminalPush{TerminalID: term.id, Event: goprotocol.TerminalEvent{Seq: uint64(i + 1), Kind: goprotocol.OutputEvent, Data: []byte("ansi")}})
	}
	term.worker.mu.Lock()
	failed := term.worker.err != nil && len(term.worker.queue) == 0 && term.worker.bytes == 0
	done := term.worker.done
	term.worker.mu.Unlock()
	if !failed || !term.closed.Load() {
		t.Fatal("overflow did not fail explicitly and release queued output")
	}
	c.mu.RLock()
	reported := c.connectionError != ""
	c.mu.RUnlock()
	if !reported {
		t.Fatal("overflow error was not exposed")
	}
	term.parseMu.Unlock()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("overflow cleanup did not finish")
	}
}

func TestStalePaneWorkerCannotParseReplacement(t *testing.T) {
	id := uuid.New()
	old := &terminalClient{id: id, emu: govt.New(20, 4, 100)}
	next := &terminalClient{id: id, emu: govt.New(20, 4, 100)}
	defer next.close()
	old.stopWorker()
	c := &WorkspaceClient{terminals: map[uuid.UUID]*terminalClient{id: next}}
	c.applyTerminalEventFor(old, goclient.TerminalPush{TerminalID: id, Event: goprotocol.TerminalEvent{Seq: 1, Kind: goprotocol.OutputEvent, Data: []byte("stale")}})
	if next.lastSeq != 0 {
		t.Fatal("stale worker changed replacement")
	}
	<-old.worker.done
}

func TestOwnedPaneBackpressurePreservesLiveQueryReply(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is required for the raw PTY query fixture")
	}
	socket, stop := startWorkspaceTestServer(t)
	defer stop()
	session, err := goclient.New(socket).OpenSession()
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	script := `import os,tty,time,select
 tty.setraw(0)
 os.write(1,b"READY")
 os.read(0,1)
 for i in range(40):
  os.write(1,b"x")
  time.sleep(.003)
 os.write(1,b"\x1b[6n")
 reply=b""
 while not reply.endswith(b"R"):
  if not select.select([0],[],[],5)[0]:
   os.write(1,b"QUERY_TIMEOUT")
   raise SystemExit(1)
  reply+=os.read(0,64)
 os.write(1,b"QUERY_REPLY_OK:"+reply.hex().encode()+b"\r\n")
 time.sleep(.1)
 `
	// Strip indentation from the Go raw literal's top-level Python statements.
	script = strings.ReplaceAll(script, "\n ", "\n")
	var spawned struct {
		TerminalID uuid.UUID `json:"terminal_id"`
	}
	if err := session.Dispatch(map[string]any{"type": "terminal.spawn", "program": python, "args": []string{"-u", "-c", script}, "columns": 80, "lines": 24}, &spawned); err != nil {
		t.Fatal(err)
	}
	c := NewWorkspaceClientWithConfig(session, nil, goconfig.Default())
	defer c.Close()
	ticket := newTerminalAttachAttempt()
	c.attaching = map[uuid.UUID]*terminalAttachAttempt{spawned.TerminalID: ticket}
	c.attachTerminalAsync(terminalSummary{TerminalID: spawned.TerminalID}, true, ticket)
	c.mu.RLock()
	term := c.terminals[spawned.TerminalID]
	c.mu.RUnlock()
	if term == nil {
		t.Fatal("terminal attach failed")
	}
	content := func() string {
		term.mu.RLock()
		defer term.mu.RUnlock()
		if term.emu == nil {
			return ""
		}
		text, _ := term.emu.ReadContent(nil, nil)
		return text.Text
	}
	awaitWorkerCondition(t, func() bool { return strings.Contains(content(), "READY") })
	term.parseMu.Lock()
	locked := true
	defer func() {
		if locked {
			term.parseMu.Unlock()
		}
	}()
	term.worker.mu.Lock()
	sub := term.worker.subscription
	term.worker.mu.Unlock()
	if sub == nil {
		t.Fatal("pane did not own an isolated subscription")
	}
	if err := session.DispatchAsync(map[string]any{"type": "terminal.send_bytes", "terminal_id": term.id, "bytes": []int{'?'}}); err != nil {
		t.Fatal(err)
	}
	awaitWorkerCondition(t, func() bool { return len(sub.Events) == cap(sub.Events) })
	// Saturating the pane queue must leave its GUI control socket available.
	if err := session.CallTimeout("ping", nil, nil, time.Second); err != nil {
		t.Fatal(err)
	}
	if len(session.Events) != 0 {
		t.Fatal("pane output entered aggregate event queue")
	}
	term.parseMu.Unlock()
	locked = false
	awaitWorkerCondition(t, func() bool { return strings.Contains(content(), "QUERY_REPLY_OK:") })
	term.worker.mu.Lock()
	workerErr := term.worker.err
	term.worker.mu.Unlock()
	if workerErr != nil {
		t.Fatalf("live query stream failed: %v", workerErr)
	}
	term.stopWorker()
	select {
	case <-term.worker.done:
	case <-time.After(time.Second):
		t.Fatal("pane worker shutdown did not complete")
	}
	drained := make(chan struct{})
	go func() {
		for range sub.Events {
		}
		close(drained)
	}()
	select {
	case <-drained:
	case <-time.After(time.Second):
		t.Fatal("pane shutdown did not close its subscription")
	}
}

func TestInitialAttachRetriesWithoutFurtherPush(t *testing.T) {
	attempts := 0
	_, err := retryTerminalAttach(context.Background(), func() (*goclient.TerminalSubscription, error) {
		attempts++
		if attempts < 3 {
			return nil, errors.New("transient attach")
		}
		return nil, nil
	})
	if err != nil || attempts != 3 {
		t.Fatalf("initial attach retry: attempts=%d err=%v", attempts, err)
	}
	attempts = 0
	_, err = retryTerminalAttach(context.Background(), func() (*goclient.TerminalSubscription, error) { attempts++; return nil, errors.New("permanent attach") })
	if err == nil || attempts != 4 {
		t.Fatalf("attach retry limit: attempts=%d err=%v", attempts, err)
	}
}

func TestAttachRetryCancellationAndStaleTicket(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	entered := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		_, err := retryTerminalAttach(ctx, func() (*goclient.TerminalSubscription, error) {
			entered <- struct{}{}
			return nil, errors.New("transient attach")
		})
		done <- err
	}()
	<-entered
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("attach cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("attach retry ignored cancellation")
	}
	id := uuid.New()
	old, next := newTerminalAttachAttempt(), newTerminalAttachAttempt()
	defer next.cancel()
	old.Store(true)
	c := &WorkspaceClient{attaching: map[uuid.UUID]*terminalAttachAttempt{id: next}}
	c.attachTerminalAsync(terminalSummary{TerminalID: id}, true, old)
	if c.attaching[id] != next {
		t.Fatal("late canceled attach removed replacement ticket")
	}
	if len(c.terminals) != 0 {
		t.Fatal("canceled attach installed terminal")
	}
}

// A worker can fill outputWake before Run reaches its receive loop. Startup
// must coalesce that notification instead of waiting on its own full channel.
func TestRunStartupCoalescesWorkerWake(t *testing.T) {
	term := &terminalClient{id: uuid.New(), emu: govt.New(20, 4, 100)}
	events := make(chan goclient.TerminalPush)
	c := &WorkspaceClient{session: &goclient.Session{Events: events}, terminals: map[uuid.UUID]*terminalClient{term.id: term}}
	term.worker.mu.Lock()
	done := make(chan struct{})
	go func() { c.Run(); close(done) }()
	awaitWorkerCondition(t, func() bool { c.mu.RLock(); defer c.mu.RUnlock(); return c.outputWake != nil })
	c.mu.RLock()
	notify := c.outputWake
	c.mu.RUnlock()
	notify <- struct{}{}
	term.worker.mu.Unlock()
	close(events)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run blocked sending its initial wake")
	}
	term.worker.mu.Lock()
	cleaned := term.worker.done
	term.worker.mu.Unlock()
	<-cleaned
}
