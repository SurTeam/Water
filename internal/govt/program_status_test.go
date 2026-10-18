package govt

import (
	"bytes"
	"strings"
	"testing"
)

func TestProgramStatusQueryOrderingAndReplay(t *testing.T) {
	query := "\x1b]7501;?\x1b\\"
	for split := 0; split <= len(query); split++ {
		e := New(80, 24, 10)
		e.Write([]byte(query[:split]))
		e.Write([]byte(query[split:] + "\x1b[c"))
		responses := e.TakeResponses()
		if len(responses) < 2 || string(responses[0]) != query || !bytes.HasPrefix(responses[1], []byte("\x1b[?")) {
			t.Fatalf("split %d: responses %q", split, responses)
		}
		e.WriteReplay([]byte(query + "\x1b[c"))
		if got := e.TakeResponses(); len(got) != 0 {
			t.Fatalf("replay replied: %q", got)
		}
		if _, _, ok := e.ProgramStatus(); ok {
			t.Fatal("query invented status")
		}
		e.Close()
	}
}

func TestProgramStatusStatesAndReset(t *testing.T) {
	e := New(80, 24, 10)
	defer e.Close()
	for _, state := range []string{"idle", "working", "blocked", "done", "error"} {
		seq := "\x1b]7501;state=" + state + ":app=pi:msg=SGk=\x1b\\"
		for split := 0; split <= len(seq); split++ {
			e.WriteReplay([]byte(seq[:split]))
			e.WriteReplay([]byte(seq[split:]))
			if s, a, ok := e.ProgramStatus(); s != state || a != "pi" || !ok {
				t.Fatalf("%s split %d: %q %q %v", state, split, s, a, ok)
			}
		}
	}
	for _, kind := range []string{"permission", "question", "auth"} {
		e.Write([]byte("\x1b]7501;state=blocked:kind=" + kind + "\x07"))
		if s, a, ok := e.ProgramStatus(); s != "blocked" || a != "" || !ok {
			t.Fatalf("kind %s: %q %q %v", kind, s, a, ok)
		}
	}
	e.Write([]byte("\x1b]7501;state=clear\x07"))
	if s, a, ok := e.ProgramStatus(); s != "" || a != "" || ok {
		t.Fatal("clear retained report")
	}
	e.Write([]byte("\x1b]9;4;3\x07\x1b]7501;state=working:app=pi\x07\x1bc"))
	if _, _, ok := e.ProgramStatus(); ok {
		t.Fatal("RIS retained report")
	}
	if s, ok := e.ProgressState(); !ok || s != 0 {
		t.Fatal("OSC 9;4 reset behavior changed")
	}
	if e.BellCount() != 0 {
		t.Fatal("OSC terminator rang bell")
	}
}

func TestProgramStatusRejectsMalformedAndUnsupportedRecords(t *testing.T) {
	e := New(80, 24, 10)
	defer e.Close()
	e.Write([]byte("\x1b]7501;state=working:app=pi\x07"))
	for _, data := range []string{
		"", "state", "state=", "state=unknown", "app=pi", "state=done:state=idle", "state=done:app=", "state=done:app=bad name", "state=done:app=π", "state=done:app=" + strings.Repeat("x", 33),
		"state=done:kind=permission", "state=blocked:kind=unknown", "state=clear:job=1", "state=done:id=1", "state=done:unknown=x", "state=done:", "state=done:msg=bad!", "state=done:msg=" + strings.Repeat("A", maxProgramStatusMessageBytes+1), strings.Repeat("A", maxProgramStatusBytes+1),
	} {
		e.Write([]byte("\x1b]7501;" + data + "\x07"))
		if s, a, ok := e.ProgramStatus(); s != "working" || a != "pi" || !ok {
			t.Fatalf("invalid %q changed status to %q %q %v", data, s, a, ok)
		}
	}
	e.Write([]byte("\x1b]7501;state=done:app=A_1.+-:msg=" + strings.Repeat("A", maxProgramStatusMessageBytes) + "\x07"))
	if s, a, ok := e.ProgramStatus(); s != "done" || a != "A_1.+-" || !ok {
		t.Fatal("bounded valid record rejected")
	}
}

func TestProgramStatusClearAndRunResetDiscardProgress(t *testing.T) {
	e := New(80, 24, 10)
	defer e.Close()
	e.Write([]byte("\x1b]9;4;4\x07\x1b]7501;state=working:app=pi\x07\x1b]7501;state=clear\x07"))
	if _, ok := e.ProgressState(); ok {
		t.Fatal("clear revived old progress")
	}
	e.Write([]byte("\x1b]9;4;3\x07\x1b]7501;state=done:app=pi\x07"))
	e.ResetAgentStatus()
	if _, _, ok := e.ProgramStatus(); ok {
		t.Fatal("status survived run reset")
	}
	if _, ok := e.ProgressState(); ok {
		t.Fatal("progress survived run reset")
	}
}
