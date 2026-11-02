package gouiapp

import (
	"bytes"
	"encoding/json"
	"github.com/SurTeam/Water/internal/gobuild"
	"github.com/SurTeam/Water/internal/goclient"
	"github.com/SurTeam/Water/internal/goconfig"
	"github.com/SurTeam/Water/internal/goserver"
	"github.com/google/uuid"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestConcurrentWindowsAttachToIndependentServer(t *testing.T) {
	socket := filepath.Join("/tmp", "water-window-start-"+uuid.NewString()+".sock")
	t.Cleanup(func() { os.Remove(socket + ".lock") })
	cfg := goconfig.Default()
	cfg.Server.Detached = false
	cfg.Startup.InitialTerminal = false
	srv := goserver.NewWithConfig(socket, cfg)
	defer srv.Close()
	go srv.ListenAndServe()
	first, err := waitForSession(socket, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	type result struct {
		session *goclient.Session
		server  *goserver.Server
		err     error
	}
	results := make(chan result, 2)
	start := make(chan struct{})
	for range 2 {
		go func() {
			<-start
			session, server, _, err := connectOrStart(socket, "", cfg, gobuild.Variant)
			results <- result{session, server, err}
		}()
	}
	close(start)
	owners := 0
	for range 2 {
		r := <-results
		if r.err != nil {
			t.Fatal(r.err)
		}
		t.Cleanup(func() { r.session.Close() })
		if r.server != nil {
			owners++
			t.Cleanup(func() { r.server.Close() })
		}
	}
	if owners != 0 {
		t.Fatalf("GUI acquired server ownership: %d", owners)
	}
}

// This integration check uses a built server without requiring a display.
func TestLegacyConfigStartsIndependentServer(t *testing.T) {
	binary := os.Getenv("WATER_TEST_SERVER_BINARY")
	if binary == "" {
		t.Skip("set WATER_TEST_SERVER_BINARY to a matching built server")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Dir(binary)+string(os.PathListSeparator)+os.Getenv("PATH"))
	dir := t.TempDir()
	socket := filepath.Join("/tmp", "water-independent-"+uuid.NewString()+".sock")
	cfg := goconfig.Default()
	cfg.Server.Detached = false
	cfg.Server.DetachOnQuit = false
	cfg.Startup.InitialTerminal = false
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(configPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	session, embedded, started, err := connectOrStart(socket, configPath, cfg, gobuild.Variant)
	if err != nil {
		t.Fatal(err)
	}
	original := session.Server
	t.Cleanup(func() {
		client := goclient.New(socket)
		info, err := client.Inspect(time.Second)
		if err != nil || info.InstanceID != original.InstanceID || info.ServerPID != original.ServerPID {
			t.Errorf("owned server identity changed: %v", err)
			return
		}
		if err := client.Call("server.shutdown", map[string]any{}, nil); err != nil {
			t.Error(err)
		}
		if err := goclient.WaitForShutdown(client, original.InstanceID, 5*time.Second); err != nil {
			t.Error(err)
		}
		os.Remove(socket + ".lock")
	})
	if embedded != nil || !started || original.ServerPID == os.Getpid() {
		t.Fatal("legacy config did not start an independent server")
	}
	var before json.RawMessage
	if err := session.Call("state.dump", map[string]any{}, &before); err != nil {
		t.Fatal(err)
	}
	operations := &serverOperations{cfg: cfg, configPath: configPath}
	operations.closeSession(session, nil)()
	next, embedded, started, err := connectOrStart(socket, configPath, cfg, gobuild.Variant)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	if embedded != nil || started || next.Server.InstanceID != original.InstanceID || next.Server.ServerPID != original.ServerPID {
		t.Fatal("GUI session restart replaced or started a server")
	}
	var after json.RawMessage
	if err := next.Call("state.dump", map[string]any{}, &after); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("GUI session restart changed model: %s -> %s", before, after)
	}
	t.Logf("independent server pid=%d instance=%s retained across session restart", original.ServerPID, original.InstanceID)
}
