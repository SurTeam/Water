package gosupervisor

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/creack/pty"
	"github.com/google/uuid"
)

const (
	// historyLimit bounds the raw output kept per terminal for re-attaching
	// servers. A resumed pane replays this history through the server's own
	// replay logic, so it never needs a screen snapshot.
	historyLimit = 2 * 1024 * 1024
	readBlock    = 128 * 1024
	// subscriberQueue bounds a slow server connection. The PTY reader must never
	// block on a subscriber: dropping the connection is recoverable (it re-attaches
	// from its offset) while blocking would stall the program's output.
	subscriberQueue = 4096
	// Output is coalesced before fanout for the same reason the server batches
	// PTY reads: a fast program otherwise produces thousands of tiny events that
	// cost more to frame and forward than the bytes they carry.
	batchIdle   = time.Millisecond
	batchMaxAge = 5 * time.Millisecond
)

// Server owns PTYs and their child processes. It is designed to outlive the
// Water server: the server connects, creates or resumes terminals, and may
// disconnect and reconnect without the programs noticing.
type Server struct {
	mu         sync.Mutex
	generation map[string]uint64
	terms      map[string]*terminal
	ln         net.Listener
	done       chan struct{}
	once       sync.Once
}

type terminal struct {
	id         string
	generation uint64
	ptmx       *os.File
	cmd        *exec.Cmd
	pid        int

	mu       sync.Mutex
	history  []byte
	start    uint64 // absolute offset of history[0]
	produced uint64 // absolute offset of the next byte
	exited   bool
	exitCode int32
	subs     map[*subscriber]struct{}

	resize chan resizeRequest
	closed chan struct{}
	once   sync.Once
}

// connWriter serializes every frame on one connection. Control replies and
// terminal output share the socket, so unsynchronized writes would interleave
// frames and corrupt the stream.
type connWriter struct {
	conn net.Conn
	mu   sync.Mutex
}

func (w *connWriter) write(frame []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, err := w.conn.Write(frame)
	return err
}

// subscriber owns a bounded outbound queue and its own writer goroutine, so a
// slow socket never blocks the PTY reader or another terminal's fanout.
type subscriber struct {
	writer *connWriter
	frames chan []byte
	mu     sync.Mutex
	closed bool
	once   sync.Once
}

func newSubscriber(writer *connWriter) *subscriber {
	sub := &subscriber{writer: writer, frames: make(chan []byte, 64)}
	go sub.writeLoop()
	return sub
}

func (s *subscriber) writeLoop() {
	for frame := range s.frames {
		if err := s.writer.write(frame); err != nil {
			s.close()
			return
		}
	}
}

func NewServer() *Server {
	return &Server{terms: make(map[string]*terminal), generation: make(map[string]uint64), done: make(chan struct{})}
}

// Listen serves the supervisor protocol on a unix socket. The socket is owned by
// this process; a stale path from a dead supervisor is replaced after a failed
// liveness probe.
func (s *Server) Listen(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if conn, err := net.DialTimeout("unix", path, 200*time.Millisecond); err == nil {
		_ = conn.Close()
		return fmt.Errorf("supervisor socket already served: %s", path)
	}
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		return err
	}
	s.ln = ln
	go s.accept()
	return nil
}

func (s *Server) accept() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			select {
			case <-s.done:
				return
			default:
			}
			continue
		}
		go s.serve(conn)
	}
}

// Close stops accepting and terminates every terminal's process group.
func (s *Server) Close() error {
	var err error
	s.once.Do(func() {
		close(s.done)
		if s.ln != nil {
			err = s.ln.Close()
		}
		s.mu.Lock()
		terms := make([]*terminal, 0, len(s.terms))
		for _, t := range s.terms {
			terms = append(terms, t)
		}
		s.terms = make(map[string]*terminal)
		s.mu.Unlock()
		for _, t := range terms {
			t.close()
		}
	})
	return err
}

func (s *Server) serve(conn net.Conn) {
	defer conn.Close()
	writer := &connWriter{conn: conn}
	reply := func(kind byte, value any) error { return writer.writeJSON(kind, value) }
	subs := make(map[string]*subscriber)
	defer func() {
		for _, sub := range subs {
			sub.close()
			s.detach(sub)
		}
	}()
	for {
		kind, payload, err := readFrame(conn)
		if err != nil {
			return
		}
		switch kind {
		case kindSpawn:
			var req spawnRequest
			if err := unmarshal(payload, &req); err != nil {
				_ = reply(kindFailure, failureResponse{Error: err.Error()})
				continue
			}
			resp, retired := s.spawn(req)
			_ = reply(kindSpawned, resp)
			if retired != nil {
				// The successor is registered and the client knows it, so the retired
				// program can be terminated without closing the pane it shared.
				retired.close()
			}
		case kindAttach:
			var req attachRequest
			if err := unmarshal(payload, &req); err != nil {
				_ = reply(kindFailure, failureResponse{Error: err.Error()})
				continue
			}
			sub := newSubscriber(writer)
			resp, replay := s.attach(req, sub)
			if resp.Error != "" {
				_ = reply(kindAttached, resp)
				continue
			}
			subs[req.ID] = sub
			if err := reply(kindAttached, resp); err != nil {
				sub.close()
				return
			}
			if len(replay) > 0 {
				sub.writeReplay(resp, replay)
			}
		case kindWrite:
			var req writeRequest
			if err := unmarshal(payload, &req); err != nil {
				continue
			}
			s.write(req.ID, req.Data)
		case kindResize:
			var req resizeRequest
			if err := unmarshal(payload, &req); err != nil {
				continue
			}
			s.resize(req)
		case kindClose:
			var req terminalRef
			if err := unmarshal(payload, &req); err != nil {
				continue
			}
			s.closeTerminal(req.ID)
		case kindForeground:
			var req terminalRef
			if err := unmarshal(payload, &req); err != nil {
				continue
			}
			_ = reply(kindForegrounded, s.foreground(req.ID))
		case kindList:
			_ = reply(kindListed, s.list())
		case kindShutdown:
			_ = s.Close()
			return
		}
	}
}

func (s *Server) spawn(req spawnRequest) (spawnedResponse, *terminal) {
	if req.ID == "" || req.Program == "" {
		return spawnedResponse{ID: req.ID, Error: "id and program are required"}, nil
	}
	s.mu.Lock()
	existing := s.terms[req.ID]
	s.mu.Unlock()
	replaced := (*terminal)(nil)
	if existing != nil && req.Replace {
		// An explicit replacement must not inherit the old program. Retire it only
		// after the successor is registered and the reply is on the wire, so the
		// server can install the new terminal before the old Exit arrives.
		replaced = existing
		existing = nil
	}
	if existing != nil && !existing.isExited() {
		// Reuse a live terminal instead of restarting the program.
		return spawnedResponse{ID: req.ID, PID: existing.pid, Generation: existing.generation, Resumed: true}, nil
	}

	cmd := exec.Command(req.Program, req.Args...)
	if req.Cwd != "" {
		cmd.Dir = req.Cwd
	}
	cmd.Env = append(append([]string{}, req.Env...), "TERM=xterm-256color", "COLORTERM=truecolor")
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: req.Lines, Cols: req.Columns})
	if err != nil {
		return spawnedResponse{ID: req.ID, Error: err.Error()}, nil
	}
	pollable, err := makePollablePTY(ptmx)
	if err != nil {
		_ = ptmx.Close()
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGHUP)
		_ = cmd.Wait()
		return spawnedResponse{ID: req.ID, Error: err.Error()}, nil
	}
	ptmx = pollable
	s.mu.Lock()
	s.generation[req.ID]++
	generation := s.generation[req.ID]
	s.mu.Unlock()
	t := &terminal{
		id:         req.ID,
		generation: generation,
		ptmx:       ptmx,
		cmd:        cmd,
		pid:        cmd.Process.Pid,
		subs:       make(map[*subscriber]struct{}),
		resize:     make(chan resizeRequest, 8),
		closed:     make(chan struct{}),
	}
	s.mu.Lock()
	s.terms[req.ID] = t
	s.mu.Unlock()
	go t.readLoop(s)
	go t.waitLoop(s)
	return spawnedResponse{ID: req.ID, PID: t.pid, Generation: t.generation}, replaced
}

func (s *Server) attach(req attachRequest, sub *subscriber) (attachedResponse, []byte) {
	s.mu.Lock()
	t := s.terms[req.ID]
	s.mu.Unlock()
	if t == nil {
		return attachedResponse{ID: req.ID, Error: "unknown terminal"}, nil
	}
	t.mu.Lock()
	start, produced := t.start, t.produced
	exited, code := t.exited, t.exitCode
	truncated := req.Offset < start
	offset := req.Offset
	if truncated {
		offset = start
	}
	replay := append([]byte(nil), t.history[offset-start:]...)
	t.mu.Unlock()

	t.mu.Lock()
	t.subs[sub] = struct{}{}
	t.mu.Unlock()
	return attachedResponse{ID: req.ID, PID: t.pid, Generation: t.generation, Offset: offset, Available: produced, Truncated: truncated, Exited: exited, Code: code}, replay
}

func (s *Server) detach(sub *subscriber) {
	s.mu.Lock()
	terms := make([]*terminal, 0, len(s.terms))
	for _, t := range s.terms {
		terms = append(terms, t)
	}
	s.mu.Unlock()
	for _, t := range terms {
		t.mu.Lock()
		delete(t.subs, sub)
		t.mu.Unlock()
	}
}

func (s *Server) write(id string, data []byte) {
	s.mu.Lock()
	t := s.terms[id]
	s.mu.Unlock()
	if t == nil || len(data) == 0 {
		return
	}
	_, _ = t.ptmx.Write(data)
}

func (s *Server) resize(req resizeRequest) {
	s.mu.Lock()
	t := s.terms[req.ID]
	s.mu.Unlock()
	if t == nil {
		return
	}
	// The read loop owns the ordering: it flushes observed output, applies the
	// size, then announces it on the same stream, so the server never sees new
	// geometry ahead of old bytes.
	select {
	case t.resize <- req:
	case <-t.closed:
	}
}

func (s *Server) closeTerminal(id string) {
	s.mu.Lock()
	t := s.terms[id]
	delete(s.terms, id)
	s.mu.Unlock()
	if t != nil {
		t.close()
	}
}

// foreground reports the PTY's foreground process group. The supervisor owns the
// master fd, so only it can answer; agent detection on the server depends on it.
func (s *Server) foreground(id string) foregroundResponse {
	s.mu.Lock()
	t := s.terms[id]
	s.mu.Unlock()
	if t == nil {
		return foregroundResponse{ID: id, Error: "unknown terminal"}
	}
	var group int
	var queryErr error
	raw, err := t.ptmx.SyscallConn()
	if err != nil {
		return foregroundResponse{ID: id, Error: err.Error()}
	}
	if err := raw.Control(func(fd uintptr) {
		group, queryErr = unix.IoctlGetInt(int(fd), unix.TIOCGPGRP)
	}); err != nil {
		return foregroundResponse{ID: id, Error: err.Error()}
	}
	if queryErr != nil {
		return foregroundResponse{ID: id, Error: queryErr.Error()}
	}
	if group <= 1 {
		return foregroundResponse{ID: id, Error: "no live foreground group"}
	}
	return foregroundResponse{ID: id, Group: group}
}

func (s *Server) list() listedResponse {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := listedResponse{Terminals: make([]listedTerminal, 0, len(s.terms))}
	for id, t := range s.terms {
		out.Terminals = append(out.Terminals, listedTerminal{ID: id, PID: t.pid, Exited: t.isExited()})
	}
	return out
}

func (t *terminal) isExited() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.exited
}

// readLoop reads the PTY and coalesces micro-bursts before publishing, so the
// server receives a few large frames instead of one frame per read.
func (t *terminal) readLoop(s *Server) {
	raw := make(chan []byte, 64)
	done := make(chan struct{})
	go func() {
		defer close(raw)
		buffer := make([]byte, readBlock)
		for {
			n, err := t.ptmx.Read(buffer)
			if n > 0 {
				chunk := make([]byte, n)
				copy(chunk, buffer[:n])
				select {
				case raw <- chunk:
				case <-done:
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	idle := time.NewTimer(time.Hour)
	maxAge := time.NewTimer(time.Hour)
	if !idle.Stop() {
		<-idle.C
	}
	if !maxAge.Stop() {
		<-maxAge.C
	}
	defer idle.Stop()
	defer maxAge.Stop()
	var idleC, maxC <-chan time.Time

	var pending []byte
	flush := func() {
		if len(pending) == 0 {
			return
		}
		t.publish(pending)
		pending = nil
	}
	stop := func(timer *time.Timer, channel *<-chan time.Time) {
		if *channel == nil {
			return
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		*channel = nil
	}
	for {
		select {
		case chunk, ok := <-raw:
			if !ok {
				flush()
				return
			}
			pending = append(pending, chunk...)
			if len(pending) >= readBlock {
				stop(idle, &idleC)
				stop(maxAge, &maxC)
				flush()
				continue
			}
			if idleC == nil {
				maxAge.Reset(batchMaxAge)
				maxC = maxAge.C
			}
			if !idle.Stop() {
				select {
				case <-idle.C:
				default:
				}
			}
			idle.Reset(batchIdle)
			idleC = idle.C
		case <-idleC:
			idleC = nil
			stop(maxAge, &maxC)
			flush()
		case <-maxC:
			maxC = nil
			stop(idle, &idleC)
			flush()
		case req := <-t.resize:
			// Ordering: every byte already read keeps the old geometry, then the
			// resize is applied and announced, then new output follows.
			stop(idle, &idleC)
			stop(maxAge, &maxC)
			flush()
			err := pty.Setsize(t.ptmx, &pty.Winsize{
				Rows: req.Lines,
				Cols: req.Columns,
				X:    uint16(minInt(65535, int(req.CellW)*int(req.Columns))),
				Y:    uint16(minInt(65535, int(req.CellH)*int(req.Lines))),
			})
			if err == nil {
				t.announceResize(req)
			}
		}
	}
}

// announceResize publishes the geometry change in stream order.
func (t *terminal) announceResize(req resizeRequest) {
	t.mu.Lock()
	subs := make([]*subscriber, 0, len(t.subs))
	for sub := range t.subs {
		subs = append(subs, sub)
	}
	t.mu.Unlock()
	payload, err := json.Marshal(resizedEvent{ID: t.id, Generation: t.generation, Lines: req.Lines, Columns: req.Columns})
	if err != nil {
		return
	}
	for _, sub := range subs {
		sub.enqueueJSON(kindResized, payload)
	}
}

// publish appends to the bounded history and fans out without ever blocking the
// PTY reader. A subscriber that cannot keep up is dropped and re-attaches from
// its offset, so slow consumers cannot stall the program.
func (t *terminal) publish(data []byte) {
	t.mu.Lock()
	offset := t.produced
	t.produced += uint64(len(data))
	t.history = append(t.history, data...)
	if over := len(t.history) - historyLimit; over > 0 {
		copy(t.history, t.history[over:])
		t.history = t.history[:len(t.history)-over]
		t.start += uint64(over)
	}
	subs := make([]*subscriber, 0, len(t.subs))
	for sub := range t.subs {
		subs = append(subs, sub)
	}
	t.mu.Unlock()
	var id [16]byte
	if parsed, err := uuid.Parse(t.id); err == nil {
		copy(id[:], parsed[:])
	}
	for _, sub := range subs {
		if !sub.writeOutput(id, t.generation, offset, data) {
			// Drop only this output subscription: the connection also carries
			// control traffic and other terminals, so closing it would be far
			// more disruptive than a re-attach.
			t.mu.Lock()
			delete(t.subs, sub)
			t.mu.Unlock()
		}
	}
}

func (t *terminal) waitLoop(s *Server) {
	err := t.cmd.Wait()
	code := int32(0)
	if t.cmd.ProcessState != nil {
		code = int32(t.cmd.ProcessState.ExitCode())
	} else if err == nil {
		code = 0
	} else {
		code = -1
	}
	t.mu.Lock()
	t.exited = true
	t.exitCode = code
	subs := make([]*subscriber, 0, len(t.subs))
	for sub := range t.subs {
		subs = append(subs, sub)
	}
	t.mu.Unlock()
	payload, err := json.Marshal(exitedEvent{ID: t.id, Generation: t.generation, Code: code})
	if err == nil {
		for _, sub := range subs {
			sub.enqueueJSON(kindExited, payload)
		}
	}
	_ = t.ptmx.Close()
}

func (t *terminal) close() {
	t.once.Do(func() {
		close(t.closed)
		if t.cmd.Process != nil {
			pid := t.cmd.Process.Pid
			if err := syscall.Kill(-pid, syscall.SIGHUP); err != nil && !errors.Is(err, syscall.ESRCH) {
				_ = t.cmd.Process.Signal(syscall.SIGHUP)
			}
		}
		_ = t.ptmx.Close()
	})
}

func (s *subscriber) close() {
	s.once.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		close(s.frames)
		_ = s.writer.conn.Close()
	})
}

// enqueue never blocks: a full queue means this subscriber fell behind, so its
// subscription is dropped and it can re-attach from its offset.
func (s *subscriber) enqueueJSON(kind byte, payload []byte) bool {
	frame := make([]byte, 5+len(payload))
	frame[0] = kind
	binary.BigEndian.PutUint32(frame[1:], uint32(len(payload)))
	copy(frame[5:], payload)
	return s.enqueue(frame)
}

func (s *subscriber) enqueue(frame []byte) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	select {
	case s.frames <- frame:
		return true
	default:
		return false
	}
}

func (s *subscriber) writeOutput(id [16]byte, generation, offset uint64, data []byte) bool {
	frame := make([]byte, 5+outputHeaderBytes+len(data))
	frame[0] = kindOutput
	binary.BigEndian.PutUint32(frame[1:], uint32(outputHeaderBytes+len(data)))
	copy(frame[5:], id[:])
	binary.BigEndian.PutUint64(frame[5+16:], generation)
	binary.BigEndian.PutUint64(frame[5+24:], offset)
	copy(frame[5+outputHeaderBytes:], data)
	return s.enqueue(frame)
}

// writeReplay sends retained history as output frames so the server rebuilds its
// replay through the same path as live output.
func (s *subscriber) writeReplay(resp attachedResponse, replay []byte) {
	var id [16]byte
	if parsed, err := uuid.Parse(resp.ID); err == nil {
		copy(id[:], parsed[:])
	}
	const chunk = 64 * 1024
	for offset := 0; offset < len(replay); offset += chunk {
		end := minInt(offset+chunk, len(replay))
		if !s.writeOutput(id, resp.Generation, resp.Offset+uint64(offset), replay[offset:end]) {
			return
		}
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func (w *connWriter) writeJSON(kind byte, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	frame := make([]byte, 5+len(payload))
	frame[0] = kind
	binary.BigEndian.PutUint32(frame[1:], uint32(len(payload)))
	copy(frame[5:], payload)
	return w.write(frame)
}
