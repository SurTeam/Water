package main

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/SurTeam/Water/internal/goclient"
	"github.com/SurTeam/Water/internal/goserver"
	"github.com/google/uuid"
)

func withCLIServer(t *testing.T, fn func(socket string)) {
	t.Helper()
	socket:=filepath.Join("/tmp","water-go-cli-"+uuid.New().String()+".sock")
	srv:=goserver.New(socket)
	done:=make(chan error,1)
	go func(){done<-srv.ListenAndServe()}()
	defer func(){
		_ = srv.Close()
		select{
		case err:=<-done:
			if err!=nil{t.Fatalf("server shutdown: %v",err)}
		case <-time.After(time.Second):
			t.Fatal("server did not stop")
		}
	}()

	client:=goclient.New(socket)
	deadline:=time.Now().Add(2*time.Second)
	for{
		var pong any
		if err:=client.Call("ping",map[string]any{},&pong);err==nil{break}
		if time.Now().After(deadline){t.Fatal("server did not become ready")}
		time.Sleep(10*time.Millisecond)
	}
	fn(socket)
}

func TestExistingScenarioFixturesThroughGoCLI(t *testing.T){
	fixtures:=[]string{"workspace_basic.json","terminal_basic.json"}
	for _,fixture:=range fixtures{
		t.Run(fixture,func(t *testing.T){
			withCLIServer(t,func(socket string){
				path:=filepath.Join("..","..","tests","scenarios",fixture)
				if err:=run([]string{"--socket",socket,"scenario","run",path});err!=nil{
					t.Fatalf("scenario %s: %v",fixture,err)
				}
			})
		})
	}
}

func TestEventTypeNameParity(t *testing.T){
	cases:=map[string]string{
		"agent_started":"agent.started",
		"terminal_output_changed":"terminal.output_changed",
		"workspace_created":"workspace.created",
	}
	for wire,want:=range cases{
		if got:=eventTypeName(wire);got!=want{
			t.Fatalf("%q => %q, want %q",wire,got,want)
		}
	}
}
