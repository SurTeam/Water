package gosupervisor

import (
	"bytes"
	"testing"
	"time"

	"github.com/google/uuid"
)

// The supervisor batches PTY reads to cut event counts for continuous output.
// That window also delays the first bytes of an interactive echo, which sit
// directly on the keystroke-to-paint path. This measures write -> first output
// event for a 1-byte echo, the same quantity the GUI's input.echo_latency
// observes, without needing a display.
func TestEchoLatencyAfterInput(t *testing.T) {
	_, socket := startSupervisor(t)
	client, err := Dial(socket)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	id := uuid.New()
	// A plain shell leaves the tty echo enabled, so a single byte comes back
	// from the kernel without any child-process buffering.
	spawnShell(t, client, id)
	_, events, err := client.Attach(id, 0)
	if err != nil {
		t.Fatal(err)
	}
	read := func(timeout time.Duration) []byte {
		t.Helper()
		deadline := time.After(timeout)
		for {
			select {
			case ev := <-events:
				if len(ev.Output) > 0 {
					return ev.Output
				}
			case <-deadline:
				t.Fatal("no output")
			}
		}
	}
	// Warm up the supervisor and the child process.
	for i := 0; i < 3; i++ {
		if err := client.Write(id, []byte("w")); err != nil {
			t.Fatal(err)
		}
		read(2 * time.Second)
		time.Sleep(30 * time.Millisecond)
	}
	var total time.Duration
	const samples = 20
	for i := 0; i < samples; i++ {
		time.Sleep(50 * time.Millisecond) // quiet gap, like typing
		start := time.Now()
		if err := client.Write(id, []byte("a")); err != nil {
			t.Fatal(err)
		}
		got := read(2 * time.Second)
		total += time.Since(start)
		if !bytes.Contains(got, []byte("a")) {
			t.Fatalf("unexpected echo: %q", got)
		}
	}
	mean := total / samples
	t.Logf("write->first output mean=%v", mean)
	// The interactive echo must not pay the 1ms coalescing window; 1.5ms is
	// the GUI's approved inclusion budget (budget+delta at 60Hz).
	if mean > 1500*time.Microsecond {
		t.Fatalf("echo latency %v exceeds the interactive bound", mean)
	}
}
