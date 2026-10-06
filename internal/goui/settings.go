package goui

import (
	"fmt"
	"image"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode"

	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
	"github.com/SurTeam/Water/internal/goagent"
	"github.com/SurTeam/Water/internal/goconfig"
)

type settingsField struct {
	group, name, label string
	editor             widget.Editor
	kind               reflect.Kind
	toggle             widget.Bool
}

var defaultSettingsTheme = DefaultTerminalTheme()

type sidebarChoice struct{ value, label string }

func sidebarSettingChoices(f settingsField) []sidebarChoice {
	if f.group == "Terminal" && f.name == "CursorStyle" {
		return []sidebarChoice{{"block", "Block"}, {"bar", "Bar"}, {"underline", "Underline"}}
	}
	if f.group != "UI" {
		return nil
	}
	if f.name == "SidebarAgentMode" {
		return []sidebarChoice{{"workspace", "Under workspace"}, {"split", "Separate agents"}}
	}
	if strings.HasPrefix(f.name, "Sidebar") && strings.HasSuffix(f.name, "Alignment") {
		return []sidebarChoice{{"left", "Left"}, {"center", "Center"}, {"right", "Right"}}
	}
	return nil
}

type settingsSave struct {
	cfg goconfig.AppConfig
	err error
}
type settingsPanel struct {
	initial                       []string
	confirmDiscard                bool
	serverConfirm                 bool
	visible, saving               bool
	group, focus                  int
	requestFocus                  bool
	draft                         goconfig.AppConfig
	base                          *goconfig.AppConfig
	fields                        []settingsField
	tabs                          [6]widget.Clickable
	save, cancel, defaults, scrim widget.Clickable
	list                          widget.List
	result                        chan settingsSave
	message                       string
}

// A window's local and remote views share immutable client preferences.
type SettingsStore struct {
	value  atomic.Pointer[goconfig.AppConfig]
	saving atomic.Bool
}

func NewSettingsStore(cfg goconfig.AppConfig) *SettingsStore {
	s := &SettingsStore{}
	s.value.Store(&cfg)
	return s
}
func (c *WorkspaceClient) SetSettingsStore(store *SettingsStore) {
	c.layoutMu.Lock()
	c.settingsStore = store
	c.layoutMu.Unlock()
}

func (c *WorkspaceClient) SetConfigPath(path string) {
	c.layoutMu.Lock()
	c.configPath = path
	c.layoutMu.Unlock()
}
func (c *WorkspaceClient) currentConfig() goconfig.AppConfig {
	if c.settingsStore != nil {
		return *c.settingsStore.value.Load()
	}
	if c.runtimeConfig != nil {
		return *c.runtimeConfig
	}
	return c.config
}
func (c *WorkspaceClient) openSettings() {
	if c.settings.visible {
		return
	}
	c.openSettingsConfig(c.currentConfig())
}
func (c *WorkspaceClient) openSettingsConfig(cfg goconfig.AppConfig) {
	if c.settings.saving {
		return
	}
	c.settings.visible = true
	c.settings.confirmDiscard = false
	c.settings.message = ""
	c.settings.draft = cfg
	if c.settingsStore != nil {
		c.settings.base = c.settingsStore.value.Load()
	}
	c.settings.fields = nil
	c.settings.focus = -1
	c.settings.list.Axis = layout.Vertical
	c.settings.list.Position = layout.Position{}
	groups := []string{"Terminal", "UI", "Shortcuts", "Theme", "Shell", "Startup", "Server", "Features"}
	for _, group := range groups {
		value := reflect.ValueOf(c.settings.draft).FieldByName(group)
		for i := 0; i < value.NumField(); i++ {
			field := value.Type().Field(i)
			if group == "Startup" && legacyPixelField(field.Name) {
				continue
			}
			if group == "Terminal" && (field.Name == "DefaultColumns" || field.Name == "DefaultLines") {
				continue
			}
			v := value.Field(i)
			// The ANSI palette is presented as 16 individual colors below.
			if (v.Kind() == reflect.Slice && group != "Shell") || v.Kind() == reflect.Map {
				continue
			}
			f := settingsField{group: group, name: field.Name, label: settingsLabel(field.Name), kind: v.Kind()}
			if v.Kind() == reflect.Bool {
				f.toggle.Value = v.Bool()
			}
			f.editor.SingleLine = true
			f.editor.SetText(fmt.Sprint(v.Interface()))
			if group == "Shell" && field.Name == "Args" {
				f.editor.SetText(formatShellArgs(c.settings.draft.Shell.Args))
				f.label = "Shell arguments"
			}
			if v.Kind() == reflect.Pointer {
				text := ""
				if !v.IsNil() {
					text = v.Elem().String()
				}
				f.editor.SetText(text)
			}
			c.settings.fields = append(c.settings.fields, f)
		}
	}
	keys := make([]string, 0, len(cfg.Theme.AgentColors))
	for kind := range cfg.Theme.AgentColors {
		keys = append(keys, kind)
	}
	sort.Strings(keys)
	for _, kind := range keys {
		f := settingsField{group: "Theme", name: "AgentColors." + kind, label: goagent.Label(goagent.Kind(kind)) + " color", kind: reflect.String}
		f.editor.SingleLine = true
		f.editor.SetText(cfg.Theme.AgentColors[kind])
		c.settings.fields = append(c.settings.fields, f)
	}
	for i := 0; i < 16; i++ {
		f := settingsField{group: "Theme", name: fmt.Sprint(i), label: fmt.Sprintf("ANSI color %d", i)}
		f.editor.SingleLine = true
		palette := DefaultTerminalTheme().Palette
		color := palette[i]
		text := fmt.Sprintf("#%02x%02x%02x", color.R, color.G, color.B)
		if i < len(c.settings.draft.Theme.ANSIColors) {
			text = c.settings.draft.Theme.ANSIColors[i]
		}
		f.editor.SetText(text)
		c.settings.fields = append(c.settings.fields, f)
	}
	c.settings.initial = c.settings.values()
	c.focusSettingsGroup()
	if c.invalidate != nil {
		c.invalidate()
	}
}

func (s *settingsPanel) values() []string {
	values := make([]string, len(s.fields))
	for i, f := range s.fields {
		if f.kind == reflect.Bool {
			values[i] = strconv.FormatBool(f.toggle.Value)
		} else {
			values[i] = f.editor.Text()
		}
	}
	return values
}

func (s *settingsPanel) cancelChanges() {
	if !s.confirmDiscard && !reflect.DeepEqual(s.initial, s.values()) {
		s.confirmDiscard = true
		s.message = "Unsaved changes. Click Discard changes to cancel, or continue editing."
		return
	}
	s.visible = false
	s.focus = -1
}

func (c *WorkspaceClient) focusSettingsGroup() {
	s := &c.settings
	s.focus = -1
	for _, row := range settingsRows(s) {
		if row.field >= 0 {
			s.focus = row.field
			s.requestFocus = true
			return
		}
	}
}

func settingsLabel(name string) string {
	switch name {
	case "CopyOrInterrupt":
		return "Copy"
	case "TabMaxTitleLength":
		return "Maximum tab title characters"
	case "SidebarHeaderHeight":
		return "Workspace row height"
	case "SidebarRowPadding":
		return "Host row padding"
	case "SidebarAgentPadding":
		return "Agent list padding"
	case "MaxTotalScrollbackBytes":
		return "Scrollback memory limit (bytes)"
	case "ReplayHistoryBytes":
		return "Reconnect history (bytes)"
	case "DefaultColumns":
		return "New terminal columns"
	case "DefaultLines":
		return "New terminal rows"
	}
	var out strings.Builder
	runes := []rune(name)
	for i, r := range runes {
		if i > 0 && unicode.IsUpper(r) && (unicode.IsLower(runes[i-1]) || i+1 < len(runes) && unicode.IsLower(runes[i+1])) {
			out.WriteByte(' ')
		}
		out.WriteRune(r)
	}
	return out.String()
}

func settingsApplyKind(f settingsField) string {
	if f.group == "Startup" && strings.HasPrefix(f.name, "Window") {
		return "New windows"
	}
	if f.group == "Startup" || f.group == "Server" || f.group == "Shell" {
		return "Restart"
	}
	if f.group == "Terminal" {
		switch f.name {
		case "DefaultColumns", "DefaultLines", "ScrollbackLines", "InactiveScrollbackLines", "MaxTotalScrollbackBytes", "ReplayHistoryBytes":
			return "Restart"
		}
	}
	return "Applies immediately"
}

func (s *settingsPanel) parse() (goconfig.AppConfig, error) {
	cfg := s.draft
	cfg.Theme.AgentColors = map[string]string{}
	for kind, color := range s.draft.Theme.AgentColors {
		cfg.Theme.AgentColors[kind] = color
	}
	cfg.Theme.ANSIColors = make([]string, 16)
	for i := range s.fields {
		f := &s.fields[i]
		label := localizedField(s.draft.UI.Language, *f)
		text := strings.TrimSpace(f.editor.Text())
		if f.group == "Theme" {
			if len(text) != 7 || text[0] != '#' {
				return cfg, fmt.Errorf(ctext(s.draft.UI.Language, "%s: use #RRGGBB"), label)
			}
			if _, err := strconv.ParseUint(text[1:], 16, 24); err != nil {
				return cfg, fmt.Errorf(ctext(s.draft.UI.Language, "%s: invalid color"), label)
			}
			if index, err := strconv.Atoi(f.name); err == nil {
				cfg.Theme.ANSIColors[index] = text
				continue
			}
			if strings.HasPrefix(f.name, "AgentColors.") {
				cfg.Theme.AgentColors[strings.TrimPrefix(f.name, "AgentColors.")] = text
				continue
			}
		}
		value := reflect.ValueOf(&cfg).Elem().FieldByName(f.group).FieldByName(f.name)
		switch value.Kind() {
		case reflect.String:
			if choices := sidebarSettingChoices(*f); len(choices) > 0 {
				valid := false
				for _, choice := range choices {
					valid = valid || text == choice.value
				}
				if !valid {
					return cfg, fmt.Errorf("%s: choose one of the displayed options", label)
				}
			}
			if f.group == "UI" && f.name == "Language" && text != "en" && text != "zh-Hans" {
				return cfg, fmt.Errorf("%s", ctext(cfg.UI.Language, "Choose English or Simplified Chinese"))
			}
			if f.group == "Shortcuts" && text != "" {
				binding := text
				if f.name == "SwitchTab" {
					expanded := numberedTabBindings(text)
					if len(expanded) == 0 {
						return cfg, fmt.Errorf("%s", ctext(s.draft.UI.Language, "Switch Tab: use a modifier followed by #, such as cmd-#"))
					}
					binding = expanded[0]
				}
				ev, ok := automationInputEvent(binding)
				if _, isKey := ev.(key.Event); !ok || !isKey {
					return cfg, fmt.Errorf(ctext(s.draft.UI.Language, "%s: invalid shortcut"), label)
				}
			}
			value.SetString(text)
		case reflect.Int:
			n, err := strconv.ParseInt(text, 10, 64)
			if err != nil {
				return cfg, fmt.Errorf(ctext(s.draft.UI.Language, "%s: enter a whole number"), label)
			}
			value.SetInt(n)
		case reflect.Float32:
			n, err := strconv.ParseFloat(text, 32)
			if err != nil || n != n || n > 1e9 || n < 0 {
				return cfg, fmt.Errorf(ctext(s.draft.UI.Language, "%s: enter a positive number"), label)
			}
			value.SetFloat(n)
		case reflect.Bool:
			value.SetBool(f.toggle.Value)
		case reflect.Pointer:
			if text == "" {
				value.SetZero()
			} else {
				value.Set(reflect.ValueOf(&text))
			}
		case reflect.Slice:
			args, err := parseShellArgs(text)
			if err != nil {
				return cfg, err
			}
			value.Set(reflect.ValueOf(args))
		}
	}
	shortcuts := reflect.ValueOf(cfg.Shortcuts)
	seen := map[string]string{}
	for i := 0; i < shortcuts.NumField(); i++ {
		spec := shortcuts.Field(i).String()
		if spec == "" {
			continue
		}
		bindings := []string{spec}
		if shortcuts.Type().Field(i).Name == "SwitchTab" {
			bindings = numberedTabBindings(spec)
		}
		for _, binding := range bindings {
			ev, _ := automationInputEvent(binding)
			id := fmt.Sprint(ev)
			if prior, ok := seen[id]; ok {
				return cfg, fmt.Errorf(ctext(s.draft.UI.Language, "Shortcut conflict: %s and %s"), prior, localizedField(s.draft.UI.Language, settingsField{name: shortcuts.Type().Field(i).Name, label: settingsLabel(shortcuts.Type().Field(i).Name)}))
			}
			seen[id] = localizedField(s.draft.UI.Language, settingsField{name: shortcuts.Type().Field(i).Name, label: settingsLabel(shortcuts.Type().Field(i).Name)})
		}
	}
	return cfg.Normalized(), nil
}

func (c *WorkspaceClient) pollSettingsSave() {
	if c.settings.result == nil {
		return
	}
	select {
	case result := <-c.settings.result:
		if c.invalidate != nil {
			c.invalidate()
		}
		c.settings.saving = false
		c.settings.result = nil
		if result.err != nil {
			c.settings.message = result.err.Error()
			return
		}
		c.runtimeConfig = &result.cfg
		group, focus := c.settings.group, c.settings.focus
		c.openSettingsConfig(result.cfg)
		c.settings.group, c.settings.focus = group, focus
		c.settings.message = "Settings saved"
	default:
	}
}

func (c *WorkspaceClient) layoutSettings(gtx layout.Context, th *material.Theme) layout.Dimensions {
	s := &c.settings
	if !s.visible {
		return layout.Dimensions{}
	}
	// Flush edits before a Save click from the same input batch is validated.
	for i := range s.fields {
		for {
			_, more := gtx.Event(pointer.Filter{Target: &s.fields[i], Kinds: pointer.Press})
			if !more {
				break
			}
			s.focus = i
			s.requestFocus = true
		}
	}
	for i := range s.fields {
		f := &s.fields[i]
		f.editor.ReadOnly = s.saving
		if f.kind == reflect.Bool {
			context := gtx
			if s.saving {
				context = context.Disabled()
			}
			f.toggle.Update(context)
		} else {
			for {
				_, more := f.editor.Update(gtx)
				if !more {
					break
				}
			}
		}
	}
	c.hitRegions = append(c.hitRegions, automationHit{Rect: image.Rectangle{Max: gtx.Constraints.Max}, Kind: hitSettingsControl})
	for s.cancel.Clicked(gtx) {
		if !s.saving {
			s.cancelChanges()
			c.restoreTerminalFocus = true
			if c.invalidate != nil {
				c.invalidate()
			}
			return layout.Dimensions{}
		}
	}
	for s.defaults.Clicked(gtx) {
		if !s.saving {
			initial := s.initial
			c.openSettingsConfig(goconfig.Default())
			s.initial = initial
		}
	}
	for s.save.Clicked(gtx) {
		if !s.saving {
			cfg, err := s.parse()
			if err != nil {
				s.message = err.Error()
				continue
			}
			if c.configPath == "" {
				s.message = "Settings path is unavailable"
				continue
			}
			s.saving = true
			if c.settingsStore != nil && !c.settingsStore.saving.CompareAndSwap(false, true) {
				s.saving = false
				s.message = "Another connection is saving settings"
				continue
			}
			if c.settingsStore != nil && c.settings.base != c.settingsStore.value.Load() {
				c.settingsStore.saving.Store(false)
				s.saving = false
				s.message = "Settings changed in another connection. Cancel and reopen to reload them."
				continue
			}
			result := make(chan settingsSave, 1)
			s.result = result
			path := c.configPath
			go func() {
				err := goconfig.Save(path, cfg)
				if c.settingsStore != nil {
					if err == nil {
						c.settingsStore.value.Store(&cfg)
					}
					c.settingsStore.saving.Store(false)
				}
				result <- settingsSave{cfg, err}
				if c.invalidate != nil {
					c.invalidate()
				}
			}()
		}
	}
	groups := settingsGroups
	for i := range s.tabs {
		for s.tabs[i].Clicked(gtx) {
			s.group = i
			c.focusSettingsGroup()
			s.list.Position = layout.Position{}
		}
	}
	return c.renderSettings(gtx, th, groups)
}

func (c *WorkspaceClient) routeSettingsInput(spec string) bool {
	ev, ok := automationInputEvent(spec)
	if !ok {
		return false
	}
	if e, ok := ev.(key.Event); ok && e.Name == key.NameEscape {
		if !c.settings.saving {
			c.settings.cancelChanges()
		}
		return true
	}
	if c.settings.focus < 0 || c.settings.focus >= len(c.settings.fields) {
		return false
	}
	var router input.Router
	frame := func(focus bool) {
		var ops op.Ops
		gtx := layout.Context{Ops: &ops, Source: router.Source(), Now: time.Now(), Metric: c.frameMetric, Constraints: layout.Exact(c.frameSize)}
		c.layoutSettings(gtx, material.NewTheme())
		if focus {
			gtx.Execute(key.FocusCmd{Tag: &c.settings.fields[c.settings.focus].editor})
		}
		router.Frame(&ops)
	}
	frame(true)
	frame(false) // Settle the focus event before routing a key into the editor.
	if edit, ok := ev.(key.EditEvent); ok {
		start, end := c.settings.fields[c.settings.focus].editor.Selection()
		edit.Range = key.Range{Start: start, End: end}
		ev = edit
	}
	router.Queue(ev)
	frame(false)
	c.settings.requestFocus = true
	return true
}

func (c *WorkspaceClient) splitPane(direction string) {
	command := c.creationCommand("pane.split")
	command["direction"] = direction
	_ = c.session.DispatchAsync(command)
}
func (c *WorkspaceClient) cycleTab(delta int) bool {
	c.mu.RLock()
	state := c.state
	c.mu.RUnlock()
	workspace := activeWorkspace(state)
	if workspace == nil || len(workspace.Tabs) == 0 {
		return false
	}
	for i, tab := range workspace.Tabs {
		if workspace.ActiveTab != nil && tab.ID == *workspace.ActiveTab {
			next := (i + delta + len(workspace.Tabs)) % len(workspace.Tabs)
			_ = c.session.DispatchAsync(map[string]any{"type": "tab.activate", "tab_id": workspace.Tabs[next].ID})
			return true
		}
	}
	return false
}

func applyViewConfig(view *TerminalView, cfg goconfig.AppConfig) {
	oldTheme := view.Theme
	oldHyperlinks := view.Hyperlinks
	oldLigatures := view.Ligatures
	view.FontSize = unit.Sp(cfg.Terminal.FontSize)
	view.CellWidth = unit.Dp(cfg.Terminal.FontSize * 0.6)
	view.LineHeight = unit.Dp(cfg.Terminal.LineHeight)
	view.FontFamily = cfg.Terminal.FontFamily
	view.Hyperlinks = cfg.Terminal.Hyperlinks
	view.Ligatures = cfg.Terminal.Ligatures
	view.Theme.Background = configColor(cfg.Theme.TerminalBackground, 0x2c2c2c)
	view.Theme.Foreground = configColor(cfg.Theme.TerminalForeground, 0xe4e4e4)
	view.Theme.Cursor = configColor(cfg.Theme.CursorBackground, 0xe4e4e4)
	view.Theme.CursorForeground = configColor(cfg.Theme.CursorForeground, 0x2c2c2c)
	view.Theme.InactiveCursor = configColor(cfg.Theme.InactiveCursor, 0x555555)
	view.Theme.InverseForeground = configColor(cfg.Theme.InverseForeground, 0x2c2c2c)
	view.Theme.InverseBackground = configColor(cfg.Theme.InverseBackground, 0xe4e4e4)
	view.Theme.Selection = configColor(cfg.Theme.SelectionBackground, 0x555555)
	for i := range view.Theme.Palette {
		view.Theme.Palette[i] = defaultSettingsTheme.Palette[i]
	}
	for i, value := range cfg.Theme.ANSIColors {
		if i >= 16 {
			break
		}
		old := view.Theme.Palette[i]
		view.Theme.Palette[i] = configColor(value, uint32(old.R)<<16|uint32(old.G)<<8|uint32(old.B))
	}
	view.baseTheme = view.Theme
	view.Theme = themeWithOverrides(view.baseTheme, view.colorOverrides)
	if oldTheme != view.Theme || oldHyperlinks != view.Hyperlinks || oldLigatures != view.Ligatures {
		view.mu.Lock()
		clear(view.cache)
		view.mu.Unlock()
	}
}
