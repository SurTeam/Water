package goterminal

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/SurTeam/Water/internal/goprotocol"
)

func TestDarwinArgsPreserveBoundariesAndExcludeEnvironment(t *testing.T) {
	raw := make([]byte, 4)
	binary.NativeEndian.PutUint32(raw, 4)
	raw = append(raw, []byte("/usr/local/bin/agent\x00\x00\x00agent alias\x00argument with spaces\x00\x00--flag=value\x00SECRET=not-an-argument\x00")...)
	args, err := parseDarwinProcessArgs(raw)
	want := []string{"agent alias", "argument with spaces", "", "--flag=value"}
	if err != nil || !reflect.DeepEqual(args, want) {
		t.Fatalf("argv boundaries lost: %v %q", err, args)
	}
	for _, malformed := range [][]byte{nil, {1, 0, 0, 0}, raw[:len(raw)-len("SECRET=not-an-argument\x00")-1]} {
		if _, err := parseDarwinProcessArgs(malformed); err == nil {
			t.Fatal("truncated argv accepted")
		}
	}
}

func TestDarwinForegroundSamplingBypassesExternalPS(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ps"), []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	program := filepath.Join(dir, "water own agent")
	if err := os.Symlink("/bin/sleep", program); err != nil {
		t.Fatal(err)
	}
	r := NewRegistry()
	defer r.CloseAll()
	term, err := r.Spawn(program, []string{"30"}, goprotocol.TerminalSize{Columns: 80, Lines: 24})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		process, err := r.ForegroundProcess(context.Background(), term.ID)
		if err == nil && process.Name == "water own agent" {
			if process.PID <= 1 || process.PID != process.PGID || process.Started == "" || !reflect.DeepEqual(process.Args, []string{program, "30"}) {
				t.Fatalf("identity or argv lost: %+v", process)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("native sample failed: %v %+v", err, process)
		}
		time.Sleep(time.Millisecond)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.ForegroundProcess(ctx, term.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation ignored: %v", err)
	}
}

func TestDarwinDisappearedForegroundGroupIsNotQueryFailure(t *testing.T) {
	// The PID range is bounded to signed 32-bit; the highest value cannot be live.
	processes, err := sampleProcessGroups(context.Background(), []int{2147483647})
	if err != nil || len(processes) != 0 {
		t.Fatalf("vanished leader failed complete sample: %v %+v", err, processes)
	}
}
