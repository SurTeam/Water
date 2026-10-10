package goui

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
