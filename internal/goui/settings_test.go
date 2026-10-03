package goui

import (
	"gioui.org/f32"
	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/widget/material"
	"github.com/SurTeam/Water/internal/goconfig"
	"github.com/SurTeam/Water/internal/govt"
	"image"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestSettingsSaveIncludesEditsFromSameInputBatch(t *testing.T) {
	c := &WorkspaceClient{config: goconfig.Default(), configPath: filepath.Join(t.TempDir(), "config.json")}
	c.openSettings()
	c.settings.group = 4
	c.focusSettingsGroup()
	var router input.Router
	frame := func() {
		var ops op.Ops
		gtx := layout.Context{Ops: &ops, Source: router.Source(), Now: time.Now(), Constraints: layout.Exact(image.Pt(960, 640))}
		c.Layout(gtx, material.NewTheme())
		router.Frame(&ops)
	}
	frame()
	frame()
	var save image.Rectangle
	for _, hit := range c.hitRegions {
		if hit.Label == "save" {
			save = hit.Rect
		}
	}
	if save.Empty() {
		t.Fatal("save button geometry absent")
	}
	point := f32.Pt(float32(save.Min.X+save.Dx()/2), float32(save.Min.Y+save.Dy()/2))
	router.Queue(key.EditEvent{Range: key.Range{Start: 0, End: 2}, Text: "123"}, pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, PointerID: 1, Buttons: pointer.ButtonPrimary, Position: point}, pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, PointerID: 1, Position: point})
	frame()
	if c.settings.result == nil {
		t.Fatal("save was not dispatched")
	}
	select {
	case result := <-c.settings.result:
		if result.err != nil {
			t.Fatal(result.err)
		}
	case <-time.After(time.Second):
		t.Fatal("save worker did not finish")
	}
	cfg, err := goconfig.Load(c.configPath)
	if err != nil || cfg.Terminal.DefaultColumns != 123 {
		t.Fatalf("same-frame edit lost: columns=%d error=%v", cfg.Terminal.DefaultColumns, err)
	}
}

func TestSettingsControlFocusSurvivesNativeFrames(t *testing.T) {
	c := &WorkspaceClient{config: goconfig.Default()}
	c.openSettings()
	var native input.Router
	frame := func() {
		var ops op.Ops
		gtx := layout.Context{Ops: &ops, Source: native.Source(), Now: time.Now(), Constraints: layout.Exact(image.Pt(960, 640))}
		c.Layout(gtx, material.NewTheme())
		native.Frame(&ops)
	}
	frame()
	frame()
	var rect image.Rectangle
	for _, hit := range c.hitRegions {
		if hit.Label == "field:Terminal.FontSize" {
			rect = hit.Rect
		}
	}
	if rect.Empty() || !c.automationClick(float32(rect.Min.X+rect.Dx()/2), float32(rect.Min.Y+rect.Dy()/2), 1) {
		t.Fatal("font editor click failed")
	}
	frame()
	frame()
	if !c.routeSettingsInput("cmd-a") {
		t.Fatal("select all failed")
	}
	frame()
	frame()
	if !c.routeSettingsInput("text:18") {
		t.Fatal("font edit failed")
	}
	frame()
	cfg, err := c.settings.parse()
	if err != nil || cfg.Terminal.FontSize != 18 || cfg.Terminal.DefaultColumns != c.config.Terminal.DefaultColumns {
		t.Fatalf("editor focus moved: font=%v columns=%d error=%v", cfg.Terminal.FontSize, cfg.Terminal.DefaultColumns, err)
	}
}

func TestGlobalSettingsShortcutWithoutTerminal(t *testing.T) {
	c := &WorkspaceClient{config: goconfig.Default()}
	var router input.Router
	frame := func() {
		var ops op.Ops
		gtx := layout.Context{Ops: &ops, Source: router.Source(), Now: time.Now(), Constraints: layout.Exact(image.Pt(960, 640))}
		c.Layout(gtx, material.NewTheme())
		router.Frame(&ops)
	}
	frame()
	router.Queue(key.Event{Name: ",", Modifiers: key.ModCommand, State: key.Press})
	frame()
	if !c.settings.visible {
		t.Fatal("settings shortcut requires a focused terminal")
	}
}

func TestWindowAndTerminalDoNotDispatchShortcutTwice(t *testing.T) {
	c := &WorkspaceClient{config: goconfig.Default()}
	var router input.Router
	terminal := &TerminalInput{OnShortcut: c.dispatchWindowShortcut}
	frame := func(focus bool) {
		var ops op.Ops
		gtx := layout.Context{Ops: &ops, Source: router.Source(), Now: time.Now(), Constraints: layout.Exact(image.Pt(960, 640))}
		c.processWindowShortcuts(gtx)
		terminal.Add(gtx, gtx.Constraints.Max)
		if focus {
			terminal.Focus(gtx)
		}
		terminal.Process(gtx, govt.Snapshot{Cols: 80, Rows: 24}, 8, 16)
		router.Frame(&ops)
	}
	frame(true)
	frame(false)
	router.Queue(key.Event{Name: "E", Modifiers: key.ModCommand, State: key.Press})
	frame(false)
	if !c.sidebarHidden {
		t.Fatal("shortcut was dispatched twice or did not reach the window")
	}
}

func TestShellArgumentsRoundTripAndStartupSettings(t *testing.T) {
	args := []string{"-c", "printf 'hello world'", "", "$HOME", "a\\b"}
	parsed, err := parseShellArgs(formatShellArgs(args))
	if err != nil || !reflect.DeepEqual(args, parsed) {
		t.Fatalf("arguments roundtrip: %q, %v", parsed, err)
	}
	if _, err := parseShellArgs("'unfinished"); err == nil {
		t.Fatal("unfinished quoting accepted")
	}
	c := &WorkspaceClient{config: goconfig.Default()}
	c.openSettings()
	for i := range c.settings.fields {
		f := &c.settings.fields[i]
		if f.group == "Shell" && f.name == "Args" {
			f.editor.SetText("-f")
		}
		if f.group == "Startup" && f.name == "DefaultCWD" {
			f.editor.SetText("/tmp")
		}
	}
	cfg, err := c.settings.parse()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Startup.DefaultCWD == nil || *cfg.Startup.DefaultCWD != "/tmp" || !reflect.DeepEqual(cfg.Shell.Args, []string{"-f"}) {
		t.Fatal("startup preferences lost")
	}
}

func TestSettingsValidationAndTerminalCache(t *testing.T) {
	c := &WorkspaceClient{config: goconfig.Default()}
	c.openSettings()
	for i := range c.settings.fields {
		f := &c.settings.fields[i]
		if f.name == "SplitDown" {
			f.editor.SetText("ctrl--")
		}
		if f.name == "TerminalBackground" {
			f.editor.SetText("#123456")
		}
	}
	cfg, err := c.settings.parse()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Shortcuts.SplitDown != "ctrl--" {
		t.Fatal("hyphen key lost")
	}
	view := NewTerminalView()
	view.cache[0] = preparedRow{hash: 9}
	applyViewConfig(view, cfg)
	if view.Theme.Background.R != 0x12 || len(view.cache) != 0 {
		t.Fatal("appearance did not invalidate prepared colors")
	}
	for i := range c.settings.fields {
		if c.settings.fields[i].name == "TerminalBackground" {
			c.settings.fields[i].editor.SetText("garbage")
		}
	}
	if _, err = c.settings.parse(); err == nil {
		t.Fatal("invalid color accepted")
	}
}

func TestSettingsRejectsConflictingBindings(t *testing.T) {
	c := &WorkspaceClient{config: goconfig.Default()}
	c.openSettings()
	for i := range c.settings.fields {
		if c.settings.fields[i].name == "SplitDown" {
			c.settings.fields[i].editor.SetText("command-t")
		}
	}
	if _, err := c.settings.parse(); err == nil {
		t.Fatal("equivalent shortcuts conflict was missed")
	}
}

func TestNumberedTabBindingsAndInverseColors(t *testing.T) {
	bindings := numberedTabBindings("ctrl-alt-#")
	if len(bindings) != 10 || bindings[0] != "ctrl-alt-1" || bindings[9] != "ctrl-alt-0" {
		t.Fatalf("numbered tabs: %v", bindings)
	}
	c := &WorkspaceClient{config: goconfig.Default()}
	c.openSettings()
	for i := range c.settings.fields {
		if c.settings.fields[i].name == "SplitDown" {
			c.settings.fields[i].editor.SetText("cmd-1")
		}
	}
	if _, err := c.settings.parse(); err == nil {
		t.Fatal("numbered binding conflict accepted")
	}
	view := NewTerminalView()
	cfg := goconfig.Default()
	cfg.Theme.InverseForeground = "#123456"
	cfg.Theme.InverseBackground = "#abcdef"
	applyViewConfig(view, cfg)
	fg, bg := view.resolveColors(govt.Cell{Inverse: true})
	if fg.R != 0x12 || bg.R != 0xab {
		t.Fatal("inverse default colors were ignored")
	}
	fg, bg = view.resolveColors(govt.Cell{Inverse: true, FG: govt.Color{Mode: govt.ColorRGB, Value: 0x010203}, BG: govt.Color{Mode: govt.ColorRGB, Value: 0x040506}})
	if fg.R != 4 || bg.R != 1 {
		t.Fatal("explicit inverse colors were replaced by theme defaults")
	}
}

func TestConfiguredEOFAndScrollUseTerminalInput(t *testing.T) {
	var output []byte
	scroll := 0
	cfg := goconfig.Default().Shortcuts
	cfg.EOF = "ctrl-alt-x"
	cfg.ScrollPageUp = "ctrl-pageup"
	terminal := &TerminalInput{Shortcuts: &cfg, OnInput: func(data []byte) { output = append(output, data...) }, OnScroll: func(lines int) { scroll += lines }}
	snap := govt.Snapshot{Cols: 80, Rows: 24}
	if !routeAutomationInput(terminal, snap, cfg.EOF) || string(output) != "\x04" {
		t.Fatalf("EOF output: %q", output)
	}
	if !routeAutomationInput(terminal, snap, cfg.ScrollPageUp) || scroll != -24 {
		t.Fatalf("page scroll: %d", scroll)
	}
	event, ok := automationInputEvent("cmd--")
	ev, isKey := event.(key.Event)
	if !ok || !isKey || ev.Name != "-" || ev.Modifiers != key.ModCommand {
		t.Fatal("default split-down key misparsed")
	}
}
