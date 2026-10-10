package goui

import (
	"time"

	"github.com/hajimehoshi/ebiten/v2"
)

// A fast byte stream does not require presenting every intermediate screen.
// Keep this clock independent of display-rate pointer/keyboard interaction.
func outputPublicationDelay(published, now time.Time) time.Duration {
	if published.IsZero() {
		return 0
	}
	return max(0, time.Second/30-now.Sub(published))
}

// Active input/output follows display presentation rather than two free-running
// 60Hz clocks. Idle/occluded windows sleep and retain their last rendered frame.
func (w *EbitengineWindow) updateFramePacing() {
	visible := windowChromeVisible(w)
	w.revealHold = nextRevealHold(w.revealHold, w.lastPacingVisible, visible)
	w.lastPacingVisible = visible
	active := w.continuousInput.Load() || len(w.queue) > 0 || time.Since(time.Unix(0, w.frameActivity.Load())) < 120*time.Millisecond
	if w.continuousInput.Load() {
		countNativeWork("count.continuous_input", 1)
	}
	// Enabling vsync in the same frame the window becomes visible makes Metal
	// wait on a display link that cannot produce a drawable until this thread
	// returns to the run loop. Keep event-paced frames until the reveal settles.
	mode := ebiten.FPSModeVsyncOffMinimum
	if visible && w.revealHold == 0 && active && !w.catchingUp {
		mode = ebiten.FPSModeVsyncOn
	}
	if ebiten.FPSMode() != mode {
		ebiten.SetFPSMode(mode)
	}
}

func windowChromeVisible(w *EbitengineWindow) bool {
	visible := !ebiten.IsWindowMinimized()
	if w.platform != nil {
		state := w.platform.Snapshot()
		visible = visible && state["hidden"] != true && state["occluded"] != true
	}
	return visible
}

// nextRevealHold counts down frames after the window becomes visible again.
// A positive value keeps presentation off the display link.
func nextRevealHold(hold int, wasVisible, visible bool) int {
	if !visible {
		return hold
	}
	if !wasVisible {
		return 8
	}
	if hold > 0 {
		return hold - 1
	}
	return 0
}

type nativeKeyRepeater struct {
	next     map[ebiten.Key]time.Time
	consumed map[ebiten.Key]bool
	initial  time.Duration
	repeat   time.Duration
}

func (r *nativeKeyRepeater) useSystemRepeat(initial, interval time.Duration) {
	if r.initial == 0 && initial > 0 {
		r.initial = initial
	}
	if r.repeat == 0 && interval > 0 {
		r.repeat = interval
	}
}

// Observe releases even on ticks owned by the IME. A consumed press must not
// become a new physical press on the following tick while it is still held.
func (r *nativeKeyRepeater) observe(pressed func(ebiten.Key) bool, consume bool) {
	for k := ebiten.Key(0); k <= ebiten.KeyMax; k++ {
		if !pressed(k) {
			delete(r.next, k)
			delete(r.consumed, k)
		} else if consume {
			if r.consumed == nil {
				r.consumed = make(map[ebiten.Key]bool)
			}
			r.consumed[k] = true
			delete(r.next, k)
		}
	}
}

// Physical navigation/control keys repeat by elapsed time, independent of a
// 60Hz/120Hz display. Never replay missed repeats as a burst after a slow frame.
func (r *nativeKeyRepeater) ready(k ebiten.Key, pressed bool, now time.Time) bool {
	if !pressed {
		delete(r.next, k)
		delete(r.consumed, k)
		return false
	}
	if r.consumed[k] {
		return false
	}
	if r.next == nil {
		r.next = make(map[ebiten.Key]time.Time)
	}
	next, held := r.next[k]
	initial, interval := 350*time.Millisecond, 50*time.Millisecond
	if r.initial > 0 {
		initial = r.initial
	}
	if r.repeat > 0 {
		interval = r.repeat
	}
	if !held {
		r.next[k] = now.Add(initial)
		return true
	}
	if now.Before(next) {
		return false
	}
	r.next[k] = now.Add(interval)
	return true
}
