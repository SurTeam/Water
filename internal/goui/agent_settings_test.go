package goui

import (
	"github.com/SurTeam/Water/internal/goagent"
	"github.com/SurTeam/Water/internal/goconfig"
	"github.com/SurTeam/Water/internal/gomodel"
	"github.com/SurTeam/Water/internal/govt"
	"github.com/google/uuid"
	"reflect"
	"testing"
)

func TestSettingsCancelRequiresExplicitDiscardAndDefaultsStayDirty(t *testing.T) {
	c := NewWorkspaceClientWithConnection(nil, nil, goconfig.Default(), "")
	c.openSettings()
	for i := range c.settings.fields {
		if c.settings.fields[i].name == "SidebarWidth" {
			c.settings.fields[i].editor.SetText("260")
		}
	}
	c.settings.cancelChanges()
	if !c.settings.visible || !c.settings.confirmDiscard {
		t.Fatal("dirty settings closed without confirmation")
	}
	c.settings.cancelChanges()
	if c.settings.visible {
		t.Fatal("explicit discard did not close")
	}
	c.openSettings()
	c.settings.cancelChanges()
	if c.settings.visible {
		t.Fatal("clean settings did not cancel")
	}
}

func TestSidebarChoiceValidation(t *testing.T) {
	c := NewWorkspaceClientWithConnection(nil, nil, goconfig.Default(), "")
	c.openSettings()
	for i := range c.settings.fields {
		if c.settings.fields[i].name == "SidebarAgentMode" {
			c.settings.fields[i].editor.SetText("invalid")
		}
	}
	if _, err := c.settings.parse(); err == nil {
		t.Fatal("invalid sidebar mode accepted")
	}
}

func TestSettingsSavedPanelStaysOpenWithFreshBaseline(t *testing.T) {
	c := NewWorkspaceClientWithConnection(nil, nil, goconfig.Default(), "")
	c.openSettings()
	c.settings.group = 1
	c.settings.saving = true
	c.settings.result = make(chan settingsSave, 1)
	cfg := goconfig.Default()
	cfg.UI.TitlebarHeight = 12
	cfg.UI.TabHeight = 8
	c.settings.result <- settingsSave{cfg: cfg}
	c.pollSettingsSave()
	if !c.settings.visible || c.settings.saving || c.settings.group != 1 || !reflect.DeepEqual(c.settings.initial, c.settings.values()) {
		t.Fatal("save must retain the panel and reset the normalized baseline")
	}
	if c.currentConfig().UI.TitlebarHeight != 12 || titlebarHeight(cfg.UI) != 12 {
		t.Fatal("compact titlebar height was expanded")
	}
	c.settings.cancelChanges()
	if c.settings.visible || c.settings.confirmDiscard {
		t.Fatal("saved settings must close without a discard prompt")
	}
}

func TestCompactChromeDimensions(t *testing.T) {
	cfg := goconfig.Default()
	cfg.UI.TitlebarHeight, cfg.UI.TabHeight, cfg.UI.TabFontSize = 12, 8, 6
	cfg.UI.SidebarHostHeaderHeight, cfg.UI.SidebarHeaderHeight, cfg.UI.SidebarAgentRowHeight = 8, 8, 8
	cfg = cfg.Normalized()
	if cfg.UI.TitlebarHeight != 12 || cfg.UI.TabHeight != 8 || cfg.UI.TabFontSize != 6 || titlebarHeight(cfg.UI) != 12 {
		t.Fatal("compact dimensions were enlarged by normalization or font size")
	}
	cfg.UI.TabHeight = 40
	cfg = cfg.Normalized()
	if cfg.UI.TabHeight != 12 || cfg.UI.TitlebarHeight != 12 {
		t.Fatal("tabs must fit inside the independently configured titlebar")
	}
}

func TestSettingsShortcutConfirmsUnsavedChanges(t *testing.T) {
	c := NewWorkspaceClientWithConnection(nil, nil, goconfig.Default(), "")
	c.dispatchShortcut("cmd-,")
	for i := range c.settings.fields {
		if c.settings.fields[i].name == "TitlebarHeight" {
			c.settings.fields[i].editor.SetText("20")
		}
	}
	c.dispatchShortcut("cmd-,")
	if !c.settings.visible || !c.settings.confirmDiscard {
		t.Fatal("shortcut discarded dirty settings without confirmation")
	}
	c.dispatchShortcut("cmd-,")
	if c.settings.visible {
		t.Fatal("confirmed shortcut did not close settings")
	}
}

func TestAgentSettingsDraftRestoreAndCancel(t *testing.T) {
	c := NewWorkspaceClientWithConnection(nil, nil, goconfig.Default(), "")
	defer c.Close()
	c.openSettings()
	c.settings.agents.loaded = true
	c.settings.agents.config = goagent.Config{}
	c.settings.agents.initial = goagent.Config{}
	c.settings.agentFields(goagent.Codex)
	if c.settings.agentDirty() {
		t.Fatal("unchanged builtin marked dirty")
	}
	for i := range c.settings.fields {
		f := &c.settings.fields[i]
		if f.group == "Agents" && f.name == "Name" {
			f.editor.SetText("Custom Codex")
		}
	}
	c.settings.cancelChanges()
	if !c.settings.visible || !c.settings.confirmDiscard {
		t.Fatal("Agent draft closed without discard confirmation")
	}
	if err := c.settings.stageAgent(); err != nil {
		t.Fatal(err)
	}
	c.agentSettingsAction("restore")
	if c.settings.agentDirty() || len(c.settings.agents.config.Overrides) != 0 {
		t.Fatal("builtin restore retained override")
	}
	c.settings.cancelChanges()
	if c.settings.visible {
		t.Fatal("clean restored draft failed to close")
	}
	c.openSettings()
	c.settings.agents.loaded = true
	c.settings.agentFields(goagent.Codex)
	c.agentSettingsAction("duplicate")
	id := c.settings.agents.selected
	if parsed, err := uuid.Parse(string(id)); err != nil || parsed.Version() != 4 {
		t.Fatal("custom identity is not UUIDv4")
	}
	if !c.settings.agentDirty() {
		t.Fatal("new custom Agent not marked dirty")
	}
	c.agentSettingsAction("delete")
	if len(c.settings.agents.config.Custom) != 0 || c.settings.agentDirty() {
		t.Fatal("draft custom deletion did not restore baseline")
	}
}

func TestAgentOSCMappingAndSource(t *testing.T) {
	p := goagent.Builtins()[0]
	p.OSC = []string{"Completed", "Running", "Error", "Running", "Running"}
	if agentMappedStatus(&p, 4) != "Running" || agentMappedStatus(&p, 0) != "Completed" {
		t.Fatal("profile mapping ignored")
	}
	if agentMappedStatus(nil, 4) != "Paused" {
		t.Fatal("legacy default mapping changed")
	}
}

func TestAgentStatusSourceReportsUnknownUntilOSC(t *testing.T) {
	c := NewWorkspaceClientWithConnection(nil, nil, goconfig.Default(), "")
	id := uuid.New()
	emu := govt.New(80, 24, 10)
	defer emu.Close()
	c.terminals[id] = &terminalClient{id: id, emu: emu}
	p := goagent.Builtins()[0]
	p.StatusSource = "osc"
	raw := map[string]any{"kind": p.ID, "label": p.Name, "terminal_id": id, "status": "running", "profile": &p}
	state := gomodel.StateDump{Agents: []any{raw}}
	if c.sidebarAgents(state)[0].status != "Unknown" {
		t.Fatal("OSC-only source invented work state")
	}
	emu.Write([]byte("\x1b]9;4;4\x07"))
	if c.sidebarAgents(state)[0].status != "Paused" {
		t.Fatal("OSC report ignored")
	}
	p.StatusSource = "process"
	if c.sidebarAgents(state)[0].status != "Running" {
		t.Fatal("process source did not ignore OSC")
	}
	raw["status"] = map[string]any{"exited": map[string]any{"code": 0}}
	if c.sidebarAgents(state)[0].status != "Completed" {
		t.Fatal("exited process retained Running state")
	}
}

func TestAgentControlRequiresSelectedProfileToOwnFocusedPane(t *testing.T) {
	c := NewWorkspaceClientWithConnection(nil, nil, goconfig.Default(), "")
	defer c.Close()
	pane := uuid.New()
	c.state.FocusedPane = &pane
	c.state.Agents = []any{map[string]any{"kind": "pi", "pane_id": pane, "status": "running"}}
	c.settings.agents.selected = goagent.Codex
	c.controlActiveAgent("interrupt")
	if c.settings.agents.busy || c.settings.agents.message != "Select the focused terminal’s Agent before controlling it" {
		t.Fatal("control did not refuse a mismatched profile")
	}
}
