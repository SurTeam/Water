package goui

import (
	"testing"

	"github.com/SurTeam/Water/internal/goconfig"
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
