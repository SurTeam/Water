package goui

import (
	"testing"
	"time"
)

func TestPublicationPeriodNeverExceedsSixtyPerSecond(t *testing.T) {
	for _, frame := range []time.Duration{0, time.Second / 144, time.Second / 120, time.Second / 60} {
		if got := publicationPeriod(frame); got != time.Second/60 {
			t.Fatalf("frame=%v period=%v, want 60Hz cap", frame, got)
		}
	}
	// A slow display must not be queued a frame it can never show.
	if got := publicationPeriod(time.Second / 30); got != time.Second/30 {
		t.Fatalf("30Hz period=%v", got)
	}
}

func TestEchoWaitBudgetFollowsFrameAndInclusion(t *testing.T) {
	s := time.Second
	for _, tc := range []struct {
		name     string
		period   time.Duration
		latency  time.Duration
		expected time.Duration
	}{
		{"unmeasured 60Hz", s / 60, 0, 0},
		{"unmeasured 120Hz", s / 120, 0, 0},
		// Budget is P/6 minus the measured rebuild/snapshot overhead.
		{"60Hz 1.3ms", s / 60, 1300 * time.Microsecond, s/60/6 - echoWaitOverhead},
		{"60Hz measured 2.85ms", s / 60, 2850 * time.Microsecond, s/60/6 - echoWaitOverhead},
		{"30Hz 1.0ms", s / 30, time.Millisecond, 2 * time.Millisecond},
		// 120Hz cannot cover a 2.8ms echo inside its frame budget.
		{"120Hz 2.8ms", s / 120, 2800 * time.Microsecond, 0},
		{"144Hz 1.0ms", s / 144, time.Millisecond, s/144/6 - echoWaitOverhead},
		{"60Hz beyond frame budget", s / 60, 4 * time.Millisecond, 0},
	} {
		if got := echoWaitBudget(tc.period, tc.latency); got != tc.expected {
			t.Fatalf("%s: budget=%v, want %v", tc.name, got, tc.expected)
		}
	}
}

func TestEchoBypassTokenBucketBoundsPerSecond(t *testing.T) {
	var bucket tokenBucket
	start := time.Unix(1, 0)
	if !bucket.take(start) {
		t.Fatal("initial burst token was refused")
	}
	if bucket.take(start.Add(time.Millisecond)) {
		t.Fatal("bucket granted a second bypass without refill")
	}
	if !bucket.take(start.Add(17 * time.Millisecond)) {
		t.Fatal("bypass was not refilled")
	}
	// Sustained rate over one second after the initial burst token.
	allowed := 0
	for i := 0; i < 120; i++ {
		if bucket.take(start.Add(17*time.Millisecond + time.Duration(i)*time.Second/120)) {
			allowed++
		}
	}
	if allowed > 60 {
		t.Fatalf("token bucket allowed %d bypasses in one second", allowed)
	}
}

func TestPresentMissDisablesEchoWait(t *testing.T) {
	w := &EbitengineWindow{}
	start := time.Unix(1, 0)
	w.notePresentedFrame(start)
	w.notePresentedFrame(start.Add(time.Second / 60))
	if w.presentMisses.Load() != 0 {
		t.Fatal("healthy frame counted as a miss")
	}
	w.notePresentedFrame(start.Add(time.Second / 60).Add(time.Second / 30))
	if w.presentMisses.Load() != 1 {
		t.Fatalf("dropped frame not counted: %d", w.presentMisses.Load())
	}
	// An idle gap is not a dropped frame.
	w.notePresentedFrame(start.Add(time.Second))
	if w.presentMisses.Load() != 1 {
		t.Fatal("idle gap counted as a miss")
	}
	if interval := w.frameInterval(); interval < time.Second/240 || interval > time.Second/30 {
		t.Fatalf("frame interval out of range: %v", interval)
	}
	// A frame that waited for its echo and still missed the presentation counts
	// against the wait; enough of them disable it. Each round pins the measured
	// period to 60Hz so a dropped frame stays detectable.
	now := start.Add(2 * time.Second)
	for i := 0; i < echoWaitMissLimit; i++ {
		w.frameIntervalNanos.Store(int64(time.Second / 60))
		w.lastDrawAt = now
		now = now.Add(time.Second / 30)
		w.echoWaitArmed = true
		w.notePresentedFrame(now)
	}
	if w.echoWaitDisabledUntil.IsZero() {
		t.Fatal("repeated present misses did not disable the echo wait")
	}
}

func TestEchoInputPathDoesNotAllocate(t *testing.T) {
	term := &terminalClient{echoSignal: make(chan struct{}, 1)}
	c := &WorkspaceClient{}
	now := time.Now()
	allocs := testing.AllocsPerRun(200, func() {
		term.noteInputEcho(now)
		_ = term.echoBypass(now)
		term.consumeEchoCredit()
		c.echoWaitTerm.Store(term)
		c.echoWaitTerm.Swap(nil)
	})
	if allocs != 0 {
		t.Fatalf("input echo path allocated %.2f objects per key", allocs)
	}
}

func TestEchoWaitOnlyRunsOnFocusedVisibleIdleWindow(t *testing.T) {
	for _, tc := range []struct {
		focused, visible, catchingUp, closing bool
		want                                  bool
	}{
		{true, true, false, false, true},
		{false, true, false, false, false},
		{true, false, false, false, false},
		{true, true, true, false, false},
		{true, true, false, true, false},
	} {
		if got := echoWaitAllowed(tc.focused, tc.visible, tc.catchingUp, tc.closing); got != tc.want {
			t.Fatalf("focused=%v visible=%v catchingUp=%v closing=%v: %v", tc.focused, tc.visible, tc.catchingUp, tc.closing, got)
		}
	}
}
