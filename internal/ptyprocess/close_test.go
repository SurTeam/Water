//go:build !windows

package ptyprocess

import (
	"context"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
)

func TestChildProcess(t *testing.T) {
	mode := os.Getenv("WATER_KILL_TEST")
	if mode == "" {
		return
	}
	signal.Ignore(syscall.SIGHUP, syscall.SIGTERM)
	if mode == "root" {
		for _, detached := range []bool{false, true} {
			cmd := exec.Command(os.Args[0], "-test.run=^TestChildProcess$")
			cmd.Env = append(os.Environ(), "WATER_KILL_TEST=leaf")
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: !detached, Setsid: detached}
			if err := cmd.Start(); err != nil {
				os.Exit(2)
			}
			suffix := "group"
			if detached {
				suffix = "session"
			}
			if err := os.WriteFile(filepath.Join(os.Getenv("WATER_KILL_DIR"), suffix), []byte(strconv.Itoa(cmd.Process.Pid)), 0600); err != nil {
				os.Exit(3)
			}
		}
	}
	for {
		time.Sleep(time.Hour)
	}
}

func TestKillSessionAndDetachedDescendants(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestChildProcess$")
	cmd.Env = append(os.Environ(), "WATER_KILL_TEST=root", "WATER_KILL_DIR="+dir)
	master, err := pty.Start(cmd)
	if err != nil {
		t.Fatal(err)
	}
	session := Capture(cmd.Process)
	defer master.Close()
	defer cmd.Wait()
	defer cmd.Process.Kill()
	neighbor := exec.Command("/bin/sh", "-c", "trap '' HUP TERM; exec sleep 60")
	other, err := pty.Start(neighbor)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	defer neighbor.Wait()
	defer neighbor.Process.Kill()
	var pids []int
	for _, name := range []string{"group", "session"} {
		deadline := time.Now().Add(5 * time.Second)
		for {
			data, e := os.ReadFile(filepath.Join(dir, name))
			if e == nil {
				pid, _ := strconv.Atoi(string(data))
				pids = append(pids, pid)
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("child not ready")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	for _, pid := range pids {
		defer syscall.Kill(pid, syscall.SIGKILL)
	}
	start := time.Now()
	if err := session.Kill(); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("cleanup exceeded bound")
	}
	// ps reports zombies until the system reaps adopted children; they are dead.
	for _, pid := range append(pids, cmd.Process.Pid) {
		deadline := time.Now().Add(3 * time.Second)
		for {
			out, e := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "stat=").Output()
			if e != nil || len(out) == 0 || out[0] == 'Z' {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("process %d survived: %s", pid, out)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	if err := syscall.Kill(neighbor.Process.Pid, 0); err != nil {
		t.Fatalf("neighbor killed: %v", err)
	}
}

func TestEnumerationFailureStillKillsChild(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "trap '' HUP TERM; exec sleep 60")
	master, err := pty.Start(cmd)
	if err != nil {
		t.Fatal(err)
	}
	session := Capture(cmd.Process)
	defer master.Close()
	defer cmd.Process.Kill()
	t.Setenv("PATH", t.TempDir())
	if err := session.Kill(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("fallback did not kill child")
	}
}

func TestSnapshotCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := snapshot(ctx); err == nil {
		t.Fatal("expected cancellation")
	}
}

func TestReapedRootDoesNotInspectReusedSession(t *testing.T) {
	root := exec.Command("/bin/sh", "-c", "exit 0")
	master, err := pty.Start(root)
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	session := Capture(root.Process)
	if session.started == "" {
		t.Fatal("root spawn identity missing")
	}
	if err := root.Wait(); err != nil {
		t.Fatal(err)
	}

	neighbor := exec.Command("/bin/sh", "-c", "exec sleep 60")
	other, err := pty.Start(neighbor)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	defer neighbor.Wait()
	defer neighbor.Process.Kill()

	// Deterministically model the historical PID being reused by a neighboring
	// session. The original os.Process remains reaped; it must not authorize any
	// discovery using its now-unrelated numeric PID.
	root.Process.Pid = neighbor.Process.Pid
	dir := t.TempDir()
	marker := filepath.Join(dir, "enumerated")
	if err := os.WriteFile(filepath.Join(dir, "ps"), []byte("#!/bin/sh\necho inspected > '"+marker+"'\nexec /bin/ps \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	if err := session.Kill(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("reaped root authorized enumeration: %v", err)
	}
	if err := neighbor.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("neighbor was killed: %v", err)
	}
}

func TestSpawnIdentityMismatchSkipsEnumeration(t *testing.T) {
	root := exec.Command("/bin/sh", "-c", "exec sleep 60")
	master, err := pty.Start(root)
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	defer root.Wait()
	defer root.Process.Kill()
	session := Capture(root.Process)
	// Model a current PID whose native identity differs from the retained spawn
	// identity. Direct Process.Kill is still permitted for the owned child.
	session.started = "different process generation"
	dir := t.TempDir()
	marker := filepath.Join(dir, "enumerated")
	if err := os.WriteFile(filepath.Join(dir, "ps"), []byte("#!/bin/sh\necho inspected > '"+marker+"'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	if err := session.Kill(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("mismatched identity authorized enumeration: %v", err)
	}
}
