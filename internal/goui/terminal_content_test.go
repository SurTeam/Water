package goui

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/SurTeam/Water/internal/goclient"
	"github.com/SurTeam/Water/internal/gomodel"
	"github.com/SurTeam/Water/internal/govt"
	"github.com/google/uuid"
)

func TestUIContentObservesOwningClientWithoutNativeFrameRequest(t *testing.T) {
	pane, id, window := uuid.New(), uuid.New(), uuid.New()
	emu := govt.New(24, 3, 50)
	defer emu.Close()
	emu.Write([]byte("protected-history\r\n\x1b[?2026hwelcome\r\nexpanded\x1b[?2026l"))
	emu.SetScrollbackOnClearScreen(false)
	emu.Write([]byte("\x1b[?2026h\x1b[2J\x1b[H\x1b[3Jwelcome\r\ncollapsed\x1b[?2026l"))
	emu.Scroll(-10)
	term := &terminalClient{id: id, emu: emu, lastSeq: 42, selection: Selection{Active: true, AnchorRow: 0, FocusRow: 0, FocusCol: 3}}
	root, _ := json.Marshal(paneTree{Type: "leaf", PaneID: pane, Terminal: &terminalProjection{Summary: terminalSummary{TerminalID: id}}})
	client := &WorkspaceClient{
		session:   &goclient.Session{WindowID: window},
		state:     gomodel.StateDump{Workspaces: []gomodel.WorkspaceDump{{Tabs: []gomodel.TabDump{{Tree: root}}}}},
		terminals: map[uuid.UUID]*terminalClient{id: term},
	}
	// A native window with no queue must not be called by this worker query.
	client.native.Store(&EbitengineWindow{})
	before, selection := emu.Snapshot(), term.selection
	raw, _ := json.Marshal(map[string]any{"pane_id": pane, "start_row": 0, "rows": 50})
	result, err := client.handleUIRequest("ui.content", raw)
	if err != nil {
		t.Fatal(err)
	}
	content := result.(terminalContentResponse)
	if content.WindowID != window || content.PaneID != pane || content.TerminalID != id || content.LastSeq != 42 || content.Source != "gui-client" {
		t.Fatalf("wrong content identity: %+v", content)
	}
	if content.Text != "protected-history\nwelcome\ncollapsed\n" || content.Selection != selection {
		t.Fatalf("query did not observe current client clear policy: %q", content.Text)
	}
	if !reflect.DeepEqual(before, emu.Snapshot()) || term.selection != selection {
		t.Fatal("query changed viewport or selection")
	}
	for _, request := range []map[string]any{{"pane_id": uuid.New()}, {"pane_id": uuid.Nil}, {"pane_id": pane, "rows": 257}} {
		raw, _ := json.Marshal(request)
		if _, err := client.handleUIRequest("ui.content", raw); err == nil {
			t.Fatal("invalid or foreign pane query accepted")
		}
	}
}
