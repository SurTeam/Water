package goupdate

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func TestStartupACKEnvironment(t *testing.T) {
	if os.Getenv("WATER_TEST_ACK_CHILD") == "1" {
		if _, present := os.LookupEnv("WATER_UPDATE_ACK"); present {
			t.Fatal("startup ACK capability remains in GUI environment")
		}
		child := exec.Command(os.Args[0], "-test.run=^TestStartupACKChildEnvironment$")
		if out, err := child.CombinedOutput(); err != nil {
			t.Fatalf("child environment: %v: %s", err, out)
		}
		path := os.Getenv("WATER_TEST_ACK_PATH")
		pid := strconv.Itoa(os.Getpid())
		if b, err := os.ReadFile(path + ".pid"); err != nil || string(b) != pid {
			t.Fatalf("startup PID marker=%q, error=%v", b, err)
		}
		if !PreserveServer() {
			t.Fatal("server protection released before ACK")
		}
		var workers sync.WaitGroup
		for i := 0; i < 32; i++ {
			workers.Add(1)
			go func() { defer workers.Done(); AcknowledgeStartup() }()
		}
		workers.Wait()
		if b, err := os.ReadFile(path); err != nil || string(b) != pid {
			t.Fatalf("GUI could not ACK captured private path: %q, %v", b, err)
		}
		if PreserveServer() {
			t.Fatal("successful ACK retained server protection")
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		AcknowledgeStartup()
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("duplicate frame rewrote ACK", err)
		}
		return
	}
	path := filepath.Join(t.TempDir(), "ready")
	cmd := exec.Command(os.Args[0], "-test.run=^TestStartupACKEnvironment$")
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "WATER_UPDATE_ACK=") && !strings.HasPrefix(entry, PreserveServerEnv+"=") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "WATER_TEST_ACK_CHILD=1", "WATER_TEST_ACK_PATH="+path, "WATER_UPDATE_ACK="+path, PreserveServerEnv+"=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("startup subprocess: %v: %s", err, out)
	}
}

func TestStartupACKChildEnvironment(t *testing.T) {
	if _, present := os.LookupEnv("WATER_UPDATE_ACK"); present {
		t.Fatal("child inherited GUI startup ACK capability")
	}
	if startupACK.path != "" {
		t.Fatal("child captured an inherited ACK path")
	}
}
