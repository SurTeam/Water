//go:build !windows

package gouiapp

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/SurTeam/Water/internal/goclient"
	"github.com/SurTeam/Water/internal/goconfig"
	"github.com/SurTeam/Water/internal/goserver"
	"github.com/SurTeam/Water/internal/goui"
	"github.com/SurTeam/Water/internal/goupdate"
	"github.com/google/uuid"
)

func TestUpdateTakeoverCleanup(t *testing.T) {
	if os.Getenv("WATER_TEST_TAKEOVER_CHILD") != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestUpdateTakeoverCleanup$", "-test.v")
		cmd.Env = append(os.Environ(), "WATER_TEST_TAKEOVER_CHILD=1", goupdate.PreserveServerEnv+"=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("takeover fixture: %v\n%s", err, out)
		}
		return
	}
	if !goupdate.PreserveServer() || os.Getenv(goupdate.PreserveServerEnv) != "" {
		t.Fatal("takeover flag missing or leaked to children")
	}
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pane.pid")
	cfg := goconfig.Default()
	cfg.Server.DetachOnQuit = false
	cfg.Shell.Program = "/bin/sh"
	cfg.Shell.Args = []string{"-c", `echo $$ > "$1"; exec cat`, "water-test", pidFile}
	socket := filepath.Join("/tmp", "water-takeover-"+uuid.NewString()+".sock")
	srv := goserver.NewWithConfig(socket, cfg)
	if err := srv.Initialize(true, true); err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	defer os.Remove(socket + ".lock")
	go srv.ListenAndServe()
	session, err := waitForSession(socket, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var before json.RawMessage
	if err := session.CallTimeout("state.dump", map[string]any{}, &before, time.Second); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	var pid int
	for time.Now().Before(deadline) {
		raw, _ := os.ReadFile(pidFile)
		pid, _ = strconv.Atoi(strings.TrimSpace(string(raw)))
		if pid > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pid <= 0 {
		t.Fatal("owned pane did not start")
	}
	w := &goui.EbitengineWindow{}
	configPath := filepath.Join(dir, "config.json")
	configJSON, _ := json.Marshal(cfg)
	if err := os.WriteFile(configPath, configJSON, 0600); err != nil {
		t.Fatal(err)
	}
	o := &serverOperations{cfg: cfg, window: w, configPath: configPath}
	// This is the deferred cleanup used when GUI initialization fails.
	o.closeSession(session, true, nil)()
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("pane process lost during failed startup: %v", err)
	}
	next, err := goclient.New(socket).OpenSession()
	if err != nil {
		t.Fatalf("server lost during failed startup: %v", err)
	}
	defer next.Close()
	var after json.RawMessage
	if err := next.CallTimeout("state.dump", map[string]any{}, &after, time.Second); err != nil {
		t.Fatal(err)
	}
	var a, b map[string]any
	_ = json.Unmarshal(before, &a)
	_ = json.Unmarshal(after, &b)
	if stringMustJSON(a["workspaces"]) != stringMustJSON(b["workspaces"]) {
		t.Fatalf("layout changed across startup cleanup: %s -> %s", before, after)
	}
	if err := shutdownLocalServer(w, next); err == nil {
		t.Fatal("explicit shutdown bypassed takeover protection")
	}
	goupdate.StartupAcknowledged()
	if goupdate.PreserveServer() {
		t.Fatal("successful ACK did not release protection")
	}
	o.closeSession(next, true, nil)()
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(socket); os.IsNotExist(err) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("normal detach_on_quit=false cleanup no longer shuts down server")
}

func stringMustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }
