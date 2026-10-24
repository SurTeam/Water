package goterminal

import (
	"github.com/SurTeam/Water/internal/gosupervisor"
	"github.com/SurTeam/Water/internal/govt"
	"testing"
)

func TestLegacyRecoveryInputModes(t *testing.T) {
	modes := "\x1b[?1049h\x1b[?1003h\x1b[?1006h"
	for _, tc := range []struct {
		name    string
		attach  gosupervisor.Attach
		resumed bool
		input   string
		want    bool
	}{
		{"legacy", gosupervisor.Attach{Truncated: true}, true, modes, true},
		{"modern_empty_checkpoint", gosupervisor.Attach{Truncated: true, RestoreSupported: true}, true, modes, false},
		{"checkpoint_present", gosupervisor.Attach{Truncated: true, Restore: []byte("\x1b[?1000l")}, true, modes, false},
		{"complete_replay", gosupervisor.Attach{}, true, modes, false},
		{"new_process", gosupervisor.Attach{Truncated: true}, false, modes, false},
		{"arbitrary_output", gosupervisor.Attach{Truncated: true}, true, "hello", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seed := legacyRecoveryInputModes(tc.attach, tc.resumed, tc.input)
			if (seed != "") != tc.want {
				t.Fatalf("seed=%q want enabled=%v", seed, tc.want)
			}
			emu := govt.New(80, 24, 100)
			defer emu.Close()
			emu.WriteReplay([]byte(seed))
			ev := govt.MouseEvent{Col: 5, Row: 3, Button: govt.MouseWheel, Action: govt.MouseUp}
			if emu.Mouse(ev) != tc.want {
				t.Fatal("wheel acceptance differs from fallback guard")
			}
			emu.WriteReplay([]byte("\x1b[?1000l"))
			if emu.Mouse(ev) {
				t.Fatal("retained reset did not override fallback")
			}
		})
	}
}
