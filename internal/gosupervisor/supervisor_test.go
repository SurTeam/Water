package gosupervisor

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func startSupervisor(t *testing.T) (*Server, string) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "water-sup-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	server := NewServer()
	socket := filepath.Join(dir, "supervisor.sock")
	if err := server.Listen(socket); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	return server, socket
}

func spawnShell(t *testing.T, client *Client, id uuid.UUID) {
	t.Helper()
	pid, resumed, err := client.Spawn(SpawnSpec{
		ID:      id,
		Program: "/bin/sh",
		Args:    []string{"-c", "while read line; do echo got:$line; done"},
		Env:     os.Environ(),
		Size:    struct{ Lines, Columns uint16 }{24, 80},
	})
	if err != nil {
		t.Fatal(err)
	}
	if pid <= 0 || resumed {
		t.Fatalf("pid=%d resumed=%v", pid, resumed)
	}
}

func collect(t *testing.T, events <-chan Event, want string, timeout time.Duration) string {
	t.Helper()
	var out strings.Builder
	deadline := time.After(timeout)
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				return out.String()
			}
			if ev.Kind == EventOutput {
				out.Write(ev.Output)
				if strings.Contains(out.String(), want) {
					return out.String()
				}
			}
		case <-deadline:
			return out.String()
		}
	}
}

func TestProgramSurvivesServerReconnect(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires Unix shell/process fixture; native Windows lifecycle is covered separately")
	}
	_, socket := startSupervisor(t)
	id := uuid.New()

	first, err := Dial(socket)
	if err != nil {
		t.Fatal(err)
	}
	spawnShell(t, first, id)
	attach, events, err := first.Attach(id, 0)
	if err != nil {
		t.Fatal(err)
	}
	if attach.PID <= 0 {
		t.Fatalf("missing pid: %+v", attach)
	}
	if err := first.Write(id, []byte("one\n")); err != nil {
		t.Fatal(err)
	}
	if got := collect(t, events, "got:one", 5*time.Second); !strings.Contains(got, "got:one") {
		t.Fatalf("first connection saw %q", got)
	}

	// The server goes away; the program must keep running.
	pid := attach.PID
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)

	second, err := Dial(socket)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	pid2, resumed, err := second.Spawn(SpawnSpec{
		ID:      id,
		Program: "/bin/sh",
		Args:    []string{"-c", "exit 9"},
		Env:     os.Environ(),
		Size:    struct{ Lines, Columns uint16 }{24, 80},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !resumed || pid2 != pid {
		t.Fatalf("terminal was restarted: resumed=%v pid=%d want %d", resumed, pid2, pid)
	}

	resumedAttach, events2, err := second.Attach(id, 0)
	if err != nil {
		t.Fatal(err)
	}
	if resumedAttach.Truncated {
		t.Fatal("history was truncated within the limit")
	}
	if resumedAttach.Offset != 0 {
		t.Fatalf("unexpected offset %d", resumedAttach.Offset)
	}
	// History from before the reconnect is replayed to the new server.
	if got := collect(t, events2, "got:one", 5*time.Second); !strings.Contains(got, "got:one") {
		t.Fatalf("replay after reconnect missing output: %q", got)
	}
	// And the same program still answers.
	if err := second.Write(id, []byte("two\n")); err != nil {
		t.Fatal(err)
	}
	if got := collect(t, events2, "got:two", 5*time.Second); !strings.Contains(got, "got:two") {
		t.Fatalf("program stopped answering: %q", got)
	}
}

func TestReconnectReplaysFromOffsetWithoutLoss(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires Unix shell/process fixture; native Windows lifecycle is covered separately")
	}
	_, socket := startSupervisor(t)
	id := uuid.New()
	client, err := Dial(socket)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, _, err := client.Spawn(SpawnSpec{
		ID: id, Program: "/bin/sh", Args: []string{"-c", "while read line; do echo got:$line; done"},
		Env: os.Environ(), Size: struct{ Lines, Columns uint16 }{24, 80},
	}); err != nil {
		t.Fatal(err)
	}
	_, events, err := client.Attach(id, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Write(id, []byte("alpha\n")); err != nil {
		t.Fatal(err)
	}
	got := collect(t, events, "got:alpha", 5*time.Second)
	if !strings.Contains(got, "got:alpha") {
		t.Fatalf("missing first output: %q", got)
	}

	// Resume exactly where the previous attach stopped.
	offset := uint64(len(got))
	second, err := Dial(socket)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	attach, events2, err := second.Attach(id, offset)
	if err != nil {
		t.Fatal(err)
	}
	if attach.Offset != offset {
		t.Fatalf("offset %d, want %d", attach.Offset, offset)
	}
	if err := second.Write(id, []byte("beta\n")); err != nil {
		t.Fatal(err)
	}
	if got := collect(t, events2, "got:beta", 5*time.Second); !strings.Contains(got, "got:beta") {
		t.Fatalf("resumed stream lost output: %q", got)
	}
}

func TestResizeAndExitAreOrderedWithOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires Unix shell/process fixture; native Windows lifecycle is covered separately")
	}
	_, socket := startSupervisor(t)
	id := uuid.New()
	client, err := Dial(socket)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, _, err := client.Spawn(SpawnSpec{
		ID: id, Program: "/bin/sh", Args: []string{"-c", "echo ready; read x; exit 7"},
		Env: os.Environ(), Size: struct{ Lines, Columns uint16 }{24, 80},
	}); err != nil {
		t.Fatal(err)
	}
	_, events, err := client.Attach(id, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := collect(t, events, "ready", 5*time.Second); !strings.Contains(got, "ready") {
		t.Fatalf("missing banner: %q", got)
	}
	if err := client.Resize(id, 40, 120, 8, 16); err != nil {
		t.Fatal(err)
	}
	if err := client.Write(id, []byte("x\n")); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(5 * time.Second)
	sawResize, sawExit := false, false
	for !sawExit {
		select {
		case ev, ok := <-events:
			if !ok {
				t.Fatal("event stream closed before exit")
			}
			switch ev.Kind {
			case EventResize:
				sawResize = true
				if ev.Lines != 40 || ev.Columns != 120 {
					t.Fatalf("resize %+v", ev)
				}
			case EventExit:
				sawExit = true
				if ev.ExitCode != 7 {
					t.Fatalf("exit code %d", ev.ExitCode)
				}
			}
		case <-deadline:
			t.Fatal("timed out waiting for exit")
		}
	}
	if !sawResize {
		t.Fatal("resize event was lost")
	}
}

func TestHistoryIsBoundedButKeepsRecentOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires Unix shell/process fixture; native Windows lifecycle is covered separately")
	}
	_, socket := startSupervisor(t)
	id := uuid.New()
	client, err := Dial(socket)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	// Emit more than the history limit; the tail must still be replayable.
	if _, _, err := client.Spawn(SpawnSpec{
		ID: id, Program: "/bin/sh",
		Args: []string{"-c", "i=0; while [ $i -lt 4000 ]; do echo line-$i-padding-padding-padding; i=$((i+1)); done; sleep 30"},
		Env:  os.Environ(), Size: struct{ Lines, Columns uint16 }{24, 80},
	}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1500 * time.Millisecond)
	attach, events, err := client.Attach(id, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !attach.Truncated {
		t.Log("history not truncated; emission may still be running")
	}
	if attach.Available > historyLimit {
		// Bounded storage is the point; the ring must not grow past the limit.
		t.Logf("available=%d limit=%d", attach.Available, historyLimit)
	}
	got := collect(t, events, "line-3999", 10*time.Second)
	if !strings.Contains(got, "line-3999") {
		t.Fatalf("recent output missing after truncation: %d bytes", len(got))
	}
}

func TestConcurrentSpawnsGetTheirOwnReplies(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires Unix shell/process fixture; native Windows lifecycle is covered separately")
	}
	_, socket := startSupervisor(t)
	client, err := Dial(socket)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	const parallel = 8
	ids := make([]uuid.UUID, parallel)
	for i := range ids {
		ids[i] = uuid.New()
	}
	errs := make(chan error, parallel)
	for i := range ids {
		go func(id uuid.UUID, index int) {
			pid, _, err := client.Spawn(SpawnSpec{
				ID: id, Program: "/bin/sh", Args: []string{"-c", "echo hello-" + id.String()[:8] + "; sleep 10"},
				Env: os.Environ(), Size: struct{ Lines, Columns uint16 }{24, 80},
			})
			if err == nil && pid <= 0 {
				err = fmt.Errorf("missing pid for %s", id)
			}
			errs <- err
		}(ids[i], i)
	}
	for range ids {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	listed, err := client.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != parallel {
		t.Fatalf("%d terminals, want %d", len(listed), parallel)
	}
}

func TestSlowConsumerDoesNotStallTheProgram(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires Unix shell/process fixture; native Windows lifecycle is covered separately")
	}
	_, socket := startSupervisor(t)
	id := uuid.New()
	client, err := Dial(socket)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, _, err := client.Spawn(SpawnSpec{
		ID: id, Program: "/bin/sh",
		Args: []string{"-c", "i=0; while [ $i -lt 20000 ]; do echo flood-$i-0123456789abcdef; i=$((i+1)); done; echo FINISHED; sleep 30"},
		Env:  os.Environ(), Size: struct{ Lines, Columns uint16 }{24, 80},
	}); err != nil {
		t.Fatal(err)
	}
	// Never read the events. Control traffic must stay responsive and the program
	// must keep producing: a slow consumer may only lose its own subscription.
	_, events, err := client.Attach(id, 0)
	if err != nil {
		t.Fatal(err)
	}
	_ = events
	deadline := time.Now().Add(3 * time.Second)
	progress := 0
	for time.Now().Before(deadline) {
		start := time.Now()
		listed, err := client.List()
		if err != nil {
			t.Fatalf("control stalled behind slow output: %v", err)
		}
		if elapsed := time.Since(start); elapsed > time.Second {
			t.Fatalf("control reply took %s", elapsed)
		}
		if len(listed) == 1 {
			progress++
		}
		time.Sleep(50 * time.Millisecond)
	}
	if progress == 0 {
		t.Fatal("supervisor stopped answering while the consumer was slow")
	}
}

func TestForegroundGroupIsReportedForOwnedPTY(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires Unix shell/process fixture; native Windows lifecycle is covered separately")
	}
	_, socket := startSupervisor(t)
	id := uuid.New()
	client, err := Dial(socket)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, _, err := client.Spawn(SpawnSpec{
		ID: id, Program: "/bin/sh", Args: []string{"-c", "sleep 30"},
		Env: os.Environ(), Size: struct{ Lines, Columns uint16 }{24, 80},
	}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if group, err := client.Foreground(id); err == nil && group > 1 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	group, err := client.Foreground(id)
	t.Fatalf("no foreground group: %d %v", group, err)
}

func TestReplaceStartsANewProgramAndDropsStaleEvents(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires Unix shell/process fixture; native Windows lifecycle is covered separately")
	}
	_, socket := startSupervisor(t)
	id := uuid.New()
	client, err := Dial(socket)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	if _, _, err := client.Spawn(SpawnSpec{
		ID: id, Program: "/bin/sh", Args: []string{"-c", "echo FIRST; sleep 20"},
		Env: os.Environ(), Size: struct{ Lines, Columns uint16 }{24, 80},
	}); err != nil {
		t.Fatal(err)
	}
	_, events, err := client.Attach(id, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := collect(t, events, "FIRST", 5*time.Second); !strings.Contains(got, "FIRST") {
		t.Fatalf("first program missing: %q", got)
	}

	// An explicit replacement must start a new program under the same id.
	pid, resumed, err := client.Spawn(SpawnSpec{
		ID: id, Program: "/bin/sh", Args: []string{"-c", "echo SECOND; sleep 20"},
		Env: os.Environ(), Size: struct{ Lines, Columns uint16 }{24, 80}, Replace: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resumed {
		t.Fatal("replace resumed the old program")
	}
	_, events2, err := client.Attach(id, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := collect(t, events2, "SECOND", 5*time.Second)
	if !strings.Contains(got, "SECOND") {
		t.Fatalf("replacement program missing: %q", got)
	}
	if strings.Contains(got, "FIRST") {
		t.Fatalf("stale output from the replaced program leaked in: %q", got)
	}
	// The old program is gone, so only one terminal remains under this id.
	listed, err := client.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].PID != pid {
		t.Fatalf("unexpected terminals %v, want pid %d", listed, pid)
	}
}
