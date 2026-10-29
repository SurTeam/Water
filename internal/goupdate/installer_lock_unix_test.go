//go:build !windows

package goupdate

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/SurTeam/Water/internal/goclient"
	"github.com/SurTeam/Water/internal/goserver"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestGuardSurvivesHandoffAndCrash(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	f, err := acquireGuard(path)
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command("/bin/sh", "-c", "exec sleep 30")
	child.ExtraFiles = []*os.File{f}
	if err = child.Start(); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if other, e := acquireGuard(path); e == nil {
		other.Close()
		t.Fatal("inherited guard did not exclude competitor")
	}
	_ = child.Process.Kill()
	_ = child.Wait()
	other, e := acquireGuard(path)
	if e != nil {
		t.Fatal(e)
	}
	other.Close()
	if _, err = os.Stat(path + ".guard"); err != nil {
		t.Fatal("stable guard was removed", err)
	}
}

func TestRecoverVisibleLocks(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		legacy, active, uncertain bool
	}{
		{"dead owner", false, false, false}, {"live owner", false, true, false},
		{"legacy dead", true, false, false}, {"legacy active", true, true, false}, {"legacy uncertain", true, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			stage := filepath.Join(dir, ".water-update-fixture")
			os.Mkdir(stage, 0700)
			p := &Plan{Directory: stage, Executable: filepath.Join(dir, "water")}
			// A child reaped by this process supplies a genuinely dead PID.
			cmd := exec.Command("/bin/sh", "-c", "exit 0")
			if err := cmd.Run(); err != nil {
				t.Fatal(err)
			}
			pid := cmd.Process.Pid
			if tc.active {
				pid = os.Getpid()
			}
			data, _ := json.Marshal(struct{ PID int }{pid})
			if tc.legacy {
				data = nil
				os.WriteFile(filepath.Join(stage, "plan.json"), []byte("{}"), 0600)
				if !tc.uncertain {
					os.WriteFile(filepath.Join(stage, "installer.pid"), []byte(strconv.Itoa(pid)), 0600)
				}
			}
			os.WriteFile(lockPath(p), data, 0600)
			guard, err := acquireGuard(lockPath(p))
			if err != nil {
				t.Fatal(err)
			}
			defer guard.Close()
			err = recoverVisible(p)
			if want := tc.active || tc.uncertain; (err != nil) != want {
				t.Fatalf("recovery error=%v, want preserve=%v", err, want)
			}
			_, err = os.Stat(lockPath(p))
			if (err == nil) != (tc.active || tc.uncertain) {
				t.Fatal("unexpected visible lock state", err)
			}
		})
	}
}

func TestTakeoverFailureKeepsInstallation(t *testing.T) {
	for _, body := range []string{"exit 4", "exec sleep 30"} {
		t.Run(body, func(t *testing.T) {
			dir := t.TempDir()
			stage := filepath.Join(dir, "stage")
			os.Mkdir(stage, 0700)
			helper := filepath.Join(dir, "helper")
			os.WriteFile(helper, []byte("#!/bin/sh\n"+body+"\n"), 0700)
			gui := filepath.Join(dir, "water")
			os.WriteFile(gui, []byte("old"), 0700)
			p := &Plan{Directory: stage, Helper: helper, Executable: gui, ParentPID: os.Getpid(), Entries: []Entry{{filepath.Join(stage, "new"), gui}}}
			for attempt := 0; attempt < 2; attempt++ {
				if err := StartInstaller(p); err == nil {
					t.Fatal("unacknowledged helper accepted")
				}
				if _, err := os.Stat(filepath.Join(stage, filepath.Base(helper))); !os.IsNotExist(err) {
					t.Fatal("failed helper copy retained", err)
				}
			}
			b, _ := os.ReadFile(gui)
			if string(b) != "old" {
				t.Fatal("installation changed")
			}
			if _, err := os.Stat(lockPath(p)); !os.IsNotExist(err) {
				t.Fatal("visible lock retained", err)
			}
		})
	}
}

func TestLiveParentPreventsReplacement(t *testing.T) {
	dir := t.TempDir()
	stage := filepath.Join(dir, "stage")
	os.Mkdir(stage, 0700)
	helper, _ := os.Executable()
	gui := filepath.Join(dir, "water")
	os.WriteFile(gui, []byte("old"), 0700)
	replacement := filepath.Join(stage, "new")
	os.WriteFile(replacement, []byte("new"), 0700)
	p := &Plan{Directory: stage, Helper: helper, Executable: gui, ParentPID: os.Getpid(), Entries: []Entry{{replacement, gui}}}
	if err := StartInstaller(p); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(stage, "installer.pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(string(b))
	proc, _ := os.FindProcess(pid)
	defer proc.Kill()
	time.Sleep(150 * time.Millisecond)
	b, _ = os.ReadFile(gui)
	if string(b) != "old" {
		t.Fatal("live parent installation replaced")
	}
	// GUI startup recovery must not remove a lock still held by its helper.
	recoverStale(gui)
	if _, err = os.Stat(lockPath(p)); err != nil {
		t.Fatal("active lock removed", err)
	}
}

func TestRollbackFailureDisablesRestart(t *testing.T) {
	dir := t.TempDir()
	stage := filepath.Join(dir, "stage")
	os.Mkdir(stage, 0700)
	target := filepath.Join(dir, "water")
	source := filepath.Join(stage, "new")
	os.WriteFile(target, []byte("old"), 0700)
	os.WriteFile(source, []byte("new"), 0700)
	p := &Plan{Directory: stage, Entries: []Entry{{source, target}}}
	launched := 0
	err := replace(p, func() error {
		launched++
		os.Mkdir(source, 0700)
		os.WriteFile(filepath.Join(source, "block"), []byte("block"), 0600)
		return fmt.Errorf("startup failed")
	})
	if !errors.Is(err, errRollbackFailed) || canRestartRestored(err) {
		t.Fatalf("unsafe restart after rollback failure: %v", err)
	}
	if launched != 1 {
		t.Fatalf("launch count=%d", launched)
	}
	if _, err := os.Stat(filepath.Join(stage, "backup-0")); err != nil {
		t.Fatal("backup lost", err)
	}
}

func TestInstallerKeeperProtectsLegacyCleanup(t *testing.T) {
	path := filepath.Join("/tmp", "water-keeper-"+strconv.Itoa(os.Getpid())+"-"+strconv.FormatInt(time.Now().UnixNano(), 36)+".sock")
	server := goserver.New(path)
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServe() }()
	defer func() { server.Close(); <-done; os.Remove(path + ".lock") }()
	client := goclient.New(path)
	deadline := time.Now().Add(3 * time.Second)
	for {
		if err := client.CallTimeout("ping", nil, nil, time.Second); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("server not ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	old, err := client.OpenSession()
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	keeper, err := openInstallerKeeper([]string{"--control-socket=" + path})
	if err != nil {
		t.Fatal(err)
	}
	defer keeper.Close()
	if err := old.Call("session.release", map[string]bool{"shutdown_if_last": true}, nil); err != nil {
		t.Fatal(err)
	}
	old.Close()
	if err := keeper.Call("ping", nil, nil); err != nil {
		t.Fatal("legacy cleanup killed server", err)
	}
	keeper.Close()
	if err := client.CallTimeout("ping", nil, nil, time.Second); err != nil {
		t.Fatal("keeper close shut down server", err)
	}
	legacy, err := client.OpenSession()
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()
	dir := t.TempDir()
	stage := filepath.Join(dir, "stage")
	os.Mkdir(stage, 0700)
	helper, _ := os.Executable()
	gui := filepath.Join(dir, "water")
	os.WriteFile(gui, []byte("old"), 0700)
	plan := &Plan{Directory: stage, Helper: helper, Executable: gui, ParentPID: os.Getpid(), Arguments: []string{"--control-socket", path}, Entries: []Entry{{filepath.Join(stage, "new"), gui}}}
	if err := StartInstaller(plan); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(stage, "installer.pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(string(b))
	proc, _ := os.FindProcess(pid)
	defer proc.Kill()
	if err := legacy.Call("session.release", map[string]bool{"shutdown_if_last": true}, nil); err != nil {
		t.Fatal(err)
	}
	legacy.Close()
	if err := client.CallTimeout("ping", nil, nil, time.Second); err != nil {
		t.Fatal("helper did not retain server during old GUI cleanup", err)
	}
	_ = proc.Kill()
	limit := time.Now().Add(time.Second)
	for processAlive(pid) && time.Now().Before(limit) {
		time.Sleep(10 * time.Millisecond)
	}
	if err := client.CallTimeout("ping", nil, nil, time.Second); err != nil {
		t.Fatal("helper disconnect shut down server", err)
	}
	if _, err := openInstallerKeeper([]string{"--socket", path + "-missing"}); err == nil {
		t.Fatal("missing explicit socket accepted")
	}
}

func TestHelperRequiresKeeperBeforeTakeover(t *testing.T) {
	dir := t.TempDir()
	stage := filepath.Join(dir, "stage")
	os.Mkdir(stage, 0700)
	helper, _ := os.Executable()
	gui := filepath.Join(dir, "water")
	os.WriteFile(gui, []byte("old"), 0700)
	p := &Plan{Directory: stage, Helper: helper, Executable: gui, ParentPID: os.Getpid(), Arguments: []string{"--socket", filepath.Join(dir, "missing.sock")}, Entries: []Entry{{filepath.Join(stage, "new"), gui}}}
	if err := StartInstaller(p); err == nil {
		t.Fatal("helper took over without required server keeper")
	}
	if _, err := os.Stat(filepath.Join(stage, filepath.Base(helper))); !os.IsNotExist(err) {
		t.Fatal("helper not cleaned", err)
	}
	p.Arguments = nil
	if err := StartInstaller(p); err != nil {
		t.Fatal("same plan could not retry after keeper failure", err)
	}
	b, err := os.ReadFile(filepath.Join(stage, "installer.pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(string(b))
	proc, _ := os.FindProcess(pid)
	_ = proc.Kill()
}
