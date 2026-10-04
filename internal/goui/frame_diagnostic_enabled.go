//go:build water_cpu_diagnostic

package goui

import (
	"context"
	"runtime"
	"runtime/trace"
	"sort"
	"strconv"
	"sync"
	"time"
)

var nativeTiming = struct {
	sync.Mutex
	values map[string]*nativeWorkSamples
}{values: make(map[string]*nativeWorkSamples)}

type nativeWorkSamples struct {
	samples     [1024]time.Duration
	intervals   [1024]time.Duration
	lastStarted time.Time
	maxInterval time.Duration
	count       int
	maximum     time.Duration
	total       time.Duration
}

func traceNativeWork(name string) func() {
	started := time.Now()
	region := trace.StartRegion(context.Background(), name)
	return func() {
		region.End()
		elapsed := time.Since(started)
		nativeTiming.Lock()
		values := nativeTiming.values[name]
		if values == nil {
			values = &nativeWorkSamples{}
			nativeTiming.values[name] = values
		}
		values.samples[values.count%len(values.samples)] = elapsed
		if !values.lastStarted.IsZero() {
			interval := started.Sub(values.lastStarted)
			values.intervals[values.count%len(values.intervals)] = interval
			values.maxInterval = max(values.maxInterval, interval)
		}
		values.lastStarted = started
		values.count++
		values.maximum = max(values.maximum, elapsed)
		values.total += elapsed
		nativeTiming.Unlock()
	}
}

func countNativeWork(name string, amount int) {
	nativeTiming.Lock()
	defer nativeTiming.Unlock()
	values := nativeTiming.values[name]
	if values == nil {
		values = &nativeWorkSamples{}
		nativeTiming.values[name] = values
	}
	values.count += amount
}

func countNativeInvalidation() {
	var pcs [4]uintptr
	n := runtime.Callers(2, pcs[:])
	frames := runtime.CallersFrames(pcs[:n])
	name := "invalidate"
	for {
		frame, more := frames.Next()
		name += "." + frame.Function + ":" + strconv.Itoa(frame.Line)
		if !more {
			break
		}
	}
	countNativeWork(name, 1)
}

// Diagnostic builds only: bounded timing counters, with an explicit reset after
// startup. Normal builds retain neither counters nor frame-timing overhead.
func NativeWorkTimings(reset bool) map[string]any {
	nativeTiming.Lock()
	defer nativeTiming.Unlock()
	result := make(map[string]any, len(nativeTiming.values))
	for name, value := range nativeTiming.values {
		values := append([]time.Duration(nil), value.samples[:min(value.count, len(value.samples))]...)
		sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
		if len(values) > 0 {
			result[name] = map[string]any{"samples": value.count, "total_ms": float64(value.total) / float64(time.Millisecond), "p99_ms": float64(values[min(len(values)-1, len(values)*99/100)]) / float64(time.Millisecond), "max_ms": float64(value.maximum) / float64(time.Millisecond)}
			intervals := append([]time.Duration(nil), value.intervals[:min(value.count, len(value.intervals))]...)
			sort.Slice(intervals, func(i, j int) bool { return intervals[i] < intervals[j] })
			result[name].(map[string]any)["gap_p99_ms"] = float64(intervals[min(len(intervals)-1, len(intervals)*99/100)]) / float64(time.Millisecond)
			result[name].(map[string]any)["gap_max_ms"] = float64(value.maxInterval) / float64(time.Millisecond)
		}
	}
	if reset {
		clear(nativeTiming.values)
	}
	return result
}
