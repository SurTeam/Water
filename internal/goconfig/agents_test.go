package goconfig

import (
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/SurTeam/Water/internal/goagent"
)

func TestAgentSavePreservesPreferencesAndRestoresDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	original := `{"future":true,"overrides":{"terminal":{"font_size":21,"future_option":true},"ui":{"sidebar_width":250}}}`
	if err := os.WriteFile(path, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	stale, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg := goagent.Config{}
	p := goagent.Builtins()[0]
	p.Name = "My Claude"
	cfg.Put(p)
	if err = SaveAgents(path, cfg); err != nil {
		t.Fatal(err)
	}
	current, err := Load(path)
	if err != nil || current.Terminal.FontSize != 21 || current.UI.SidebarWidth != 250 {
		t.Fatalf("preferences lost: %v %#v", err, current)
	}
	stale.UI.SidebarWidth = 280
	if err = SavePreferences(path, stale); err != nil {
		t.Fatal(err)
	}
	current, err = Load(path)
	if err != nil || goagent.Profiles(current.Agents)[0].Name != "My Claude" {
		t.Fatal("stale desktop settings overwrote server Agent configuration")
	}
	web := current.Web
	web.ListenPort = 45679
	if err = SaveWeb(path, web); err != nil {
		t.Fatal(err)
	}
	current, err = Load(path)
	if err != nil || len(current.Agents.Overrides) != 1 || current.Web.ListenPort != 45679 {
		t.Fatal("Web save replaced Agent definitions")
	}
	if err = SaveAgents(path, goagent.Config{}); err != nil {
		t.Fatal(err)
	}
	current, err = Load(path)
	if err != nil || !reflect.DeepEqual(current.Agents, goagent.Config{}) || current.UI.SidebarWidth != 280 {
		t.Fatal("restore must remove old overrides and retain preferences")
	}
	data, _ := os.ReadFile(path)
	if len(data) == 0 {
		t.Fatal("no saved configuration")
	}
}
func TestConcurrentAgentAndPreferenceSavesPreserveBoth(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := Default()
	cfg.UI.SidebarWidth = 277
	agents := goagent.Config{}
	p := goagent.Builtins()[0]
	p.Enabled = false
	agents.Put(p)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Add(2)
	go func() { defer wg.Done(); errs <- SavePreferences(path, cfg) }()
	go func() { defer wg.Done(); errs <- SaveAgents(path, agents) }()
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	loaded, err := Load(path)
	if err != nil || loaded.UI.SidebarWidth != 277 || goagent.Profiles(loaded.Agents)[0].Enabled {
		t.Fatalf("concurrent fields lost: %v", err)
	}
}
