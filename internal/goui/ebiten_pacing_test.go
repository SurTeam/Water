package goui

import (
	"testing"
	"time"

	"github.com/hajimehoshi/ebiten/v2"
)

func TestOutputPublicationKeepsFirstAndLatestViewBounded(t *testing.T) {
	now := time.Unix(1, 0)
	if outputPublicationDelay(time.Time{}, now) != 0 {
		t.Fatal("first output was delayed")
	}
	interval := time.Second / 30
	for _, elapsed := range []time.Duration{0, time.Millisecond, interval - 1} {
		if got := outputPublicationDelay(now, now.Add(elapsed)); got != interval-elapsed {
			t.Fatalf("publication before deadline: elapsed=%v delay=%v", elapsed, got)
		}
	}
	for _, elapsed := range []time.Duration{interval, time.Second} {
		if outputPublicationDelay(now, now.Add(elapsed)) != 0 {
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
