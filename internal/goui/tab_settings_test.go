package goui

import (
	"encoding/json"
	"testing"
	"unicode/utf8"

	"github.com/SurTeam/Water/internal/goconfig"
	"github.com/SurTeam/Water/internal/gomodel"
	"github.com/google/uuid"
)

func TestTabCommandUsesActivePaneAndPreservesCustomName(t *testing.T) {
	first, active := uuid.New(), uuid.New()
	tree, err := json.Marshal(paneTree{Type: "split", First: &paneTree{PaneID: first, Terminal: &terminalProjection{Summary: terminalSummary{ProcessName: "zsh"}}}, Second: &paneTree{PaneID: active, Terminal: &terminalProjection{Summary: terminalSummary{ProcessName: "sleep"}}}})
	if err != nil {
		t.Fatal(err)
	}
	tab := gomodel.TabDump{Title: "zsh", ActivePane: active, Tree: tree}
	if got := tabCommandTitle(tab); got != "sleep" {
		t.Fatalf("active process: %q", got)
	}
	custom := "我的自定义标签名称很长，仍然优先"
	tab.TitleOverride = &custom
	if got := tabCommandTitle(tab); got != custom {
		t.Fatalf("custom name overwritten: %q", got)
	}
	if got := boundedTabTitle(tabCommandTitle(tab), 8); got != "我的自定义标签…" || utf8.RuneCountInString(got) != 8 {
		t.Fatalf("UTF-8 truncation: %q", got)
	}
}

func TestSettingsSectionsIncludeEveryVisibleFieldOnce(t *testing.T) {
	c := &WorkspaceClient{config: goconfig.Default()}
	c.openSettings()
	seen := map[int]bool{}
	for group := 0; group < 5; group++ {
		c.settings.group = group
		section := ""
		for _, row := range settingsRows(&c.settings) {
			if row.field < 0 {
				section = row.section
				if ctext("zh-Hans", section) == section {
					t.Fatalf("untranslated heading: %s", section)
				}
				continue
			}
			if section == "" || row.section != section || seen[row.field] {
				t.Fatalf("invalid section/duplicate field: %+v", row)
			}
			seen[row.field] = true
			f := c.settings.fields[row.field]
			if f.group == "Terminal" && (f.name == "DefaultColumns" || f.name == "DefaultLines") {
				t.Fatal("duplicate terminal geometry exposed")
			}
		}
	}
	if len(seen) != len(c.settings.fields) {
		t.Fatalf("missing fields: %d/%d", len(seen), len(c.settings.fields))
	}
}
