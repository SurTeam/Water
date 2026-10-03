package govt

import (
	"bytes"
	"strings"
	"testing"
)

func TestColorQueriesAndOverrides(t *testing.T) {
	e := New(20, 3, 10)
	defer e.Close()
	var colors [259]uint32
	colors[256], colors[257], colors[258], colors[1] = 0xe4e4e4, 0x123456, 0xabcdef, 0xff0000
	e.SetDefaultColors(colors)
	e.Write([]byte("\x1b]10;?\x07\x1b]11;?\x1b\\\x1b]12;?\x07\x1b]4;1;?\x07"))
	response := string(bytes.Join(e.TakeResponses(), nil))
	for _, want := range []string{"10;rgb:e4e4/e4e4/e4e4", "11;rgb:1212/3434/5656", "12;rgb:abab/cdcd/efef", "4;1;rgb:ffff/0000/0000"} {
		if !strings.Contains(response, want) {
			t.Fatalf("missing %q in %q", want, response)
		}
	}
	e.Write([]byte("\x1b]4;1;#010203;2;#040506\x07\x1b]11;#112233\x07"))
	s := e.Snapshot()
	if s.ColorOverrides[1] != 0x010203 || s.ColorOverrides[2] != 0x040506 || s.ColorOverrides[257] != 0x112233 {
		t.Fatal(s.ColorOverrides)
	}
	e.Write([]byte("\x1b]104\x07"))
	s = e.Snapshot()
	if len(s.ColorOverrides) != 1 || s.ColorOverrides[257] != 0x112233 {
		t.Fatal("palette reset affected special color", s.ColorOverrides)
	}
	e.Write([]byte("\x1b]111\x07\x1b]11;?\x07"))
	if len(e.Snapshot().ColorOverrides) != 0 || !strings.Contains(string(bytes.Join(e.TakeResponses(), nil)), "1212/3434/5656") {
		t.Fatal("restore did not use configured defaults")
	}
	e.WriteReplay([]byte("\x1b]10;?\x07\x1b]11;?\x07"))
	if len(e.TakeResponses()) != 0 {
		t.Fatal("replay generated probe replies")
	}
}
