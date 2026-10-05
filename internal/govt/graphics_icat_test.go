package govt

import (
	"os"
	"strings"
	"testing"
)

// The GUI smoke can capture an actual installed kitten's stream for checking
// identical behavior under arbitrary PTY block boundaries.
func TestKittenCapturedStream(t *testing.T) {
	path := os.Getenv("WATER_ICAT_FIXTURE")
	if path == "" {
		t.Skip("set WATER_ICAT_FIXTURE to an icat stream")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, block := range []int{len(data), 128 * 1024, 8192, 4096, 17} {
		e := New(90, 26, 2000)
		e.SetCellSize(16, 44)
		for at := 0; at < len(data); at += block {
			e.Write(data[at:min(at+block, len(data))])
		}
		s := e.Snapshot()
		if len(s.Images) == 0 {
			for id, r := range e.graphics.images {
				t.Logf("id=%d unicode=%v size=%v placements=%d", id, r.unicodePlaceholder, r.placeholderSize, len(r.placements))
			}
			for y, row := range s.RowsData {
				for x, c := range row.Cells {
					if strings.ContainsRune(c.Text, terminalImagePlaceholder) {
						t.Logf("placeholder %d,%d: %x fg=%+v", x, y, []rune(c.Text), c.FG)
						break
					}
				}
			}
			t.Fatalf("block=%d: no image, stored=%d pending=%v carry=%d", block, e.graphics.storedBytes, e.graphics.pending != nil, len(e.graphics.parser.buffer))
		}
		e.Close()
	}
}
