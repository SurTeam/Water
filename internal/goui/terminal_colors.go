package goui

import (
	"github.com/SurTeam/Water/internal/goconfig"
	"github.com/SurTeam/Water/internal/govt"
	"image/color"
)

func terminalDefaultColors(cfg goconfig.AppConfig) [259]uint32 {
	v := NewTerminalView()
	applyViewConfig(v, cfg)
	return themeColors(v.Theme)
}

func themeColors(theme TerminalTheme) [259]uint32 {
	var colors [259]uint32
	pack := func(c color.NRGBA) uint32 { return uint32(c.R)<<16 | uint32(c.G)<<8 | uint32(c.B) }
	for i, c := range theme.Palette {
		colors[i] = pack(c)
	}
	colors[256], colors[257], colors[258] = pack(theme.Foreground), pack(theme.Background), pack(theme.Cursor)
	return colors
}

func syncTerminalColors(term *terminalClient) bool {
	colors := themeColors(term.view.baseTheme)
	if colors != term.defaultColors {
		term.defaultColors = colors
		term.emu.SetDefaultColors(colors)
		return true
	}
	return false
}

func applyTerminalColors(v *TerminalView, snap govt.Snapshot) {
	if len(v.colorOverrides) == 0 && len(snap.ColorOverrides) == 0 {
		return
	}
	old := v.Theme
	v.colorOverrides = snap.ColorOverrides
	v.Theme = themeWithOverrides(v.baseTheme, snap.ColorOverrides)
	if old != v.Theme {
		v.cache = map[int]preparedRow{}
	}
}

func themeWithOverrides(theme TerminalTheme, overrides map[int]uint32) TerminalTheme {
	for index, rgb := range overrides {
		c := color.NRGBA{R: uint8(rgb >> 16), G: uint8(rgb >> 8), B: uint8(rgb), A: 255}
		switch {
		case index >= 0 && index < 256:
			theme.Palette[index] = c
		case index == 256:
			theme.Foreground = c
		case index == 257:
			theme.Background = c
		case index == 258:
			theme.Cursor = c
		}
	}
	return theme
}
