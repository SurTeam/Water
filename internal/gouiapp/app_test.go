package gouiapp

import (
	"github.com/SurTeam/Water/internal/gobuild"
	"github.com/SurTeam/Water/internal/goclient"
	"github.com/SurTeam/Water/internal/goconfig"
	"github.com/SurTeam/Water/internal/goserver"
	"github.com/google/uuid"
	"os"
	"path/filepath"
	"testing"
)

func TestConcurrentWindowsAttachToOneEmbeddedServer(t *testing.T) {
	socket := filepath.Join("/tmp", "water-window-start-"+uuid.NewString()+".sock")
	t.Cleanup(func() { os.Remove(socket + ".lock") })
	cfg := goconfig.Default()
	cfg.Server.Detached = false
	cfg.Startup.InitialTerminal = false
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
	if owners != 1 {
		t.Fatalf("embedded owners=%d want=1", owners)
	}
}
