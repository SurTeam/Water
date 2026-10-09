package goserver

import (
	"github.com/SurTeam/Water/internal/goconfig"
	"net/http"
	"path/filepath"
	"testing"
	"time"
)

func TestWebAutostartPreferenceKeepsPortAndRunningListener(t *testing.T) {
	s, client, origin := webFixture(t)
	s.ConfigPath = filepath.Join(t.TempDir(), "config.json")
	cfg := goconfig.Default()
	cfg.Web = s.web.config()
	if err := goconfig.Save(s.ConfigPath, cfg); err != nil {
		t.Fatal(err)
	}
	listener := s.web.listener
	for _, enabled := range []bool{true, false, true} {
		web := s.web.config()
		web.Enabled = enabled
		if err := s.web.configure(web); err != nil {
			t.Fatal(err)
		}
		if s.web.listener != listener || s.web.status().State != "running" {
			t.Fatal("saving startup preference restarted the listener")
		}
		saved, err := goconfig.Load(s.ConfigPath)
		if err != nil || saved.Web.Enabled != enabled || saved.Web.Origin() != origin {
			t.Fatalf("saved Web=%+v err=%v", saved.Web, err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	saved, err := goconfig.Load(s.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	restarted := NewWithConfig(filepath.Join("/tmp", "water-web-restart-"+s.InstanceID.String()+".sock"), saved)
	restarted.ConfigPath = s.ConfigPath
	done := make(chan error, 1)
	go func() { done <- restarted.ListenAndServe() }()
	t.Cleanup(func() {
		_ = restarted.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(time.Second):
			t.Error("restart cleanup timed out")
		}
	})
	deadline := time.Now().Add(3 * time.Second)
	for {
		response, err := client.Get(origin + "/")
		if err == nil {
			response.Body.Close()
			if response.StatusCode != http.StatusOK {
				t.Fatalf("restart HTTP status %d", response.StatusCode)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Web did not autostart at persisted port: %v", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if restarted.web.status().PublicURL != origin {
		t.Fatal("restart changed saved address/port")
	}
}
