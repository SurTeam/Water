package goui

import (
	"bytes"
	"github.com/SurTeam/Water/internal/goclient"
	"github.com/SurTeam/Water/internal/goprotocol"
	"github.com/SurTeam/Water/internal/govt"
	"github.com/google/uuid"
	"testing"
)

func TestOutputPreservesSelectionAndTracksHistoryTrim(t *testing.T) {
	id := uuid.New()
	e := govt.New(20, 4, 12)
	defer e.Close()
	e.Write(bytes.Repeat([]byte("history\r\n"), 10))
	e.Scroll(-3)
	e.HoldViewport()
	s := e.Snapshot()
	term := &terminalClient{id: id, emu: e, snapshot: s, selection: Selection{Active: true, Absolute: true, AnchorRow: s.YDisp, FocusRow: s.YDisp, FocusCol: 6}}
	c := &WorkspaceClient{terminals: map[uuid.UUID]*terminalClient{id: term}}
	push := func(seq uint64, data string) {
		c.applyTerminalEvents([]goclient.TerminalPush{{TerminalID: id, Event: goprotocol.TerminalEvent{Seq: seq, Kind: goprotocol.OutputEvent, Data: []byte(data)}}})
	}
	before := e.SelectionText(term.selection.AnchorRow, 0, term.selection.FocusRow, 6)
	push(1, "new\r\n")
	if !term.selection.Active || term.snapshot.YDisp != s.YDisp {
		t.Fatal("output cleared selection or moved history")
	}
	push(2, "new\r\nnew\r\nnew\r\n")
	if !term.selection.Active || e.SelectionText(term.selection.AnchorRow, 0, term.selection.FocusRow, 6) != before {
		t.Fatal("trim moved selection to different content")
	}
	push(3, string(bytes.Repeat([]byte("new\r\n"), 20)))
	if term.selection.Active {
		t.Fatal("expired selection was retained")
	}
}
