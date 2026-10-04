package goconfig

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

// Keep the documented settings schema in sync with the configuration types.
func TestDocumentedSettingsSchema(t *testing.T) {
	source, err := os.ReadFile("../../config.example.json")
	if err != nil {
		t.Fatal(err)
	}
	var sections map[string]map[string]json.RawMessage
	if err := json.Unmarshal(source, &sections); err != nil {
		t.Fatal(err)
	}
	for section, value := range map[string]any{"ui": UIConfig{}, "shortcuts": ShortcutConfig{}, "theme": ThemeConfig{}, "terminal": TerminalConfig{}, "startup": StartupConfig{}, "server": ServerConfig{}, "shell": ShellConfig{}, "features": FeatureConfig{}} {
		fields, ok := sections[section]
		if !ok {
			t.Fatalf("documented section %s absent", section)
		}
		known := map[string]bool{}
		typ := reflect.TypeOf(value)
		for i := 0; i < typ.NumField(); i++ {
			tag := typ.Field(i).Tag.Get("json")
			name := strings.Split(tag, ",")[0]
			if name == "" || name == "-" {
				continue
			}
			known[name] = true
			// Optional legacy aliases are accepted but omitted when saving.
			if !strings.Contains(tag, ",omitempty") {
				if _, ok := fields[name]; !ok {
					t.Errorf("%s.%s missing in config.example.json", section, name)
				}
			}
		}
		for name := range fields {
			if !known[name] {
				t.Errorf("%s.%s missing in configuration type", section, name)
			}
		}
	}
}
