package govt

import (
	"testing"
	"unsafe"
)

// Guard the snapshot's hottest allocation against accidental padding growth.
func TestCellCompactLayout(t *testing.T) {
	type previousLayout struct {
		Text                                                            string
		Width                                                           uint8
		FG, BG                                                          Color
		Bold, Italic, Dim, Underline, Strikethrough, Inverse, Invisible bool
		URLID                                                           int
		LinkURI                                                         string
	}
	compact, previous := unsafe.Sizeof(Cell{}), unsafe.Sizeof(previousLayout{})
	if compact >= previous {
		t.Fatalf("cell layout has not reduced padding: %d >= %d", compact, previous)
	}
	t.Logf("snapshot cell: %d bytes (previous layout %d)", compact, previous)
}
