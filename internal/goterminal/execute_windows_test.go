//go:build windows

package goterminal

import (
	"context"
	"errors"
	"github.com/SurTeam/Water/internal/goagent"
	"github.com/SurTeam/Water/internal/goprotocol"
	"github.com/SurTeam/Water/internal/gopty"
	"github.com/SurTeam/Water/internal/gosupervisor"
	"github.com/google/uuid"
	"golang.org/x/sys/windows"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func windowsPowerShell(t *testing.T) *Terminal {
	t.Helper()
	program, err := exec.LookPath("powershell.exe")
	if err != nil {
		t.Fatal(err)
	}
	r := NewRegistry()
	t.Cleanup(r.CloseAll)
	term, err := r.Spawn(program, []string{"-NoLogo", "-NoProfile"}, goprotocol.TerminalSize{Columns: 160, Lines: 40})
	if err != nil {
		t.Fatal(err)
	}
	waitPowerShell(t, term)
	return term
}
func waitPowerShell(t *testing.T, term *Terminal) {
	t.Helper()
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		if term.CurrentDirectory() != "" {
			if fg, e := term.ForegroundGroup(); e == nil && fg == term.processID() {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("PowerShell did not become ready; cwd=%q", term.CurrentDirectory())
}
func TestWindowsExecutePowerShellStatusEnvironmentDirectory(t *testing.T) {
	term := windowsPowerShell(t)
	dir := filepath.Join(t.TempDir(), "中文 space")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	term.Write([]byte("$env:WATER_EXEC_VALUE='inherited_中文'; Set-Location -LiteralPath '" + psQuote(dir) + "'\r"))
	deadline := time.Now().Add(5 * time.Second)
	for term.CurrentDirectory() != dir && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if got := term.CurrentDirectory(); got != dir {
		t.Fatalf("cwd=%q want %q", got, dir)
	}
	result, err := term.Execute(context.Background(), ExecuteOptions{Command: "Write-Output $env:WATER_EXEC_VALUE; Write-Output $PWD.ProviderPath; exit 7", Timeout: 15 * time.Second})
	if err != nil {
		t.Fatalf("execute: %v; result=%+v", err, result)
	}
	if result.ExitCode == nil || *result.ExitCode != 7 || !strings.Contains(result.Text, "inherited_中文") || !strings.Contains(result.Text, dir) {
		var raw strings.Builder
		for _, ev := range term.Replay() {
			raw.Write(ev.Data)
		}
		t.Fatalf("result=%+v code=%v raw=%q", result, *result.ExitCode, raw.String())
	}
	if strings.Contains(result.Text, "WATER_START_") || strings.Contains(result.Text, "ReadAllText") {
		t.Fatalf("wrapper leaked into capture: %q", result.Text)
	}
	next, err := term.Execute(context.Background(), ExecuteOptions{Command: "Write-Output 'NEXT_OK'"})
	if err != nil || !strings.Contains(next.Text, "NEXT_OK") {
		t.Fatalf("shell did not survive: %+v %v", next, err)
	}
}
func TestWindowsExecuteTimeoutCancellationAndLimits(t *testing.T) {
	term := windowsPowerShell(t)
	// Warm the native helper separately so a tiny execution bound also tests
	// cancellation during command operation rather than compiler startup only.
	if _, err := term.Execute(context.Background(), ExecuteOptions{Command: "Write-Output 'WARM'"}); err != nil {
		t.Fatal(err)
	}
	result, err := term.Execute(context.Background(), ExecuteOptions{Command: "Write-Output 'BEFORE_TIMEOUT'; Start-Sleep -Seconds 30", Timeout: 1500 * time.Millisecond})
	if err != nil || !result.TimedOut || !strings.Contains(result.Text, "BEFORE_TIMEOUT") {
		var raw strings.Builder
		for _, ev := range term.Replay() {
			raw.Write(ev.Data)
		}
		t.Fatalf("timeout result=%+v err=%v raw=%q", result, err, raw.String())
	}
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(250*time.Millisecond, cancel)
	result, err = term.Execute(ctx, ExecuteOptions{Command: "Start-Sleep -Seconds 30"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%+v err=%v", result, err)
	}
	result, err = term.Execute(context.Background(), ExecuteOptions{Command: "1..30 | ForEach-Object { Write-Output ('ROW_'+$_) }", MaxRows: 3, MaxBytes: 24})
	if err != nil || !result.Truncated || len(result.Text) > 24 {
		t.Fatalf("bounds=%+v err=%v", result, err)
	}
}
func TestWindowsExecuteSupervisedAndResumed(t *testing.T) {
	server := gosupervisor.NewServer()
	socket := filepath.Join(t.TempDir(), "s.sock")
	if err := server.Listen(socket); err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	first := NewRegistry()
	if err := first.AttachSupervisor(socket); err != nil {
		t.Fatal(err)
	}
	program, _ := exec.LookPath("powershell.exe")
	key := uuid.New()
	size := goprotocol.TerminalSize{Columns: 160, Lines: 40}
	term, _, err := first.SpawnSupervised(key, program, []string{"-NoLogo", "-NoProfile"}, size, "", false)
	if err != nil {
		t.Fatal(err)
	}
	waitPowerShell(t, term)
	pid := term.processID()
	first.DetachSupervisor()
	second := NewRegistry()
	defer second.CloseAll()
	if err = second.AttachSupervisor(socket); err != nil {
		t.Fatal(err)
	}
	resumed, ok, err := second.SpawnSupervised(key, program, []string{"-NoLogo", "-NoProfile"}, size, "", true)
	if err != nil || !ok || resumed.processID() != pid {
		t.Fatalf("resume %v %v", ok, err)
	}
	result, err := resumed.Execute(context.Background(), ExecuteOptions{Command: "Write-Output 'RESUMED_EXECUTE'"})
	if err != nil || !strings.Contains(result.Text, "RESUMED_EXECUTE") {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestWindowsExecutionLeaf(t *testing.T) {
	if os.Getenv("WATER_EXECUTION_LEAF") == "" {
		return
	}
	time.Sleep(time.Minute)
	os.Exit(0)
}
func TestWindowsExecuteKillsOwnedDescendants(t *testing.T) {
	t.Setenv("WATER_EXECUTION_LEAF", "1")
	term := windowsPowerShell(t)
	pidFile := filepath.Join(t.TempDir(), "leaf-pid")
	// Start-Process descendants automatically join the nested command Job, even
	// when they create a new console. Cancellation cannot leave them running.
	command := "$p=Start-Process -FilePath '" + psQuote(os.Args[0]) + "' -ArgumentList '-test.run=^TestWindowsExecutionLeaf$' -PassThru; [IO.File]::WriteAllText('" + psQuote(pidFile) + "',[string]$p.Id); Start-Sleep -Seconds 30"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	handle := make(chan windows.Handle, 1)
	go func() {
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if b, e := os.ReadFile(pidFile); e == nil {
				pid, _ := strconv.Atoi(string(b))
				h, _ := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
				handle <- h
				cancel()
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		handle <- 0
		cancel()
	}()
	result, err := term.Execute(ctx, ExecuteOptions{Command: command, Timeout: 12 * time.Second})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%+v %v", result, err)
	}
	h := <-handle
	if h == 0 {
		t.Fatal("descendant was not observed")
	}
	defer windows.CloseHandle(h)
	if state, e := windows.WaitForSingleObject(h, 3000); e != nil || state != windows.WAIT_OBJECT_0 {
		t.Fatalf("owned descendant survived: %d %v", state, e)
	}
	if fg, e := term.ForegroundGroup(); e != nil || fg != term.processID() {
		t.Fatalf("pane shell did not survive: %d %v", fg, e)
	}
}
func TestWindowsForegroundRecognizesOwnedAgent(t *testing.T) {
	t.Setenv("WATER_WINDOWS_TERMINAL_CHILD", "1")
	term := windowsPowerShell(t)
	agent := filepath.Join(t.TempDir(), "codex.exe")
	data, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(agent, data, 0700); err != nil {
		t.Fatal(err)
	}
	if err = term.Write([]byte("& '" + psQuote(agent) + "' '-test.run=^TestWindowsTerminalChild$'\r")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		p, e := term.registry.ForegroundProcess(context.Background(), term.ID)
		if e == nil && p.Name == "codex" && p.PID != term.processID() && goagent.Detect(p.Name, p.Args) != nil {
			term.Write([]byte("finish\r"))
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("owned agent was not classified")
}

func TestWindowsExecuteLongCommandAndExitSemantics(t *testing.T) {
	term := windowsPowerShell(t)
	command := "Write-Output 'LONG_COMMAND_OK'\n#"
	command += strings.Repeat("x", MaxExecuteCommandBytes-len(command))
	result, err := term.Execute(context.Background(), ExecuteOptions{Command: command})
	if err != nil || !strings.Contains(result.Text, "LONG_COMMAND_OK") {
		t.Fatalf("long command: %+v %v", result, err)
	}
	for _, item := range []struct {
		command string
		code    int
	}{{"cmd.exe /c exit 9", 9}, {"throw 'expected failure'", 1}, {"cmd.exe /c exit 7; Write-Output 'SUCCESS_AFTER_NATIVE'", 0}} {
		result, err = term.Execute(context.Background(), ExecuteOptions{Command: item.command})
		if err != nil || result.ExitCode == nil || *result.ExitCode != item.code {
			t.Fatalf("%q result=%+v err=%v", item.command, result, err)
		}
	}
	if err := term.Resize(goprotocol.TerminalSize{Columns: 24, Lines: 20}); err != nil {
		t.Fatal(err)
	}
	result, err = term.Execute(context.Background(), ExecuteOptions{Command: "Write-Output 'NARROW_OK'"})
	if err != nil || !strings.Contains(result.Text, "NARROW_OK") {
		t.Fatalf("wrapped markers: %+v %v", result, err)
	}
}

func TestWindowsExecuteTinyTimeoutDoesNotRunLater(t *testing.T) {
	term := windowsPowerShell(t)
	target := filepath.Join(t.TempDir(), "must-not-run")
	result, err := term.Execute(context.Background(), ExecuteOptions{Command: "[IO.File]::WriteAllText('" + psQuote(target) + "','ran')", Timeout: time.Millisecond})
	if err != nil || !result.TimedOut {
		t.Fatalf("tiny timeout: %+v %v", result, err)
	}
	if _, err = os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("cancelled command ran")
	}
	next, err := term.Execute(context.Background(), ExecuteOptions{Command: "Write-Output 'AFTER_TINY_TIMEOUT'"})
	if err != nil || !strings.Contains(next.Text, "AFTER_TINY_TIMEOUT") {
		t.Fatalf("shell after tiny timeout: %+v %v", next, err)
	}
	if _, err = os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("cancelled command ran after returning")
	}
	p, err := gopty.InspectProcess(context.Background(), term.processID())
	if err != nil {
		t.Fatal(err)
	}
	cwdFile := gopty.CwdFile(p.PID, p.Started)
	if err = term.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(cwdFile); !os.IsNotExist(err) {
		t.Fatalf("cwd tracker survived terminal close: %v", err)
	}
}
