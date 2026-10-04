package govt

import "testing"

func TestBellUsesParserAndDoesNotTreatOSCSeparatorAsBell(t *testing.T) {
	e := New(80, 24, 100)
	e.Write([]byte("\x1b]0;title\x07"))
	if e.BellCount() != 0 {
		t.Fatal("OSC terminator triggered bell")
	}
	e.Write([]byte("\x07\x07"))
	if e.BellCount() != 2 {
		t.Fatal("BEL parser events missing")
	}
}
