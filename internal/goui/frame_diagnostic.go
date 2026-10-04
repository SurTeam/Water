//go:build !water_cpu_diagnostic

package goui

func traceNativeWork(name string) func() { return nil }
