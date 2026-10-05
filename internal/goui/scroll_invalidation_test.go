package goui

import "testing"

func TestHistoryScrollWakesFrameWithoutExtendingInputActivity(t *testing.T) {
	c, term := attachedScrollTestTerminal(t)
	window := &EbitengineWindow{frameWake: make(chan struct{}, 1)}
	c.native.Store(window)
	c.invalidate = window.Invalidate
	term.input.OnScroll(-1)
	if window.frameRevision.Load() == 0 || len(window.frameWake) != 1 {
		t.Fatal("scroll did not immediately wake a changed frame")
	}
	if window.frameActivity.Load() != 0 {
		t.Fatal("scroll requested continued idle input frames")
	}
	before := window.frameRevision.Load()
	term.input.OnSelectionStart(0, 0, 1)
	term.input.OnSelectionAutoScroll(0, 3, -1)
	if window.frameRevision.Load() <= before {
		t.Fatal("selection autoscroll stopped waking changed frames")
	}
}
