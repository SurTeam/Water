package goremote

import (
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/SurTeam/Water/internal/goclient"
	"github.com/SurTeam/Water/internal/goprotocol"
	"github.com/google/uuid"
)

const (
	startTimeout = 12 * time.Second
	retryInterval = 50 * time.Millisecond
)

type Tunnel struct {
	destination   string
	localSocket   string
	controlSocket string
	remoteSocket  string
	forwardSpec   string
}

type serverInfo struct {
	BuildVariant    string `json:"build_variant"`
	ServerVersion   string `json:"server_version"`
	ProtocolVersion uint32 `json:"protocol_version"`
	APISignature    string `json:"api_signature"`
}

func ValidateDestination(destination string) (string,error) {
	destination=strings.TrimSpace(destination)
	if destination=="" || strings.HasPrefix(destination,"-") || len(destination)>255 {
		return "",errors.New("SSH destination must not be empty, start with '-', or exceed 255 bytes")
	}
	for _,r:=range destination {
		if r<0x20 || r==0x7f || r==' ' || r=='\t' || r=='\n' || r=='\r' {
			return "",errors.New("SSH destination must not contain whitespace or control characters")
		}
	}
	return destination,nil
}

func Connect(destination string)(*Tunnel,error){
	destination,err:=ValidateDestination(destination);if err!=nil{return nil,err}
	remote:=remoteControlSocket(destination)
	control:=controlMasterSocket(destination)
	local:=filepath.Join(os.TempDir(),fmt.Sprintf("water-go-ssh-%d-%s.sock",os.Geteuid(),strings.ReplaceAll(uuid.New().String(),"-","")))
	_ = os.Remove(local)
	if err:=ensureControlMaster(destination,control);err!=nil{return nil,err}
	spec:=local+":"+remote
	if err:=runSSH("-S",control,"-O","forward","-o","ExitOnForwardFailure=yes","-L",spec,destination);err!=nil{return nil,err}
	t:=&Tunnel{destination:destination,localSocket:local,controlSocket:control,remoteSocket:remote,forwardSpec:spec}
	if t.compatible() { return t,nil }

	var info serverInfo
	infoErr:=goclient.New(local).Call("server.info",map[string]any{},&info)
	if infoErr==nil {
		_ = t.Close()
		return nil,fmt.Errorf("remote Water server is incompatible: protocol=%d signature=%q version=%q",info.ProtocolVersion,info.APISignature,info.ServerVersion)
	}
	if err:=startRemoteServer(destination,control,remote);err!=nil{
		_ = t.Close()
		return nil,err
	}
	deadline:=time.Now().Add(startTimeout)
	for time.Now().Before(deadline){
		if t.compatible(){return t,nil}
		time.Sleep(retryInterval)
	}
	_ = t.Close()
	return nil,fmt.Errorf("remote Water server at %s did not become ready within %s",destination,startTimeout)
}

func (t *Tunnel) LocalSocket()string{return t.localSocket}
func (t *Tunnel) RemoteSocket()string{return t.remoteSocket}
func (t *Tunnel) Destination()string{return t.destination}
func (t *Tunnel) Client()*goclient.Client{return goclient.New(t.localSocket)}

func (t *Tunnel) compatible()bool{
	var info serverInfo
	if err:=t.Client().Call("server.info",map[string]any{},&info);err!=nil{return false}
	return info.ProtocolVersion==goprotocol.ProtocolVersion &&
		info.APISignature==goprotocol.APISignature &&
		info.BuildVariant=="dev" &&
		info.ServerVersion=="go-rewrite"
}

func (t *Tunnel) Close()error{
	_ = runSSH("-S",t.controlSocket,"-O","cancel","-L",t.forwardSpec,t.destination)
	return os.Remove(t.localSocket)
}

func controlMasterSocket(destination string)string{
	return filepath.Join(os.TempDir(),fmt.Sprintf("water-go-ssh-%d-%016x.ctl",os.Geteuid(),stableID(destination)))
}

func remoteControlSocket(destination string)string{
	if p:=os.Getenv("WATER_REMOTE_CONTROL_SOCKET");p!=""{return p}
	return filepath.Join(os.TempDir(),fmt.Sprintf("water-go-vgo-rewrite-p%d-%016x.sock",goprotocol.ProtocolVersion,stableID(destination)))
}

func stableID(value string)uint64{
	h:=fnv.New64a();_,_=h.Write([]byte(value));return h.Sum64()
}

func ensureControlMaster(destination,control string)error{
	if command("ssh","-S",control,"-O","check",destination).Run()==nil{return nil}
	_ = os.Remove(control)
	return runSSH(
		"-M","-N","-f",
		"-o","ControlMaster=yes",
		"-o","ControlPersist=600",
		"-o","BatchMode=yes",
		"-o","Compression=yes",
		"-o","ServerAliveInterval=15",
		"-o","ServerAliveCountMax=2",
		"-o","ConnectTimeout=10",
		"-S",control,destination,
	)
}

func startRemoteServer(destination,control,remoteSocket string)error{
	program:=os.Getenv("WATER_REMOTE_SERVER_COMMAND")
	if program==""{
		return errors.New("matching remote Go server is not installed; set WATER_REMOTE_SERVER_COMMAND to the remote water-server executable")
	}
	if strings.ContainsAny(program," \t\r\n'\";$&|<>") {
		return errors.New("WATER_REMOTE_SERVER_COMMAND must be a simple remote executable path")
	}
	remoteQuoted:=shellQuote(remoteSocket)
	commandText:=fmt.Sprintf(
		"command -v %s >/dev/null 2>&1 || exit 127; %s --socket %s >/tmp/water-go-server.log 2>&1 </dev/null &",
		program,program,remoteQuoted,
	)
	return runSSH("-S",control,"-o","BatchMode=yes",destination,commandText)
}

func runSSH(args ...string)error{
	cmd:=command("ssh",args...)
	out,err:=cmd.CombinedOutput()
	if err!=nil{
		msg:=strings.TrimSpace(string(out));if msg==""{msg=err.Error()}
		return errors.New(msg)
	}
	return nil
}

func command(name string,args ...string)*exec.Cmd{
	if name=="ssh" {
		if custom:=os.Getenv("WATER_SSH_PROGRAM");custom!=""{name=custom}
		if config:=os.Getenv("WATER_SSH_CONFIG");config!=""{
			args=append([]string{"-F",config},args...)
		}
	}
	return exec.Command(name,args...)
}

func shellQuote(value string)string{
	return "'"+strings.ReplaceAll(value,"'","'\\''")+"'"
}

func DebugPaths(destination string)(string,string,error){
	d,err:=ValidateDestination(destination);if err!=nil{return "","",err}
	return controlMasterSocket(d),remoteControlSocket(d),nil
}

func UserIDString()string{return strconv.Itoa(os.Geteuid())}
