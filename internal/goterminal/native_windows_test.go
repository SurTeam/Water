//go:build windows

package goterminal

import (
	"fmt"
	"github.com/SurTeam/Water/internal/goprotocol"
	"os"
	"strings"
	"testing"
	"time"
)

func TestWindowsTerminalChild(t *testing.T) {
	if os.Getenv("WATER_WINDOWS_TERMINAL_CHILD") == "" {
		return
	}
	fmt.Println("READY_TERMINAL")
	var line string
	fmt.Scanln(&line)
	fmt.Println("FINAL_TERMINAL:" + line)
	os.Exit(0)
}
func TestWindowsTerminalOrderedResizeAndExit(t *testing.T) {
	t.Setenv("WATER_WINDOWS_TERMINAL_CHILD", "1")
	registry := NewRegistry()
	defer registry.CloseAll()
	term, err := registry.Spawn(os.Args[0], []string{"-test.run=^TestWindowsTerminalChild$"}, goprotocol.TerminalSize{Columns: 80, Lines: 24})
	if err != nil {
		t.Fatal(err)
	}
	events, _, cancel := term.Subscribe()
	defer cancel()
	var out strings.Builder
	deadline := time.After(8 * time.Second)
	ready := false
	for !ready {
		select {
		case ev := <-events:
			if ev.Kind == goprotocol.OutputEvent {
				out.Write(ev.Data)
				ready = strings.Contains(out.String(), "READY_TERMINAL")
			}
		case <-deadline:
			t.Fatal("no startup output")
		}
	}
	if err := term.ResizeWithCells(goprotocol.TerminalSize{Columns: 100, Lines: 32}, 10, 20); err != nil {
		t.Fatal(err)
	}
	if err := term.Write([]byte("nativeinput\r")); err != nil {
		t.Fatal(err)
	}
	sawResize := false
	for {
		select {
		case ev := <-events:
			switch ev.Kind {
			case goprotocol.ResizeEvent:
				sawResize = true
			case goprotocol.OutputEvent:
				out.Write(ev.Data)
			case goprotocol.ExitEvent:
				if !sawResize || !strings.Contains(out.String(), "FINAL_TERMINAL:nativeinput") {
					t.Fatalf("unordered stream resize=%v output=%q", sawResize, out.String())
				}
				return
			}
		case <-deadline:
			t.Fatalf("no exit, output %q", out.String())
		}
	}
}
