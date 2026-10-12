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

func TestTerminalTrafficCoalescesWithoutClosingSession(t *testing.T) {
	s, peer := pipeSession(t)
	id := uuid.New()
	for i := 0; i < 100; i++ {
		if err := s.DispatchAsync(map[string]any{"type": "terminal.send_bytes", "terminal_id": id, "bytes": []int{i}}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 5; i++ {
		if err := s.DispatchAsync(map[string]any{"type": "terminal.resize", "terminal_id": id, "columns": 80 + i, "lines": 24, "cell_width": 8, "cell_height": 16}); err != nil {
			t.Fatal(err)
		}
	}
	if s.Err() != nil {
		t.Fatalf("terminal traffic closed the session: %v", s.Err())
	}
	frames := 0
	var got []int
	var columns int
	deadline := time.After(time.Second)
	for frames < 3 {
		var frame goprotocol.Frame
		var err error
		done := make(chan struct{})
		go func() {
			frame, err = goprotocol.ReadFrame(peer)
			close(done)
		}()
		select {
		case <-done:
		case <-deadline:
			t.Fatalf("timed out after %d frames", frames)
		}
		if err != nil {
			t.Fatal(err)
		}
		var message goprotocol.WireMessage
		if err = json.Unmarshal(frame.JSON, &message); err != nil {
			t.Fatal(err)
		}
		var params struct {
			Command struct {
				Type    string `json:"type"`
				Bytes   []int  `json:"bytes"`
				Columns int    `json:"columns"`
			} `json:"command"`
		}
		if err = json.Unmarshal(message.Params, &params); err != nil {
			t.Fatal(err)
		}
		frames++
		got = append(got, params.Command.Bytes...)
		if params.Command.Type == "terminal.resize" {
			columns = params.Command.Columns
		}
	}
	if frames != 3 || columns != 84 || len(got) != 100 || got[0] != 0 || got[99] != 99 {
		t.Fatalf("frames=%d columns=%d bytes=%v", frames, columns, got)
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
