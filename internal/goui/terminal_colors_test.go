package goui

import (
	"github.com/SurTeam/Water/internal/goconfig"
	"github.com/SurTeam/Water/internal/govt"
	"testing"
)

func TestTerminalColorsRestoreAndKeepCachesWhileUnchanged(t *testing.T) {
	cfg := goconfig.Default()
	cfg.Theme.TerminalBackground = "#123456"
	v := NewTerminalView()
	applyViewConfig(v, cfg)
	base := v.Theme
	snap := govt.Snapshot{ColorOverrides: map[int]uint32{200: 0x010203, 257: 0x112233}}
	applyTerminalColors(v, snap)
	if v.Theme == base {
		t.Fatal("overrides not projected")
	}
	v.cache[3] = preparedRow{hash: 123}
	applyViewConfig(v, cfg)
	applyTerminalColors(v, snap)
	if v.cache[3].hash != 123 {
		t.Fatal("unchanged color state invalidated cached rows")
	}
	applyTerminalColors(v, govt.Snapshot{})
	if v.Theme != base {
		t.Fatal("palette or default color not restored")
	}
	if terminalDefaultColors(cfg)[257] != 0x123456 {
		t.Fatal("probe defaults differ from configured rendering")
	}
}
