package goclient

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/SurTeam/Water/internal/goprotocol"
	"github.com/google/uuid"
)

type subscriptionTestConn struct {
	conn     net.Conn
	mu       sync.Mutex
	input    chan goprotocol.TerminalInputFrame
	messages chan goprotocol.WireMessage
}

func (c *subscriptionTestConn) reply(id uint64, result any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return goprotocol.WriteJSON(c.conn, goprotocol.Success(id, result))
}
func (c *subscriptionTestConn) event(id uuid.UUID, seq uint64, data string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return goprotocol.WriteTerminal(c.conn, id, goprotocol.TerminalEvent{Seq: seq, Kind: goprotocol.OutputEvent, Data: []byte(data)})
}

func subscriptionTestServer(t *testing.T, attach func(*subscriptionTestConn, goprotocol.WireMessage, <-chan struct{})) string {
	t.Helper()
	socket := filepath.Join(testSocketDirectory(t), "water-sub-"+uuid.NewString()+".sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	var mu sync.Mutex
	var connections []net.Conn
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			connections = append(connections, conn)
			mu.Unlock()
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer conn.Close()
				c := &subscriptionTestConn{conn: conn, input: make(chan goprotocol.TerminalInputFrame, 16), messages: make(chan goprotocol.WireMessage, 16)}
				for {
					frame, err := goprotocol.ReadFrame(conn)
					if err != nil {
						return
					}
					if frame.Input != nil {
						select {
						case c.input <- *frame.Input:
						default:
						}
						continue
					}
					var msg goprotocol.WireMessage
					if json.Unmarshal(frame.JSON, &msg) != nil {
						return
					}
					select {
					case c.messages <- msg:
					default:
					}
					switch msg.Method {
					case "server.inspect":
						_ = c.reply(msg.RequestID, subscriptionTestInfo())
					case "session.open":
						_ = c.reply(msg.RequestID, map[string]any{"window_id": uuid.New()})
					case "terminal.attach":
						attach(c, msg, stop)
					default:
						_ = c.reply(msg.RequestID, map[string]any{})
					}
				}
			}()
		}
	}()
	t.Cleanup(func() {
		close(stop)
		_ = listener.Close()
		mu.Lock()
		for _, conn := range connections {
			_ = conn.Close()
		}
		mu.Unlock()
		wg.Wait()
	})
	return socket
}

func TestOwnedTerminalSubscriptionPreservesAttachLiveBoundary(t *testing.T) {
	id := uuid.New()
	socket := subscriptionTestServer(t, func(c *subscriptionTestConn, msg goprotocol.WireMessage, _ <-chan struct{}) {
		// Live events can arrive before the attach RPC reply is delivered. The
		// dedicated queue must already exist, with no forwarding consumer required.
		_ = c.event(id, 8, "\x1b[6n")
		_ = c.reply(msg.RequestID, map[string]any{"last_seq": 7})
		for seq := uint64(9); seq < 30; seq++ {
			if c.event(id, seq, "live") != nil {
				return
			}
		}
	})
	parent, _ := pipeSession(t)
	parent.SocketPath = socket
	var replay struct {
		LastSeq uint64 `json:"last_seq"`
	}
	sub, err := parent.AttachTerminal(context.Background(), id, &replay)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	if replay.LastSeq != 7 {
		t.Fatalf("replay boundary: %d", replay.LastSeq)
	}
	for seq := uint64(8); seq < 30; seq++ {
		select {
		case event := <-sub.Events:
			if event.Event.Seq != seq {
				t.Fatalf("event sequence=%d, want %d", event.Event.Seq, seq)
			}
			if seq == 8 && string(event.Event.Data) != "\x1b[6n" {
				t.Fatal("live query lost")
			}
		case <-time.After(time.Second):
			t.Fatal("live event was lost across attach")
		}
	}
	if len(parent.Events) != 0 {
		t.Fatal("isolated events leaked into aggregate queue")
	}
}

func TestOwnedTerminalSubscriptionCarriesInputForItsPane(t *testing.T) {
	id := uuid.New()
	conns := make(chan *subscriptionTestConn, 1)
	socket := subscriptionTestServer(t, func(c *subscriptionTestConn, msg goprotocol.WireMessage, _ <-chan struct{}) {
		conns <- c
		_ = c.reply(msg.RequestID, map[string]any{"last_seq": 0})
	})
	parent, _ := pipeSession(t)
	parent.SocketPath = socket
	parent.BinaryInput = true
	sub, err := parent.AttachTerminal(context.Background(), id, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	stream := <-conns
	data := []byte("\x1b[97;5u")
	if err := parent.SendInput(id, data); err != nil {
		t.Fatal(err)
	}
	select {
	case frame := <-stream.input:
		if frame.TerminalID != id || string(frame.Data) != string(data) {
			t.Fatalf("dedicated input frame = %#v", frame)
		}
	case <-time.After(time.Second):
		t.Fatal("pane input did not use its dedicated connection")
	}
	select {
	case <-sub.Events:
		t.Fatal("unexpected event")
	default:
	}
	parent.writeMu.Lock()
	staged := len(parent.staged)
	parent.writeMu.Unlock()
	if staged != 0 || len(parent.outbound) != 0 {
		t.Fatal("pane input consumed the shared GUI session queue")
	}
}

func TestDedicatedInputFallsBackAfterSubscriptionClose(t *testing.T) {
	id := uuid.New()
	socket := subscriptionTestServer(t, func(c *subscriptionTestConn, msg goprotocol.WireMessage, _ <-chan struct{}) {
		_ = c.reply(msg.RequestID, map[string]any{})
	})
	parent, peer := pipeSession(t)
	parent.SocketPath = socket
	parent.BinaryInput = true
	sub, err := parent.AttachTerminal(context.Background(), id, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = sub.Close()
	if err := parent.SendInput(id, []byte("x")); err != nil {
		t.Fatal(err)
	}
	_ = peer.SetReadDeadline(time.Now().Add(time.Second))
	frame, err := goprotocol.ReadFrame(peer)
	if err != nil {
		t.Fatal(err)
	}
	if frame.Input == nil || frame.Input.TerminalID != id || string(frame.Input.Data) != "x" {
		t.Fatalf("fallback input frame = %#v", frame.Input)
	}
}

func TestDedicatedConnectionCarriesPaneResize(t *testing.T) {
	id := uuid.New()
	conns := make(chan *subscriptionTestConn, 1)
	socket := subscriptionTestServer(t, func(c *subscriptionTestConn, msg goprotocol.WireMessage, _ <-chan struct{}) {
		conns <- c
		_ = c.reply(msg.RequestID, map[string]any{})
	})
	parent, peer := pipeSession(t)
	parent.SocketPath = socket
	sub, err := parent.AttachTerminal(context.Background(), id, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	stream := <-conns
	if err := parent.DispatchAsync(map[string]any{
		"type": "terminal.resize", "terminal_id": id, "columns": 100, "lines": 30,
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stream.input:
		t.Fatal("resize unexpectedly used a binary input frame")
	case <-time.After(50 * time.Millisecond):
	}
	var resize goprotocol.WireMessage
	timeout := time.After(time.Second)
	for resize.Method == "" {
		select {
		case msg := <-stream.messages:
			if msg.Method == "command.dispatch" {
				resize = msg
			}
		case <-timeout:
			t.Fatal("resize did not use the dedicated connection")
		}
	}
	var params struct {
		Command struct {
			Type    string `json:"type"`
			Columns int    `json:"columns"`
			Lines   int    `json:"lines"`
		} `json:"command"`
	}
	if err := json.Unmarshal(resize.Params, &params); err != nil {
		t.Fatal(err)
	}
	if params.Command.Type != "terminal.resize" || params.Command.Columns != 100 || params.Command.Lines != 30 {
		t.Fatalf("dedicated resize = %+v", params.Command)
	}
	parent.writeMu.Lock()
	staged := len(parent.staged)
	parent.writeMu.Unlock()
	if staged != 0 || len(parent.outbound) != 0 {
		t.Fatal("pane resize consumed the shared GUI session queue")
	}
	_ = peer.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	if _, err := goprotocol.ReadFrame(peer); err == nil {
		t.Fatal("pane resize leaked onto the shared GUI connection")
	}
}

func TestOwnedSubscriptionLateCancellationDoesNotDetachReplacement(t *testing.T) {
	id := uuid.New()
	entered := make(chan *subscriptionTestConn, 2)
	release := make(chan struct{})
	socket := subscriptionTestServer(t, func(c *subscriptionTestConn, msg goprotocol.WireMessage, stop <-chan struct{}) {
		entered <- c
		var p struct {
			TerminalID uuid.UUID `json:"terminal_id"`
		}
		_ = json.Unmarshal(msg.Params, &p)
		// First attach deliberately returns after its replacement has attached.
		select {
		case <-release:
		case <-stop:
			return
		}
		_ = c.reply(msg.RequestID, map[string]any{"last_seq": 0})
	})
	parent, _ := pipeSession(t)
	parent.SocketPath = socket
	ctx, cancel := context.WithCancel(context.Background())
	firstDone := make(chan error, 1)
	go func() {
		sub, err := parent.AttachTerminal(ctx, id, nil)
		if sub != nil {
			_ = sub.Close()
		}
		firstDone <- err
	}()
	<-entered
	secondDone := make(chan *TerminalSubscription, 1)
	secondErr := make(chan error, 1)
	go func() {
		sub, err := parent.AttachTerminal(context.Background(), id, nil)
		secondDone <- sub
		secondErr <- err
	}()
	secondConn := <-entered
	cancel()
	select {
	case err := <-firstDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled attach: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancel did not interrupt attach")
	}
	close(release)
	sub := <-secondDone
	if err := <-secondErr; err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	if err := secondConn.event(id, 1, "replacement"); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-sub.Events:
		if string(event.Event.Data) != "replacement" {
			t.Fatal("wrong replacement event")
		}
	case <-time.After(time.Second):
		t.Fatal("late cancel detached replacement")
	}
	parent.terminalMu.Lock()
	count := len(parent.terminalSubscriptions)
	parent.terminalMu.Unlock()
	if count != 1 {
		t.Fatalf("owned subscriptions=%d, want replacement only", count)
	}
	_ = sub.Close()
	parent.terminalMu.Lock()
	count = len(parent.terminalSubscriptions)
	parent.terminalMu.Unlock()
	if count != 0 {
		t.Fatal("subscription close retained ownership")
	}
}

func TestClosingOwnedSubscriptionUnblocksFullIndependentQueue(t *testing.T) {
	id := uuid.New()
	socket := subscriptionTestServer(t, func(c *subscriptionTestConn, msg goprotocol.WireMessage, _ <-chan struct{}) {
		_ = c.reply(msg.RequestID, map[string]any{})
		for seq := uint64(1); seq < 128; seq++ {
			if c.event(id, seq, "x") != nil {
				return
			}
		}
	})
	parent, _ := pipeSession(t)
	parent.SocketPath = socket
	sub, err := parent.AttachTerminal(context.Background(), id, nil)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.After(time.Second)
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for len(sub.Events) < cap(sub.Events) {
		select {
		case <-tick.C:
		case <-deadline:
			t.Fatal("independent queue did not fill")
		}
	}
	if len(parent.Events) != 0 {
		t.Fatal("full pane queue occupied aggregate queue")
	}
	_ = sub.Close()
	drained := make(chan struct{})
	go func() {
		for range sub.Events {
		}
		close(drained)
	}()
	select {
	case <-drained:
	case <-time.After(time.Second):
		t.Fatal("closed subscription retained blocked reader")
	}
}

func subscriptionTestInfo() goprotocol.ServerInfo {
	d := Descriptor("dev")
	d.Capabilities = goprotocol.ServerCapabilities()
	d.RequiredCapabilities = []string{"terminal-stream/v1"}
	return goprotocol.ServerInfo{Descriptor: d}
}

func TestFullOwnedPaneDoesNotBlockAnotherSubscription(t *testing.T) {
	busyID, quietID := uuid.New(), uuid.New()
	socket := subscriptionTestServer(t, func(c *subscriptionTestConn, msg goprotocol.WireMessage, _ <-chan struct{}) {
		var params struct {
			TerminalID uuid.UUID `json:"terminal_id"`
		}
		_ = json.Unmarshal(msg.Params, &params)
		_ = c.reply(msg.RequestID, map[string]any{})
		if params.TerminalID == busyID {
			for seq := uint64(1); seq < 128; seq++ {
				if c.event(busyID, seq, "busy") != nil {
					return
				}
			}
		} else {
			_ = c.event(quietID, 1, "quiet")
		}
	})
	parent, _ := pipeSession(t)
	parent.SocketPath = socket
	busy, err := parent.AttachTerminal(context.Background(), busyID, nil)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.After(time.Second)
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for len(busy.Events) < cap(busy.Events) {
		select {
		case <-tick.C:
		case <-deadline:
			t.Fatal("busy pane did not reach bounded queue")
		}
	}
	quiet, err := parent.AttachTerminal(context.Background(), quietID, nil)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-quiet.Events:
		if event.TerminalID != quietID || string(event.Event.Data) != "quiet" {
			t.Fatal("wrong quiet pane output")
		}
	case <-time.After(time.Second):
		t.Fatal("busy pane blocked quiet pane")
	}
	_ = parent.Close()
	parent.terminalMu.Lock()
	remaining := len(parent.terminalSubscriptions)
	parent.terminalMu.Unlock()
	if remaining != 0 {
		t.Fatal("parent shutdown retained owned subscriptions")
	}
	for _, sub := range []*TerminalSubscription{busy, quiet} {
		select {
		case _, ok := <-sub.Events:
			if ok {
				t.Fatal("shutdown retained queued payload")
			}
		case <-time.After(time.Second):
			t.Fatal("parent shutdown did not close subscription")
		}
	}
}

func TestParentCloseCancelsPendingOwnedAttach(t *testing.T) {
	entered := make(chan struct{}, 1)
	socket := subscriptionTestServer(t, func(_ *subscriptionTestConn, _ goprotocol.WireMessage, stop <-chan struct{}) {
		entered <- struct{}{}
		<-stop
	})
	parent, _ := pipeSession(t)
	parent.SocketPath = socket
	attached := make(chan error, 1)
	go func() {
		sub, err := parent.AttachTerminal(context.Background(), uuid.New(), nil)
		if sub != nil {
			_ = sub.Close()
		}
		attached <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("attach did not reach RPC barrier")
	}
	closed := make(chan struct{})
	go func() { _ = parent.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("parent close waited for attach RPC")
	}
	select {
	case err := <-attached:
		if err == nil {
			t.Fatal("pending attach succeeded after shutdown")
		}
	case <-time.After(time.Second):
		t.Fatal("pending attach survived parent shutdown")
	}
	parent.terminalMu.Lock()
	remaining := len(parent.terminalSubscriptions)
	parent.terminalMu.Unlock()
	if remaining != 0 {
		t.Fatal("pending attach retained owned subscription")
	}
}
