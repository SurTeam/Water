package goconfig

import (
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// Guard the migration against silently dropping a field in the Rust schema.
func TestRustSettingsSchemaParity(t *testing.T) {
	source, err := os.ReadFile("../../src/config.rs")
	if err != nil {
		t.Fatal(err)
	}
	for rust, value := range map[string]any{"UiConfig": UIConfig{}, "ShortcutConfig": ShortcutConfig{}, "ThemeConfig": ThemeConfig{}, "TerminalConfig": TerminalConfig{}, "StartupConfig": StartupConfig{}, "ShellConfig": ShellConfig{}, "FeatureConfig": FeatureConfig{}} {
		body := regexp.MustCompile("(?s)pub struct " + rust + " \\{(.*?)\\n\\}").FindStringSubmatch(string(source))
		if len(body) != 2 {
			t.Fatalf("Rust schema %s absent", rust)
		}
		known := map[string]bool{}
		typ := reflect.TypeOf(value)
		for i := 0; i < typ.NumField(); i++ {
			known[strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]] = true
		}
		for _, field := range regexp.MustCompile("pub (\\w+):").FindAllStringSubmatch(body[1], -1) {
			if !known[field[1]] {
				t.Errorf("%s.%s missing in Go settings", rust, field[1])
			}
		}
	}
}
