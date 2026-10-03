package xterm

// Ported from xterm.js src/common/InputHandler.ts — DCS sequence handlers.

import (
	"encoding/hex"
	"fmt"
	"strings"
)

// requestStatusString handles DECRQSS (DCS $ q Pt ST).
// It responds with DECRPSS containing the requested terminal setting.
//
// Supported requests:
//   - "q  → DECSCA (protection attribute): responds with 0 or 1
//   - "p  → DECSCL (conformance level): responds with 61;1
//   - r   → DECSTBM (scroll region): responds with top;bottom
//   - m   → SGR (graphic rendition): responds with 0m
//   - ' 'q (SP q) → DECSCUSR (cursor style): responds with style number
//
// Unknown requests receive DCS 0 $ r ST.
func (h *InputHandler) requestStatusString(data string, params *Params) bool {
	respond := func(s string) bool {
		h.coreService.TriggerDataEvent("\x1b"+s+"\x1b\\", false, false)
		return true
	}

	switch data {
	case "\"q":
		// DECSCA — protection attribute
		p := 0
		if h.curAttrData.IsProtected() != 0 {
			p = 1
		}
		return respond(fmt.Sprintf("P1$r%d\"q", p))

	case "\"p":
		// DECSCL — conformance level (always report VT100 level 1)
		return respond("P1$r61;1\"p")

	case "r":
		// DECSTBM — scroll region
		buf := h.activeBuffer()
		return respond(fmt.Sprintf("P1$r%d;%dr", buf.ScrollTop+1, buf.ScrollBottom+1))

	case "m":
		return respond("P1$r" + h.sgrStatus() + "m")

	case " q":
		// DECSCUSR — cursor style
		styles := map[CursorStyle]int{
			CursorStyleBlock:     2,
			CursorStyleUnderline: 4,
			CursorStyleBar:       6,
		}
		opts := h.optionsService.Options
		cursorStyle, blink := opts.CursorStyle, opts.CursorBlink
		if override := h.coreService.DecPrivateModes.CursorStyle; override != nil {
			cursorStyle = *override
		}
		if override := h.coreService.DecPrivateModes.CursorBlinkOverride; override != nil {
			blink = *override
		}
		style := styles[cursorStyle]
		if blink {
			style--
		}
		return respond(fmt.Sprintf("P1$r%d q", style))

	default:
		// Unknown request
		return respond("P0$r")
	}
}

func (h *InputHandler) sgrStatus() string {
	a := &h.curAttrData
	parts := []string{"0"}
	underline := a.Fg & FgFlagUnderline
	underlineCode := "4"
	if a.Extended != nil {
		style := (a.Extended.ext & ExtFlagUnderlineStyle) >> 26
		if style != 0 {
			underline = 1
			underlineCode = fmt.Sprintf("4:%d", style)
		}
	}
	for _, flag := range []struct {
		set  uint32
		code string
	}{{a.IsBold(), "1"}, {a.IsDim(), "2"}, {a.IsItalic(), "3"}, {underline, underlineCode}, {a.IsBlink(), "5"}, {a.IsInverse(), "7"}, {a.IsInvisible(), "8"}, {a.IsStrikethrough(), "9"}, {a.IsOverline(), "53"}} {
		if flag.set != 0 {
			parts = append(parts, flag.code)
		}
	}
	appendColor := func(prefix string, mode uint32, c int) {
		switch mode {
		case AttrCMP16, AttrCMP256:
			parts = append(parts, fmt.Sprintf("%s;5;%d", prefix, c))
		case AttrCMRGB:
			parts = append(parts, fmt.Sprintf("%s;2;%d;%d;%d", prefix, c>>16&255, c>>8&255, c&255))
		}
	}
	appendColor("38", a.GetFgColorMode(), a.GetFgColor())
	appendColor("48", a.GetBgColorMode(), a.GetBgColor())
	if a.Extended != nil && a.Extended.UnderlineColor() != ^uint32(0) {
		appendColor("58", a.GetUnderlineColorMode(), a.GetUnderlineColor())
	}
	return strings.Join(parts, ";")
}

func (h *InputHandler) requestTermcap(data string, _ *Params) bool {
	if len(data) > 4096 {
		return true
	}
	for i, nameHex := range strings.Split(data, ";") {
		if i >= 64 {
			break
		}
		name, err := hex.DecodeString(nameHex)
		value, ok := map[string]string{"TN": h.optionsService.Options.TermName, "name": h.optionsService.Options.TermName, "Co": "256", "colors": "256", "RGB": "8"}[string(name)]
		if err != nil || !ok {
			h.coreService.TriggerDataEvent("\x1bP0+r\x1b\\", false, false)
			break
		}
		h.coreService.TriggerDataEvent("\x1bP1+r"+nameHex+"="+hex.EncodeToString([]byte(value))+"\x1b\\", false, false)
	}
	return true
}
