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

func TestFitFrameToVisibleHeight(t *testing.T) {
	tall := screenFrame{x: 100, y: 400, width: 900, height: 1200}
	visible := screenFrame{x: 0, y: 40, width: 1512, height: 900}
	got, ok := fitFrameToVisibleHeight(tall, visible)
	if !ok || got.height != 900 || got.y != 40 || got.x != 100 || got.width != 900 {
		t.Fatalf("tall window = %+v ok=%v", got, ok)
	}
	low := screenFrame{x: 10, y: 0, width: 800, height: 700}
	got, ok = fitFrameToVisibleHeight(low, visible)
	if !ok || got.y != 40 || got.height != 700 {
		t.Fatalf("low window = %+v ok=%v", got, ok)
	}
	high := screenFrame{x: 10, y: 300, width: 800, height: 700}
	got, ok = fitFrameToVisibleHeight(high, visible)
	if !ok || got.y != 240 || got.height != 700 {
		t.Fatalf("high window = %+v ok=%v", got, ok)
	}
	if _, ok = fitFrameToVisibleHeight(screenFrame{x: 10, y: 80, width: 800, height: 700}, visible); ok {
		t.Fatal("fitting window was changed")
	}
	if _, ok = fitFrameToVisibleHeight(tall, screenFrame{}); ok {
		t.Fatal("unknown screen was changed")
	}
}
