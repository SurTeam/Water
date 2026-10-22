package gosupervisor

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestOutputThroughputKeepsUpWithFastProducer guards the hot path: a program that
// floods the PTY must not lose bytes or fall behind through the supervisor.
func TestOutputThroughputKeepsUpWithFastProducer(t *testing.T) {
	_, socket := startSupervisor(t)
	id := uuid.New()
	client, err := Dial(socket)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	const lines = 20000
	if _, _, err := client.Spawn(SpawnSpec{
		ID: id, Program: "/bin/sh",
		Args: []string{"-c", fmt.Sprintf("i=0; while [ $i -lt %d ]; do echo 0123456789abcdef0123456789abcdef-$i; i=$((i+1)); done; sleep 30", lines)},
		Env:  os.Environ(), Size: struct{ Lines, Columns uint16 }{24, 80},
	}); err != nil {
		t.Fatal(err)
	}
	attach, events, err := client.Attach(id, 0)
	if err != nil {
		t.Fatal(err)
	}
	_ = attach

	start := time.Now()
	seen := 0
	bytes := 0
	want := fmt.Sprintf("0123456789abcdef0123456789abcdef-%d", lines-1)
	deadline := time.After(20 * time.Second)
	var tail []byte
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				t.Fatalf("stream closed after %d lines / %d bytes", seen, bytes)
			}
			if ev.Kind == EventOutput {
				bytes += len(ev.Output)
				tail = append(tail, ev.Output...)
				if len(tail) > 4096 {
					tail = tail[len(tail)-4096:]
				}
				seen++
				if string(tail) != "" && contains(tail, want) {
					elapsed := time.Since(start)
					t.Logf("%d bytes in %d events, %s, %.1f MB/s", bytes, seen, elapsed, float64(bytes)/elapsed.Seconds()/1e6)
					return
				}
			}
		case <-deadline:
			t.Fatalf("timed out after %d events / %d bytes", seen, bytes)
		}
	}
}

func contains(haystack []byte, needle string) bool {
	return len(needle) == 0 || indexOf(string(haystack), needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
