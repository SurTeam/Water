package goterminal

import (
	"bytes"
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"

	"github.com/SurTeam/Water/internal/goprotocol"
	"github.com/SurTeam/Water/internal/govt"
)

func publishReplay(t *Terminal, data string) {
	t.publish(goprotocol.TerminalEvent{
		Kind: goprotocol.OutputEvent,
		Size: t.size,
		Data: []byte(data),
	})
}

func replayText(t *Terminal) string {
	var b strings.Builder
	for _, ev := range t.Replay() {
		if ev.Kind == goprotocol.OutputEvent {
			b.Write(ev.Data)
		}
	}
	return b.String()
}

func TestIncrementalPrimaryCompactionMatchesFullScan(t *testing.T) {
	newTerminal := func() *Terminal {
		return &Terminal{
			size:            goprotocol.TerminalSize{Columns: 80, Lines: 24},
			scrollbackLines: 20,
		}
	}
	incremental, full := newTerminal(), newTerminal()
	rng := rand.New(rand.NewSource(42))
	inputs := []string{
		"short\x1b[31mcolored\x1b[0m line\n",
		strings.Repeat("plain", 15000),
		strings.Repeat("\x1b[Hframe", 12000),
		"\nnext\n", "\x1b[?1049h", "\x1b[?1049l",
		"\x1b[?10", "49h", "\x1b]title\x1b\\",
	}
	for step := 0; step < 150; step++ {
		if step%11 == 0 {
			size := goprotocol.TerminalSize{Columns: 20 + rng.Intn(100), Lines: 2 + rng.Intn(30)}
			incremental.size, full.size = size, size
			full.primaryRedrawCheck = true
			incremental.publish(goprotocol.TerminalEvent{Kind: goprotocol.ResizeEvent, Size: size})
			full.publish(goprotocol.TerminalEvent{Kind: goprotocol.ResizeEvent, Size: size})
		}
		data := inputs[rng.Intn(len(inputs))]
		for len(data) > 0 {
			n := min(len(data), 1+rng.Intn(4096))
			full.primaryRedrawCheck = true // Exercise the previous unconditional full scan.
			publishReplay(incremental, data[:n])
			publishReplay(full, data[:n])
			if !reflect.DeepEqual(incremental.Replay(), full.Replay()) {
				t.Fatalf("replay differs at step %d, chunk %d", step, n)
			}
			data = data[n:]
		}
	}
}

func BenchmarkPrimaryScrollbackAppend(b *testing.B) {
	for _, fullScan := range []bool{true, false} {
		name := "incremental"
		if fullScan {
			name = "full-scan"
		}
		b.Run(name, func(b *testing.B) {
			term := &Terminal{
				size:            goprotocol.TerminalSize{Columns: 167, Lines: 33},
				scrollbackLines: 10000,
			}
			history := strings.Repeat("\x1b[32m"+strings.Repeat("x", 790)+"\x1b[0m\n", 10000)
			publishReplay(term, history)
			const output = "\x1b[32mshort incremental output\x1b[0m\n"
			b.ReportAllocs()
			b.SetBytes(int64(len(output)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if fullScan {
					term.primaryRedrawCheck = true
				}
				publishReplay(term, output)
			}
		})
	}
}

func TestPrimaryScrollbackSurvivesAltScreenFlood(t *testing.T) {
	term := &Terminal{
		size:            goprotocol.TerminalSize{Columns: 80, Lines: 24},
		scrollbackLines: 50,
		subs:            map[uint64]*subscriber{},
	}
	var primary strings.Builder
	for i := 0; i < 10; i++ {
		line := strings.Repeat("shell", i) + "\n"
		primary.WriteString(line)
		publishReplay(term, line)
	}
	before := primary.String()
	publishReplay(term, "\x1b[?1049h"+strings.Repeat("ALT-REDRAW\n", 5000)+"\x1b[?1049l")
	publishReplay(term, "after-tui\n")

	got := replayText(term)
	if !strings.Contains(got, before) {
		t.Fatalf("primary scrollback was truncated by the TUI:\n%s", got)
	}
	if !strings.Contains(got, "after-tui\n") {
		t.Fatalf("post-TUI output missing: %q", got)
	}
	if strings.Contains(got, "ALT-REDRAW") {
		t.Fatalf("alternate-screen redraw was kept after the TUI exited: %q", got[:min(200, len(got))])
	}
	if !strings.Contains(got, "\x1b[?1049h") || !strings.Contains(got, "\x1b[?1049l") {
		t.Fatalf("alt-screen enter/leave missing, replay can stick off the primary buffer: %q", got)
	}
	if term.outputNewlines != strings.Count(before, "\n")+1 {
		t.Fatalf("alt-screen newlines counted as scrollback: %d", term.outputNewlines)
	}
	emu := govt.New(80, 24, 50)
	defer emu.Close()
	emu.WriteReplay([]byte(got))
	snap := emu.Snapshot()
	if snap.AltScreen {
		t.Fatal("replay left the emulator on the alternate screen")
	}
	var screen strings.Builder
	for _, row := range snap.RowsData {
		for _, cell := range row.Cells {
			screen.WriteString(cell.Text)
		}
		screen.WriteByte('\n')
	}
	if !strings.Contains(screen.String(), "after-tui") || !strings.Contains(screen.String(), "shell") {
		t.Fatalf("restored screen lost primary scrollback: %q", screen.String())
	}
	if strings.Contains(screen.String(), "ALT-REDRAW") {
		t.Fatal("restored screen still shows alternate-screen redraw")
	}
}

func TestRunningTUIDoesNotEvictPrimaryLines(t *testing.T) {
	term := &Terminal{
		size:            goprotocol.TerminalSize{Columns: 80, Lines: 24},
		scrollbackLines: 20,
		subs:            map[uint64]*subscriber{},
	}
	publishReplay(term, "keep-me\n")
	publishReplay(term, "\x1b[?1049h"+strings.Repeat("\x1b[Hframe", 100000))
	got := replayText(term)
	if !strings.Contains(got, "keep-me\n") {
		t.Fatal("entering a TUI dropped primary scrollback")
	}
	if len(term.altReplay) > screenReplayLimit(80, 24) {
		t.Fatalf("live alt screen kept %d bytes", len(term.altReplay))
	}
	if len(term.altReplay) > 0 && !bytes.Contains(term.altReplay, []byte("frame")) {
		t.Fatal("live alt screen dropped its current frame")
	}
}

func TestScrollbackLineTrimKeepsNewestPrimaryLines(t *testing.T) {
	term := &Terminal{
		size:            goprotocol.TerminalSize{Columns: 80, Lines: 2},
		scrollbackLines: 3,
		subs:            map[uint64]*subscriber{},
	}
	for i := 0; i < 20; i++ {
		publishReplay(term, fmt.Sprintf("L%02d\n", i))
	}
	got := replayText(term)
	if strings.Contains(got, "L00\n") || strings.Contains(got, "L10\n") {
		t.Fatalf("old lines kept: %q", got)
	}
	if !strings.Contains(got, "L19\n") || !strings.Contains(got, "L17\n") {
		t.Fatalf("newest scrollback missing: %q", got)
	}
	if term.outputNewlines > 3+2 {
		t.Fatalf("newlines %d exceed scrollback+viewport", term.outputNewlines)
	}
}

func TestPlainLongLineIsNotTreatedAsRedraw(t *testing.T) {
	term := &Terminal{
		size:            goprotocol.TerminalSize{Columns: 80, Lines: 24},
		scrollbackLines: 10,
		subs:            map[uint64]*subscriber{},
	}
	line := strings.Repeat("a", 80_000) + "\n"
	publishReplay(term, line)
	if replayText(term) != line {
		t.Fatal("plain scrollback line was truncated")
	}
}

func TestPrimaryRedrawDoesNotRemoveNeighboringLines(t *testing.T) {
	term := &Terminal{
		size:            goprotocol.TerminalSize{Columns: 40, Lines: 10},
		scrollbackLines: 20,
		subs:            map[uint64]*subscriber{},
	}
	publishReplay(term, "alpha\n")
	publishReplay(term, "beta\n")
	publishReplay(term, strings.Repeat("\x1b[H\x1b[2J", 20000))
	publishReplay(term, "gamma\n")
	got := replayText(term)
	if !strings.Contains(got, "alpha\n") || !strings.Contains(got, "beta\n") || !strings.Contains(got, "gamma\n") {
		t.Fatalf("lines around a primary redraw were lost: %q", got)
	}
	if len(got) > 256*1024 {
		t.Fatalf("primary redraw kept %d bytes", len(got))
	}
}

func TestAltEnterSplitAcrossChunks(t *testing.T) {
	term := &Terminal{
		size:            goprotocol.TerminalSize{Columns: 80, Lines: 24},
		scrollbackLines: 10,
		subs:            map[uint64]*subscriber{},
	}
	publishReplay(term, "prompt\n\x1b[?10")
	if term.altScreen {
		t.Fatal("partial CSI entered the alt screen")
	}
	publishReplay(term, "49h"+strings.Repeat("Z", 1000))
	if !term.altScreen {
		t.Fatal("split alt-screen enter was not recognized")
	}
	if !strings.Contains(replayText(term), "prompt\n") {
		t.Fatal("prompt lost across a split control sequence")
	}
}

func TestCapEscapeBytesDropsOpenOSC(t *testing.T) {
	data := []byte("visible\x1b]1337;File=name=x:")
	data = append(data, bytes.Repeat([]byte("A"), 1000)...)
	got := capEscapeBytes(data, 64)
	if bytes.Contains(got, []byte("\x1b]1337")) {
		t.Fatalf("open OSC kept: %q", got)
	}
	if !bytes.Contains(got, []byte("visible")) {
		t.Fatalf("plain prefix dropped: %q", got)
	}
}
