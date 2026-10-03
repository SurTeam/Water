package govt

import (
	"fmt"
	"github.com/SurTeam/Water/internal/xterm"
)

// Color slots 0..255 are the palette; 256..258 are foreground/background/cursor.
func (e *Emulator) SetDefaultColors(colors [259]uint32) {
	old := e.defaultColors.Swap(&colors)
	if old != nil && old[257] != colors[257] && e.colorSchemeUpdates.Load() && !e.backgroundOverridden.Load() {
		e.enqueueLiveResponse(colorSchemeResponse(colors[257]))
	}
}

func (e *Emulator) reportColorScheme() {
	rgb := uint32(0)
	if colors := e.defaultColors.Load(); colors != nil {
		rgb = colors[257]
	}
	if override, ok := e.colorOverrides[257]; ok {
		rgb = override
	}
	e.reportColorSchemeValue(rgb)
}

func (e *Emulator) reportColorSchemeValue(rgb uint32) {
	e.enqueueResponse(colorSchemeResponse(rgb))
}

func colorSchemeResponse(rgb uint32) []byte {
	mode := 1 // dark
	if 299*((rgb>>16)&255)+587*((rgb>>8)&255)+114*(rgb&255) >= 128000 {
		mode = 2
	}
	return []byte(fmt.Sprintf("\x1b[?997;%dn", mode))
}

func (e *Emulator) handleColors(events []xterm.ColorEvent) {
	for _, event := range events {
		index := event.Index
		if index < 0 || index >= 259 {
			continue
		}
		switch event.Type {
		case xterm.ColorRequestReport:
			rgb, ok := e.colorOverrides[index]
			if !ok && e.defaultColors.Load() != nil {
				rgb = e.defaultColors.Load()[index]
			}
			prefix := fmt.Sprintf("4;%d", index)
			if index >= 256 {
				prefix = fmt.Sprint(index - 256 + 10)
			}
			e.enqueueResponse([]byte(fmt.Sprintf("\x1b]%s;rgb:%04x/%04x/%04x\x1b\\", prefix, ((rgb>>16)&255)*257, ((rgb>>8)&255)*257, (rgb&255)*257)))
		case xterm.ColorRequestSet:
			if event.Color != nil {
				c := *event.Color
				e.colorOverrides[index] = uint32(c[0])<<16 | uint32(c[1])<<8 | uint32(c[2])
			}
		case xterm.ColorRequestRestore:
			delete(e.colorOverrides, index)
		}
	}
	_, overridden := e.colorOverrides[257]
	e.backgroundOverridden.Store(overridden)
}
