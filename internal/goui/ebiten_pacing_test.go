package goui

import (
	"testing"
	"time"

	"github.com/SurTeam/Water/internal/govt"
	"github.com/hajimehoshi/ebiten/v2"
)

func TestFrameScheduleSleepsUntilMaintenanceOrVisibleBlink(t *testing.T) {
	for _, tc := range []struct {
		phase time.Duration
		blink bool
		want  time.Duration
	}{
		{0, false, 250 * time.Millisecond},
		{599 * time.Millisecond, false, 250 * time.Millisecond},
		{0, true, 250 * time.Millisecond},
		{500 * time.Millisecond, true, 100 * time.Millisecond},
		{599 * time.Millisecond, true, time.Millisecond},
		{600 * time.Millisecond, true, 250 * time.Millisecond},
		{999 * time.Millisecond, true, time.Millisecond},
	} {
		now := time.Unix(10, int64(tc.phase))
		if got := frameScheduleDelay(now, false, tc.blink); got != tc.want {
			t.Fatalf("phase=%v blink=%v: delay=%v, want %v", tc.phase, tc.blink, got, tc.want)
		}
		if got := frameScheduleDelay(now, true, tc.blink); got != time.Second/60 {
			t.Fatalf("pending input/output lost its fast retry: %v", got)
		}
	}
}

func TestCursorVisibilityExcludesHiddenAndScrolledOffscreen(t *testing.T) {
	visible := govt.Snapshot{Cols: 80, Rows: 24, CursorX: 2, CursorY: 3}
	if !terminalCursorVisible(visible) {
		t.Fatal("visible cursor rejected")
	}
	for _, mutate := range []func(*govt.Snapshot){
		func(s *govt.Snapshot) { s.CursorHide = true },
		func(s *govt.Snapshot) { s.CursorX = -1 },
		func(s *govt.Snapshot) { s.CursorX = s.Cols },
		func(s *govt.Snapshot) { s.CursorY = -1 },
		func(s *govt.Snapshot) { s.CursorY = s.Rows },
	} {
		snap := visible
		mutate(&snap)
		if terminalCursorVisible(snap) {
			t.Fatalf("invisible cursor would drive blinking: %+v", snap)
		}
	}
}

func TestOutputPublicationKeepsFirstAndLatestViewBounded(t *testing.T) {
	now := time.Unix(1, 0)
	if outputPublicationDelay(time.Time{}, now, 0) != 0 {
		t.Fatal("first output was delayed")
	}
	interval := time.Second / 60
	for _, elapsed := range []time.Duration{0, time.Millisecond, interval - 1} {
		if got := outputPublicationDelay(now, now.Add(elapsed), 0); got != interval-elapsed {
			t.Fatalf("publication before deadline: elapsed=%v delay=%v", elapsed, got)
		}
	}
	for _, elapsed := range []time.Duration{interval, time.Second} {
		if outputPublicationDelay(now, now.Add(elapsed), 0) != 0 {
			t.Fatal("latest view stayed pending after deadline")
		}
	}
}

func TestPhysicalKeyRepeatUsesTimeWithoutCatchUpBursts(t *testing.T) {
	for _, cadence := range []time.Duration{time.Second / 60, time.Second / 120} {
		var r nativeKeyRepeater
		start := time.Unix(1, 0)
		if !r.ready(ebiten.KeyBackspace, true, start) {
			t.Fatal("first press missing")
		}
		for elapsed := cadence; elapsed < 350*time.Millisecond; elapsed += cadence {
			if r.ready(ebiten.KeyBackspace, true, start.Add(elapsed)) {
				t.Fatal("repeat delay depends on refresh rate")
			}
		}
		now := start.Add(time.Second)
		if !r.ready(ebiten.KeyBackspace, true, now) || r.ready(ebiten.KeyBackspace, true, now) {
			t.Fatal("missed repeats replayed in a burst")
		}
		if r.ready(ebiten.KeyBackspace, true, now.Add(49*time.Millisecond)) || !r.ready(ebiten.KeyBackspace, true, now.Add(50*time.Millisecond)) {
			t.Fatal("repeat interval differs")
		}
		r.ready(ebiten.KeyBackspace, false, now)
		if !r.ready(ebiten.KeyBackspace, true, now) {
			t.Fatal("new press did not reset repeat state")
		}
	}
}

func TestIMEConsumedKeysStaySuppressedUntilRelease(t *testing.T) {
	for _, k := range []ebiten.Key{ebiten.KeyEnter, ebiten.KeyBackspace, ebiten.KeyArrowDown} {
		var r nativeKeyRepeater
		now := time.Unix(1, 0)
		pressed := func(key ebiten.Key) bool { return key == k }
		r.observe(pressed, true)
		for _, elapsed := range []time.Duration{time.Second / 60, time.Second} {
			r.observe(pressed, false)
			if r.ready(k, true, now.Add(elapsed)) {
				t.Fatalf("IME consumed key %v was sent again", k)
			}
		}
		// A release can occur while composition still owns the frame.
		r.observe(func(ebiten.Key) bool { return false }, true)
		if !r.ready(k, true, now.Add(2*time.Second)) {
			t.Fatalf("fresh press %v was suppressed", k)
		}
	}
}
