package goterminal

import (
	"bytes"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/SurTeam/Water/internal/goprotocol"
)

func TestPTYRemainsPollableAcrossResize(t *testing.T) {
	r := NewRegistry()
	defer r.CloseAll()
	term, err := r.Spawn("/bin/sh", []string{"-c", "read _"}, goprotocol.TerminalSize{Columns: 80, Lines: 24})
	if err != nil {
		t.Fatal(err)
	}
	if err := term.Resize(goprotocol.TerminalSize{Columns: 100, Lines: 30}); err != nil {
		t.Fatal(err)
	}
	if err := term.ptmx.SetReadDeadline(time.Time{}); err != nil {
		t.Fatalf("PTY is not pollable: %v", err)
	}
	raw, err := term.ptmx.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var flags uintptr
	var errno syscall.Errno
	if err := raw.Control(func(fd uintptr) { flags, _, errno = syscall.Syscall(syscall.SYS_FCNTL, fd, syscall.F_GETFL, 0) }); err != nil {
		t.Fatal(err)
	}
	if errno != 0 || flags&syscall.O_NONBLOCK == 0 {
		t.Fatalf("resize restored blocking descriptor: flags=%#x error=%v", flags, errno)
	}
}

func TestRawReaderBarrierPausesBeforeFurtherReads(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	term := &Terminal{ptmx: reader, closed: make(chan struct{}), rawFlushRequests: make(chan rawFlushRequest, 1)}
	defer close(term.closed)
	out := make(chan []byte, 2)
	free := make(chan []byte, 2)
	free <- make([]byte, readBlockBytes)
	free <- make([]byte, readBlockBytes)
	done := make(chan struct{})
	go func() { defer close(done); term.rawReadLoop(out, free) }()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	if _, err := writer.Write([]byte("before")); err != nil {
		t.Fatal(err)
	}
	select {
	case block := <-out:
		if !bytes.Equal(block, []byte("before")) {
			t.Fatalf("before barrier: %q", block)
		}
		free <- block[:readBlockBytes]
	case <-deadline.C:
		t.Fatal("missing output before barrier")
	}
	barrier := rawFlushRequest{ready: make(chan struct{}), resume: make(chan struct{})}
	term.rawFlushRequests <- barrier
	if err := reader.SetReadDeadline(time.Now()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-barrier.ready:
	case <-deadline.C:
		t.Fatal("raw reader did not reach barrier")
	}
	if _, err := writer.Write([]byte("after")); err != nil {
		t.Fatal(err)
	}
	select {
	case block := <-out:
		t.Fatalf("reader crossed paused barrier: %q", block)
	default:
	}
	close(barrier.resume)
	select {
	case block := <-out:
		if !bytes.Equal(block, []byte("after")) {
			t.Fatalf("after barrier: %q", block)
		}
		free <- block[:readBlockBytes]
	case <-deadline.C:
		t.Fatal("reader did not resume")
	}
	_ = writer.Close()
	select {
	case <-done:
	case <-deadline.C:
		t.Fatal("reader did not stop after EOF")
	}
}
