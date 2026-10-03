package govt

import (
	"bytes"
	"reflect"
	"testing"
)

func TestCellSizeQueryAcrossEverySplit(t *testing.T) {
	input := []byte("before\x1b[1mstyled\x1b[0m\x1b[16tmiddle\x1b[17t\x1b[16tafter")
	for split := 0; split <= len(input); split++ {
		e := New(80, 24, 100)
		e.SetCellSize(11, 23)
		e.Write(input[:split])
		e.Write(input[split:])
		if got := bytes.Join(e.TakeResponses(), nil); !bytes.Equal(got, []byte("\x1b[6;23;11t\x1b[6;23;11t")) {
			t.Fatalf("split %d: %q", split, got)
		}
		e.Close()
	}
}

func TestQueryInterruptingUnterminatedOSCRecoversParser(t *testing.T) {
	e := New(80, 24, 100)
	defer e.Close()
	e.Write([]byte("\x1b]2;title\x1b[16t\x07"))
	if got := string(bytes.Join(e.TakeResponses(), nil)); got != "\x1b[6;16;8t" {
		t.Fatal("query failed to cancel unfinished OSC", got)
	}
}

func TestOrdinaryANSIAuxiliaryParsersDoNotAllocate(t *testing.T) {
	data := bytes.Repeat([]byte("\r\x1b[0K\x1b[1mBold\x1b[0m \x1b[4mUnderline\x1b[0m\r\n"), 100)
	var graphics graphicsParser
	var links osc8Parser
	allocations := testing.AllocsPerRun(100, func() {
		if len(graphics.feed(data)) != 0 || len(links.feed(data)) != 0 {
			t.Fatal("ordinary ANSI generated auxiliary events")
		}
	})
	if allocations != 0 || len(graphics.buffer) != 0 || links.state != osc8Normal {
		t.Fatalf("allocations=%g OSC state=%d", allocations, links.state)
	}
}

func TestOSC8StyledTextAcrossEverySplit(t *testing.T) {
	data := []byte("\x1b[1mBold\x1b[0m\x1b]8;id=docs;https://example.com\x1b\\link\x1b]8;;\x1b\\plain")
	want := []osc8Event{{params: "id=docs", uri: "https://example.com"}, {}}
	for split := 0; split <= len(data); split++ {
		var parser osc8Parser
		got := append(parser.feed(data[:split]), parser.feed(data[split:])...)
		if !reflect.DeepEqual(got, want) || parser.state != osc8Normal {
			t.Fatalf("split %d: events=%#v state=%d", split, got, parser.state)
		}
	}
}
