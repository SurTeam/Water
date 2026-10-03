package goui

import (
	"github.com/SurTeam/Water/internal/goconfig"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestEveryConfigFieldHasSettingsControlAndRoundTrips(t *testing.T) {
	cfg := goconfig.Default()
	c := &WorkspaceClient{config: cfg}
	c.openSettings()
	controls := map[string]bool{}
	for _, f := range c.settings.fields {
		controls[f.group+"."+f.name] = true
	}
	config := reflect.ValueOf(cfg)
	for i := 0; i < config.NumField(); i++ {
		group := config.Type().Field(i).Name
		value := config.Field(i)
		for j := 0; j < value.NumField(); j++ {
			field := value.Type().Field(j).Name
			if field == "AgentColors" {
				for kind := range cfg.Theme.AgentColors {
					if !controls["Theme.AgentColors."+kind] {
						t.Errorf("agent color %s missing", kind)
					}
				}
				continue
			}
			if field == "ANSIColors" {
				for n := 0; n < 16; n++ {
					if !controls["Theme."+strconv.Itoa(n)] {
						t.Errorf("palette control %d missing", n)
					}
				}
				continue
			}
			if !controls[group+"."+field] {
				t.Errorf("control %s.%s missing", group, field)
			}
		}
	}
	for i := range c.settings.fields {
		f := &c.settings.fields[i]
		if f.group == "Theme" {
			f.editor.SetText("#a1b2c3")
		}
		if f.group == "UI" {
			if f.kind == reflect.Bool {
				f.toggle.Value = !f.toggle.Value
			}
			if f.kind == reflect.Float32 && f.name != "SidebarAgentRowWidth" {
				v := reflect.ValueOf(cfg.UI).FieldByName(f.name).Float()
				f.editor.SetText(strconv.FormatFloat(v+.5, 'f', -1, 32))
			}
		}
	}
	parsed, err := c.settings.parse()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := goconfig.Save(path, parsed); err != nil {
		t.Fatal(err)
	}
	reloaded, err := goconfig.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(parsed, reloaded) {
		t.Fatal("settings round-trip lost a field")
	}
	if cfg.Theme.AgentColors["codex"] == parsed.Theme.AgentColors["codex"] {
		t.Fatal("agent edit mutated shared config")
	}
	for kind, value := range parsed.Theme.AgentColors {
		if !strings.EqualFold(value, "#a1b2c3") {
			t.Errorf("agent %s value %q", kind, value)
		}
	}
}
