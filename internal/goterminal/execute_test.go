//go:build !windows

package goterminal

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/SurTeam/Water/internal/goprotocol"
	"github.com/SurTeam/Water/internal/gosupervisor"
	"github.com/google/uuid"
)

func executionShell(t *testing.T) *Terminal {
	t.Helper()
	r := NewRegistry()
	t.Cleanup(r.CloseAll)
	term, err := r.Spawn("/bin/sh", []string{"-i"}, goprotocol.TerminalSize{Columns: 80, Lines: 24})
	if err != nil {
		t.Fatal(err)
	}
	return waitExecutionShell(t, term)
}

func waitExecutionShell(t *testing.T, term *Terminal) *Terminal {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if fg, err := term.ForegroundGroup(); err == nil && fg == term.processID() && len(term.Replay()) > 0 {
			return term
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("interactive shell did not become ready")
	return nil
}
func TestExecuteRenderedAndStatus(t *testing.T) {
	term := executionShell(t)
	got, err := term.Execute(context.Background(), ExecuteOptions{Command: "printf 'old\\rnew\\033[K\\n'; printf '\\033[31mred\\033[0m\\n'; exit 7"})
	if err != nil {
		t.Fatal(err)
	}
	if got.ExitCode == nil || *got.ExitCode != 7 || got.TimedOut || !strings.Contains(got.Text, "new\nred") || strings.Contains(got.Text, "old") || strings.Contains(got.Text, "water-exec") || strings.Contains(got.Text, "printf") {
		t.Fatalf("result %#v", got)
	}
}
func TestExecuteTimeoutShellSurvives(t *testing.T) {
	for _, tt := range []struct {
		name, command string
		kill          bool
	}{{"term", "printf 'started\\n'; sleep 30", false}, {"kill", "trap '' TERM; printf 'started\\n'; while :; do sleep 1; done", true}} {
		t.Run(tt.name, func(t *testing.T) {
			term := executionShell(t)
			before := time.Now()
			got, err := term.Execute(context.Background(), ExecuteOptions{Command: tt.command, Timeout: 300 * time.Millisecond})
			if err != nil {
				t.Fatal(err)
			}
			if !got.TimedOut || !strings.Contains(got.Text, "started") || len(got.Signals) == 0 || got.Signals[0] != "TERM" {
				t.Fatalf("result %#v", got)
			}
			if tt.kill && (len(got.Signals) < 2 || got.Signals[1] != "KILL") {
				t.Fatalf("missing KILL: %#v", got)
			}
			if time.Since(before) > 3*time.Second {
				t.Fatal("cleanup took too long")
			}
			again, err := term.Execute(context.Background(), ExecuteOptions{Command: "printf 'survived\\n'"})
			if err != nil || again.ExitCode == nil || *again.ExitCode != 0 || !strings.Contains(again.Text, "survived") {
				t.Fatalf("shell did not survive: %#v %v", again, err)
			}
		})
	}
}
func TestExecuteBoundsAndLongInput(t *testing.T) {
	term := executionShell(t)
	got, err := term.Execute(context.Background(), ExecuteOptions{Command: "printf '%s\\n' '" + strings.Repeat("x", 2000) + "'; printf 'last\\n'", MaxRows: 2, MaxBytes: 90})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Truncated || len(got.Text) > 90 || strings.Contains(got.Text, "water_exec_input") {
		t.Fatalf("result %#v", got)
	}
	for _, opts := range []ExecuteOptions{{Command: ""}, {Command: "x", Timeout: -1}, {Command: "x", MaxRows: 257}, {Command: "x", MaxBytes: 1024*1024 + 1}, {Command: strings.Repeat("x", MaxExecuteCommandBytes+1)}} {
		if _, err := term.Execute(context.Background(), opts); err == nil {
			t.Fatalf("accepted %#v", opts)
		}
	}
	term.executeMu.Lock()
	_, err = term.Execute(context.Background(), ExecuteOptions{Command: "true"})
	term.executeMu.Unlock()
	if err == nil {
		t.Fatal("accepted concurrent execution")
	}
}
func TestExecuteAltAndCancellation(t *testing.T) {
	term := executionShell(t)
	got, err := term.Execute(context.Background(), ExecuteOptions{Command: "printf '\\033[?1049h\\033[2J\\033[Halternate'"})
	if err != nil || !got.AltScreen || !strings.Contains(got.Text, "alternate") {
		t.Fatalf("alternate result %#v %v", got, err)
	}
	// Restore the primary buffer through another execution before cancellation.
	if _, err := term.Execute(context.Background(), ExecuteOptions{Command: "printf '\\033[?1049l'"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	got, err = term.Execute(ctx, ExecuteOptions{Command: "sleep 30"})
	if err != context.DeadlineExceeded || got.TimedOut || len(got.Signals) == 0 {
		t.Fatalf("cancel result %#v %v", got, err)
	}
}

func TestExecuteRejectsBusyShell(t *testing.T) {
	term := executionShell(t)
	if err := term.Write([]byte("sleep 30\n")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if fg, err := term.ForegroundGroup(); err == nil && fg != term.cmd.Process.Pid {
			if _, err := term.Execute(context.Background(), ExecuteOptions{Command: "true"}); err == nil {
				t.Fatal("accepted busy shell")
			}
			// Only this test's captured foreground child is terminated.
			_ = syscall.Kill(-fg, syscall.SIGTERM)
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("foreground child did not start")
}

func TestExecuteMarkerFragmentation(t *testing.T) {
	prefix := []byte("\x1b]777;unique;")
	var normal strings.Builder
	var markers []string
	p := executeMarkerParser{prefix: prefix, normal: func(b []byte) { normal.Write(b) }, marker: func(s string) { markers = append(markers, s) }}
	for _, b := range []byte("echo " + string(prefix) + "start;123\ahello\rworld" + string(prefix) + "end;7\atail") {
		p.write([]byte{b})
	}
	p.flush()
	if normal.String() != "echo hello\rworldtail" || strings.Join(markers, ",") != "start;123,end;7" {
		t.Fatalf("%q %#v", normal.String(), markers)
	}
}

func TestExecuteNestedIgnoringDescendant(t *testing.T) {
	term := executionShell(t)
	got, err := term.Execute(context.Background(), ExecuteOptions{Command: `/bin/sh -c 'trap "" TERM; while :; do sleep 1; done' & echo DESCENDANT:$!; wait`, Timeout: 400 * time.Millisecond})
	if err != nil || !got.TimedOut || strings.Join(got.Signals, ",") != "TERM,KILL" {
		t.Fatalf("%#v %v", got, err)
	}
	index := strings.Index(got.Text, "DESCENDANT:")
	if index < 0 {
		t.Fatalf("missing descendant: %#v", got)
	}
	fields := strings.Fields(got.Text[index+len("DESCENDANT:"):])
	pid, err := strconv.Atoi(fields[0])
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if syscall.Kill(pid, 0) != nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("ignoring descendant %d survived cleanup", pid)
}

func TestExecuteTinyTimeoutAbortsSubmission(t *testing.T) {
	term := executionShell(t)
	command := "printf 'SHOULD_NOT_RUN\\n'; #" + strings.Repeat("x", MaxExecuteCommandBytes-32)
	if len(command) > MaxExecuteCommandBytes {
		t.Fatal("fixture too long")
	}
	before := time.Now()
	got, err := term.Execute(context.Background(), ExecuteOptions{Command: command, Timeout: time.Millisecond})
	if err != nil || !got.TimedOut || time.Since(before) > 2*time.Second {
		t.Fatalf("%#v %v", got, err)
	}
	again, err := term.Execute(context.Background(), ExecuteOptions{Command: "printf 'ready\\n'"})
	if err != nil || again.ExitCode == nil || *again.ExitCode != 0 || !strings.Contains(again.Text, "ready") || strings.Contains(again.Text, "SHOULD_NOT_RUN") {
		t.Fatalf("%#v %v", again, err)
	}
}

func TestExecuteLiteralControlBytesAndConsecutive(t *testing.T) {
	term := executionShell(t)
	got, err := term.Execute(context.Background(), ExecuteOptions{Command: "printf '%s' 'before\x03\x04\x15\x1b\rafter\nliteral'"})
	if err != nil || got.ExitCode == nil || *got.ExitCode != 0 || !strings.Contains(got.Text, "literal") {
		t.Fatalf("%#v %v", got, err)
	}
	for i := 0; i < 10; i++ {
		got, err = term.Execute(context.Background(), ExecuteOptions{Command: "printf 'next\\n'"})
		if err != nil || got.ExitCode == nil || *got.ExitCode != 0 {
			t.Fatalf("iteration %d: %#v %v", i, got, err)
		}
	}
}

func TestExecuteTransientAltAndUpwardCursor(t *testing.T) {
	for _, command := range []string{"printf '\\033[?1049hsecret\\033[?1049lprimary'", "printf 'first\\nsecond\\033[1A\\rchanged'"} {
		term := executionShell(t)
		got, err := term.Execute(context.Background(), ExecuteOptions{Command: command})
		if err != nil || !got.Truncated {
			t.Fatalf("%#v %v", got, err)
		}
		if strings.Contains(command, "1049") && !got.AltScreen {
			t.Fatalf("transient alt missed: %#v", got)
		}
	}
}

func TestExecuteResizeInvalidatesRange(t *testing.T) {
	term := executionShell(t)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		time.Sleep(100 * time.Millisecond)
		_ = term.Resize(goprotocol.TerminalSize{Columns: 40, Lines: 12})
	}()
	got, err := term.Execute(context.Background(), ExecuteOptions{Command: "printf 'before\\n'; sleep 0.2; printf 'after\\n'"})
	<-finished
	if err != nil || !got.Truncated || !strings.Contains(got.Text, "after") {
		t.Fatalf("%#v %v", got, err)
	}
}

func TestExecuteTransportContainsOnlySafeLines(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString([]byte("a\n\r\x03\x04\x15\x1b'\"é"))
	transport := splitExecuteInput("printf '%b' '" + encoded + "'\n")
	for _, line := range strings.Split(transport, "\n") {
		if len(line) > 800 {
			t.Fatalf("oversize physical line %d", len(line))
		}
		for _, b := range []byte(line) {
			if b < 32 || b > 126 {
				t.Fatalf("unsafe transport byte %x", b)
			}
		}
	}
}

func TestExecuteMaximumCommandTransport(t *testing.T) {
	term := executionShell(t)
	command := "printf 'maximum\\n'; #"
	command += strings.Repeat("x", MaxExecuteCommandBytes-len(command))
	got, err := term.Execute(context.Background(), ExecuteOptions{Command: command, Timeout: 3 * time.Second})
	if err != nil || got.ExitCode == nil || *got.ExitCode != 0 || got.TimedOut || !strings.Contains(got.Text, "maximum") {
		t.Fatalf("%#v %v", got, err)
	}
}

func TestInteractivePOSIXShellWithoutCmd(t *testing.T) {
	for _, tt := range []struct {
		name string
		args []string
		want bool
	}{
		{"default", nil, true},
		{"interactive", []string{"-i"}, true},
		{"login", []string{"--login", "--norc", "-i"}, true},
		{"command", []string{"-ic", "true"}, false},
		{"script", []string{"script.sh"}, false},
		{"unknown-option", []string{"--unknown"}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			term := &Terminal{program: "/bin/sh", pid: 123, args: tt.args}
			if got := term.interactivePOSIXShell(); got != tt.want {
				t.Fatalf("eligibility = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestExecuteSupervisedAndResumed(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "water-exec-sup-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	supervisor := gosupervisor.NewServer()
	socket := filepath.Join(dir, "supervisor.sock")
	if err := supervisor.Listen(socket); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = supervisor.Close() })
	r := NewRegistry()
	t.Cleanup(r.DetachSupervisor)
	if err := r.AttachSupervisor(socket); err != nil {
		t.Fatal(err)
	}
	key := uuid.New()
	args := []string{"-i"}
	term, resumed, err := r.SpawnSupervised(key, "/bin/sh", args, goprotocol.TerminalSize{Columns: 80, Lines: 24}, "", false)
	if err != nil || resumed {
		t.Fatalf("spawn: resumed=%v err=%v", resumed, err)
	}
	// The mirror must own its argument slice, rather than the caller's memory.
	args[0] = "-c"
	if term.cmd != nil || !term.interactivePOSIXShell() {
		t.Fatal("invalid supervised shell metadata")
	}
	waitExecutionShell(t, term)
	pid := term.processID()
	for _, recover := range []bool{false, true} {
		if recover {
			r.DetachSupervisor()
			r = NewRegistry()
			t.Cleanup(r.DetachSupervisor)
			if err := r.AttachSupervisor(socket); err != nil {
				t.Fatal(err)
			}
			term, resumed, err = r.SpawnSupervised(key, "/bin/sh", []string{"-i"}, goprotocol.TerminalSize{Columns: 80, Lines: 24}, "", true)
			if err != nil || !resumed || term.processID() != pid {
				t.Fatalf("resume: resumed=%v err=%v", resumed, err)
			}
			waitExecutionShell(t, term)
		}
		got, err := term.Execute(context.Background(), ExecuteOptions{Command: "printf 'old\rnew\033[K\n'; exit 7"})
		if err != nil || got.ExitCode == nil || *got.ExitCode != 7 || !strings.Contains(got.Text, "new") || strings.Contains(got.Text, "old") {
			t.Fatalf("execute: %#v %v", got, err)
		}
		got, err = term.Execute(context.Background(), ExecuteOptions{Command: "trap '' TERM; printf 'started\n'; while :; do sleep 1; done", Timeout: 300 * time.Millisecond})
		if err != nil || !got.TimedOut || !strings.Contains(got.Text, "started") || len(got.Signals) < 2 || got.Signals[0] != "TERM" || got.Signals[1] != "KILL" {
			t.Fatalf("timeout: %#v %v", got, err)
		}
		got, err = term.Execute(context.Background(), ExecuteOptions{Command: "printf 'survived\n'"})
		if err != nil || got.ExitCode == nil || *got.ExitCode != 0 || !strings.Contains(got.Text, "survived") {
			t.Fatalf("survival: %#v %v", got, err)
		}
	}
}
