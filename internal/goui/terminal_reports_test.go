package goui

import (
	"bytes"
	"fmt"
	"image"
	"testing"

	"github.com/SurTeam/Water/internal/govt"
)

func TestMetricsIgnoreClosedTerminalDuringReplacement(t *testing.T) {
	term := &terminalClient{emu: govt.New(80, 24, 100)}
	setTerminalWindowMetrics(term, govt.WindowMetrics{Width: 800, Height: 600})
	term.close()
	setTerminalWindowMetrics(term, govt.WindowMetrics{Width: 900, Height: 700})
}

// A newly attached window can publish its measured grid before it receives
// native focus. Pixel reports must already use those measured cell metrics;
// only the shared PTY resize is restricted to the focused window.
func TestUnfocusedNativeLayoutUpdatesPixelReportsWithoutResizingPTY(t *testing.T) {
	emu := govt.New(100, 32, 100)
	defer emu.Close()
	emu.SetCellSize(9, 18) // Startup estimate, before the font resolves.
	term := &terminalClient{emu: emu, cols: 100, rows: 32}
	c := &WorkspaceClient{} // Unfocused, with no transport: must not dispatch.
	w := &EbitengineWindow{}
	for _, cell := range []image.Point{image.Pt(16, 44), image.Pt(20, 48)} {
		w.resizeTerminal(c, term, image.Rect(0, 0, 80*cell.X, 24*cell.Y), cell.X, cell.Y)
		emu.Write([]byte("\x1b[14t\x1b[16t"))
		got := bytes.Join(emu.TakeResponses(), nil)
		want := fmt.Sprintf("\x1b[4;%d;%dt\x1b[6;%d;%dt", 32*cell.Y, 100*cell.X, cell.Y, cell.X)
		if string(got) != want {
			t.Fatalf("stale pixel report before focus: got %q, want %q", got, want)
		}
		if term.cols != 100 || term.rows != 32 || emu.FrameSnapshot().Cols != 100 || emu.FrameSnapshot().Rows != 32 {
			t.Fatal("unfocused window changed the shared terminal geometry")
		}
	}
}
