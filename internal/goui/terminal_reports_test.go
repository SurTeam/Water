package goui

import (
	"github.com/SurTeam/Water/internal/govt"
	"testing"
)

func TestMetricsIgnoreClosedTerminalDuringReplacement(t *testing.T) {
	term := &terminalClient{emu: govt.New(80, 24, 100)}
	setTerminalWindowMetrics(term, govt.WindowMetrics{Width: 800, Height: 600})
	term.close()
	setTerminalWindowMetrics(term, govt.WindowMetrics{Width: 900, Height: 700})
}
