//go:build water_cpu_diagnostic

package main

import (
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"runtime"
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
	go http.Serve(listener, mux)
}
