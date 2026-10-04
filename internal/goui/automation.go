package goui

import (
	"image"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"gioui.org/f32"
	"gioui.org/io/event"
	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/widget/material"

	"github.com/SurTeam/Water/internal/govt"
)

// Route control input through the same focused Gio handler as window input.
// Text is an edit event, while named keys use the current terminal modes.
func routeAutomationInput(terminal *TerminalInput, snapshot govt.Snapshot, spec string) bool {
	ev, ok := automationInputEvent(spec)
	if !ok {
		return false
	}
	var router input.Router
	frame := func(focus bool) {
		var ops op.Ops
		gtx := layout.Context{Ops: &ops, Source: router.Source(), Now: time.Now(), Constraints: layout.Exact(image.Pt(800, 600))}
		terminal.Add(gtx, gtx.Constraints.Max)
		if focus {
			terminal.Focus(gtx)
		}
		terminal.Process(gtx, snapshot, 8, 16)
		router.Frame(&ops)
	}
	frame(true)
	router.Queue(ev)
	frame(false)
	return true
}

func automationInputEvent(spec string) (event.Event, bool) {
	if strings.HasPrefix(spec, "text:") {
		return key.EditEvent{Text: strings.TrimPrefix(spec, "text:")}, true
	}
	spec = strings.TrimSpace(spec)
	if utf8.RuneCountInString(spec) == 1 {
		return key.EditEvent{Text: spec}, true
	}
	spec = strings.ReplaceAll(strings.ToLower(spec), "page-up", "pageup")
	spec = strings.ReplaceAll(spec, "page-down", "pagedown")
	// A trailing hyphen is the key itself (default split-down: cmd--).
	if strings.HasSuffix(spec, "--") {
		spec = strings.TrimSuffix(spec, "-") + "minus"
	}
	parts := strings.Split(spec, "-")
	var mods key.Modifiers
	for _, part := range parts[:len(parts)-1] {
		switch part {
		case "ctrl", "control":
			mods |= key.ModCtrl
		case "alt", "option":
			mods |= key.ModAlt
		case "shift":
			mods |= key.ModShift
		case "cmd", "command":
			mods |= key.ModCommand
		case "super":
			mods |= key.ModSuper
		default:
			return nil, false
		}
	}
	name := parts[len(parts)-1]
	names := map[string]key.Name{
		"space": key.Name(" "),
		"minus": key.Name("-"),
		"enter": key.NameReturn, "return": key.NameReturn,
		"tab": key.NameTab, "escape": key.NameEscape, "esc": key.NameEscape,
		"backspace": key.NameDeleteBackward, "delete": key.NameDeleteForward,
		"insert": key.Name("Insert"),
		"up":     key.NameUpArrow, "down": key.NameDownArrow,
		"left": key.NameLeftArrow, "right": key.NameRightArrow,
		"home": key.NameHome, "end": key.NameEnd,
		"pageup": key.NamePageUp, "pagedown": key.NamePageDown,
		"f1": key.NameF1, "f2": key.NameF2, "f3": key.NameF3, "f4": key.NameF4,
		"f5": key.NameF5, "f6": key.NameF6, "f7": key.NameF7, "f8": key.NameF8,
		"f9": key.NameF9, "f10": key.NameF10, "f11": key.NameF11, "f12": key.NameF12,
	}
	keyName, ok := names[name]
	if !ok && strings.HasPrefix(name, "f") {
		if n, err := strconv.Atoi(name[1:]); err == nil && n >= 13 && n <= 24 {
			keyName, ok = key.Name(strings.ToUpper(name)), true
		}
	}
	if !ok {
		if utf8.RuneCountInString(name) != 1 {
			return nil, false
		}
		keyName = key.Name(strings.ToUpper(name))
	}
	return key.Event{Name: keyName, Modifiers: mods, State: key.Press}, true
}

func shortcutMatches(ev key.Event, spec string) bool {
	parsed, ok := automationInputEvent(spec)
	if !ok {
		return false
	}
	want, ok := parsed.(key.Event)
	return ok && ev.Name == want.Name && ev.Modifiers == want.Modifiers
}

func (c *WorkspaceClient) automationDrag(x, y, toX, toY float32) bool {
	c.layoutMu.Lock()
	defer c.layoutMu.Unlock()
	var router input.Router
	th := material.NewTheme()
	now := time.Now()
	frame := func() {
		var ops op.Ops
		gtx := layout.Context{Ops: &ops, Source: router.Source(), Metric: c.frameMetric, Constraints: layout.Exact(c.frameSize), Now: now}
		c.hitRegions = c.hitRegions[:0]
		c.layoutUnlocked(gtx, th)
		router.Frame(&ops)
	}
	frame()
	allowed := false
	for _, hit := range c.hitRegions {
		if hit.Kind == hitDivider && image.Pt(int(x), int(y)).In(hit.Rect) {
			allowed = true
			break
		}
	}
	if !allowed || c.settings.visible {
		return false
	}
	for _, ev := range []pointer.Event{
		{Kind: pointer.Press, Position: f32.Pt(x, y), Buttons: pointer.ButtonPrimary},
		// Gio derives Drag from a raw Move while the primary button is held.
		{Kind: pointer.Move, Position: f32.Pt(toX, toY), Buttons: pointer.ButtonPrimary},
		{Kind: pointer.Release, Position: f32.Pt(toX, toY)},
	} {
		ev.Source = pointer.Mouse
		ev.PointerID = 1
		router.Queue(ev)
		frame()
	}
	if c.invalidate != nil {
		c.invalidate()
	}
	return true
}

func (c *WorkspaceClient) automationSettingsWheel(x, y, dx, dy float32) bool {
	c.layoutMu.Lock()
	defer c.layoutMu.Unlock()
	if !c.settings.visible {
		return false
	}
	var router input.Router
	th := material.NewTheme()
	frame := func() {
		var ops op.Ops
		gtx := layout.Context{Ops: &ops, Source: router.Source(), Metric: c.frameMetric, Constraints: layout.Exact(c.frameSize), Now: time.Now()}
		c.hitRegions = c.hitRegions[:0]
		c.layoutUnlocked(gtx, th)
		router.Frame(&ops)
	}
	frame()
	router.Queue(pointer.Event{Kind: pointer.Scroll, Source: pointer.Mouse, PointerID: 1, Position: f32.Pt(x, y), Scroll: f32.Pt(dx, dy)})
	frame()
	frame()
	if c.invalidate != nil {
		c.invalidate()
	}
	return true
}
