package goplatform

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestFileLockHelper(t *testing.T) {
	if os.Getenv("WATER_PLATFORM_LOCK_TEST") == "" {
		return
	}
	file, err := os.OpenFile(os.Getenv("WATER_PLATFORM_LOCK_TEST"), os.O_RDWR, 0600)
	if err != nil {
		os.Exit(2)
	}
	defer file.Close()
	if err := LockFile(file, true); err == nil {
		UnlockFile(file)
		os.Exit(3)
	}
	os.Exit(0)
}

func TestFileLockExcludesOtherProcessAndReleases(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := LockFile(file, false); err != nil {
		t.Fatal(err)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestFileLockHelper$")
	child.Env = append(os.Environ(), "WATER_PLATFORM_LOCK_TEST="+path)
	if out, err := child.CombinedOutput(); err != nil {
		t.Fatalf("cross-process exclusion: %s: %v", out, err)
	}
	second, err := os.OpenFile(path, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	acquired := make(chan error, 1)
	go func() { acquired <- LockFile(second, false) }()
	select {
	case err := <-acquired:
		t.Fatalf("blocking lock returned before release: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if err := UnlockFile(file); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-acquired:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("lock did not release")
	}
	if err := UnlockFile(second); err != nil {
		t.Fatal(err)
	}
}
