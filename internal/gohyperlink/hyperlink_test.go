//go:build unix

package gohyperlink

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileURLPathAndTargetValidation(t *testing.T){
	path,err:=FileURLPath("file://remote/tmp/a%20b%23c")
	if err!=nil{t.Fatal(err)}
	if path!="/tmp/a b#c"{t.Fatalf("decoded path = %q",path)}

	path,err=FileURLPath("file:///tmp/%E4%B8%AD%E6%96%87")
	if err!=nil{t.Fatal(err)}
	if path!="/tmp/中文"{t.Fatalf("unicode path = %q",path)}

	for _,target:=range []string{
		"",
		"relative/path",
		"1https://example.com",
		"http s://example.com",
		"https://example.com/
",
		"file://host",
		"file:///tmp/%",
		"file:///tmp/%00",
	}{
		if err:=ValidateTarget(target);err==nil{
			t.Fatalf("ValidateTarget(%q) unexpectedly succeeded",target)
		}
	}
	for _,target:=range []string{
		"https://example.com/a",
		"ssh://example.com/path",
		"file:///tmp/a%20b",
	}{
		if err:=ValidateTarget(target);err!=nil{
			t.Fatalf("ValidateTarget(%q): %v",target,err)
		}
	}
}

func TestDownloadDirectory(t *testing.T){
	dir,err:=DownloadDirectory("~/Downloads/Water")
	if err!=nil{t.Fatal(err)}
	if !filepath.IsAbs(dir){t.Fatalf("home directory expansion is not absolute: %q",dir)}

	tmp:=t.TempDir()
	got,err:=DownloadDirectory(tmp)
	if err!=nil{t.Fatal(err)}
	if got!=tmp{t.Fatalf("download directory = %q, want %q",got,tmp)}

	for _,value:=range []string{"","relative","~someone/files","/tmp/
"}{
		if _,err:=DownloadDirectory(value);err==nil{
			t.Fatalf("DownloadDirectory(%q) unexpectedly succeeded",value)
		}
	}
}

func TestDownloadRemoteUsesControlMasterAndIsolatedDirectory(t *testing.T){
	tmp:=t.TempDir()
	logPath:=filepath.Join(tmp,"scp.args")
	fakeSCP:=filepath.Join(tmp,"fake-scp")
	script:=`#!/bin/sh
set -eu
printf '%s
' "$@" > "$WATER_FAKE_SCP_LOG"
for last do :; done
mkdir -p "$last"
printf 'payload' > "$last/test file.txt"
`
	if err:=os.WriteFile(fakeSCP,[]byte(script),0o755);err!=nil{t.Fatal(err)}
	t.Setenv("WATER_SCP_PROGRAM",fakeSCP)
	t.Setenv("WATER_FAKE_SCP_LOG",logPath)

	path,err:=DownloadRemote("example-host","file://remote/tmp/test%20file.txt",tmp)
	if err!=nil{t.Fatal(err)}
	if filepath.Base(path)!="test file.txt"{t.Fatalf("downloaded path = %q",path)}
	data,err:=os.ReadFile(path)
	if err!=nil{t.Fatal(err)}
	if string(data)!="payload"{t.Fatalf("downloaded payload = %q",data)}

	args,err:=os.ReadFile(logPath)
	if err!=nil{t.Fatal(err)}
	text:=string(args)
	if !strings.Contains(text,"ControlPath="){
		t.Fatalf("scp did not receive ControlPath: %s",text)
	}
	if !strings.Contains(text,"example-host:/tmp/test file.txt"){
		t.Fatalf("scp source missing: %s",text)
	}
	if filepath.Dir(path)==tmp{
		t.Fatalf("download was not isolated in a unique directory: %q",path)
	}
}
