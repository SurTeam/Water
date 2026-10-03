package govt

import "testing"

func TestOSC8TargetsStayCorrectAfterErasureAndTaggedReuse(t *testing.T) {
	for _, together := range []bool{false, true} {
		t.Run(map[bool]string{false: "separate writes", true: "same write"}[together], func(t *testing.T) {
			e := New(20, 3, 10)
			defer e.Close()
			e.Write([]byte("\x1b]8;id=tag;https://example.com/a\x1b\\old\x1b]8;;\x1b\\"))
			e.Write([]byte("\r\x1b[2K"))
			a := "\x1b]8;id=tag;https://example.com/a\x1b\\A\x1b]8;;\x1b\\"
			b := "\x1b]8;;https://example.com/b\x1b\\B\x1b]8;;\x1b\\"
			if together {
				e.Write([]byte(a + b))
			} else {
				e.Write([]byte(a))
				e.Write([]byte(b))
			}
			s := e.Snapshot()
			if got := s.RowsData[0].Cells[1].LinkURI; got != "https://example.com/b" {
				t.Fatalf("displayed B opens %q, want B's target", got)
			}
			if got := s.RowsData[0].Cells[0].LinkURI; got != "https://example.com/a" {
				t.Fatalf("reused tagged A has target %q", got)
			}
		})
	}
}
