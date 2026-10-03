package govt

import (
	"bytes"
	"reflect"
	"testing"
)

func TestCellSizeQueryFilterAcrossEverySplit(t *testing.T) {
	input := []byte("before\x1b[1mstyled\x1b[0m\x1b[16tmiddle\x1b[17t\x1b[16tafter")
	want := []byte("before\x1b[1mstyled\x1b[0mmiddle\x1b[17tafter")
	for split := 0; split <= len(input); split++ {
		e := New(80, 24, 100)
		e.SetCellSize(11, 23)
		first := append([]byte(nil), e.filterCellSizeQueryLocked(input[:split])...)
		got := append(first, e.filterCellSizeQueryLocked(input[split:])...)
		if !bytes.Equal(got, want) || len(e.pendingCellSizeQuery) != 0 {
			t.Fatalf("split %d: output=%q pending=%q", split, got, e.pendingCellSizeQuery)
		}
		responses := bytes.Join(e.TakeResponses(), nil)
		if !bytes.Equal(responses, []byte("\x1b[6;23;11t\x1b[6;23;11t")) {
			t.Fatalf("split %d: responses=%q", split, responses)
		}
		e.Close()
	}
}

func TestCellSizeQueryFilterPreservesFalsePrefixes(t *testing.T) {
	for _, input := range []string{"\x1b[1mA", "\x1b[16xB", "\x1b\x1b[0mC"} {
		e := New(80, 24, 100)
		var got []byte
		for _, b := range []byte(input) {
			got = append(got, e.filterCellSizeQueryLocked([]byte{b})...)
		}
		if string(got) != input || len(e.TakeResponses()) != 0 || len(e.pendingCellSizeQuery) != 0 {
			t.Fatalf("false query prefix changed: input=%q output=%q", input, got)
		}
		e.Close()
	}
}

func TestOrdinaryANSIAuxiliaryParsersDoNotAllocate(t *testing.T) {
	data := bytes.Repeat([]byte("\r\x1b[0K\x1b[1mBold\x1b[0m \x1b[4mUnderline\x1b[0m\r\n"), 100)
	e := New(80, 24, 100)
	defer e.Close()
	var graphics graphicsParser
	var links osc8Parser
	allocations := testing.AllocsPerRun(100, func() {
		filtered := e.filterCellSizeQueryLocked(data)
		if &filtered[0] != &data[0] {
			t.Fatal("ordinary ANSI filtering copied the input")
		}
		if len(graphics.feed(data)) != 0 || len(links.feed(data)) != 0 {
			t.Fatal("ordinary ANSI generated auxiliary events")
		}
	})
	if allocations != 0 || len(graphics.buffer) != 0 || len(e.pendingCellSizeQuery) != 0 || links.state != osc8Normal {
		t.Fatalf("ordinary ANSI: allocations=%g graphics=%d query=%d OSC state=%d", allocations, len(graphics.buffer), len(e.pendingCellSizeQuery), links.state)
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
