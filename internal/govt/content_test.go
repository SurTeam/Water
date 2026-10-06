package govt

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestReadContentPagesHistoryWithoutChangingViewport(t *testing.T) {
	e := New(24, 4, 400)
	defer e.Close()
	for n := 0; n < 300; n++ {
		e.Write([]byte(fmt.Sprintf("row-%03d 中文😀\r\n", n)))
	}
	e.Scroll(-12)
	before := e.Snapshot()
	start, count := 0, MaxContentRows
	first, err := e.ReadContent(&start, &count)
	if err != nil {
		t.Fatal(err)
	}
	if first.Rows != 256 || first.StartRow != 0 || !strings.HasPrefix(first.Lines[0], "row-000 中文😀") {
		t.Fatalf("wrong history page: rows=%d start=%d first=%q", first.Rows, first.StartRow, first.Lines[0])
	}
	start = first.Rows
	second, err := e.ReadContent(&start, &count)
	if err != nil || second.Rows != first.TotalRows-first.Rows || !strings.Contains(second.Text, "row-299 中文😀") {
		t.Fatalf("wrong second page: %+v err=%v", second, err)
	}
	visible, err := e.ReadContent(nil, nil)
	if err != nil || visible.StartRow != before.YDisp || visible.Rows != before.Rows {
		t.Fatalf("default query did not read viewport: %+v err=%v", visible, err)
	}
	if !reflect.DeepEqual(before, e.Snapshot()) || len(e.TakeResponses()) != 0 {
		t.Fatal("read-only content query changed terminal state")
	}
}

func TestReadContentBoundsTrimAndAlternateScreen(t *testing.T) {
	e := New(12, 3, 5)
	defer e.Close()
	e.Write([]byte(strings.Repeat("expired\r\n", 20)))
	content, err := e.ReadContent(nil, nil)
	if err != nil || content.TrimmedLines == 0 || content.TotalRows > 8 {
		t.Fatalf("trim metadata mismatch: %+v %v", content, err)
	}
	for _, start := range []int{-1, content.TotalRows + 1} {
		if _, err := e.ReadContent(&start, nil); err == nil {
			t.Fatal("out-of-range row accepted")
		}
	}
	for _, count := range []int{0, -1, MaxContentRows + 1} {
		if _, err := e.ReadContent(nil, &count); err == nil {
			t.Fatal("unbounded or empty page accepted")
		}
	}
	e.Write([]byte("\x1b[?1049h\x1b[?2026hALT 中文"))
	content, err = e.ReadContent(nil, nil)
	if err != nil || !content.AltScreen || !content.SynchronizedOutput || content.TotalRows != 3 || !strings.Contains(content.Text, "ALT 中文") {
		t.Fatalf("wrong active buffer: %+v %v", content, err)
	}
	e.Write([]byte("\x1b[?2026l"))
	content, _ = e.ReadContent(nil, nil)
	if content.SynchronizedOutput {
		t.Fatal("completed synchronized frame still reported pending")
	}
}
