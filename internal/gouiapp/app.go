package gouiapp

import (
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"gioui.org/app"
	"gioui.org/op"
	"gioui.org/unit"
	"gioui.org/widget/material"

	"github.com/SurTeam/Water/internal/goclient"
	"github.com/SurTeam/Water/internal/goconfig"
	"github.com/SurTeam/Water/internal/goremote"
	"github.com/SurTeam/Water/internal/goserver"
	"github.com/SurTeam/Water/internal/goui"
)

func Run(arguments []string, buildVariant string) error {
	var socket string
	var configPath string
	var sshDestination string
	flags:=flag.NewFlagSet("water",flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	flags.StringVar(&socket,"socket","","Unix control socket (compatibility alias)")
	flags.StringVar(&socket,"control-socket","","Unix control socket")
	flags.StringVar(&configPath,"config",goconfig.DefaultLoadPath(buildVariant),"Water config JSON")
	flags.StringVar(&sshDestination,"ssh","","SSH destination for a remote Water server")
	if err:=flags.Parse(arguments);err!=nil{return err}

	cfg,err:=goconfig.Load(configPath)
	if err!=nil{return fmt.Errorf("config: %w",err)}
	socket=resolveGUISocket(socket,cfg,buildVariant)
	var tunnel *goremote.Tunnel
	if strings.TrimSpace(sshDestination)!="" {
		tunnel,err=goremote.Connect(sshDestination)
		if err!=nil{return fmt.Errorf("connect remote Water server: %w",err)}
		defer tunnel.Close()
		socket=tunnel.LocalSocket()
	}

	runErr:=make(chan error,1)
	go func(){runErr<-runWindow(socket,configPath,cfg,buildVariant)}()
	app.Main()
	select{
	case err:=<-runErr:
		return err
	default:
		return nil
	}
}

func runWindow(socket,configPath string,cfg goconfig.AppConfig,buildVariant string) error {
	w:=new(app.Window)
	w.Option(
		app.Title("Water"),
		app.Size(unit.Dp(cfg.Startup.WindowWidth),unit.Dp(cfg.Startup.WindowHeight)),
		app.MinSize(unit.Dp(cfg.Startup.WindowMinWidth),unit.Dp(cfg.Startup.WindowMinHeight)),
	)

	session,embedded,ownsDetached,err:=connectOrStart(socket,configPath,cfg,buildVariant)
	if err!=nil{return err}
	if embedded!=nil{defer embedded.Close()}
	defer session.Close()
	if embedded==nil && ownsDetached && !cfg.Server.DetachOnQuit {
		defer func(){_ = goclient.New(socket).Call("server.shutdown",map[string]any{},nil)}()
	}

	view:=goui.NewWorkspaceClientWithConfig(session,w.Invalidate,cfg)
	defer view.Close()
	if err:=view.Bootstrap();err!=nil{return err}
	go view.Run()

	th:=material.NewTheme()
	var ops op.Ops
	for{
		switch e:=w.Event().(type){
		case app.DestroyEvent:
			return e.Err
		case app.FrameEvent:
			gtx:=app.NewContext(&ops,e)
			view.Layout(gtx,th)
			e.Frame(&ops)
			ops.Reset()
		}
	}
}

func connectOrStart(socket,configPath string,cfg goconfig.AppConfig,buildVariant string)(*goclient.Session,*goserver.Server,bool,error){
	client:=goclient.New(socket)
	if session,err:=client.OpenSession();err==nil{return session,nil,false,nil}
	if conn,err:=net.DialTimeout("unix",socket,250*time.Millisecond);err==nil{
		_ = conn.Close()
		return nil,nil,false,fmt.Errorf("control socket belongs to an incompatible server: %s",socket)
	}
	if !cfg.Server.AutoStart{
		return nil,nil,false,fmt.Errorf("server is not available at %s and auto_start is disabled",socket)
	}

	if !cfg.Server.Detached{
		srv:=goserver.NewWithConfig(socket,cfg)
		if err:=srv.Initialize(cfg.Startup.InitialWorkspace,cfg.Startup.InitialTerminal&&cfg.Startup.InitialWorkspace);err!=nil{
			return nil,nil,false,err
		}
		errCh:=make(chan error,1)
		go func(){errCh<-srv.ListenAndServe()}()
		session,err:=waitForSession(socket,3*time.Second)
		if err!=nil{
			_ = srv.Close()
			select{case serveErr:=<-errCh: if serveErr!=nil{return nil,nil,false,serveErr};default:}
			return nil,nil,false,err
		}
		return session,srv,false,nil
	}

	if err:=startDetachedServer(socket,configPath,buildVariant);err!=nil{return nil,nil,false,err}
	session,err:=waitForSession(socket,5*time.Second)
	if err!=nil{return nil,nil,false,err}
	return session,nil,true,nil
}

func waitForSession(socket string,timeout time.Duration)(*goclient.Session,error){
	deadline:=time.Now().Add(timeout)
	var last error
	for time.Now().Before(deadline){
		session,err:=goclient.New(socket).OpenSession()
		if err==nil{return session,nil}
		last=err
		time.Sleep(50*time.Millisecond)
	}
	if last==nil{last=fmt.Errorf("timed out")}
	return nil,fmt.Errorf("server at %s did not become ready: %w",socket,last)
}

func startDetachedServer(socket,configPath,buildVariant string)error{
	exe,err:=os.Executable();if err!=nil{return err}
	dir:=filepath.Dir(exe)
	candidates:=[]string{filepath.Join(dir,"water-server"),filepath.Join(dir,"water-srv-dev")}
	if buildVariant!="release"{
		candidates[0],candidates[1]=candidates[1],candidates[0]
	}
	serverPath:=""
	for _,candidate:=range candidates{
		if info,statErr:=os.Stat(candidate);statErr==nil&&!info.IsDir(){
			serverPath=candidate
			break
		}
	}
	if serverPath==""{
		if found,lookErr:=exec.LookPath("water-server");lookErr==nil{
			serverPath=found
		}else{
			return fmt.Errorf("water-server executable not found next to %s or in PATH",exe)
		}
	}
	variantOut,err:=exec.Command(serverPath,"--build-variant").Output()
	if err!=nil{return fmt.Errorf("inspect sibling water-server: %w",err)}
	if got:=strings.TrimSpace(string(variantOut));got!=buildVariant{
		return fmt.Errorf("sibling water-server variant %q does not match GUI variant %q",got,buildVariant)
	}
	cmd:=exec.Command(
		serverPath,
		"--control-socket",socket,
		"--config",configPath,
		"--daemonize",
	)
	if out,err:=cmd.CombinedOutput();err!=nil{
		message:=strings.TrimSpace(string(out))
		if message==""{message=err.Error()}
		return fmt.Errorf("could not start water-server: %s",message)
	}
	return nil
}

func resolveGUISocket(explicit string,cfg goconfig.AppConfig,buildVariant string)string{
	if explicit!=""{return explicit}
	if p:=os.Getenv("WATER_CONTROL_SOCKET");p!=""{return p}
	if p:=os.Getenv("WATER_SOCKET");p!=""{return p}
	if cfg.Server.SocketPath!=nil&&strings.TrimSpace(*cfg.Server.SocketPath)!=""{
		return strings.TrimSpace(*cfg.Server.SocketPath)
	}
	if cfg.Startup.ControlSocket!=nil&&strings.TrimSpace(*cfg.Startup.ControlSocket)!=""{
		return strings.TrimSpace(*cfg.Startup.ControlSocket)
	}
	if buildVariant=="release"{return "/tmp/water.sock"}
	return "/tmp/water-dev.sock"
}
