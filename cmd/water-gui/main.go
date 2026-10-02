package main

import (
	"flag"
	"fmt"
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
	"github.com/SurTeam/Water/internal/goserver"
	"github.com/SurTeam/Water/internal/goui"
)

const buildVariant = "dev"

func main() {
	var socket string
	var configPath string
	flags:=flag.NewFlagSet(os.Args[0],flag.ExitOnError)
	flags.StringVar(&socket,"socket","","Unix control socket (compatibility alias)")
	flags.StringVar(&socket,"control-socket","","Unix control socket")
	flags.StringVar(&configPath,"config",goconfig.DefaultLoadPath(buildVariant),"Water config JSON")
	_ = flags.Parse(os.Args[1:])

	cfg,err:=goconfig.Load(configPath)
	if err!=nil{
		fmt.Fprintln(os.Stderr,"water-gui: config:",err)
		os.Exit(1)
	}
	socket=resolveGUISocket(socket,cfg)

	go run(socket,configPath,cfg)
	app.Main()
}

func run(socket,configPath string,cfg goconfig.AppConfig) {
	w:=new(app.Window)
	w.Option(
		app.Title("Water"),
		app.Size(unit.Dp(cfg.Startup.WindowWidth),unit.Dp(cfg.Startup.WindowHeight)),
		app.MinSize(unit.Dp(cfg.Startup.WindowMinWidth),unit.Dp(cfg.Startup.WindowMinHeight)),
	)

	session,embedded,err:=connectOrStart(socket,configPath,cfg)
	if err!=nil{
		fmt.Fprintln(os.Stderr,"water-gui:",err)
		return
	}
	if embedded!=nil{
		defer embedded.Close()
	}
	defer session.Close()
	if embedded==nil && !cfg.Server.DetachOnQuit {
		defer func(){
			_ = goclient.New(socket).Call("server.shutdown",map[string]any{},nil)
		}()
	}

	view:=goui.NewWorkspaceClientWithConfig(session,w.Invalidate,cfg)
	defer view.Close()
	if err:=view.Bootstrap();err!=nil{
		fmt.Fprintln(os.Stderr,"water-gui:",err)
		return
	}
	go view.Run()

	th:=material.NewTheme()
	var ops op.Ops
	for{
		switch e:=w.Event().(type){
		case app.DestroyEvent:
			return
		case app.FrameEvent:
			gtx:=app.NewContext(&ops,e)
			view.Layout(gtx,th)
			e.Frame(&ops)
			ops.Reset()
		}
	}
}

func connectOrStart(socket,configPath string,cfg goconfig.AppConfig)(*goclient.Session,*goserver.Server,error){
	client:=goclient.New(socket)
	if session,err:=client.OpenSession();err==nil{
		return session,nil,nil
	}
	if !cfg.Server.AutoStart{
		return nil,nil,fmt.Errorf("server is not available at %s and auto_start is disabled",socket)
	}

	if !cfg.Server.Detached{
		srv:=goserver.NewWithConfig(socket,cfg)
		if err:=srv.Initialize(cfg.Startup.InitialWorkspace,cfg.Startup.InitialTerminal&&cfg.Startup.InitialWorkspace);err!=nil{
			return nil,nil,err
		}
		errCh:=make(chan error,1)
		go func(){errCh<-srv.ListenAndServe()}()
		session,err:=waitForSession(socket,3*time.Second)
		if err!=nil{
			_ = srv.Close()
			select{case serveErr:=<-errCh: if serveErr!=nil{return nil,nil,serveErr};default:}
			return nil,nil,err
		}
		return session,srv,nil
	}

	if err:=startDetachedServer(socket,configPath);err!=nil{
		return nil,nil,err
	}
	session,err:=waitForSession(socket,5*time.Second)
	if err!=nil{return nil,nil,err}
	return session,nil,nil
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

func startDetachedServer(socket,configPath string)error{
	exe,err:=os.Executable();if err!=nil{return err}
	serverPath:=filepath.Join(filepath.Dir(exe),"water-server")
	if _,err:=os.Stat(serverPath);err!=nil{
		if found,lookErr:=exec.LookPath("water-server");lookErr==nil{
			serverPath=found
		}else{
			return fmt.Errorf("water-server executable not found next to %s or in PATH",exe)
		}
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

func resolveGUISocket(explicit string,cfg goconfig.AppConfig)string{
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

