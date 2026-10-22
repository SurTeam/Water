package goterminal

import (
	"context"
	"errors"
	"github.com/SurTeam/Water/internal/goagent"
	"github.com/google/uuid"
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

func TestForegroundKeepsValidRecordsFromPartialPSFailure(t *testing.T) {
	output := []byte("123 123 Tue Oct 13 00:00:00 2026 /usr/local/bin/my-agent --work\n124 123 Tue Oct 13 00:00:00 2026 sleep 30\ninvalid row\n")
	queryErr := errors.New("one requested process disappeared")
	processes, err := parseForegroundOutput(output, queryErr, nil)
	if err != nil || len(processes) != 1 || processes[123].Name != "my-agent" || processes[123].Started == "" {
		t.Fatalf("valid record lost after partial failure: %v %#v", err, processes)
	}
	if _, err = parseForegroundOutput(nil, queryErr, nil); err == nil {
		t.Fatal("query failure without output was ignored")
	}
	if _, err = parseForegroundOutput(output, queryErr, context.DeadlineExceeded); err == nil {
		t.Fatal("deadline failure was ignored")
	}
}

func TestForegroundProcessSamplesOnlyTarget(t *testing.T) {
	r := NewRegistry()
	defer r.CloseAll()
	target, err := r.Spawn("/bin/sleep", []string{"30"}, goprotocol.TerminalSize{Columns: 80, Lines: 24})
	if err != nil {
		t.Fatal(err)
	}
	// A live unrelated terminal with no PTY would panic if queried. It must not
	// participate in a targeted sample, regardless of map iteration order.
	unrelated := uuid.New()
	r.mu.Lock()
	r.terms[unrelated] = &Terminal{ID: unrelated, closed: make(chan struct{})}
	r.mu.Unlock()
	defer func() { r.mu.Lock(); delete(r.terms, unrelated); r.mu.Unlock() }()
	p, err := r.ForegroundProcess(context.Background(), target.ID)
	if err != nil || p.PID == 0 {
		t.Fatalf("target sample: %#v %v", p, err)
	}
}

func TestForegroundSamplingDeadlineIncludesGroupQuery(t *testing.T) {
	term := &Terminal{closed: make(chan struct{})}
	start := time.Now()
	_, err := sampleTerminalsWithQuery(context.Background(), []*Terminal{term}, func(_ *Terminal, ctx context.Context) (int, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > time.Second {
			t.Error("group query has no sampling budget")
		}
		<-ctx.Done()
		return 0, ctx.Err()
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline lost: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("sampling took %s", elapsed)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = sampleTerminalsWithQuery(ctx, nil, func(*Terminal, context.Context) (int, error) { t.Fatal("canceled query ran"); return 0, nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
}
