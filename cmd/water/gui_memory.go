package main

import (
	"os"
	"runtime/debug"
	"runtime/metrics"
	"time"
)

// Leave room for GPU textures and native window/driver allocations in the GUI's
// roughly 300 MB target. This is a soft Go-runtime budget, not an RSS ceiling;
// large live fonts or many panes can exceed it. Respect an explicit user budget.
func configureGUIMemory() func() {
	if os.Getenv("GOMEMLIMIT") != "" {
		return func() {}
	}
	debug.SetMemoryLimit(guiMemoryBudget(0))
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		samples := []metrics.Sample{{Name: "/gc/heap/live:bytes"}}
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				metrics.Read(samples)
				debug.SetMemoryLimit(guiMemoryBudget(samples[0].Value.Uint64()))
			}
		}
	}()
	return func() { close(done) }
}

// A limit below the live fonts/history makes every allocation assist GC and
// can pause input for hundreds of milliseconds. Preserve 64 MiB of headroom;
// never try to meet the process target by repeatedly collecting live data.
func guiMemoryBudget(live uint64) int64 {
	const headroom = 64 << 20
	const maxLimit = uint64(1<<63 - 1)
	if live > maxLimit-headroom {
		return int64(maxLimit)
	}
	return max(192<<20, int64(live+headroom))
}
