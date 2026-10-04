package goterminal

import (
	"context"
	"github.com/SurTeam/Water/internal/goagent"
	"os/exec"
	"testing"
	"time"

	"github.com/SurTeam/Water/internal/goprotocol"
)

func TestForegroundRecognizesFourAgentPTYJobs(t *testing.T) {
	shell, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh required")
	}
	if _, err := exec.LookPath("/opt/homebrew/bin/zsh"); err == nil {
		shell = "/opt/homebrew/bin/zsh"
	}
	for _, name := range []string{"claude", "pi", "codex", "opencode"} {
		t.Run(name, func(t *testing.T) {
			r := NewRegistry()
			defer r.CloseAll()
			term, err := r.Spawn(shell, []string{"-f", "-c", "exec -a " + name + " /bin/sleep 30"}, goprotocol.TerminalSize{Columns: 80, Lines: 24})
			if err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				if got := r.ForegroundNames(context.Background())[term.ID]; got == name {
					if goagent.Detect(got, nil) == nil {
						t.Fatal("foreground not classified")
					}
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
			t.Fatal("PTY process not recognized", name)
		})
	}
}

func TestForegroundNameFollowsPTYJobAndReturnsToShell(t *testing.T) {
	r := NewRegistry()
	term, err := r.Spawn("/bin/sh", []string{"-i"}, goprotocol.TerminalSize{Columns: 80, Lines: 24})
	if err != nil {
		t.Fatal(err)
	}
	defer r.CloseAll()
	waitName := func(name string) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if got := r.ForegroundNames(context.Background())[term.ID]; got == name {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("foreground never became %s", name)
	}
	waitName("sh")
	if err := term.Write([]byte("sleep 30\n")); err != nil {
		t.Fatal(err)
	}
	waitName("sleep")
	if err := term.Write([]byte{3}); err != nil {
		t.Fatal(err)
	}
	waitName("sh")
}
