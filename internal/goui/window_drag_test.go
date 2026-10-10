package goui

import "testing"

func TestTopDragTargetPrefersLaterHits(t *testing.T) {
	targets := []dragTarget{
		{kind: hitTitlebar, minX: 0, minY: 0, maxX: 800, maxY: 40},
		{kind: hitWindowClose, minX: 8, minY: 0, maxX: 28, maxY: 28},
		{kind: hitTitlebar, minX: 0, minY: 40, maxX: 220, maxY: 600},
		{kind: hitWorkspace, minX: 12, minY: 80, maxX: 200, maxY: 120},
	}
	if got := topDragTarget(targets, 100, 10); got != hitTitlebar {
		t.Fatalf("empty titlebar = %v", got)
	}
	if got := topDragTarget(targets, 12, 10); got != hitWindowClose {
		t.Fatalf("traffic light = %v", got)
	}
	if got := topDragTarget(targets, 40, 200); got != hitTitlebar {
		t.Fatalf("sidebar blank = %v", got)
	}
	if got := topDragTarget(targets, 40, 90); got != hitWorkspace {
		t.Fatalf("workspace row = %v", got)
	}
	if got := topDragTarget(targets, 400, 200); got != 0 {
		t.Fatalf("terminal = %v", got)
	}
}

func TestWindowDragNeedsFramesSkipsTitlebar(t *testing.T) {
	if windowDragNeedsFrames(nil) {
		t.Fatal("nil drag requests frames")
	}
	if windowDragNeedsFrames(&nativeDrag{kind: hitTitlebar}) {
		t.Fatal("titlebar movement should not redraw every tick")
	}
	if !windowDragNeedsFrames(&nativeDrag{edges: 1}) {
		t.Fatal("border resize must keep redrawing")
	}
	if !windowDragNeedsFrames(&nativeDrag{kind: hitPane}) {
		t.Fatal("selection drag must keep redrawing")
	}
}

func TestRevealHoldSuppressesVsyncAfterShow(t *testing.T) {
	if got := nextRevealHold(0, false, true); got != 8 {
		t.Fatalf("show = %d", got)
	}
	hold := 8
	for i := 0; i < 8; i++ {
		hold = nextRevealHold(hold, true, true)
	}
	if hold != 0 {
		t.Fatalf("settled = %d", hold)
	}
	if got := nextRevealHold(3, true, false); got != 3 {
		t.Fatalf("hidden should not count down: %d", got)
	}
	if got := nextRevealHold(1, false, true); got != 8 {
		t.Fatalf("show again = %d", got)
	}
}
