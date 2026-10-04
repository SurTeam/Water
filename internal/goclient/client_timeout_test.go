package goclient

import (
	"encoding/json"
	"errors"
	"github.com/SurTeam/Water/internal/goprotocol"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSessionSimulatedLatencyKeepsRequestsOrdered(t *testing.T) {
	s, peer := pipeSession(t)
	finished := make(chan error, 1)
	go func() {
		for i := 0; i < 3; i++ {
			frame, err := goprotocol.ReadFrame(peer)
			if err != nil {
				finished <- err
				return
			}
			var request goprotocol.WireMessage
			if err = json.Unmarshal(frame.JSON, &request); err != nil {
				finished <- err
				return
			}
			// A transport simulator holds each response; the real network is untouched.
			timer := time.NewTimer(40 * time.Millisecond)
			select {
			case <-timer.C:
			case <-s.done:
				timer.Stop()
				finished <- errors.New("session closed during latency")
				return
			}
			if err = goprotocol.WriteJSON(peer, goprotocol.Success(request.RequestID, map[string]int{"sequence": i})); err != nil {
				finished <- err
				return
			}
		}
		finished <- nil
	}()
	for i := 0; i < 3; i++ {
		var reply struct {
			Sequence int `json:"sequence"`
		}
		if err := s.CallTimeout("test.latency", nil, &reply, time.Second); err != nil {
			t.Fatal(err)
		}
		if reply.Sequence != i {
			t.Fatal("responses reordered")
		}
	}
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("latency simulator did not finish")
	}
}

func TestSessionSimulatedBlackholeBoundsRequestAndReleasesWriter(t *testing.T) {
	s, _ := pipeSession(t)
	started := time.Now()
	if err := s.CallTimeout("server.info", nil, nil, 30*time.Millisecond); err == nil {
		t.Fatal("blackhole returned success")
	}
	if time.Since(started) > time.Second {
		t.Fatal("blackhole blocked request")
	}
	if s.Err() == nil {
		t.Fatal("timed out session remained healthy")
	}
	select {
	case <-s.done:
	case <-time.After(time.Second):
		t.Fatal("blocked writer survived timeout")
	}
}

func TestCallTimeoutBoundsUnresponsiveServer(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "water-rpc.")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "server.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	finish := make(chan struct{})
	defer close(finish)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		<-finish
	}()
	start := time.Now()
	err = New(path).CallTimeout("server.shutdown", nil, nil, 30*time.Millisecond)
	var timeout net.Error
	if !errors.As(err, &timeout) || !timeout.Timeout() {
		t.Fatalf("expected timeout, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("shutdown RPC took %v", elapsed)
	}
}
