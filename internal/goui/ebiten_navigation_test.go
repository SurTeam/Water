package goui

import (
	"encoding/json"
	"github.com/SurTeam/Water/internal/goclient"
	"github.com/SurTeam/Water/internal/goconfig"
	"github.com/SurTeam/Water/internal/gomodel"
	"github.com/google/uuid"
	"testing"
	"time"
)

func TestNativeWorkspaceNavigationRoutesAcrossOwningConnections(t *testing.T) {
	socket1, stop1 := startWorkspaceTestServer(t)
	defer stop1()
	socket2, stop2 := startWorkspaceTestServer(t)
	defer stop2()
	cfg := goconfig.Default()
	cfg.UI.WorkspaceNavigationAcrossHosts = true
	manager := NewMultiWorkspaceClient(nil)
	defer manager.Close()
	var clients []*WorkspaceClient
	var ids []uuid.UUID
	for i, socket := range []string{socket1, socket2} {
		session, err := goclient.New(socket).OpenSession()
		if err != nil {
			t.Fatal(err)
		}
		view := NewWorkspaceClientWithConfig(session, nil, cfg)
		id := uuid.New()
		if err := manager.AddConnection(ConnectionEntry{ID: id, Name: socket, Kind: "local"}, view, func() { _ = session.Close() }, i == 0); err != nil {
			t.Fatal(err)
		}
		clients = append(clients, view)
		ids = append(ids, id)
	}
	if err := manager.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	manager.Run()
	window := &EbitengineWindow{multi: manager, views: map[*WorkspaceClient]*nativeView{}}
	first := clients[0]
	// A command forwarded from the first server still targets the window's
	// active connection after switching, rather than mutating that first model.
	queued := func(spec string) {
		raw, _ := json.Marshal(map[string]string{"keystroke": spec})
		request := nativeRequest{view: first, method: "ui.keystroke", params: raw, reply: make(chan nativeReply, 1), expires: time.Now().Add(time.Second)}
		window.handleRequest(request)
		if reply := <-request.reply; reply.err != nil {
			t.Fatal(reply.err)
		}
	}
	queued("ctrl-tab")
	if manager.ActiveConnectionID() != ids[1] {
		t.Fatal("next workspace did not select next host")
	}
	queued("cmd-shift-n")
	deadline := time.Now().Add(time.Second)
	for {
		var second gomodel.StateDump
		if err := goclient.New(socket2).Call("state.dump", nil, &second); err != nil {
			t.Fatal(err)
		}
		if len(second.Workspaces) == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("new workspace was not routed to active host")
		}
		time.Sleep(time.Millisecond)
	}
	var state gomodel.StateDump
	if err := goclient.New(socket1).Call("state.dump", nil, &state); err != nil {
		t.Fatal(err)
	}
	if len(state.Workspaces) != 1 {
		t.Fatal("inactive host was mutated")
	}
}
