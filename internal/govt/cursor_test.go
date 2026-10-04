package govt

import (
	"fmt"
	"testing"
)

func TestCursorDefaultsAndApplicationOverrides(t *testing.T) {
	e := New(20, 4, 10)
	defer e.Close()
	for _, style := range []string{"block", "bar", "underline"} {
		for _, blink := range []bool{true, false} {
			e.SetCursorDefaults(style, blink)
			e.Write([]byte("\x1b[0 q"))
			snap := e.Snapshot()
			if snap.CursorStyle != style || snap.CursorBlink != blink {
				t.Fatalf("defaults %s/%t: %+v", style, blink, snap)
			}
		}
	}
	for code := 1; code <= 6; code++ {
		e.Write([]byte(fmt.Sprintf("\x1b[%d q", code)))
		e.SetCursorDefaults("bar", false)
		snap := e.Snapshot()
		want := []string{"block", "block", "underline", "underline", "bar", "bar"}[code-1]
		if snap.CursorStyle != want || snap.CursorBlink != (code%2 == 1) {
			t.Fatalf("DECSCUSR %d: %+v", code, snap)
		}
	}
	e.Write([]byte("\x1b[0 q\x1bP$q q\x1b\\"))
	if got := string(e.TakeResponses()[0]); got != "\x1bP1$r6 q\x1b\\" {
		t.Fatalf("reported defaults: %q", got)
	}
}
