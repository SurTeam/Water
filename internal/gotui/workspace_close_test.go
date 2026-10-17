package gotui

import (
	"testing"

	"github.com/SurTeam/Water/internal/goconfig"
	"github.com/SurTeam/Water/internal/gomodel"
	"github.com/gdamore/tcell/v2"
	"github.com/google/uuid"
)

func TestCloseWorkspaceShortcutMatching(t *testing.T) {
	for _, tc := range []struct {
		spec string
		key  tcell.Key
		r    rune
		mod  tcell.ModMask
		want bool
	}{
		{goconfig.Default().Shortcuts.CloseWorkspace, tcell.KeyCtrlW, 0, tcell.ModAlt, true},
		{"ctrl-alt-w", tcell.KeyRune, 'w', tcell.ModCtrl | tcell.ModAlt, true},
		{"ctrl-alt-w", tcell.KeyRune, 'w', tcell.ModAlt, false},
		{"ctrl-alt-w", tcell.KeyCtrlW, 0, 0, false},
		{"alt-shift-w", tcell.KeyRune, 'W', tcell.ModAlt | tcell.ModShift, true},
		{"alt-f4", tcell.KeyF4, 0, tcell.ModAlt, true},
		{"", tcell.KeyCtrlW, 0, tcell.ModAlt, false},
		{"cmd-w", tcell.KeyRune, 'w', 0, false},
		{"invalid-alt-w", tcell.KeyRune, 'w', tcell.ModAlt, false},
	} {
		if got := matchesShortcut(tcell.NewEventKey(tc.key, tc.r, tc.mod), tc.spec); got != tc.want {
			t.Errorf("%q key=%v mod=%v: got %v want %v", tc.spec, tc.key, tc.mod, got, tc.want)
		}
	}
}

func TestWorkspaceCloseConfirmation(t *testing.T) {
	for _, prefix := range []bool{false, true} {
		for _, tabs := range []int{0, 1, 2} {
			for _, confirm := range []bool{false, true} {
				a := newApp(nil, simulation(t, 60, 24), 100)
				a.closeWorkspaceShortcut = "alt-c"
				id, other := uuid.New(), uuid.New()
				ws := gomodel.WorkspaceDump{ID: id}
				for i := 0; i < tabs; i++ {
					ws.Tabs = append(ws.Tabs, gomodel.TabDump{ID: uuid.New()})
				}
				a.state.Workspaces = []gomodel.WorkspaceDump{ws, {ID: other}}
				a.workspace = id
				if prefix {
					a.key(tcell.NewEventKey(tcell.KeyCtrlB, 0, 0))
					a.key(tcell.NewEventKey(tcell.KeyRune, 'W', 0))
				} else {
					a.key(tcell.NewEventKey(tcell.KeyRune, 'c', tcell.ModAlt))
				}
				if tabs > 0 {
					if a.promptKind == "" || len(a.commands) != 0 {
						t.Fatal("workspace containing tabs closed without confirmation")
					}
					a.workspace = other
					if confirm {
						for _, r := range "yes" {
							a.key(tcell.NewEventKey(tcell.KeyRune, r, 0))
						}
						a.key(tcell.NewEventKey(tcell.KeyEnter, 0, 0))
					} else {
						a.key(tcell.NewEventKey(tcell.KeyEscape, 0, 0))
					}
				} else if a.promptKind != "" {
					t.Fatal("empty workspace requested confirmation")
				}
				if tabs == 0 || confirm {
					select {
					case cmd := <-a.commands:
						if cmd["type"] != "workspace.close" || cmd["workspace_id"] != id {
							t.Fatalf("wrong close command: %v", cmd)
						}
					default:
						t.Fatal("missing close command")
					}
				} else if len(a.commands) != 0 {
					t.Fatal("cancel dispatched command")
				}
				if a.confirmWorkspace != uuid.Nil {
					t.Fatal("confirmation target retained")
				}
				a.close()
			}
		}
	}
}

func TestWorkspaceShortcutUnmatchedInput(t *testing.T) {
	a := newApp(nil, simulation(t, 60, 24), 100)
	defer a.close()
	a.closeWorkspaceShortcut = "alt-c"
	a.focus = uuid.New()
	a.panes = []pane{{id: a.focus, term: uuid.New()}}
	a.key(tcell.NewEventKey(tcell.KeyRune, 'w', tcell.ModCtrl|tcell.ModAlt))
	if got := queuedBytes(t, a); got != "\x1b\x17" {
		t.Fatalf("unmatched shortcut input=%q", got)
	}
	a.workspace = uuid.New()
	a.key(tcell.NewEventKey(tcell.KeyRune, 'c', tcell.ModAlt))
	if len(a.commands) != 0 || a.promptKind != "" {
		t.Fatal("missing workspace should be a no-op")
	}
}
