package gosupervisor

import (
	"bytes"
	"net"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestAttachOrdersResponseReplayAndConcurrentLive(t *testing.T) {
	server := NewServer()
	id := uuid.NewString()
	term := &terminal{id: id, generation: 1, subs: make(map[*subscriber]struct{})}
	server.terms[id] = term
	history := bytes.Repeat([]byte("h"), historyLimit)
	term.produced = uint64(len(history))
	term.appendHistoryLocked(history)
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	_ = right.SetReadDeadline(time.Now().Add(5 * time.Second))
	sub := newSubscriber(&connWriter{conn: left})
	defer sub.close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 10; i++ {
			term.publish([]byte("l"))
		}
	}()
	// Racing output may fall before or after the snapshot, but must never
	// precede its response or interleave with its replay.
	resp, _ := server.attach(attachRequest{ID: id}, sub)
	kind, payload, err := readFrame(right)
	if err != nil || kind != kindAttached {
		t.Fatalf("first frame kind=%d err=%v", kind, err)
	}
	var wire attachedResponse
	if err := unmarshal(payload, &wire); err != nil || wire.Available != resp.Available {
		t.Fatalf("response=%+v err=%v", wire, err)
	}
	next := wire.Offset
	for next < wire.Available {
		kind, payload, err = readFrame(right)
		if err != nil || kind != kindOutput {
			t.Fatalf("replay kind=%d err=%v", kind, err)
		}
		header, data, err := parseOutput(payload)
		if err != nil || header.offset != next || next+uint64(len(data)) > wire.Available {
			t.Fatalf("out of order: next=%d header=%+v len=%d err=%v", next, header, len(data), err)
		}
		next += uint64(len(data))
	}
	<-done
	for next < uint64(historyLimit+10) {
		kind, payload, err = readFrame(right)
		if err != nil || kind != kindOutput {
			t.Fatalf("live kind=%d err=%v", kind, err)
		}
		header, data, err := parseOutput(payload)
		if err != nil || header.offset != next {
			t.Fatalf("live next=%d header=%+v err=%v", next, header, err)
		}
		next += uint64(len(data))
	}
}
