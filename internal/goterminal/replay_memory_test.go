package goterminal

import (
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"testing"
)

// Use the same two-GC sequence for live and cleared samples: heap profile
// accounting can lag collection. Optional profiles belong to isolated tests.
func replayMemorySample(t *testing.T, r *Registry, name string) uint64 {
	t.Helper()
	runtime.GC()
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	count, retained := r.ExitedRetention()
	t.Logf("%s HeapAlloc=%d HeapInuse=%d active=%d exited=%d exitedBytes=%d replayBytes=%d goroutines=%d", name, m.HeapAlloc, m.HeapInuse, r.Count(), count, retained, r.RetainedReplayBytes(), runtime.NumGoroutine())
	if dir := os.Getenv("WATER_TEST_MEMORY_PROFILE_DIR"); dir != "" {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		file, err := os.Create(filepath.Join(dir, name+".pprof"))
		if err != nil {
			t.Fatal(err)
		}
		err = pprof.WriteHeapProfile(file)
		closeErr := file.Close()
		if err != nil {
			t.Fatal(err)
		}
		if closeErr != nil {
			t.Fatal(closeErr)
		}
	}
	runtime.KeepAlive(r)
	return m.HeapAlloc
}

func TestCapEscapeBytesAllocationBound(t *testing.T) {
	data := make([]byte, 262143)
	for i := 0; i < len(data); i += 3 {
		copy(data[i:], "\x1b[H")
	}
	if allocs := testing.AllocsPerRun(100, func() { capBenchmarkResult = capEscapeBytes(data, len(data)) }); allocs != 0 {
		t.Fatalf("complete in-limit input allocated %g times", allocs)
	}
	if allocs := testing.AllocsPerRun(100, func() { capBenchmarkResult = capEscapeBytes(data, 32768) }); allocs > 1 {
		t.Fatalf("capped input allocated %g times", allocs)
	}
	capBenchmarkResult = nil
}
