//go:build unix

package goclient

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/SurTeam/Water/internal/goprotocol"
	"github.com/google/uuid"
)

func TestGoClientAgainstRustServer(t *testing.T) {
	serverBin:=os.Getenv("WATER_RUST_SERVER_BIN")
	if serverBin==""{t.Skip("WATER_RUST_SERVER_BIN is not set")}
	socket:=filepath.Join("/tmp","water-rust-compat-"+uuid.New().String()+".sock")
	config:=filepath.Join("/tmp","water-rust-compat-"+uuid.New().String()+".json")
	defer os.Remove(socket)
	defer os.Remove(config)
	if err:=os.WriteFile(config,[]byte(`{
		"startup":{"initial_workspace":false,"initial_terminal":false},
		"shell":{"program":"/bin/sh","args":["-l"]}
	}`),0o600);err!=nil{t.Fatal(err)}

	cmd:=exec.Command(serverBin,"--control-socket",socket,"--config",config,"--empty-workspace")
	cmd.Stdout=os.Stdout
	cmd.Stderr=os.Stderr
	if err:=cmd.Start();err!=nil{t.Fatal(err)}
	defer func(){
		_ = New(socket).Call("server.shutdown",map[string]any{},nil)
		done:=make(chan error,1)
		go func(){done<-cmd.Wait()}()
		select{
		case <-done:
		case <-time.After(2*time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
	}()

	client:=New(socket)
	deadline:=time.Now().Add(10*time.Second)
	var lastErr error
	for {
		var pong any
		if err:=client.Call("ping",map[string]any{},&pong);err==nil{break}else{lastErr=err}
		if time.Now().After(deadline){t.Fatalf("Rust server did not become ready: %v",lastErr)}
		time.Sleep(25*time.Millisecond)
	}

	var info struct{
		ProtocolVersion uint32 `json:"protocol_version"`
		APISignature string `json:"api_signature"`
	}
	if err:=client.Call("server.info",map[string]any{},&info);err!=nil{t.Fatal(err)}
	if info.ProtocolVersion!=goprotocol.ProtocolVersion || info.APISignature!=goprotocol.APISignature{
		t.Fatalf("server compatibility = protocol %d signature %q",info.ProtocolVersion,info.APISignature)
	}

	if err:=client.Dispatch(map[string]any{"type":"workspace.create"},nil);err!=nil{t.Fatal(err)}
	if err:=client.Dispatch(map[string]any{"type":"tab.new","title":"Cross"},nil);err!=nil{t.Fatal(err)}

	var spawned struct{TerminalID uuid.UUID `json:"terminal_id"`}
	if err:=client.Dispatch(map[string]any{
		"type":"terminal.spawn",
		"program":"/bin/sh",
		"args":[]string{"-c","read line; printf 'CROSS:%s\\n' \"$line\""},
		"columns":80,
		"lines":24,
	},&spawned);err!=nil{t.Fatal(err)}
	if spawned.TerminalID==uuid.Nil{t.Fatal("Rust server returned nil terminal id")}

	session,err:=client.OpenSession()
	if err!=nil{t.Fatal(err)}
	defer session.Close()
	var attached struct{
		LastSeq uint64 `json:"last_seq"`
		Size goprotocol.TerminalSize `json:"size"`
	}
	if err:=session.Attach(spawned.TerminalID,&attached);err!=nil{t.Fatal(err)}

	if err:=client.Dispatch(map[string]any{
		"type":"terminal.send_text",
		"terminal_id":spawned.TerminalID,
		"text":"hello-cross\n",
	},nil);err!=nil{t.Fatal(err)}

	var live bytes.Buffer
	timer:=time.NewTimer(5*time.Second)
	defer timer.Stop()
	for {
		select {
		case push,ok:=<-session.Events:
			if !ok{t.Fatal("Rust session closed before terminal exit")}
			if push.TerminalID!=spawned.TerminalID{continue}
			if push.Event.Seq<=attached.LastSeq{continue}
			switch push.Event.Kind{
			case goprotocol.OutputEvent:
				live.Write(push.Event.Data)
				if bytes.Contains(live.Bytes(),[]byte("CROSS:hello-cross")){
					return
				}
			case goprotocol.ExitEvent:
				if !bytes.Contains(live.Bytes(),[]byte("CROSS:hello-cross")){
					t.Fatalf("live WT4 stream missing expected output: %q",live.String())
				}
				return
			}
		case <-timer.C:
			t.Fatalf("timed out waiting for Rust WT4 output; got %q",live.String())
		}
	}
}
