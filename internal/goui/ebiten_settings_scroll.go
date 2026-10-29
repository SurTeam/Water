package goui

import (
	"image"
	"math"

	"github.com/google/uuid"
	"github.com/hajimehoshi/ebiten/v2"
)

func settingsScrollLimit(content, viewport int) float64 { return float64(max(0, content-viewport)) }

func settingsScrollClamp(offset float64, content, viewport int) float64 {
	return min(settingsScrollLimit(content, viewport), max(0, offset))
}

func settingsScrollThumb(track image.Rectangle, content, viewport, minimum int, offset float64) image.Rectangle {
	if content <= viewport || track.Empty() {
		return image.Rectangle{}
	}
	height := min(track.Dy(), max(minimum, track.Dy()*viewport/max(1, content)))
	travel := track.Dy() - height
	y := track.Min.Y + int(math.Round(float64(travel)*settingsScrollClamp(offset, content, viewport)/settingsScrollLimit(content, viewport)))
	return image.Rect(track.Min.X, y, track.Max.X, y+height)
}

func (w *EbitengineWindow) beginSettingsScroll(c *WorkspaceClient, dst *ebiten.Image, viewport image.Rectangle, contentHeight int) (*ebiten.Image, int) {
	v := w.view(c)
	if v.settingsScrollGroup != c.settings.group || v.settingsScroll != v.settingsScrollRow {
		v.settingsOffset = float64(v.settingsScroll * w.dp(44))
	}
	v.settingsScrollGroup, v.settingsScrollRow = c.settings.group, v.settingsScroll
	v.settingsViewport, v.settingsContentHeight = viewport, contentHeight
	v.settingsOffset = settingsScrollClamp(v.settingsOffset, contentHeight, viewport.Dy())
	v.settingsTrack = image.Rect(viewport.Max.X+w.dp(6), viewport.Min.Y, viewport.Max.X+w.dp(16), viewport.Max.Y)
	v.settingsThumb = settingsScrollThumb(v.settingsTrack, contentHeight, viewport.Dy(), w.dp(24), v.settingsOffset)
	v.settingsHitStart = len(c.hitRegions)
	if dst != nil {
		dst = dst.SubImage(viewport.Intersect(dst.Bounds())).(*ebiten.Image)
	}
	return dst, int(math.Round(v.settingsOffset))
}

func (w *EbitengineWindow) endSettingsScroll(c *WorkspaceClient, dst *ebiten.Image) {
	v := w.view(c)
	for i := v.settingsHitStart; i < len(c.hitRegions); i++ {
		c.hitRegions[i].Rect = c.hitRegions[i].Rect.Intersect(v.settingsViewport)
	}
	if v.settingsThumb.Empty() {
		return
	}
	cfg := c.currentConfig()
	fg := configColor(cfg.Theme.UIForeground, 0xe6eaea)
	nativeRect(dst, v.settingsTrack, mixColor(configColor(cfg.Theme.ChromeBackground, 0x171b20), fg, .08))
	w.round(dst, v.settingsThumb, w.dp(4), mixColor(configColor(cfg.Theme.ChromeBackground, 0x171b20), fg, .52))
	w.hit(c, v.settingsTrack, hitSettingsControl, uuid.Nil, "scroll:track")
	w.hit(c, v.settingsThumb, hitSettingsControl, uuid.Nil, "scroll:thumb")
}

func (w *EbitengineWindow) settingsScrollPointer(c *WorkspaceClient, y int) {
	v := w.view(c)
	travel := v.settingsTrack.Dy() - v.settingsThumb.Dy()
	if travel <= 0 {
		return
	}
	v.settingsOffset = settingsScrollClamp(float64(y-v.settingsTrack.Min.Y)*settingsScrollLimit(v.settingsContentHeight, v.settingsViewport.Dy())/float64(travel), v.settingsContentHeight, v.settingsViewport.Dy())
}
