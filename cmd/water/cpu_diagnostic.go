//go:build water_cpu_diagnostic

package main

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"runtime"

	"github.com/hajimehoshi/ebiten/v2"
)

// Local-only instrumentation for profiling an isolated native GUI. This file
// is excluded from ordinary builds. Bind explicitly to loopback for profiling.
func init() {
	address := os.Getenv("WATER_DIAGNOSTIC_PPROF")
	if address == "" || isControlInvocation(os.Args[1:]) {
		return
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		panic(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/goroutine", pprof.Handler("goroutine").ServeHTTP)
	mux.HandleFunc("/debug/pprof/allocs", func(w http.ResponseWriter, r *http.Request) {
		// Allocation records can lag two GC cycles. Collect outside the timed
		// CPU window so startup font allocations don't leak into its delta.
		runtime.GC()
		runtime.GC()
		pprof.Handler("allocs").ServeHTTP(w, r)
	})
	mux.HandleFunc("/debug/memory", func(w http.ResponseWriter, r *http.Request) {
		var memory runtime.MemStats
		runtime.ReadMemStats(&memory)
		var graphics ebiten.DebugInfo
		ebiten.ReadDebugInfo(&graphics)
		json.NewEncoder(w).Encode(map[string]any{
			"heap_alloc": memory.HeapAlloc, "heap_inuse": memory.HeapInuse,
			"heap_sys": memory.HeapSys, "heap_released": memory.HeapReleased,
			"num_gc": memory.NumGC, "total_alloc": memory.TotalAlloc,
			"gpu_image_bytes": graphics.TotalGPUImageMemoryUsageInBytes,
		})
	})
	go http.Serve(listener, mux)
}
