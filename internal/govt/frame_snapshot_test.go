package govt

import (
	"fmt"
	"math/rand"
	"reflect"
	"testing"
)

func TestFrameSnapshotSharesOnlyUnchangedImmutableRows(t *testing.T) {
	e := New(20, 4, 100)
	defer e.Close()
	e.Write([]byte("first\r\nsecond"))
	a := e.FrameSnapshot()
	e.Write([]byte("\x1b[2;1Hchanged"))
	b := e.FrameSnapshot()
	if &a.RowsData[0].Cells[0] != &b.RowsData[0].Cells[0] {
		t.Fatal("unchanged row was copied")
	}
	if &a.RowsData[1].Cells[0] == &b.RowsData[1].Cells[0] || a.RowsData[1].Cells[0].Text != "s" {
		t.Fatal("changed row mutated a retained frame")
	}
	e.Write([]byte("\x1b[3;4H"))
	c := e.FrameSnapshot()
	for row := range b.RowsData {
		if &b.RowsData[row].Cells[0] != &c.RowsData[row].Cells[0] {
			t.Fatal("cursor-only movement copied cells")
		}
	}
}

func TestFrameSnapshotMatchesIndependentCopyAcrossTerminalMutations(t *testing.T) {
	e := New(20, 6, 100)
	defer e.Close()
	rng := rand.New(rand.NewSource(13))
	commands := []string{"text", "中文😀", "e\u0301", "\r\n", "\x1b[2J", "\x1b[2K",
		"\x1b[K", "\x1b[3@", "\x1b[2P", "\x1b[4X", "\x1b[2L", "\x1b[2M",
		"\x1b[1S", "\x1b[1T", "\x1b[?1049h", "\x1b[?1049l", "\x1b[31mX\x1b[0m",
		"\x1b]8;;https://example.test\aURL\x1b]8;;\a", "\x1b[?25l", "\x1b[?25h"}
	for n := 0; n < 500; n++ {
		switch n % 29 {
		case 0:
			e.Resize(12+rng.Intn(20), 4+rng.Intn(5))
		case 1:
			e.Scroll(-2)
		case 2:
			e.ScrollToBottom()
		default:
			e.Write([]byte(fmt.Sprintf("\x1b[%d;%dH%s", 1+rng.Intn(5), 1+rng.Intn(12), commands[rng.Intn(len(commands))])))
		}
		shared, independent := e.FrameSnapshot(), e.Snapshot()
		if !reflect.DeepEqual(shared, independent) {
			t.Fatalf("frame differs from independent snapshot after mutation %d", n)
		}
		if len(e.frameRows) > shared.Rows {
			t.Fatal("frame cache retained history rows")
		}
	}
}

func BenchmarkFrameSnapshotCursorOnly(b *testing.B) {
	e := New(80, 24, 10000)
	defer e.Close()
	e.Write(ansiFixture(256 * 1024))
	e.FrameSnapshot()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		e.FrameSnapshot()
	}
}
