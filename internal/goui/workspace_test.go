package goui

import (
	"testing"

	"github.com/SurTeam/Water/internal/goconfig"
	"github.com/SurTeam/Water/internal/gomodel"
	"github.com/google/uuid"
)

func TestRemoteFileHyperlinkRequiresConfirmation(t *testing.T){
	cfg:=goconfig.Default()
	cfg.Terminal.Hyperlinks=true
	cfg.Terminal.RemoteHyperlinkAutoDownload=false
	client:=&WorkspaceClient{
		config:cfg.Normalized(),
		remoteDestination:"example-host",
	}

	client.activateHyperlink("file://remote/tmp/report.txt")
	client.hyperlinkMu.Lock()
	prompt:=client.hyperlinkPrompt
	client.hyperlinkMu.Unlock()
	if prompt==nil{
		t.Fatal("remote file hyperlink did not create a confirmation prompt")
	}
	if prompt.URI!="file://remote/tmp/report.txt" || prompt.Destination!="example-host"{
		t.Fatalf("unexpected prompt: %#v",prompt)
	}
	if prompt.Working || prompt.Error!=""{
		t.Fatalf("new prompt should be idle: %#v",prompt)
	}

	client.cancelHyperlink()
	client.hyperlinkMu.Lock()
	defer client.hyperlinkMu.Unlock()
	if client.hyperlinkPrompt!=nil{
		t.Fatalf("cancel left prompt behind: %#v",client.hyperlinkPrompt)
	}
}

func TestDisabledHyperlinksDoNotPrompt(t *testing.T){
	cfg:=goconfig.Default()
	cfg.Terminal.Hyperlinks=false
	client:=&WorkspaceClient{
		config:cfg.Normalized(),
		remoteDestination:"example-host",
	}
	client.activateHyperlink("file://remote/tmp/report.txt")
	client.hyperlinkMu.Lock()
	defer client.hyperlinkMu.Unlock()
	if client.hyperlinkPrompt!=nil{
		t.Fatalf("disabled hyperlinks created prompt: %#v",client.hyperlinkPrompt)
	}
}


func TestApplyStateIgnoresOlderRevision(t *testing.T){
	invalidations:=0
	client:=&WorkspaceClient{
		state:gomodel.StateDump{StateRevision:12},
		terminals:make(map[uuid.UUID]*terminalClient),
		invalidate:func(){invalidations++},
	}
	client.applyState(gomodel.StateDump{StateRevision:11})
	client.mu.RLock()
	got:=client.state.StateRevision
	client.mu.RUnlock()
	if got!=12{
		t.Fatalf("stale snapshot rolled state back to revision %d",got)
	}
	if invalidations!=0{
		t.Fatalf("stale snapshot invalidated UI %d times",invalidations)
	}

	client.applyState(gomodel.StateDump{StateRevision:13})
	client.mu.RLock()
	got=client.state.StateRevision
	client.mu.RUnlock()
	if got!=13{
		t.Fatalf("newer snapshot did not apply: revision=%d",got)
	}
	if invalidations!=1{
		t.Fatalf("newer snapshot invalidations=%d, want 1",invalidations)
	}
}
