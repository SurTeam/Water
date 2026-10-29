package goui

import (
	"image"
	"testing"

	"github.com/SurTeam/Water/internal/goconfig"
)

func scrollTestWindow() (*EbitengineWindow, *WorkspaceClient) {
	c := NewWorkspaceClientWithConnection(nil, nil, goconfig.Default(), "")
	c.settings.visible = true
	w := &EbitengineWindow{scale: 1, views: map[*WorkspaceClient]*nativeView{}}
	return w, c
}

func TestSettingsScrollFractionalWheelAndBounds(t *testing.T) {
	w, c := scrollTestWindow()
	w.beginSettingsScroll(c, nil, image.Rect(20, 40, 300, 240), 1000)
	for i := 0; i < 8; i++ {
		if !w.wheel(c, image.Pt(50, 50), 0, .125) {
			t.Fatal("wheel escaped settings")
		}
	}
	v := w.view(c)
	if v.settingsOffset != 1 {
		t.Fatalf("fractional wheel lost: %v", v.settingsOffset)
	}
	w.wheel(c, image.Point{}, 0, 2000)
	if v.settingsOffset != 800 {
		t.Fatalf("bottom clamp: %v", v.settingsOffset)
	}
	w.wheel(c, image.Point{}, 0, -2000)
	if v.settingsOffset != 0 {
		t.Fatalf("top clamp: %v", v.settingsOffset)
	}
	w.wheel(c, image.Point{}, 2, 0)
	if v.settingsOffset != 0 {
		t.Fatal("horizontal wheel changed vertical offset")
	}
}

func TestSettingsScrollThumbAndClipping(t *testing.T) {
	w, c := scrollTestWindow()
	viewport := image.Rect(20, 40, 300, 240)
	w.beginSettingsScroll(c, nil, viewport, 1000)
	v := w.view(c)
	c.hitRegions = append(c.hitRegions, automationHit{Rect: image.Rect(10, 20, 310, 60)})
	w.endSettingsScroll(c, nil)
	if c.hitRegions[0].Rect != image.Rect(20, 40, 300, 60) {
		t.Fatalf("unclipped hit: %v", c.hitRegions[0].Rect)
	}
	if v.settingsThumb.Min.Y != viewport.Min.Y {
		t.Fatal("thumb not at top")
	}
	w.settingsScrollPointer(c, 10000)
	w.beginSettingsScroll(c, nil, viewport, 1000)
	if v.settingsOffset != 800 || v.settingsThumb.Max.Y != v.settingsTrack.Max.Y {
		t.Fatalf("thumb bottom: %v %v", v.settingsOffset, v.settingsThumb)
	}
	w.settingsScrollPointer(c, -10000)
	if v.settingsOffset != 0 {
		t.Fatal("drag above track did not clamp")
	}
	w.beginSettingsScroll(c, nil, viewport, 100)
	if !v.settingsThumb.Empty() || v.settingsOffset != 0 {
		t.Fatal("short page retained scrolling")
	}
}

func TestSettingsScrollCategoryAndResize(t *testing.T) {
	w, c := scrollTestWindow()
	viewport := image.Rect(20, 40, 300, 240)
	w.beginSettingsScroll(c, nil, viewport, 1000)
	v := w.view(c)
	v.settingsOffset = 450.25
	_, offset := w.beginSettingsScroll(c, nil, viewport, 1000)
	if offset != 450 || v.settingsOffset != 450.25 {
		t.Fatal("layout discarded subpixel remainder")
	}
	c.settings.group++
	w.beginSettingsScroll(c, nil, viewport, 1000)
	if v.settingsOffset != 0 {
		t.Fatal("category retained old scroll")
	}
	v.settingsOffset = 800
	w.beginSettingsScroll(c, nil, image.Rect(20, 40, 300, 640), 1000)
	if v.settingsOffset != 400 {
		t.Fatalf("resize failed clamp: %v", v.settingsOffset)
	}
}

func TestSettingsScrollbarPointerCaptureAndTrackPaging(t *testing.T) {
	w, c := scrollTestWindow()
	w.size = image.Pt(800, 600)
	viewport := image.Rect(20, 40, 300, 240)
	w.beginSettingsScroll(c, nil, viewport, 1000)
	w.endSettingsScroll(c, nil)
	v := w.view(c)
	p := image.Pt(v.settingsThumb.Min.X+2, v.settingsThumb.Min.Y+7)
	if !w.pointerDown(c, p, 1) || w.drag == nil {
		t.Fatal("thumb failed to capture")
	}
	w.pointerMove(c, image.Pt(p.X, 10000))
	w.pointerUp(c, image.Pt(p.X, 10000))
	if v.settingsOffset != 800 || w.drag != nil {
		t.Fatal("thumb drag failed bottom clamp/release")
	}
	c.hitRegions = nil
	w.beginSettingsScroll(c, nil, viewport, 1000)
	w.endSettingsScroll(c, nil)
	p = image.Pt(v.settingsTrack.Min.X+2, v.settingsTrack.Min.Y+2)
	w.pointerDown(c, p, 1)
	w.pointerUp(c, p)
	if v.settingsOffset != 600 || w.drag != nil {
		t.Fatalf("track should page once: %v", v.settingsOffset)
	}
}
