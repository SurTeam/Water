package goui

import (
	"time"

	"github.com/hajimehoshi/ebiten/v2"
)

// Active input/output follows display presentation rather than two free-running
// 60Hz clocks. Idle/occluded windows sleep and retain their last rendered frame.
func (w *EbitengineWindow) updateFramePacing() {
	visible := !ebiten.IsWindowMinimized()
	if w.platform != nil {
		state := w.platform.Snapshot()
		visible = visible && state["hidden"] != true && state["occluded"] != true
	}
	active := w.continuousInput.Load() || len(w.queue) > 0 || time.Since(time.Unix(0, w.frameActivity.Load())) < 120*time.Millisecond
	mode := ebiten.FPSModeVsyncOffMinimum
	if visible && active {
		mode = ebiten.FPSModeVsyncOn
	}
	if ebiten.FPSMode() != mode {
		ebiten.SetFPSMode(mode)
	}
}

type nativeKeyRepeater struct{ next map[ebiten.Key]time.Time }

// Physical navigation/control keys repeat by elapsed time, independent of a
// 60Hz/120Hz display. Never replay missed repeats as a burst after a slow frame.
func (r *nativeKeyRepeater) ready(k ebiten.Key, pressed bool, now time.Time) bool {
	if !pressed {
		delete(r.next, k)
		return false
	}
	if r.next == nil {
		r.next = make(map[ebiten.Key]time.Time)
	}
	next, held := r.next[k]
	if !held {
		r.next[k] = now.Add(350 * time.Millisecond)
		return true
	}
	if now.Before(next) {
		return false
	}
	r.next[k] = now.Add(50 * time.Millisecond)
	return true
}
