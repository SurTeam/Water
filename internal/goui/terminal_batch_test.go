package goui

import (
	"strings"
	"testing"

	"github.com/SurTeam/Water/internal/goclient"
	"github.com/SurTeam/Water/internal/goprotocol"
	"github.com/SurTeam/Water/internal/govt"
	"github.com/google/uuid"
)

func TestTerminalBatchPreservesOrderAndDeduplicates(t *testing.T) {
	id := uuid.New()
	emu := govt.New(20, 4, 100)
	defer emu.Close()
	term := &terminalClient{id: id, emu: emu}
	invalidations := 0
	c := &WorkspaceClient{terminals: map[uuid.UUID]*terminalClient{id: term}, invalidate: func() { invalidations++ }}
	push := func(event goprotocol.TerminalEvent) goclient.TerminalPush {
		return goclient.TerminalPush{TerminalID: id, Event: event}
	}
	c.applyTerminalEvents([]goclient.TerminalPush{
		push(goprotocol.TerminalEvent{Seq: 1, Kind: goprotocol.OutputEvent, Data: []byte("before\r\n")}),
		push(goprotocol.TerminalEvent{Seq: 2, Kind: goprotocol.ResizeEvent, Size: goprotocol.TerminalSize{Columns: 30, Lines: 5}}),
		push(goprotocol.TerminalEvent{Seq: 2, Kind: goprotocol.OutputEvent, Data: []byte("DUPLICATE")}),
		push(goprotocol.TerminalEvent{Seq: 3, Kind: goprotocol.OutputEvent, Data: []byte("after")}),
		push(goprotocol.TerminalEvent{Seq: 4, Kind: goprotocol.ExitEvent}),
	})
	var text strings.Builder
	for _, row := range term.snapshot.RowsData {
		for _, cell := range row.Cells {
			text.WriteString(cell.Text)
		}
	}
	if !strings.Contains(text.String(), "before") || !strings.Contains(text.String(), "after") || strings.Contains(text.String(), "DUPLICATE") {
		t.Fatalf("batched screen = %q", text.String())
	}
	if term.lastSeq != 4 || term.snapshot.Cols != 30 || term.snapshot.Rows != 5 || invalidations != 1 {
		t.Fatalf("seq=%d size=%dx%d invalidations=%d", term.lastSeq, term.snapshot.Cols, term.snapshot.Rows, invalidations)
	}
	c.applyTerminalEvent(push(goprotocol.TerminalEvent{Seq: 3, Kind: goprotocol.OutputEvent, Data: []byte("stale")}))
	if invalidations != 1 {
		t.Fatal("duplicate event invalidated the frame")
	}
}
