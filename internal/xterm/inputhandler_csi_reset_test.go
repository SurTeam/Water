package xterm

import "testing"

// TestCSIRISFullReset verifies that CSI ? c (RIS) performs the same full reset
// as ESC c: screen cleared, attributes and modes back to defaults.
func TestCSIRISFullReset(t *testing.T) {
	term := New(WithCols(20), WithRows(3))
	defer term.Dispose()

	term.WriteString("\x1b[1;31mhello")
	if got := term.GetLine(0); got != "hello" {
		t.Fatalf("pre-reset line 0 = %q, want %q", got, "hello")
	}

	term.WriteString("\x1b[?c")

	if got := term.String(); got != "" {
		t.Fatalf("screen after CSI ? c = %q, want blank screen", got)
	}
	for y := 0; y < 3; y++ {
		if got := term.GetLine(y); got != "" {
			t.Fatalf("line %d after reset = %q, want empty", y, got)
		}
	}
	if term.Buffer().Lines.Length() < 3 {
		t.Fatalf("buffer has %d lines, want at least 3", term.Buffer().Lines.Length())
	}
	if term.Buffer().YBase != 0 || term.Buffer().YDisp != 0 {
		t.Fatalf("viewport after reset = (base=%d disp=%d), want (0,0)", term.Buffer().YBase, term.Buffer().YDisp)
	}
	if term.Buffer().YBase > 0 {
		t.Fatalf("YBase after reset = %d, want 0 (no retained scrollback)", term.Buffer().YBase)
	}
	if term.coreService.IsCursorHidden {
		t.Fatal("cursor should be visible after full reset")
	}
	if term.CursorX() != 0 || term.CursorY() != 0 {
		t.Fatalf("cursor after reset = (%d,%d), want (0,0)", term.CursorX(), term.CursorY())
	}
	if got := term.GetLine(0); got != "" {
		t.Fatalf("line 0 after reset = %q, want empty", got)
	}
}

// TestCSIRISDoesNotAnswerDA ensures CSI ? c does not emit a DA1 device
// attributes response ("\x1b[?1;2c"): it is a reset, not a query.
func TestCSIRISDoesNotAnswerDA(t *testing.T) {
	term := New(WithCols(20), WithRows(3))
	defer term.Dispose()

	var data []string
	term.OnData(func(s string) { data = append(data, s) })

	term.WriteString("\x1b[?c")
	if len(data) != 0 {
		t.Fatalf("unexpected responses to CSI ? c: %q", data)
	}

	term.WriteString("\x1b[0c")
	if len(data) != 1 {
		t.Fatalf("expected one DA1 response to CSI 0 c, got %q", data)
	}
	if data[0] != "\x1b[?1;2c" {
		t.Fatalf("DA1 response = %q, want %q", data[0], "\x1b[?1;2c")
	}
}

// TestCSIPrimaryDeviceAttributes covers CSI Ps c (DA1). Ps=0 or absent must
// answer with the xterm identity; any other Ps is silently ignored per spec.
func TestCSIPrimaryDeviceAttributes(t *testing.T) {
	for _, seq := range []string{"\x1b[c", "\x1b[0c"} {
		term := New(WithCols(20), WithRows(3))
		defer term.Dispose()

		var data []string
		term.OnData(func(s string) { data = append(data, s) })

		term.WriteString(seq)
		if len(data) != 1 {
			t.Fatalf("%q: expected one DA1 response, got %q", seq, data)
		}
		if data[0] != "\x1b[?1;2c" {
			t.Fatalf("%q: DA1 response = %q, want %q", seq, data[0], "\x1b[?1;2c")
		}
	}

	term := New(WithCols(20), WithRows(3))
	defer term.Dispose()
	var data []string
	term.OnData(func(s string) { data = append(data, s) })

	term.WriteString("\x1b[1c")
	if len(data) != 0 {
		t.Fatalf("CSI 1 c should be ignored, got %q", data)
	}
}

// TestCSISecondaryDeviceAttributes covers CSI > Ps c (DA2).
func TestCSISecondaryDeviceAttributes(t *testing.T) {
	term := New(WithCols(20), WithRows(3))
	defer term.Dispose()

	var data []string
	term.OnData(func(s string) { data = append(data, s) })

	term.WriteString("\x1b[>0c")
	if len(data) != 1 {
		t.Fatalf("expected one DA2 response, got %q", data)
	}
	if data[0] != "\x1b[>0;276;0c" {
		t.Fatalf("DA2 response = %q, want %q", data[0], "\x1b[>0;276;0c")
	}

	term.WriteString("\x1b[>1c")
	if len(data) != 1 {
		t.Fatalf("CSI > 1 c should be ignored, got %q", data)
	}
}

// TestCSIRISMatchesESCRIS checks CSI ? c and ESC c produce identical state.
func TestCSIRISMatchesESCRIS(t *testing.T) {
	mutate := func() *Terminal {
		term := New(WithCols(20), WithRows(3))
		term.WriteString("\x1b[1;31mtext\x1b[?25l\x1b[?7l")
		term.coreService.DecPrivateModes.Origin = true
		return term
	}

	byCSI := mutate()
	byCSI.WriteString("\x1b[?c")

	byESC := mutate()
	byESC.WriteString("\x1bc")

	if byCSI.String() != byESC.String() {
		t.Fatalf("screen differs: CSI ? c=%q ESC c=%q", byCSI.String(), byESC.String())
	}
	if byCSI.coreService.IsCursorHidden != byESC.coreService.IsCursorHidden {
		t.Fatal("cursor visibility differs between CSI ? c and ESC c")
	}
	if byCSI.coreService.DecPrivateModes.Origin != byESC.coreService.DecPrivateModes.Origin {
		t.Fatal("origin mode differs between CSI ? c and ESC c")
	}
}