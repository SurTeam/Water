package goui

import (
	"image"
	"testing"
	"time"

	"github.com/SurTeam/Water/internal/goconfig"
	"github.com/SurTeam/Water/internal/govt"
)

// Model a parser that owns both its lifecycle gate and emulator. A frame must
// return without observing partially parsed state or committing new geometry.
func TestNativeTerminalUpdatesDeferWhileParsing(t *testing.T) {
	for _, name := range []string{"restore", "resize", "cursor", "colors"} {
		resize := name == "resize"
		t.Run(name, func(t *testing.T) {
			emu := govt.New(20, 4, 100)
			defer emu.Close()
			term := &terminalClient{emu: emu, snapshot: emu.FrameSnapshot(), view: NewTerminalView(), cols: 20, rows: 4, cellWidth: 8, cellHeight: 16}
			term.viewStale.Store(true)
			update := func() { ensureTerminalView(term) }
			if resize {
				update = func() {
					(&EbitengineWindow{}).resizeTerminal(&WorkspaceClient{}, term, image.Rect(0, 0, 300, 100), 10, 20)
				}
			}
			if name == "cursor" {
				cfg := goconfig.AppConfig{}
				cfg.Terminal.CursorStyle = "bar"
				update = func() { syncTerminalCursor(term, cfg) }
			}
			if name == "colors" {
				update = func() { syncTerminalColors(term) }
			}
			term.parseMu.Lock()
			done := make(chan struct{})
			go func() { update(); close(done) }()
			select {
			case <-done:
			case <-time.After(time.Second):
				term.parseMu.Unlock()
				<-done
				t.Fatal("GUI waited for parsing")
			}
			if !term.viewStale.Load() || term.cellWidth != 8 || term.cellHeight != 16 || term.cols != 20 || term.rows != 4 || term.cursorStyle != "" {
				term.parseMu.Unlock()
				t.Fatal("busy GUI update committed pending state")
			}
			emu.Write([]byte("latest"))
			term.parseMu.Unlock()
			update()
			if name != "colors" && term.snapshot.RowsData[0].Cells[0].Text != "l" {
				t.Fatal("retry missed parsed output")
			}
			if resize {
				if term.cellWidth != 10 || term.cellHeight != 20 {
					t.Fatal("retry missed pixel metrics")
				}
				if term.cols != 20 || term.rows != 4 {
					t.Fatal("unfocused resize changed shared grid")
				}
			} else if name == "colors" {
				if term.defaultColors != themeColors(term.view.baseTheme) {
					t.Fatal("retry missed default colors")
				}
			} else if name == "cursor" {
				if term.cursorStyle != "bar" {
					t.Fatal("retry missed cursor defaults")
				}
			} else if term.viewStale.Load() {
				t.Fatal("retry left restored view stale")
			}
			// Covers success and no-op exits: neither may retain the lifecycle lock.
			update()
			if !term.parseMu.TryLock() {
				t.Fatal("GUI retained parser gate")
			}
			term.parseMu.Unlock()
			term.close()
			update()
			if !term.parseMu.TryLock() {
				t.Fatal("closed terminal retained parser gate")
			}
			term.parseMu.Unlock()
		})
	}
}

func TestNativePointerAndCopyCallbacksDoNotWaitForBusyPane(t *testing.T) {
	_, term := attachedScrollTestTerminal(t)
	term.parseMu.Lock()
	done := make(chan struct{})
	go func() {
		term.input.OnScroll(-1)
		term.input.OnMouse(govt.MouseEvent{})
		term.input.OnSelectionBoundaryStart(0, 0, false)
		term.input.OnSelectionStart(0, 0, 2)
		term.input.OnSelectionAutoScroll(0, 0, -1)
		_ = term.input.OnCopy()
		close(done)
	}()
	select {
	case <-done:
		term.parseMu.Unlock()
	case <-time.After(time.Second):
		term.parseMu.Unlock()
		<-done
		t.Fatal("pointer/copy callback waited for busy pane parser")
	}
}
