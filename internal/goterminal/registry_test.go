//go:build !windows

package goterminal

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/SurTeam/Water/internal/goprotocol"
	"github.com/SurTeam/Water/internal/gosupervisor"
	"github.com/google/uuid"
)

func TestDelayedReaderPreservesAcceptedInput(t *testing.T) {
	for _, supervised := range []bool{false, true} {
		t.Run(fmt.Sprintf("supervised=%v", supervised), func(t *testing.T) {
			r := NewRegistry()
			defer r.CloseAll()
			if supervised {
				server := gosupervisor.NewServer()
				directory, err := os.MkdirTemp("/tmp", "water-input-retry-")
				if err != nil {
					t.Fatal(err)
				}
				defer os.RemoveAll(directory)
				socket := filepath.Join(directory, "supervisor.sock")
				if err := server.Listen(socket); err != nil {
					t.Fatal(err)
				}
				defer server.Close()
				if err := r.AttachSupervisor(socket); err != nil {
					t.Fatal(err)
				}
			}
			payload := bytes.Repeat([]byte("byte-stream-\x1b[99u"), 16384)
			command := fmt.Sprintf("stty raw -echo; printf INPUT_READY; sleep 1; head -c %d | shasum -a 256; sleep 1", len(payload))
			size := goprotocol.TerminalSize{Columns: 80, Lines: 24}
			var term *Terminal
			var err error
			if supervised {
				term, _, err = r.SpawnSupervised(uuid.New(), "/bin/sh", []string{"-c", command}, size, "", false)
			} else {
				term, err = r.Spawn("/bin/sh", []string{"-c", command}, size)
			}
			if err != nil {
				t.Fatal(err)
			}
			waitTerminalText(t, term, "INPUT_READY")
			if err := term.EnqueueInput(payload); err != nil {
				t.Fatal(err)
			}
			waitTerminalText(t, term, fmt.Sprintf("%x", sha256.Sum256(payload)))
		})
	}
}

func TestExitIsSequencedAfterFinalOutput(t *testing.T) {
	r := NewRegistry()
	term, err := r.Spawn("/bin/sh", []string{"-c", "printf final-output"}, goprotocol.TerminalSize{Columns: 80, Lines: 24})
	if err != nil {
		t.Fatal(err)
	}
	defer r.CloseAll()

	events, done, cancel := term.Subscribe()
	defer cancel()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case ev := <-events:
			if ev.Kind == goprotocol.ExitEvent {
				replay := term.Replay()
				if len(replay) == 0 || replay[len(replay)-1].Kind != goprotocol.ExitEvent {
					t.Fatalf("exit is not last event: %#v", replay)
				}
				var out []byte
				for _, item := range replay {
					if item.Kind == goprotocol.OutputEvent {
						out = append(out, item.Data...)
					}
				}
				if !bytes.Contains(out, []byte("final-output")) {
					t.Fatalf("missing final output: %q", out)
				}
				return
			}
		case <-done:
			t.Fatal("subscription ended before exit")
		case <-deadline.C:
			t.Fatal("timed out waiting for exit")
		}
	}
}

func TestCloseTerminatesProcessGroup(t *testing.T) {
	r := NewRegistry()
	term, err := r.Spawn("/bin/sh", []string{
		"-c",
		// Report readiness from the child after resetting the background shell's
		// signal disposition, so Close tests group delivery rather than fork setup.
		"/bin/sh -c 'trap - HUP; printf \"CHILD:%s\\n\" \"$$\"; exec sleep 30' & wait",
	}, goprotocol.TerminalSize{Columns: 80, Lines: 24})
	if err != nil {
		t.Fatal(err)
	}
	defer r.CloseAll()

	deadline := time.Now().Add(2 * time.Second)
	var childPID int
	for time.Now().Before(deadline) {
		var raw []byte
		for _, ev := range term.Replay() {
			if ev.Kind == goprotocol.OutputEvent {
				raw = append(raw, ev.Data...)
			}
		}
		text := string(raw)
		if idx := strings.Index(text, "CHILD:"); idx >= 0 {
			line := text[idx+len("CHILD:"):]
			if end := strings.IndexAny(line, "\r\n"); end >= 0 {
				line = line[:end]
			}
			childPID, _ = strconv.Atoi(strings.TrimSpace(line))
			if childPID > 1 {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if childPID <= 1 {
		t.Fatal("child PID was not reported")
	}

	if err := term.Close(); err != nil {
		t.Fatal(err)
	}
	killDeadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(killDeadline) {
		if err := syscall.Kill(childPID, 0); err == syscall.ESRCH {
			return
		}
		// An orphaned zombie has terminated; reaping belongs to the OS, and
		// kill(pid, 0) alone can still report it as present under load.
		out, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(childPID)).Output()
		if err == nil && strings.HasPrefix(strings.TrimSpace(string(out)), "Z") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	state, _ := exec.Command("ps", "-o", "pid=,ppid=,pgid=,stat=,command=", "-p", strconv.Itoa(childPID)).CombinedOutput()
	t.Logf("descendant state: %s", state)
	_ = syscall.Kill(childPID, syscall.SIGKILL)
	t.Fatalf("terminal descendant %d survived process-group close", childPID)
}

func TestPTYOutputIsCoalescedBeforeFanout(t *testing.T) {
	r := NewRegistry()
	term, err := r.Spawn(
		"/bin/sh",
		[]string{"-c", "yes WATER_BATCH | head -c 1048576"},
		goprotocol.TerminalSize{Columns: 80, Lines: 24},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer r.CloseAll()

	events, done, cancel := term.Subscribe()
	defer cancel()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	outputEvents := 0
	outputBytes := 0
	for {
		select {
		case ev := <-events:
			switch ev.Kind {
			case goprotocol.OutputEvent:
				outputEvents++
				outputBytes += len(ev.Data)
			case goprotocol.ExitEvent:
				if outputBytes < 1024*1024 {
					t.Fatalf("coalesced output retained only %d bytes", outputBytes)
				}
				if outputEvents >= 512 {
					t.Fatalf("1 MiB flood produced %d output events; tiny PTY reads were not coalesced", outputEvents)
				}
				return
			}
		case <-done:
			t.Fatal("subscription ended before exit")
		case <-timer.C:
			t.Fatal("timed out waiting for coalesced terminal flood")
		}
	}
}

func TestResizeFollowsObservedOutputAndPrecedesLaterOutput(t *testing.T) {
	r := NewRegistry()
	term, err := r.Spawn(
		"/bin/sh",
		[]string{"-c", "printf BEFORE_RESIZE; read _; printf AFTER_RESIZE"},
		goprotocol.TerminalSize{Columns: 80, Lines: 24},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer r.CloseAll()

	events, done, cancel := term.Subscribe()
	defer cancel()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()

	var ordered []goprotocol.TerminalEvent
	beforeSeen := false
	resized := false
	for {
		select {
		case ev := <-events:
			ordered = append(ordered, ev)
			if ev.Kind == goprotocol.OutputEvent && bytes.Contains(ev.Data, []byte("BEFORE_RESIZE")) && !beforeSeen {
				beforeSeen = true
				if err := term.Resize(goprotocol.TerminalSize{Columns: 100, Lines: 31}); err != nil {
					t.Fatal(err)
				}
				resized = true
				if err := term.Write([]byte("\n")); err != nil {
					t.Fatal(err)
				}
			}
			if ev.Kind == goprotocol.ExitEvent {
				if !beforeSeen || !resized {
					t.Fatal("terminal exited before resize sequence completed")
				}
				beforeIndex, resizeIndex, afterIndex, exitIndex := -1, -1, -1, -1
				for i, item := range ordered {
					switch item.Kind {
					case goprotocol.OutputEvent:
						if beforeIndex < 0 && bytes.Contains(item.Data, []byte("BEFORE_RESIZE")) {
							beforeIndex = i
						}
						if afterIndex < 0 && bytes.Contains(item.Data, []byte("AFTER_RESIZE")) {
							afterIndex = i
						}
					case goprotocol.ResizeEvent:
						if item.Size.Columns == 100 && item.Size.Lines == 31 && resizeIndex < 0 {
							resizeIndex = i
						}
					case goprotocol.ExitEvent:
						exitIndex = i
					}
				}
				if !(beforeIndex >= 0 && beforeIndex < resizeIndex && resizeIndex < afterIndex && afterIndex < exitIndex) {
					t.Fatalf("stream order before=%d resize=%d after=%d exit=%d events=%#v", beforeIndex, resizeIndex, afterIndex, exitIndex, ordered)
				}
				return
			}
		case <-done:
			t.Fatal("subscription ended before exit")
		case <-timer.C:
			t.Fatal("timed out waiting for resize ordering")
		}
	}
}

func TestInputQueueIsBoundedPerTerminal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires Unix shell/process fixture; native Windows lifecycle is covered separately")
	}
	r := NewRegistry()
	defer r.CloseAll()
	term, err := r.Spawn("/bin/sh", []string{"-c", "exec sleep 30"}, goprotocol.TerminalSize{Columns: 20, Lines: 4})
	if err != nil {
		t.Fatal(err)
	}
	// The child never reads stdin. Filling the queue must return an explicit
	// error rather than block the caller or consume the server's read loop.
	deadline := time.Now().Add(time.Second)
	var full error
	for time.Now().Before(deadline) {
		full = term.EnqueueInput(bytes.Repeat([]byte("x"), 64*1024))
		if errors.Is(full, ErrInputQueueFull) {
			break
		}
		if full != nil {
			t.Fatalf("unexpected queue error: %v", full)
		}
	}
	if !errors.Is(full, ErrInputQueueFull) {
		t.Fatal("unread stdin did not fill the per-terminal queue")
	}
	if !errors.Is(term.EnqueueInput([]byte("x")), ErrInputQueueFull) {
		t.Fatal("full queue accepted another write")
	}
	if _, ok := r.Get(term.ID); !ok {
		t.Fatal("input queue overflow removed the pane")
	}
	if err := syscall.Kill(term.pid, 0); err != nil {
		t.Fatalf("input queue overflow killed the pane process: %v", err)
	}
}

// Each pane must own its connections. Sharing one supervisor socket would let a
// stalled write for one pane delay control traffic for all of them.
func TestSupervisedPanesUseIndependentConnections(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires Unix shell/process fixture; native Windows lifecycle is covered separately")
	}
	supervisor := gosupervisor.NewServer()
	dir, err := os.MkdirTemp("/tmp", "water-sup-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "supervisor.sock")
	if err := supervisor.Listen(socket); err != nil {
		t.Fatal(err)
	}
	defer supervisor.Close()

	r := NewRegistry()
	defer r.CloseAll()
	if err := r.AttachSupervisor(socket); err != nil {
		t.Fatal(err)
	}
	size := goprotocol.TerminalSize{Columns: 80, Lines: 24}
	slow, _, err := r.SpawnSupervised(uuid.New(), "/bin/sh", []string{"-c", "exec sleep 30"}, size, "", false)
	if err != nil {
		t.Fatal(err)
	}
	quiet, _, err := r.SpawnSupervised(uuid.New(), "/bin/sh", []string{"-c", "printf 'QUIET_READY\\n'; exec sleep 30"}, size, "", false)
	if err != nil {
		t.Fatal(err)
	}
	waitTerminalText(t, quiet, "QUIET_READY")

	slow.supervisor.mu.Lock()
	slowControl, slowOutput := slow.supervisor.control, slow.supervisor.output
	slow.supervisor.mu.Unlock()
	quiet.supervisor.mu.Lock()
	quietControl, quietOutput := quiet.supervisor.control, quiet.supervisor.output
	quiet.supervisor.mu.Unlock()
	if slowControl == nil || quietControl == nil || slowOutput == nil || quietOutput == nil {
		t.Fatal("supervised panes did not open both connections")
	}
	if slowControl == quietControl || slowOutput == quietOutput {
		t.Fatal("supervised panes share a supervisor connection")
	}
	if slowControl == slowOutput {
		t.Fatal("a pane shares its output and control connections")
	}

	// Losing the slow pane's control connection must not disturb the quiet pane.
	_ = slowControl.Close()
	start := time.Now()
	if err := quiet.EnqueueInput([]byte("printf 'QUIET_INPUT\\n'\n")); err != nil {
		t.Fatalf("quiet input: %v", err)
	}
	waitTerminalText(t, quiet, "QUIET_INPUT")
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("quiet pane waited %s after another pane's connection closed", elapsed)
	}
	if err := quiet.Resize(goprotocol.TerminalSize{Columns: 100, Lines: 30}); err != nil {
		t.Fatalf("quiet resize: %v", err)
	}
}

func TestSupervisedBlockedInputDoesNotBlockCloseOrControl(t *testing.T) {
	server := gosupervisor.NewServer()
	directory, err := os.MkdirTemp("/tmp", "water-sup-input-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(directory)
	socket := filepath.Join(directory, "supervisor.sock")
	if err := server.Listen(socket); err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	registry := NewRegistry()
	defer registry.CloseAll()
	if err := registry.AttachSupervisor(socket); err != nil {
		t.Fatal(err)
	}
	size := goprotocol.TerminalSize{Columns: 80, Lines: 24}
	slow, _, err := registry.SpawnSupervised(uuid.New(), "/bin/sh", []string{"-c", "stty raw -echo; printf BLOCK_READY; exec sleep 30"}, size, "", false)
	if err != nil {
		t.Fatal(err)
	}
	quiet, _, err := registry.SpawnSupervised(uuid.New(), "/bin/sh", []string{"-c", "exec sleep 30"}, size, "", false)
	if err != nil {
		t.Fatal(err)
	}
	waitTerminalText(t, slow, "BLOCK_READY")
	queued := make(chan struct{})
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		payload := bytes.Repeat([]byte("x"), 128*1024)
		for index := 0; index < 64; index++ {
			if err := slow.supervisedWrite(payload); err != nil {
				return
			}
			if index == 7 {
				close(queued)
			}
		}
	}()
	select {
	case <-queued:
	case <-time.After(2 * time.Second):
		t.Fatal("input did not reach the supervisor")
	}
	select {
	case <-writerDone:
		t.Fatal("non-reading pane did not apply input backpressure")
	case <-time.After(100 * time.Millisecond):
	}
	slow.supervisor.mu.Lock()
	input, control, output := slow.supervisor.input, slow.supervisor.control, slow.supervisor.output
	slow.supervisor.mu.Unlock()
	if input == nil || input == control || input == output {
		t.Fatal("blocked input shares a control/output connection")
	}
	controlDone := make(chan error, 1)
	go func() {
		if err := slow.Resize(goprotocol.TerminalSize{Columns: 100, Lines: 30}); err != nil {
			controlDone <- err
			return
		}
		if err := quiet.Resize(goprotocol.TerminalSize{Columns: 100, Lines: 30}); err != nil {
			controlDone <- err
			return
		}
		controlDone <- slow.Close()
	}()
	select {
	case err := <-controlDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("blocked input held resize or close")
	}
	select {
	case <-writerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("closing pane did not unblock its input writer")
	}
}

// The output connection can be dropped independently while the program keeps
// running. The mirror must reconnect from its last offset without duplicating
// or losing the retained stream.
func TestSupervisedOutputReconnectsFromOffset(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires Unix shell/process fixture; native Windows lifecycle is covered separately")
	}
	supervisor := gosupervisor.NewServer()
	dir, err := os.MkdirTemp("/tmp", "water-sup-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "supervisor.sock")
	if err := supervisor.Listen(socket); err != nil {
		t.Fatal(err)
	}
	defer supervisor.Close()

	r := NewRegistry()
	defer r.CloseAll()
	if err := r.AttachSupervisor(socket); err != nil {
		t.Fatal(err)
	}
	term, _, err := r.SpawnSupervised(uuid.New(), "/bin/sh", []string{"-c", "while read line; do echo got:$line; done"}, goprotocol.TerminalSize{Columns: 80, Lines: 24}, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := term.EnqueueInput([]byte("first\n")); err != nil {
		t.Fatal(err)
	}
	waitTerminalText(t, term, "got:first")

	term.supervisor.mu.Lock()
	output := term.supervisor.output
	term.supervisor.output = nil
	term.supervisor.mu.Unlock()
	if output == nil {
		t.Fatal("pane has no output connection")
	}
	_ = output.Close()

	if err := term.EnqueueInput([]byte("second\n")); err != nil {
		t.Fatalf("input after output loss: %v", err)
	}
	waitTerminalText(t, term, "got:second")
	if got := terminalOutput(term); strings.Count(got, "got:first") != 1 {
		t.Fatalf("reconnect duplicated history: %q", got)
	}
}

// A transient failure while delivering one pane's input must not remove the
// pane. Removing a supervised terminal sends a real close command and kills
// the program, which a reconnect is supposed to avoid.
func TestSupervisedInputFailureKeepsPaneAlive(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires Unix shell/process fixture; native Windows lifecycle is covered separately")
	}
	supervisor := gosupervisor.NewServer()
	dir, err := os.MkdirTemp("/tmp", "water-sup-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "supervisor.sock")
	if err := supervisor.Listen(socket); err != nil {
		t.Fatal(err)
	}
	defer supervisor.Close()

	r := NewRegistry()
	defer r.CloseAll()
	if err := r.AttachSupervisor(socket); err != nil {
		t.Fatal(err)
	}
	term, _, err := r.SpawnSupervised(uuid.New(), "/bin/sh", []string{"-c", "while read line; do echo got:$line; done"}, goprotocol.TerminalSize{Columns: 80, Lines: 24}, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := term.EnqueueInput([]byte("before\n")); err != nil {
		t.Fatal(err)
	}
	waitTerminalText(t, term, "got:before")

	term.supervisor.mu.Lock()
	control := term.supervisor.control
	term.supervisor.control = nil
	term.supervisor.mu.Unlock()
	if control == nil {
		t.Fatal("pane has no control connection")
	}
	_ = control.Close()

	// The closed control connection makes the first write fail. The pane must
	// still exist, and the following write must reconnect and reach the program.
	if err := term.EnqueueInput([]byte("after\n")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, ok := r.Get(term.ID); !ok {
			t.Fatal("transient input failure removed the supervised pane")
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	waitTerminalText(t, term, "got:after")
}

func waitTerminalText(t *testing.T, term *Terminal, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(terminalOutput(term), want) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("terminal did not produce %q; output=%q", want, terminalOutput(term))
}

func terminalOutput(term *Terminal) string {
	var out strings.Builder
	for _, ev := range term.Replay() {
		if ev.Kind == goprotocol.OutputEvent {
			out.Write(ev.Data)
		}
	}
	return out.String()
}
