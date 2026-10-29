//go:build windows

package gosupervisor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestWindowsSupervisorChild(t *testing.T) {
	if os.Getenv("WATER_WINDOWS_SUPERVISOR_CHILD") == "" {
		return
	}
	fmt.Println("READY_SUPERVISOR")
	for {
		var line string
		if _, err := fmt.Scanln(&line); err != nil {
			break
		}
		if line == "quit" {
			fmt.Println("FINAL_SUPERVISOR")
			break
		}
		fmt.Println("GOT_SUPERVISOR:" + line)
	}
	os.Exit(0)
}
func TestWindowsSupervisorReconnectResizeExit(t *testing.T) {
	server := NewServer()
	socket := filepath.Join(t.TempDir(), "s.sock")
	if err := server.Listen(socket); err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	first, err := Dial(socket)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	id := uuid.New()
	spec := SpawnSpec{ID: id, Program: os.Args[0], Args: []string{"-test.run=^TestWindowsSupervisorChild$"}, Env: append(os.Environ(), "WATER_WINDOWS_SUPERVISOR_CHILD=1"), Size: struct{ Lines, Columns uint16 }{24, 80}}
	pid, resumed, err := first.Spawn(spec)
	if err != nil || resumed || pid <= 0 {
		t.Fatalf("spawn %d %v %v", pid, resumed, err)
	}
	_, events, err := first.Attach(id, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := collect(t, events, "READY_SUPERVISOR", 8*time.Second); !strings.Contains(got, "READY_SUPERVISOR") {
		t.Fatalf("startup %q", got)
	}
	if err := first.Write(id, []byte("before\r")); err != nil {
		t.Fatal(err)
	}
	if got := collect(t, events, "GOT_SUPERVISOR:before", 8*time.Second); !strings.Contains(got, "GOT_SUPERVISOR:before") {
		t.Fatalf("input %q", got)
	}
	first.Close()
	second, err := Dial(socket)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	pid2, resumed, err := second.Spawn(spec)
	if err != nil || !resumed || pid2 != pid {
		t.Fatalf("resumed %d %v %v", pid2, resumed, err)
	}
	_, events, err = second.Attach(id, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := collect(t, events, "GOT_SUPERVISOR:before", 8*time.Second); !strings.Contains(got, "GOT_SUPERVISOR:before") {
		t.Fatalf("replay %q", got)
	}
	if err := second.Resize(id, 32, 100, 0, 0); err != nil {
		t.Fatal(err)
	}
	second.Write(id, []byte("after\rquit\r"))
	var out strings.Builder
	sawResize := false
	deadline := time.After(8 * time.Second)
	for {
		select {
		case ev := <-events:
			switch ev.Kind {
			case EventResize:
				sawResize = true
			case EventOutput:
				out.Write(ev.Output)
			case EventExit:
				if !sawResize || !strings.Contains(out.String(), "GOT_SUPERVISOR:after") || !strings.Contains(out.String(), "FINAL_SUPERVISOR") {
					t.Fatalf("unordered/incomplete stream resize=%v output=%q", sawResize, out.String())
				}
				return
			}
		case <-deadline:
			t.Fatalf("no exit; output=%q", out.String())
		}
	}
}
