package goui

import "math"

// dragTarget is one hit region in window points, origin at the top left.
// Later entries win, matching the framebuffer hit test.
type dragTarget struct {
	kind                   automationHitKind
	minX, minY, maxX, maxY float64
}

type dragTargetList struct {
	targets []dragTarget
}

func (w *EbitengineWindow) publishDragTargets(c *WorkspaceClient) {
	if c == nil {
		return
	}
	scale := w.scale
	if scale <= 0 {
		scale = 1
	}
	targets := make([]dragTarget, len(c.hitRegions))
	for i, hit := range c.hitRegions {
		targets[i] = dragTarget{
			kind: hit.Kind,
			minX: float64(hit.Rect.Min.X) / scale,
			minY: float64(hit.Rect.Min.Y) / scale,
			maxX: float64(hit.Rect.Max.X) / scale,
			maxY: float64(hit.Rect.Max.Y) / scale,
		}
	}
	w.dragTargets.Store(&dragTargetList{targets: targets})
}

func topDragTarget(targets []dragTarget, x, y float64) automationHitKind {
	for i := len(targets) - 1; i >= 0; i-- {
		t := targets[i]
		if x >= t.minX && x < t.maxX && y >= t.minY && y < t.maxY {
			return t.kind
		}
	}
	return 0
}

// Window movement only changes the frame origin. Selection, splits and border
// resizing still need per-tick redraws.
func windowDragNeedsFrames(d *nativeDrag) bool {
	return d != nil && d.kind != hitTitlebar
}

func (w *EbitengineWindow) consumeTitlebarZoom() bool {
	return w != nil && w.titlebarZoom.CompareAndSwap(true, false)
}

// screenFrame is an AppKit-style rectangle: origin at the bottom left, in points.
type screenFrame struct {
	x, y, width, height float64
}

// fitFrameToVisibleHeight shrinks a window that is taller than the screen it
// is on and shifts it so the vertical span stays inside that screen. Width and
// horizontal position are left untouched. A non-positive visible height means
// the screen metrics are not usable yet.
func fitFrameToVisibleHeight(frame, visible screenFrame) (screenFrame, bool) {
	if visible.height <= 0 {
		return frame, false
	}
	height := frame.height
	y := frame.y
	if height > visible.height {
		height = visible.height
	}
	if y < visible.y {
		y = visible.y
	}
	if y+height > visible.y+visible.height {
		y = visible.y + visible.height - height
	}
	const eps = 0.5
	if math.Abs(y-frame.y) < eps && math.Abs(height-frame.height) < eps {
		return frame, false
	}
	frame.y = y
	frame.height = height
	return frame, true
}
