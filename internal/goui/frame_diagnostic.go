//go:build !water_cpu_diagnostic

package goui

import "time"

func traceNativeWork(name string) func() { return nil }

func countNativeWork(name string, amount int) {}

func countNativeInvalidation() {}

func sampleNativeLatency(name string, elapsed time.Duration) {}
