package goui

import (
	"image"
	"strings"
	"testing"

	"github.com/SurTeam/Water/internal/goconfig"
)

func TestAllSettingsHaveChineseLabels(t *testing.T) {
	c := &WorkspaceClient{config: goconfig.Default()}
	c.openSettings()
	for _, field := range c.settings.fields {
		if field.group == "Startup" && legacyPixelField(field.name) {
			t.Fatalf("legacy pixel control exposed: %s", field.name)
		}
		translated := localizedField("zh-Hans", field)
		if translated == field.label {
			t.Errorf("missing translation for %s.%s", field.group, field.name)
		}
		if localizedField("en", field) != field.label {
			t.Errorf("English label changed: %s", field.name)
		}
	}
	c.settings.draft.UI.Language = "zh-Hans"
	for i := range c.settings.fields {
		f := &c.settings.fields[i]
		if f.name == "Language" {
			f.editor.SetText("zh-Hans")
		}
		if f.name == "WindowRows" {
			f.editor.SetText("invalid")
		}
	}
	if _, err := c.settings.parse(); err == nil || !strings.Contains(err.Error(), "请输入整数") {
		t.Fatalf("localized validation: %v", err)
	}
}

func TestWindowGridGeometryAtDifferentDPI(t *testing.T) {
	for _, scale := range []float64{1, 1.25, 1.5, 2} {
		for _, sidebar := range []bool{false, true} {
			cfg := goconfig.Default()
			cfg.UI.SidebarVisible = sidebar
			cfg.UI.TitlebarHeight, cfg.UI.TabHeight = 22, 25
			dp := func(v float32) int { return int(float64(v)*scale + .5) }
			cell := nativeCellMetrics{width: dp(8), height: dp(21)}
			size := windowGridSize(cfg, scale, cell, 103, 37)
			left := dp(cfg.UI.WindowPadding)
			if sidebar {
				left = dp(cfg.UI.SidebarWidth) - left + dp(cfg.UI.SidebarResizeHandleWidth)
			}
			r := image.Rect(left, dp(titlebarHeight(cfg.UI))+dp(cfg.UI.WindowPadding), dp(float32(size.X))-dp(cfg.UI.WindowPadding), dp(float32(size.Y))-dp(cfg.UI.WindowPadding)).Inset(dp(cfg.UI.PanePadding))
			grid := fittedTerminalGrid(r, cell.width, cell.height)
			if grid.Dx()/cell.width != 103 || grid.Dy()/cell.height != 37 {
				t.Fatalf("scale %g sidebar %t: %v", scale, sidebar, grid)
			}
			if r.Dx()-grid.Dx() >= cell.width || r.Dy()-grid.Dy() >= cell.height {
				t.Fatal("whole unused cells around grid")
			}
			if abs((grid.Min.X-r.Min.X)-(r.Max.X-grid.Max.X)) > 1 || abs((grid.Min.Y-r.Min.Y)-(r.Max.Y-grid.Max.Y)) > 1 {
				t.Fatal("unbalanced grid remainder")
			}
		}
	}
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
