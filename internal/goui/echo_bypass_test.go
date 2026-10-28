package goui

import (
	"testing"
	"time"

	"github.com/SurTeam/Water/internal/goclient"
	"github.com/SurTeam/Water/internal/goprotocol"
	"github.com/SurTeam/Water/internal/govt"
	"github.com/google/uuid"
)

func TestEchoCreditsAreBoundedAndExpire(t *testing.T) {
	term := &terminalClient{echoSignal: make(chan struct{}, 1)}
	now := time.Now()
	for i := 0; i < 10; i++ {
		term.noteInputEcho(now)
	}
	if got := term.echoCredits.Load(); got != maxEchoCredits {
		t.Fatalf("credits = %d, want %d", got, maxEchoCredits)
	}
	if !term.echoBypass(now) {
		t.Fatal("fresh input did not allow a bypass")
	}
	if term.echoBypass(now.Add(echoBypassWindow)) {
		t.Fatal("expired input still allowed a bypass")
	}
	if term.echoCredits.Load() != 0 || term.echoDeadline.Load() != 0 {
		t.Fatal("expired bypass state was not cleared")
	}
}

func TestEchoCreditIsConsumedOncePerPublish(t *testing.T) {
	term := &terminalClient{echoSignal: make(chan struct{}, 1)}
	now := time.Now()
	term.noteInputEcho(now)
	term.noteInputEcho(now)
	if !term.echoBypass(now) {
		t.Fatal("two inputs should allow a bypass")
	}
	term.consumeEchoCredit()
	if !term.echoBypass(now) {
		t.Fatal("second credit should remain usable")
	}
	term.consumeEchoCredit()
	if term.echoBypass(now) {
		t.Fatal("exhausted credits still allowed a bypass")
	}
	if term.echoDeadline.Load() != 0 {
		t.Fatal("deadline was not cleared with the last credit")
	}
}

func TestEchoBypassPublishesAheadOfWindowAndSignals(t *testing.T) {
	id := uuid.New()
	emu := govt.New(20, 4, 100)
	defer emu.Close()
	term := &terminalClient{id: id, emu: emu, echoSignal: make(chan struct{}, 1)}
	events := make(chan goclient.TerminalPush, 8)
	invalidated := make(chan struct{}, 8)
	c := &WorkspaceClient{
		session:      &goclient.Session{Events: events},
		terminals:    map[uuid.UUID]*terminalClient{id: term},
		snapshotWake: make(chan struct{}, 1),
		invalidate:   func() { invalidated <- struct{}{} },
	}
	c.native.Store(&EbitengineWindow{frameWake: invalidated})
	done := make(chan struct{})
	go func() { c.Run(); close(done) }()
	defer func() {
		close(events)
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("worker did not exit")
		}
	}()
	awaitView := func(message string) {
		t.Helper()
		select {
		case <-invalidated:
		case <-time.After(time.Second):
			t.Fatal(message)
		}
	}
	push := func(seq uint64, text string) {
		events <- goclient.TerminalPush{TerminalID: id, Event: goprotocol.TerminalEvent{Seq: seq, Kind: goprotocol.OutputEvent, Data: []byte(text)}}
	}
	// The first publish is always immediate and starts the shared window.
	push(1, "a")
	awaitView("first output was not published")
	// A second output without input must wait for the display to consume the
	// first view and for the publication window.
	push(2, "b")
	select {
	case <-invalidated:
		t.Fatal("non-echo output bypassed the window")
	case <-time.After(5 * time.Millisecond):
	}
	// Input into a quiet terminal marks the next output as the echo: it must
	// publish immediately, even though the window has not elapsed and the
	// first view is unacked.
	term.noteInputEcho(time.Now())
	push(3, "c")
	awaitView("echo was not published ahead of the window")
	select {
	case <-term.echoSignal:
	case <-time.After(time.Second):
		t.Fatal("echo publish did not signal the bounded wait")
	}
	term.mu.RLock()
	text := term.snapshot.RowsData[0].Cells[2].Text
	term.mu.RUnlock()
	if text != "c" {
		t.Fatalf("echo snapshot missing latest output: %q", text)
	}
	if term.echoCredits.Load() != 0 {
		t.Fatal("echo credit was not consumed")
	}
}

func TestStreamingTerminalKeepsTheSharedWindow(t *testing.T) {
	term := &terminalClient{echoSignal: make(chan struct{}, 1)}
	now := time.Now()
	// A streaming terminal produced far more than a keystroke echo since the
	// previous input, so it must keep coalescing instead of bypassing.
	term.noteInputEcho(now)
	term.outputBytes.Add(64 << 10)
	term.noteInputEcho(now.Add(20 * time.Millisecond))
	if term.echoBypass(now.Add(20 * time.Millisecond)) {
		t.Fatal("streaming terminal bypassed the publication window")
	}
	// Ordinary typing only produces a small self-echo between keys.
	term2 := &terminalClient{echoSignal: make(chan struct{}, 1)}
	term2.noteInputEcho(now)
	term2.outputBytes.Add(3)
	term2.noteInputEcho(now.Add(20 * time.Millisecond))
	if !term2.echoBypass(now.Add(20 * time.Millisecond)) {
		t.Fatal("typing echo was denied the bypass")
	}
}

func TestExpiredEchoCreditDoesNotBypassBackgroundOutput(t *testing.T) {
	term := &terminalClient{echoSignal: make(chan struct{}, 1)}
	now := time.Now()
	term.noteInputEcho(now)
	// No output arrived; a later background event must not inherit the bypass.
	if term.echoBypass(now.Add(echoBypassWindow + time.Millisecond)) {
		t.Fatal("expired credit bypassed background output")
	}
	if term.echoCredits.Load() != 0 {
		t.Fatal("expired credit was retained")
	}
}

func TestPublicationPublishesAllPendingTerminalsInOneBatch(t *testing.T) {
	wake := make(chan struct{}, 8)
	c := &WorkspaceClient{terminals: map[uuid.UUID]*terminalClient{}}
	c.native.Store(&EbitengineWindow{frameWake: wake})
	pending := make(map[*terminalClient]bool)
	texts := []string{"a", "b", "c", "d"}
	terms := make([]*terminalClient, 0, len(texts))
	for _, text := range texts {
		id := uuid.New()
		emu := govt.New(20, 4, 100)
		defer emu.Close()
		term := &terminalClient{id: id, emu: emu, echoSignal: make(chan struct{}, 1)}
		c.mu.Lock()
		c.terminals[id] = term
		c.mu.Unlock()
		c.applyTerminalEventDeferred(goclient.TerminalPush{TerminalID: id, Event: goprotocol.TerminalEvent{Seq: 1, Kind: goprotocol.OutputEvent, Data: []byte(text)}})
		pending[term] = true
		terms = append(terms, term)
	}
	if !c.publishTerminalSnapshots(pending) {
		t.Fatal("batch did not publish")
	}
	select {
	case <-wake:
	default:
		t.Fatal("batch did not request a frame")
	}
	select {
	case <-wake:
		t.Fatal("batch requested more than one frame")
	default:
	}
	for i, term := range terms {
		term.mu.RLock()
		got := term.snapshot.RowsData[0].Cells[0].Text
		term.mu.RUnlock()
		if got != texts[i] {
			t.Fatalf("terminal %d snapshot %q, want %q", i, got, texts[i])
		}
	}
}
