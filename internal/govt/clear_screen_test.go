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

func TestApplicationRedrawReplacesHistoryAndPreservesPrefix(t *testing.T) {
	for _, policyTiming := range []string{"before-replay", "before-frame", "after-frame"} {
		t.Run(policyTiming, func(t *testing.T) {
			e := New(32, 4, 128)
			defer e.Close()
			if policyTiming == "before-replay" {
				e.SetScrollbackOnClearScreen(false)
			}
			e.WriteReplay([]byte("shell-history-one\r\nshell-history-two\r\n"))
			if policyTiming == "before-frame" {
				e.SetScrollbackOnClearScreen(false)
			}
			frame := func(clear bool, lines int) {
				e.Write([]byte("\x1b[?2026h"))
				if clear {
					// Match Pi's full redraw, split inside ANSI sequences.
					for _, chunk := range []string{"\x1b[2", "J\x1b[H\x1b[", "3J"} {
						e.Write([]byte(chunk))
					}
				}
				e.Write([]byte("welcome-marker\r\n"))
				for n := 0; n < lines; n++ {
					e.Write([]byte(fmt.Sprintf("expanded-line-%d\r\n", n)))
				}
				e.Write([]byte("composer>\x1b[?2026l"))
			}
			frame(false, 10)
			if policyTiming == "after-frame" {
				e.SetScrollbackOnClearScreen(false)
			}
			for _, lines := range []int{2, 12, 0, 8, 0} {
				// Repeated metadata snapshots must not move the history boundary.
				e.SetScrollbackOnClearScreen(false)
				frame(true, lines)
				e.Scroll(-e.Snapshot().YBase)
				var text strings.Builder
				for {
					s := e.Snapshot()
					text.WriteString(clearScreenText(Snapshot{RowsData: s.RowsData[:1]}))
					if s.YDisp == s.YBase {
						text.WriteString(clearScreenText(Snapshot{RowsData: s.RowsData[1:]}))
						break
					}
					e.Scroll(1)
				}
				got := text.String()
				if strings.Count(got, "welcome-marker") != 1 || !strings.Contains(got, "shell-history-one") || !strings.Contains(got, "shell-history-two") {
					t.Fatalf("redraw duplicated welcome or lost prefix: %q", got)
				}
				if strings.Count(got, "expanded-line-") != lines {
					t.Fatalf("redraw kept stale expanded contents: %q", got)
				}
			}
			e.SetScrollbackOnClearScreen(true)
			base := e.Snapshot().YBase
			e.Write([]byte("\x1b[H\x1b[2Jshell>"))
			if e.Snapshot().YBase <= base {
				t.Fatal("shell clear policy was not restored")
			}
		})
	}
}

func TestApplicationRedrawBoundaryExpiresWithBoundedHistory(t *testing.T) {
	e := New(20, 3, 5)
	defer e.Close()
	e.Write([]byte("old-shell\r\n"))
	e.SetScrollbackOnClearScreen(false)
	e.Write([]byte("\x1b[?2026h" + strings.Repeat("app-line\r\n", 20) + "\x1b[?2026l"))
	e.Resize(18, 4)
	e.Write([]byte("\x1b[?2026h\x1b[2J\x1b[H\x1b[3Jshort\x1b[?2026l"))
	s := e.Snapshot()
	if s.YBase != 0 || !strings.Contains(clearScreenText(s), "short") || strings.Contains(clearScreenText(s), "app-line") {
		t.Fatalf("expired prefix left stale application history: %+v", s)
	}
}

func TestApplicationRedrawReplaySeparatesRenderingLifetimes(t *testing.T) {
	e := New(32, 4, 128)
	defer e.Close()
	e.SetScrollbackOnClearScreen(false)
	e.WriteReplay([]byte("first-shell\r\n\x1b[?2004h\x1b[?2026hfirst-app\r\nfirst-done\x1b[?2026l\x1b[?2004l\r\nsecond-shell\r\n\x1b[?2004h\x1b[?2026h"))
	e.WriteReplay([]byte(strings.Repeat("old-second-app\r\n", 12) + "\x1b[?2026l"))
	e.Resize(20, 5)
	e.WriteReplay([]byte("\x1b[?2026h\x1b[2J\x1b[H\x1b[3Jnew-second-app\x1b[?2026l"))
	e.Scroll(-e.Snapshot().YBase)
	prefix := clearScreenText(e.Snapshot())
	for _, want := range []string{"first-shell", "first-app", "first-done", "second-shell"} {
		if !strings.Contains(prefix, want) {
			t.Fatalf("replay erased a previous lifetime's history (%s): %q", want, prefix)
		}
	}
	e.ScrollToBottom()
	if s := e.Snapshot(); s.YBase != 4 || strings.Contains(clearScreenText(s), "old-second-app") || !strings.Contains(clearScreenText(s), "new-second-app") {
		t.Fatalf("replay did not replace only current application history: %+v", s)
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
