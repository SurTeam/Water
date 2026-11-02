package goui

import (
	"testing"

	"github.com/SurTeam/Water/internal/goconfig"
)

func TestServerLifecycleFieldsAreNotEditable(t *testing.T) {
	c := NewWorkspaceClientWithConnection(nil, nil, goconfig.Default(), "")
	c.openSettings()
	for _, field := range c.settings.fields {
		if field.group == "Server" && (field.name == "Detached" || field.name == "DetachOnQuit") {
			t.Fatalf("legacy server lifecycle field %s is still editable", field.name)
		}
	}
}
