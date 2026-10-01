package goremote

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"path"
	"strings"
)

//go:embed payloads/*
var embeddedServers embed.FS

type RemoteTarget struct {
	OS   string
	Arch string
}

func detectRemoteTarget(destination,control string)(RemoteTarget,error){
	cmd:=command("ssh","-S",control,"-o","BatchMode=yes",destination,"uname -s; uname -m")
	out,err:=cmd.CombinedOutput()
	if err!=nil{return RemoteTarget{},fmt.Errorf("detect remote target: %w: %s",err,strings.TrimSpace(string(out)))}
	lines:=strings.Fields(string(out))
	if len(lines)<2{return RemoteTarget{},errors.New("remote target probe returned incomplete output")}
	osName:=strings.ToLower(lines[0])
	switch osName{
	case "darwin":
	case "linux":
	default:return RemoteTarget{},fmt.Errorf("unsupported remote OS %q",lines[0])
	}
	arch:=strings.ToLower(lines[1])
	switch arch{
	case "arm64","aarch64":arch="arm64"
	case "x86_64","amd64":arch="amd64"
	default:return RemoteTarget{},fmt.Errorf("unsupported remote architecture %q",lines[1])
	}
	return RemoteTarget{OS:osName,Arch:arch},nil
}

func embeddedServerPayload(target RemoteTarget)([]byte,bool){
	name:=fmt.Sprintf("payloads/water-server-%s-%s",target.OS,target.Arch)
	data,err:=embeddedServers.ReadFile(name)
	return data,err==nil&&len(data)>0
}

func deployEmbeddedServer(destination,control string,target RemoteTarget,payload []byte)(string,error){
	if len(payload)==0{return "",errors.New("empty embedded server payload")}
	remoteDir:=path.Join("$HOME",".cache","water-go","go-rewrite",fmt.Sprintf("p%d",4),target.OS+"-"+target.Arch)
	remoteBin:=path.Join(remoteDir,"water-server")
	// $HOME is intentionally expanded by the remote shell; all remaining path
	// components are fixed build identities and contain no shell metacharacters.
	script:=fmt.Sprintf(
		"set -eu; mkdir -p %s; tmp=%s.tmp.$$; cat > \"$tmp\"; chmod 700 \"$tmp\"; mv \"$tmp\" %s",
		remoteDir,remoteBin,remoteBin,
	)
	cmd:=command("ssh","-S",control,"-o","BatchMode=yes",destination,script)
	cmd.Stdin=bytes.NewReader(payload)
	out,err:=cmd.CombinedOutput()
	if err!=nil{return "",fmt.Errorf("deploy remote Water server: %w: %s",err,strings.TrimSpace(string(out)))}
	return remoteBin,nil
}
