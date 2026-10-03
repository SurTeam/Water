package goui

import (
	"image"
	"strings"
	"time"
	"unicode/utf8"

	"gioui.org/io/event"
	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"

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
		"enter": key.NameReturn, "return": key.NameReturn,
		"tab": key.NameTab, "escape": key.NameEscape, "esc": key.NameEscape,
		"backspace": key.NameDeleteBackward, "delete": key.NameDeleteForward,
		"up": key.NameUpArrow, "down": key.NameDownArrow,
		"left": key.NameLeftArrow, "right": key.NameRightArrow,
		"home": key.NameHome, "end": key.NameEnd,
		"pageup": key.NamePageUp, "pagedown": key.NamePageDown,
		"f1": key.NameF1, "f2": key.NameF2, "f3": key.NameF3, "f4": key.NameF4,
		"f5": key.NameF5, "f6": key.NameF6, "f7": key.NameF7, "f8": key.NameF8,
		"f9": key.NameF9, "f10": key.NameF10, "f11": key.NameF11, "f12": key.NameF12,
	}
	keyName, ok := names[name]
	if !ok {
		if utf8.RuneCountInString(name) != 1 {
			return nil, false
		}
		keyName = key.Name(strings.ToUpper(name))
	}
	return key.Event{Name: keyName, Modifiers: mods, State: key.Press}, true
}
