package goui

import (
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/SurTeam/Water/internal/goclient"
	"github.com/SurTeam/Water/internal/goconfig"
	"github.com/SurTeam/Water/internal/govt"
	"github.com/google/uuid"
)

func attachedScrollTestTerminal(t *testing.T) (*WorkspaceClient, *terminalClient) {
	t.Helper()
	socket, stop := startWorkspaceTestServer(t)
	t.Cleanup(stop)
	shell := "/opt/homebrew/bin/zsh"
	if _, err := os.Stat(shell); err != nil {
		var lookupErr error
		shell, lookupErr = exec.LookPath("zsh")
		if lookupErr != nil {
			t.Skip("zsh is required for the attached terminal fixture")
		}
	}
	var spawned struct {
		TerminalID uuid.UUID `json:"terminal_id"`
	}
	if err := goclient.New(socket).Dispatch(map[string]any{"type": "terminal.spawn", "program": shell, "args": []string{"-f", "-c", "read -r hold"}, "columns": 20, "lines": 4}, &spawned); err != nil {
		t.Fatal(err)
	}
	session, err := goclient.New(socket).OpenSession()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	c := NewWorkspaceClientWithConfig(session, nil, goconfig.Default())
	t.Cleanup(c.Close)
	c.attachTerminal(terminalSummary{TerminalID: spawned.TerminalID}, true)
	term := c.terminals[spawned.TerminalID]
	if term == nil {
		t.Fatal("terminal attach failed")
	}
	for n := 0; n < 12; n++ {
		term.emu.Write([]byte(fmt.Sprintf("row %02d 中文\r\n", n)))
	}
	term.snapshot = term.emu.FrameSnapshot()
	return c, term
}

func TestRejectedTerminalMouseDoesNotReplaceSnapshot(t *testing.T) {
	_, term := attachedScrollTestTerminal(t)
	before := term.snapshot
	term.selection = Selection{Active: true}
	if term.input.OnMouse(govt.MouseEvent{Col: 1, Row: 1, Button: govt.MouseWheel, Action: govt.MouseDown}) {
		t.Fatal("terminal without mouse mode accepted wheel")
	}
	if &before.RowsData[0] != &term.snapshot.RowsData[0] {
		t.Fatal("rejected mouse event rebuilt visible snapshot")
	}
	if !term.selection.Active {
		t.Fatal("rejected mouse event cleared selection")
	}
	term.emu.Write([]byte("\x1b[?1000h\x1b[?1006h"))
	if !term.input.OnMouse(govt.MouseEvent{Col: 1, Row: 1, Button: govt.MouseWheel, Action: govt.MouseDown}) {
		t.Fatal("enabled mouse reporting rejected wheel")
	}
	if term.selection.Active {
		t.Fatal("accepted mouse report retained selection")
	}
	if len(term.emu.TakeResponses()) != 0 {
		t.Fatal("accepted mouse report was not flushed to transport")
	}
}

func TestAcceptedMouseReportsDoNotRebuildOrRedrawScreen(t *testing.T) {
	c, term := attachedScrollTestTerminal(t)
	term.emu.Write([]byte("\x1b[?1003h\x1b[?1006h"))
	term.snapshot = term.emu.FrameSnapshot()
	before := term.snapshot
	var invalidations atomic.Int64
	c.invalidate = func() { invalidations.Add(1) }
	event := govt.MouseEvent{Col: 2, Row: 2, Button: govt.MouseNone, Action: govt.MouseMove}
	if !term.input.OnMouse(event) {
		t.Fatal("ANY tracking rejected movement")
	}
	if &before.RowsData[0] != &term.snapshot.RowsData[0] {
		t.Fatal("mouse report rebuilt visible snapshot")
	}
	if invalidations.Load() != 0 {
		t.Fatal("mouse report without visual changes invalidated the window")
	}
	term.selection = Selection{Active: true}
	if !term.input.OnMouse(event) || term.selection.Active {
		t.Fatal("accepted mouse report did not clear selection")
	}
	if invalidations.Load() != 1 {
		t.Fatal("clearing selection must invalidate once")
	}
	if &before.RowsData[0] != &term.snapshot.RowsData[0] {
		t.Fatal("clearing selection rebuilt visible snapshot")
	}
	if len(term.emu.TakeResponses()) != 0 {
		t.Fatal("mouse reports were not flushed to transport")
	}
}

func TestHistoryScrollAndSelectionReuseImmutableRows(t *testing.T) {
	_, term := attachedScrollTestTerminal(t)
	before := term.snapshot
	term.input.OnScroll(-1)
	after := term.snapshot
	if after.YDisp != before.YDisp-1 {
		t.Fatal("history scroll did not move viewport")
	}
	if &before.RowsData[0].Cells[0] != &after.RowsData[1].Cells[0] {
		t.Fatal("unchanged row copied during viewport scroll")
	}
	if !reflect.DeepEqual(after, term.emu.Snapshot()) {
		t.Fatal("shared scroll snapshot differs from independent copy")
	}
	term.input.OnSelectionStart(0, 0, 1)
	anchor := term.selection.AnchorRow
	term.input.OnSelectionAutoScroll(3, 3, -1)
	selected := term.snapshot
	if selected.YDisp != after.YDisp-1 || term.selection.AnchorRow != anchor || term.selection.FocusRow != selected.YDisp+3 {
		t.Fatal("absolute selection coordinates changed during autoscroll")
	}
	if &after.RowsData[0].Cells[0] != &selected.RowsData[1].Cells[0] {
		t.Fatal("selection autoscroll copied an unchanged row")
	}
	if term.input.OnCopy() == "" {
		t.Fatal("selection stopped copying history text")
	}
	if !reflect.DeepEqual(selected, term.emu.Snapshot()) {
		t.Fatal("selection snapshot differs from independent copy")
	}
	retained := before.RowsData[0].Cells[0].Text
	term.emu.Write([]byte("\x1b[Hchanged"))
	term.emu.FrameSnapshot()
	if before.RowsData[0].Cells[0].Text != retained {
		t.Fatal("retained frame was mutated")
	}
}
