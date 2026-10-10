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
	visible := !ebiten.IsWindowMinimized()
	if w.platform != nil {
		state := w.platform.Snapshot()
		visible = visible && state["hidden"] != true && state["occluded"] != true
	}
	active := w.continuousInput.Load() || len(w.queue) > 0 || time.Since(time.Unix(0, w.frameActivity.Load())) < 120*time.Millisecond
	if w.continuousInput.Load() {
		countNativeWork("count.continuous_input", 1)
	}
	mode := ebiten.FPSModeVsyncOffMinimum
	if visible && active {
		mode = ebiten.FPSModeVsyncOn
	}
	if ebiten.FPSMode() != mode {
		ebiten.SetFPSMode(mode)
	}
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
