package goclient

import (
	"encoding/json"
	"errors"
	"github.com/SurTeam/Water/internal/goprotocol"
	"github.com/google/uuid"
	"net"
	"strings"
	"testing"
	"time"
)

func TestSessionSendQueueHasByteBudget(t *testing.T) {
	s, _ := pipeSession(t)
	raw, err := json.Marshal(strings.Repeat("x", 512*1024))
	if err != nil {
		t.Fatal(err)
	}
	accepted := 0
	for accepted < sessionSendQueueCapacity {
		err = s.enqueue(goprotocol.WireMessage{Method: "test", Params: raw}, false)
		if err != nil {
			break
		}
		accepted++
	}
	if !errors.Is(err, ErrSessionBackpressure) || accepted >= sessionSendQueueCapacity {
		t.Fatalf("byte budget did not bind before slot capacity: accepted=%d err=%v", accepted, err)
	}
}

func pipeSession(t *testing.T) (*Session, net.Conn) {
	t.Helper()
	client, peer := net.Pipe()
	s := &Session{conn: client, Build: "dev", Events: make(chan TerminalPush, terminalEventQueueCapacity), Pushes: make(chan goprotocol.WireMessage, 64), replies: map[uint64]chan goprotocol.WireMessage{}, done: make(chan struct{}), outbound: make(chan pendingSessionFrame, sessionSendQueueCapacity)}
	go s.readLoop()
	go s.writeLoop()
	t.Cleanup(func() { _ = s.Close(); _ = peer.Close() })
	return s, peer
}

func TestAsyncDispatchIsBoundedWhenPeerStopsReading(t *testing.T) {
	s, _ := pipeSession(t)
	result := make(chan error, 1)
	go func() {
		for i := 0; i < sessionSendQueueCapacity+2; i++ {
			if err := s.DispatchAsync(map[string]any{"type": "tab.new"}); err != nil {
				result <- err
				return
			}
		}
		result <- nil
	}()
	select {
	case err := <-result:
		if !errors.Is(err, ErrSessionBackpressure) {
			t.Fatalf("backpressure: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("async dispatch performed blocking socket I/O")
	}
	if !errors.Is(s.Err(), ErrSessionBackpressure) {
		t.Fatal("saturation did not end the session explicitly")
	}
	select {
	case _, ok := <-s.Pushes:
		if ok {
			t.Fatal("unexpected push")
		}
	case <-time.After(time.Second):
		t.Fatal("blocked writer prevented session close")
	}
}

func TestSessionCloseUnblocksFullTerminalQueue(t *testing.T) {
	s, peer := pipeSession(t)
	sent := make(chan error, 1)
	id := uuid.New()
	go func() {
		for i := 0; i <= terminalEventQueueCapacity; i++ {
			if err := goprotocol.WriteTerminal(peer, id, goprotocol.TerminalEvent{Seq: uint64(i + 1), Kind: goprotocol.OutputEvent, Data: []byte("x"), Size: goprotocol.TerminalSize{Columns: 80, Lines: 24}}); err != nil {
				sent <- err
				return
			}
		}
		sent <- nil
	}()
	select {
	case err := <-sent:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("queue did not reach its delivery barrier")
	}
	_ = s.Close()
	select {
	case _, ok := <-s.Pushes:
		if ok {
			t.Fatal("unexpected push")
		}
	case <-time.After(time.Second):
		t.Fatal("full terminal queue leaked the reader")
	}
	for range s.Events {
	}
}

func TestQueuedDispatchPreservesAcceptedOrder(t *testing.T) {
	s, peer := pipeSession(t)
	result := make(chan error, 1)
	go func() {
		for i := 0; i < 20; i++ {
			frame, err := goprotocol.ReadFrame(peer)
			if err != nil {
				result <- err
				return
			}
			var message goprotocol.WireMessage
			if err = json.Unmarshal(frame.JSON, &message); err != nil {
				result <- err
				return
			}
			var params struct {
				Command struct {
					Index int `json:"index"`
				} `json:"command"`
			}
			if err = json.Unmarshal(message.Params, &params); err != nil {
				result <- err
				return
			}
			if params.Command.Index != i {
				result <- errors.New("dispatch order changed")
				return
			}
		}
		result <- nil
	}()
	for i := 0; i < 20; i++ {
		if err := s.DispatchAsync(map[string]any{"type": "test", "index": i}); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("writer stalled")
	}
}
