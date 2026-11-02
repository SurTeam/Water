package goserver

import (
	"encoding/json"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/SurTeam/Water/internal/goprotocol"
)

type observedWriteConn struct {
	net.Conn
	once    sync.Once
	writing chan struct{}
}

func (c *observedWriteConn) Write(p []byte) (int, error) {
	c.once.Do(func() { close(c.writing) })
	return c.Conn.Write(p)
}

func TestBlockedSessionOpenDoesNotHoldCommandLock(t *testing.T) {
	s := New("")
	defer s.Close()
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	conn := &observedWriteConn{Conn: a, writing: make(chan struct{})}
	ss := &session{conn: conn, snapshotRequests: make(chan struct{}, 1)}
	done := make(chan error, 1)
	go func() {
		done <- s.dispatch(ss, goprotocol.WireMessage{Method: "session.open", ProtocolVersion: goprotocol.ProtocolVersion, Params: json.RawMessage(`{"role":"gui"}`)})
	}()
	select {
	case <-conn.writing:
	case <-time.After(time.Second):
		t.Fatal("handshake never reached write")
	}
	operated := make(chan struct{})
	go func() {
		s.executeCommand("workspace.new", json.RawMessage(`{"type":"workspace.new","initial_terminal":false}`))
		close(operated)
	}()
	select {
	case <-operated:
	case <-time.After(time.Second):
		b.Close()
		<-done
		<-operated
		t.Fatal("blocked handshake held global command lock")
	}
	b.Close()
	<-done
}

func TestStalledSessionWriteClosesConnection(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	ss := &session{conn: a}
	done := make(chan error, 1)
	go func() { done <- ss.write(goprotocol.Success(1, map[string]bool{"ok": true})) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("stalled write succeeded")
		}
	case <-time.After(8 * time.Second):
		t.Fatal("stalled write did not respect deadline")
	}
	_ = b.SetReadDeadline(time.Now().Add(time.Second))
	var buf [1]byte
	if _, err := b.Read(buf[:]); err != io.EOF {
		t.Fatalf("failed frame left connection reusable: %v", err)
	}
}
