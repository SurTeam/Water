package goterminal

import (
	"bytes"
	"runtime"
	"testing"
	"time"

	"github.com/SurTeam/Water/internal/goprotocol"
	"github.com/google/uuid"
)

// Populate real replay entries without allocating PTYs for the cache policy test.
func cacheExitedReplay(r *Registry, data []byte) uuid.UUID {
	t := &Terminal{ID: uuid.New(), replayLimit: defaultReplayBytes}
	r.terms[t.ID] = t
	t.publish(goprotocol.TerminalEvent{Kind: goprotocol.OutputEvent, Data: data})
	code := int32(0)
	t.publish(goprotocol.TerminalEvent{Kind: goprotocol.ExitEvent, Code: &code})
	r.retire(t.ID)
	return t.ID
}

func TestExitedReplayCacheByteLimit(t *testing.T) {
	r := NewRegistry()
	var first, last uuid.UUID
	for i := 0; i < 12; i++ {
		last = cacheExitedReplay(r, bytes.Repeat([]byte{'x'}, 2*1024*1024))
		if i == 0 {
			first = last
		}
		if n := r.RetainedReplayBytes(); n > exitedReplayLimit {
			t.Fatalf("exit %d retained %d bytes, limit %d", i, n, exitedReplayLimit)
		}
	}
	if _, ok := r.Get(first); ok {
		t.Fatal("old replay was not evicted by byte budget")
	}
	term, ok := r.Get(last)
	if !ok {
		t.Fatal("most recent exit is unavailable")
	}
	replay := term.Replay()
	if len(replay) != 2 || len(replay[0].Data) != 2*1024*1024 || replay[1].Kind != goprotocol.ExitEvent {
		t.Fatal("recent replay lost final output or Exit")
	}
	if count, _ := r.ExitedRetention(); count != 3 || r.Count() != 0 {
		t.Fatalf("cached=%d active=%d, want 3 and 0", count, r.Count())
	}
}

func TestExitedReplayHeapPlateaus(t *testing.T) {
	r := NewRegistry()
	defer r.CloseAll()
	heap := func() uint64 {
		runtime.GC()
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		runtime.KeepAlive(r)
		return m.HeapAlloc
	}
	baseline := heap()
	cycle := func() {
		for i := 0; i < 12; i++ {
			cacheExitedReplay(r, bytes.Repeat([]byte{'x'}, 2*1024*1024))
		}
	}
	cycle()
	first := heap()
	cycle()
	second := heap()
	if second > first+1024*1024 {
		t.Fatalf("live heap kept growing after exit cache saturated: first=%d second=%d", first, second)
	}
	r.CloseAll()
	cleared := heap()
	if second < cleared || second-cleared < 4*1024*1024 {
		t.Fatalf("clearing exit cache did not release retained heap: retained=%d cleared=%d", second, cleared)
	}
	t.Logf("HeapAlloc after GC: baseline=%d after_12_exits=%d after_24_exits=%d cleared=%d", baseline, first, second, cleared)
}

func TestExitedReplayCacheCountLimit(t *testing.T) {
	r := NewRegistry()
	active := &Terminal{ID: uuid.New()}
	r.terms[active.ID] = active
	var ids []uuid.UUID
	for i := 0; i < exitedTerminalLimit+8; i++ {
		ids = append(ids, cacheExitedReplay(r, []byte("final")))
	}
	if count, _ := r.ExitedRetention(); count != exitedTerminalLimit {
		t.Fatalf("cached=%d, want %d", count, exitedTerminalLimit)
	}
	for i, id := range ids {
		_, ok := r.Get(id)
		if ok != (i >= 8) {
			t.Fatalf("exit %d availability=%v, FIFO eviction failed", i, ok)
		}
	}
	if got, ok := r.Get(active.ID); !ok || got != active || r.Count() != 1 {
		t.Fatal("cache eviction affected active terminal")
	}
}

func TestNaturalExitRetiresAndExplicitRemovalDoesNotResurrect(t *testing.T) {
	r := NewRegistry()
	defer r.CloseAll()
	term, err := r.Spawn("/bin/sh", []string{"-c", "printf final-output"}, goprotocol.TerminalSize{Columns: 80, Lines: 24})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		if count, _ := r.ExitedRetention(); count == 1 && r.Count() == 0 {
			break
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatal("natural exit did not retire from active registry")
		}
	}
	cached, ok := r.Get(term.ID)
	if !ok {
		t.Fatal("recent exit cannot be queried")
	}
	replay := cached.Replay()
	if len(replay) < 2 || replay[len(replay)-1].Kind != goprotocol.ExitEvent {
		t.Fatal("Exit missing from retained replay")
	}
	var output []byte
	for _, ev := range replay {
		output = append(output, ev.Data...)
	}
	if !bytes.Contains(output, []byte("final-output")) {
		t.Fatalf("lost final output: %q", output)
	}
	r.Remove(term.ID)
	r.retire(term.ID)
	if _, ok := r.Get(term.ID); ok || r.RetainedReplayBytes() != 0 {
		t.Fatal("explicitly removed exit was resurrected or retained replay")
	}

	live, err := r.Spawn("/bin/sh", []string{"-c", "read line"}, goprotocol.TerminalSize{Columns: 80, Lines: 24})
	if err != nil {
		t.Fatal(err)
	}
	r.Remove(live.ID)
	select {
	case <-live.readerDone:
	case <-deadline.C:
		t.Fatal("removed terminal reader did not close")
	}
	r.retire(live.ID)
	if _, ok := r.Get(live.ID); ok {
		t.Fatal("removed live terminal was resurrected")
	}
}

func TestCloseAllClearsExitedReplay(t *testing.T) {
	r := NewRegistry()
	id := cacheExitedReplay(r, []byte("final"))
	r.CloseAll()
	r.retire(id)
	if count, n := r.ExitedRetention(); count != 0 || n != 0 || r.RetainedReplayBytes() != 0 {
		t.Fatalf("CloseAll retained exits: count=%d bytes=%d", count, n)
	}
	if _, ok := r.Get(id); ok {
		t.Fatal("closed registry still exposes exited terminal")
	}
}
