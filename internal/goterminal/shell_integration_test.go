package goterminal

import (
	"bytes"
	"github.com/SurTeam/Water/internal/goprotocol"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestZshForwardDeletePreservesStartupAndCustomBinding(t *testing.T) {
	zsh, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh unavailable")
	}
	for _, custom := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing", true: "custom"}[custom], func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("ZDOTDIR", dir)
			rc := "bindkey -e\nPS1='WATER_READY> '\n"
			if custom {
				rc += "bindkey -M emacs $'\\e[3~' backward-delete-char\n"
			}
			if err := os.WriteFile(filepath.Join(dir, ".zshenv"), []byte("export WATER_STARTUP_SEEN=yes\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, ".zshrc"), []byte(rc), 0600); err != nil {
				t.Fatal(err)
			}
			r := NewRegistry()
			term, err := r.Spawn(zsh, []string{"-i"}, goprotocol.TerminalSize{Columns: 100, Lines: 12})
			if err != nil {
				t.Fatal(err)
			}
			events, done, cancel := term.Subscribe()
			t.Cleanup(func() {
				_ = term.Close()
				timer := time.NewTimer(3 * time.Second)
				defer timer.Stop()
				for {
					select {
					case ev := <-events:
						if ev.Kind == goprotocol.ExitEvent {
							cancel()
							r.CloseAll()
							return
						}
					case <-timer.C:
						cancel()
						r.CloseAll()
						t.Errorf("terminal did not exit during cleanup")
						return
					}
				}
			})
			var output bytes.Buffer
			wait := func(marker string) {
				t.Helper()
				timer := time.NewTimer(3 * time.Second)
				defer timer.Stop()
				for !strings.Contains(output.String(), marker) {
					select {
					case ev := <-events:
						if ev.Kind == goprotocol.OutputEvent {
							output.Write(ev.Data)
						}
					case <-done:
						t.Fatalf("shell exited: %q", output.String())
					case <-timer.C:
						t.Fatalf("timeout %s: %q", marker, output.String())
					}
				}
			}
			wait("WATER_READY>")
			if err := term.Write([]byte("printf 'RESULT:%s\\n' abXc\x1b[D\x1b[D\x1b[3~\r")); err != nil {
				t.Fatal(err)
			}
			want := "RESULT:abc"
			if custom {
				want = "RESULT:aXc"
			}
			wait(want)
			if err := term.Write([]byte("printf 'ENV:%s:%s\\n' $WATER_STARTUP_SEEN $ZDOTDIR\r")); err != nil {
				t.Fatal(err)
			}
			wait("ENV:yes:" + dir)
		})
	}
}
