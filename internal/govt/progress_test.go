package govt

import "testing"

func TestOSCProgressAcrossChunksAndReplay(t *testing.T) {
	sequence := "\x1b]9;4;4;25\x1b\\"
	for split := 0; split <= len(sequence); split++ {
		e := New(80, 24, 10)
		e.Write([]byte(sequence[:split]))
		e.Write([]byte(sequence[split:]))
		if state, ok := e.ProgressState(); !ok || state != 4 {
			t.Fatalf("split %d: state=%d reported=%v", split, state, ok)
		}
		e.WriteReplay([]byte("\x1b]9;4;0\x07"))
		if state, ok := e.ProgressState(); !ok || state != 0 {
			t.Fatalf("replay: state=%d reported=%v", state, ok)
		}
		if e.BellCount() != 0 {
			t.Fatal("OSC terminator triggered a bell")
		}
		e.Close()
	}
}

func TestOSCProgressIgnoresMalformedAndNotificationSequences(t *testing.T) {
	e := New(80, 24, 10)
	defer e.Close()
	if _, ok := e.ProgressState(); ok {
		t.Fatal("unreported progress should be unknown")
	}
	for _, data := range []string{"4;1;50", "4;3", "4;2;100", "4;4", "4;0"} {
		e.Write([]byte("\x1b]9;" + data + "\x07"))
		if state, ok := e.ProgressState(); !ok || state != int(data[2]-'0') {
			t.Fatalf("valid %q: state=%d reported=%v", data, state, ok)
		}
	}
	for _, data := range []string{"build finished", "4", "4;5", "4;-1", "4;01", "4;1;", "4;1;-5", "4;1;abc", "4;1;3;4", "4;1;999999999999999999999999999999999999999999999999999999999999999999999"} {
		e.Write([]byte("\x1b]9;" + data + "\x07"))
		if state, ok := e.ProgressState(); !ok || state != 0 {
			t.Fatalf("malformed %q changed state=%d reported=%v", data, state, ok)
		}
	}
}

func TestHardResetWithdrawsReportedProgress(t *testing.T) {
	e := New(80, 24, 10)
	defer e.Close()
	e.Write([]byte("\x1bc"))
	if _, ok := e.ProgressState(); ok {
		t.Fatal("reset invented a progress report")
	}
	e.Write([]byte("\x1b]9;4;3\x07\x1b[5;5H\x1bc"))
	if state, ok := e.ProgressState(); !ok || state != 0 {
		t.Fatalf("reset progress: state=%d reported=%v", state, ok)
	}
	snapshot := e.Snapshot()
	if snapshot.CursorX != 0 || snapshot.CursorY != 0 {
		t.Fatal("progress handler swallowed the terminal reset")
	}
}
