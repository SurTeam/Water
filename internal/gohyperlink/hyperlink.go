package gohyperlink

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

func FileURLPath(uri string)(string,error){
	if !strings.HasPrefix(uri,"file://"){return "",errors.New("not a file URL")}
	parsed,err:=url.Parse(uri)
	if err!=nil{return "",err}
	path,err:=url.PathUnescape(parsed.EscapedPath())
	if err!=nil{return "",errors.New("file URL contains invalid escaping")}
	if path=="" || !filepath.IsAbs(path) || hasControl(path){
		return "",errors.New("invalid absolute file path")
	}
	return path,nil
}

func DownloadDirectory(value string)(string,error){
	if hasControl(value){return "",errors.New("invalid download directory")}
	value=strings.TrimSpace(value)
	if value==""{return "",errors.New("invalid download directory")}
	if value=="~" || strings.HasPrefix(value,"~/"){
		home,err:=os.UserHomeDir();if err!=nil{return "",err}
		if value=="~"{return home,nil}
		return filepath.Join(home,strings.TrimPrefix(value,"~/")),nil
	}
	if !filepath.IsAbs(value){return "",errors.New("download directory must be absolute or start with ~/")}
	return value,nil
}

func ValidateTarget(target string)error{
	if target=="" || hasControl(target){return errors.New("invalid hyperlink target")}
	colon:=strings.IndexByte(target,':')
	if colon<=0{return errors.New("invalid hyperlink scheme")}
	scheme:=target[:colon]
	for idx,r:=range scheme{
		if idx==0 && !isAlpha(r){return errors.New("invalid hyperlink scheme")}
		if !isAlpha(r) && !isDigit(r) && r!='+' && r!='.' && r!='-'{
			return errors.New("invalid hyperlink scheme")
		}
	}
	if scheme=="file"{
		_,err:=FileURLPath(target)
		return err
	}
	return nil
}

func OpenTarget(target string)error{
	if err:=ValidateTarget(target);err!=nil{return err}
	if strings.HasPrefix(target,"file:"){
		path,err:=FileURLPath(target);if err!=nil{return err}
		return OpenPath(path)
	}
	return launch(target)
}

func OpenPath(path string)error{
	if path=="" || hasControl(path){return errors.New("invalid path")}
	return launch(path)
}

func launch(target string)error{
	program:="xdg-open"
	if runtime.GOOS=="darwin"{program="open"}
	cmd:=exec.Command(program,target)
	cmd.Stdin=nil
	cmd.Stdout=nil
	cmd.Stderr=nil
	if err:=cmd.Start();err!=nil{return fmt.Errorf("%s: %w",program,err)}
	go func(){_ = cmd.Wait()}()
	return nil
}

func hasControl(value string)bool{
	for _,r:=range value{if r<0x20 || r==0x7f{return true}}
	return false
}
func isAlpha(r rune)bool{return r>='a'&&r<='z'||r>='A'&&r<='Z'}
func isDigit(r rune)bool{return r>='0'&&r<='9'}
