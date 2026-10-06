package govt

import (
	"fmt"
	"strings"
	"testing"
)

func TestCursorFollowsLivePromptWhileBrowsingClearScreenHistory(t *testing.T) {
	e := New(24, 4, 32)
	defer e.Close()
	for n := 0; n < 12; n++ {
		e.Write([]byte(fmt.Sprintf("history%d\r\n", n)))
	}
	e.Write([]byte("prompt> \x1b[H\x1b[2Jprompt> draft"))
	live := e.Snapshot()
	if live.YBase < live.Rows || live.CursorY != 0 {
		t.Fatalf("expected cleared prompt with more than one page of history: %+v", live)
	}
	for _, offset := range []int{1, 3, 4, 8} {
		e.ScrollToBottom()
		e.Scroll(-offset)
		s := e.Snapshot()
		if s.CursorY != offset || s.CursorX != live.CursorX {
			t.Fatalf("offset %d: cursor (%d,%d), expected (%d,%d)", offset, s.CursorX, s.CursorY, live.CursorX, offset)
		}
		if offset < s.Rows {
			row := Snapshot{RowsData: []Row{s.RowsData[s.CursorY]}}
			if !strings.Contains(clearScreenText(row), "prompt> draft") {
				t.Fatal("visible cursor is not on the live input row")
			}
		} else if s.CursorY < s.Rows {
			t.Fatal("offscreen input row must not leave a cursor on historical text")
		}
	}
	e.ScrollToBottom()
	e.Write([]byte("!"))
	if s := e.Snapshot(); s.CursorY != 0 || s.CursorX != live.CursorX+1 || !strings.Contains(clearScreenText(s), "prompt> draft!") {
		t.Fatalf("returning to live input lost cursor alignment: %+v", s)
	}
}

func clearScreenText(s Snapshot) string {
	var text strings.Builder
	for _, row := range s.RowsData {
		for _, cell := range row.Cells {
			text.WriteString(cell.Text)
		}
		text.WriteByte('\n')
	}
	return text.String()
}

func TestShellClearScreenRetainsViewportInHistory(t *testing.T) {
	for _, chunks := range [][]string{{"\x1b[H\x1b[2J"}, {"\x1b[", "H\x1b[2", "J"}} {
		e := New(24, 4, 8)
		e.Write([]byte("history1\r\nhistory2\r\nprompt> draft"))
		for _, chunk := range chunks {
			e.Write([]byte(chunk))
		}
		e.Write([]byte("prompt> draft"))
		s := e.Snapshot()
		if s.CursorY != 0 || strings.Contains(clearScreenText(s), "history") {
			t.Fatalf("clear-screen did not redraw at top: %+v", s)
		}
		e.Scroll(-s.YBase)
		if text := clearScreenText(e.Snapshot()); !strings.Contains(text, "history1") || !strings.Contains(text, "history2") {
			t.Fatalf("clear-screen lost history: %q", text)
		}
		e.ScrollToBottom()
		e.Write([]byte("\x1b[3J"))
		if s := e.Snapshot(); s.YBase == 0 {
			t.Fatalf("application erase cleared retained scrollback: %+v", s)
		}
		e.Scroll(-s.YBase)
		if text := clearScreenText(e.Snapshot()); !strings.Contains(text, "history1") || !strings.Contains(text, "history2") {
			t.Fatalf("application erase lost history: %q", text)
		}
		e.Close()
	}
}

func TestClearScreenKeepsBoundedHistoryAndCursor(t *testing.T) {
	e := New(20, 3, 2)
	defer e.Close()
	for n := 0; n < 10; n++ {
		e.Write([]byte("\x1b[Hone\r\ntwo\r\nthree\x1b[2J"))
		s := e.Snapshot()
		if s.YBase != 2 || s.CursorY != 2 || s.CursorX != 5 {
			t.Fatalf("clear-screen broke bounded history or cursor: %+v", s)
		}
	}
	base := e.Snapshot().YBase
	e.Write([]byte("\x1b[2J"))
	if e.Snapshot().YBase != base {
		t.Fatal("clearing an empty viewport grew history")
	}
	e.Write([]byte("\x1b[?1049halt\x1b[2J"))
	if s := e.Snapshot(); !s.AltScreen || s.YBase != 0 || strings.Contains(clearScreenText(s), "alt") {
		t.Fatalf("alternate screen must erase without history: %+v", s)
	}
}
