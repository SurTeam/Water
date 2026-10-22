package goui

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/SurTeam/Water/internal/goclient"
	"github.com/SurTeam/Water/internal/goprotocol"
	"github.com/SurTeam/Water/internal/govt"
	"github.com/google/uuid"
)

func TestTerminalViewsCoalesceUntilDisplayConsumptionAndPublicationDeadline(t *testing.T) {
	id := uuid.New()
	emu := govt.New(20, 4, 100)
	defer emu.Close()
	term := &terminalClient{id: id, emu: emu}
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
	push := func(seq uint64, text string) {
		events <- goclient.TerminalPush{TerminalID: id, Event: goprotocol.TerminalEvent{Seq: seq, Kind: goprotocol.OutputEvent, Data: []byte(text)}}
	}
	awaitView := func() {
		t.Helper()
		select {
		case <-invalidated:
		case <-time.After(time.Second):
			t.Fatal("view was not published")
		}
	}
	push(1, "a")
	awaitView()
	push(2, "b")
	push(3, "c")
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(time.Millisecond)
	defer poll.Stop()
	for {
		term.mu.RLock()
		parsed := term.lastSeq == 3
		term.mu.RUnlock()
		if parsed {
			break
		}
		select {
		case <-poll.C:
		case <-deadline.C:
			t.Fatal("output parsing waited for display")
		}
	}
	select {
	case <-invalidated:
		t.Fatal("burst published before display consumed first view")
	default:
	}
	c.snapshotWake <- struct{}{}
	awaitView()
	term.mu.RLock()
	defer term.mu.RUnlock()
	if term.snapshot.RowsData[0].Cells[2].Text != "c" {
		t.Fatal("display request did not publish latest complete view")
	}
}

func TestTerminalCloseSerializesWithParsing(t *testing.T) {
	id := uuid.New()
	emu := govt.New(80, 24, 100)
	term := &terminalClient{id: id, emu: emu}
	c := &WorkspaceClient{terminals: map[uuid.UUID]*terminalClient{id: term}}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for seq := uint64(1); seq <= 50; seq++ {
			c.applyTerminalEventDeferred(goclient.TerminalPush{TerminalID: id, Event: goprotocol.TerminalEvent{Seq: seq, Kind: goprotocol.OutputEvent, Data: bytes.Repeat([]byte("abc\r\n"), 100)}})
		}
	}()
	term.close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("closing terminal deadlocked parsing")
	}
}

func TestInputAtBottomFollowsDelayedOutputAfterViewportHold(t *testing.T) {
	id := uuid.New()
	emu := govt.New(24, 5, 64)
	defer emu.Close()
	emu.Write([]byte("prompt> command"))
	emu.HoldViewport()
	term := &terminalClient{id: id, emu: emu, snapshot: emu.Snapshot()}
	if term.snapshot.YDisp != term.snapshot.YBase {
		t.Fatal("fixture must start at the live viewport bottom")
	}
	invalidations := 0
	c := &WorkspaceClient{
		terminals:  map[uuid.UUID]*terminalClient{id: term},
		invalidate: func() { invalidations++ },
	}

	term.inputPending.Store(true)
	if c.publishTerminalSnapshots(make(map[*terminalClient]bool)) {
		t.Fatal("input request at the live bottom should wait for PTY echo")
	}
	if !term.inputPending.Load() {
		t.Fatal("input follow-up was cleared before output arrived")
	}

	c.applyTerminalEvents([]goclient.TerminalPush{{
		TerminalID: id,
		Event: goprotocol.TerminalEvent{
			Seq: 1, Kind: goprotocol.OutputEvent,
			Data: bytes.Repeat([]byte("command output\r\n"), 20),
		},
	}})
	if term.inputPending.Load() || term.snapshot.YDisp != term.snapshot.YBase || invalidations != 1 {
		t.Fatalf("delayed output did not publish at bottom: viewport=%d base=%d pending=%t invalidations=%d", term.snapshot.YDisp, term.snapshot.YBase, term.inputPending.Load(), invalidations)
	}
}

func TestInputFollowsViewportWithoutWaitingForOutput(t *testing.T) {
	id := uuid.New()
	emu := govt.New(24, 5, 64)
	defer emu.Close()
	emu.Write(bytes.Repeat([]byte("history\r\n"), 40))
	emu.Scroll(-10)
	term := &terminalClient{id: id, emu: emu, snapshot: emu.Snapshot(), selection: Selection{Active: true}}
	if term.snapshot.YDisp == term.snapshot.YBase {
		t.Fatal("fixture did not scroll into history")
	}
	invalidations := 0
	c := &WorkspaceClient{terminals: map[uuid.UUID]*terminalClient{id: term}, invalidate: func() { invalidations++ }}
	term.inputPending.Store(true)
	c.publishTerminalSnapshots(make(map[*terminalClient]bool))
	if term.snapshot.YDisp != term.snapshot.YBase || term.selection.Active || term.inputPending.Load() || invalidations != 1 {
		t.Fatalf("input follow-up not published: viewport=%d base=%d selected=%t pending=%t invalidations=%d", term.snapshot.YDisp, term.snapshot.YBase, term.selection.Active, term.inputPending.Load(), invalidations)
	}
	c.publishTerminalSnapshots(make(map[*terminalClient]bool))
	if invalidations != 1 {
		t.Fatal("completed input kept invalidating idle frames")
	}
}

func TestHiddenTerminalOutputDoesNotWakeWindow(t *testing.T) {
	hidden := uuid.New()
	visible := uuid.New()
	emu := govt.New(20, 4, 100)
	defer emu.Close()
	term := &terminalClient{id: hidden, emu: emu}
	wake := make(chan struct{}, 4)
	window := &EbitengineWindow{frameWake: wake}
	shown := map[uuid.UUID]struct{}{visible: {}}
	window.visibleTerminals.Store(&shown)
	c := &WorkspaceClient{terminals: map[uuid.UUID]*terminalClient{hidden: term}}
	c.native.Store(window)
	c.applyTerminalEvents([]goclient.TerminalPush{{TerminalID: hidden, Event: goprotocol.TerminalEvent{Seq: 1, Kind: goprotocol.OutputEvent, Data: []byte("hidden")}}})
	select {
	case <-wake:
		t.Fatal("hidden terminal scheduled a frame")
	default:
	}
	if !term.viewStale.Load() {
		t.Fatal("hidden terminal did not keep a stale view")
	}
	shown = map[uuid.UUID]struct{}{hidden: {}}
	window.visibleTerminals.Store(&shown)
	c.applyTerminalEvents([]goclient.TerminalPush{{TerminalID: hidden, Event: goprotocol.TerminalEvent{Seq: 2, Kind: goprotocol.OutputEvent, Data: []byte(" shown")}}})
	select {
	case <-wake:
	default:
		t.Fatal("visible terminal did not schedule a frame")
	}
	if term.viewStale.Load() {
		t.Fatal("visible publish left the view stale")
	}
}

func TestTerminalBatchPreservesOrderAndDeduplicates(t *testing.T) {
	id := uuid.New()
	emu := govt.New(20, 4, 100)
	defer emu.Close()
	term := &terminalClient{id: id, emu: emu}
	invalidations := 0
	c := &WorkspaceClient{terminals: map[uuid.UUID]*terminalClient{id: term}, invalidate: func() { invalidations++ }}
	push := func(event goprotocol.TerminalEvent) goclient.TerminalPush {
		return goclient.TerminalPush{TerminalID: id, Event: event}
	}
	c.applyTerminalEvents([]goclient.TerminalPush{
		push(goprotocol.TerminalEvent{Seq: 1, Kind: goprotocol.OutputEvent, Data: []byte("before\r\n")}),
		push(goprotocol.TerminalEvent{Seq: 2, Kind: goprotocol.ResizeEvent, Size: goprotocol.TerminalSize{Columns: 30, Lines: 5}}),
		push(goprotocol.TerminalEvent{Seq: 2, Kind: goprotocol.OutputEvent, Data: []byte("DUPLICATE")}),
		push(goprotocol.TerminalEvent{Seq: 3, Kind: goprotocol.OutputEvent, Data: []byte("after")}),
		push(goprotocol.TerminalEvent{Seq: 4, Kind: goprotocol.ExitEvent}),
	})
	var text strings.Builder
	for _, row := range term.snapshot.RowsData {
		for _, cell := range row.Cells {
			text.WriteString(cell.Text)
		}
	}
	if !strings.Contains(text.String(), "before") || !strings.Contains(text.String(), "after") || strings.Contains(text.String(), "DUPLICATE") {
		t.Fatalf("batched screen = %q", text.String())
	}
	if term.lastSeq != 4 || term.snapshot.Cols != 30 || term.snapshot.Rows != 5 || invalidations != 1 {
		t.Fatalf("seq=%d size=%dx%d invalidations=%d", term.lastSeq, term.snapshot.Cols, term.snapshot.Rows, invalidations)
	}
	c.applyTerminalEvent(push(goprotocol.TerminalEvent{Seq: 3, Kind: goprotocol.OutputEvent, Data: []byte("stale")}))
	if invalidations != 1 {
		t.Fatal("duplicate event invalidated the frame")
	}
}

func TestSnapshotPublicationWaitsForParserWithoutHoldingViewLock(t *testing.T) {
	emu := govt.New(20, 4, 100)
	term := &terminalClient{emu: emu, snapshot: emu.FrameSnapshot()}
	term.parseMu.Lock()
	started := make(chan struct{})
	done := make(chan struct{})
	go func() {
		close(started)
		term.publishSnapshot()
		close(done)
	}()
	<-started
	// Draw must retain access to the previous immutable view while publication
	// waits for the parser/lifetime owner.
	if !term.mu.TryRLock() {
		term.parseMu.Unlock()
		t.Fatal("publication held the view lock while waiting for parsing")
	}
	term.mu.RUnlock()
	select {
	case <-done:
		term.parseMu.Unlock()
		t.Fatal("publication did not serialize with parsing")
	default:
	}
	emu.Write([]byte("latest"))
	term.parseMu.Unlock()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("publication did not resume after parsing")
	}
	if term.snapshot.RowsData[0].Cells[0].Text != "l" {
		t.Fatal("publication missed the completed parse")
	}
	term.close()
	term.publishSnapshot() // A queued publication after close must be harmless.
}

func TestSnapshotPublicationSerializesWithClose(t *testing.T) {
	term := &terminalClient{emu: govt.New(80, 24, 100)}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 100; i++ {
			term.publishSnapshot()
		}
	}()
	term.close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("snapshot publication deadlocked closing terminal")
	}
}
