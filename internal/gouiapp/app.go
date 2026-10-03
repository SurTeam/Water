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

	"github.com/google/uuid"

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

type stringListFlag []string

func (values *stringListFlag) String() string {
	return strings.Join(*values,",")
}

func (values *stringListFlag) Set(value string) error {
	value=strings.TrimSpace(value)
	if value==""{return fmt.Errorf("SSH destination must not be empty")}
	*values=append(*values,value)
	return nil
}

func Run(arguments []string, buildVariant string) error {
	var socket string
	var configPath string
	var sshDestinations stringListFlag
	flags:=flag.NewFlagSet("water",flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	flags.StringVar(&socket,"socket","","Unix control socket (compatibility alias)")
	flags.StringVar(&socket,"control-socket","","Unix control socket")
	flags.StringVar(&configPath,"config",goconfig.DefaultLoadPath(buildVariant),"Water config JSON")
	flags.Var(&sshDestinations,"ssh","SSH destination to add to this window (may be repeated)")
	if err:=flags.Parse(arguments);err!=nil{return err}

	cfg,err:=goconfig.Load(configPath)
	if err!=nil{return fmt.Errorf("config: %w",err)}
	socket=resolveGUISocket(socket,cfg,buildVariant)

	runErr:=make(chan error,1)
	go func(){runErr<-runWindowWithConnections(socket,configPath,cfg,buildVariant,sshDestinations)}()
	app.Main()
	select{
	case err:=<-runErr:
		return err
	default:
		return nil
	}
}

func runWindow(socket,configPath string,cfg goconfig.AppConfig,buildVariant string) error {
	return runWindowWithConnections(socket,configPath,cfg,buildVariant,nil)
}

func runWindowWithConnection(socket,configPath string,cfg goconfig.AppConfig,buildVariant,remoteDestination string) error {
	var destinations []string
	if strings.TrimSpace(remoteDestination)!=""{destinations=[]string{remoteDestination}}
	return runWindowWithConnections(socket,configPath,cfg,buildVariant,destinations)
}

func runWindowWithConnections(socket,configPath string,cfg goconfig.AppConfig,buildVariant string,sshDestinations []string) error {
	w:=new(app.Window)
	w.Option(
		app.Title("Water"),
		app.Size(unit.Dp(cfg.Startup.WindowWidth),unit.Dp(cfg.Startup.WindowHeight)),
		app.MinSize(unit.Dp(cfg.Startup.WindowMinWidth),unit.Dp(cfg.Startup.WindowMinHeight)),
	)

	multi:=goui.NewMultiWorkspaceClient(w.Invalidate)

	localSession,embedded,ownsDetached,localErr:=connectOrStart(socket,configPath,cfg,buildVariant)
	if localErr==nil{
		if embedded!=nil{defer embedded.Close()}
		if embedded==nil && ownsDetached && !cfg.Server.DetachOnQuit {
			defer func(){_ = goclient.New(socket).Call("server.shutdown",map[string]any{},nil)}()
		}
		localView:=goui.NewWorkspaceClientWithConnection(localSession,w.Invalidate,cfg,"")
		if err:=multi.AddConnection(goui.ConnectionEntry{
			ID:uuid.New(),
			Name:"Local",
			Kind:"local",
			Status:"connected",
			SocketPath:socket,
		},localView,func(){_ = localSession.Close()},len(sshDestinations)==0);err!=nil{
			return err
		}
	}else if len(sshDestinations)==0{
		return localErr
	}

	// Register this after local server lifecycle defers so child sessions and
	// terminal attachments are closed before an embedded/detached server stops.
	defer multi.Close()

	remoteFactory:=func(rawDestination string)(goui.ConnectionEntry,*goui.WorkspaceClient,func(),error){
		destination,err:=goremote.ValidateDestination(rawDestination)
		if err!=nil{return goui.ConnectionEntry{},nil,nil,err}
		for _,entry:=range multi.ConnectionEntries(){
			if entry.Kind=="remote" && entry.Destination==destination{
				return goui.ConnectionEntry{},nil,nil,fmt.Errorf("remote connection %s is already open",destination)
			}
		}
		tunnel,err:=goremote.Connect(destination)
		if err!=nil{return goui.ConnectionEntry{},nil,nil,fmt.Errorf("connect remote Water server %s: %w",destination,err)}
		session,err:=tunnel.Client().OpenSession()
		if err!=nil{
			_ = tunnel.Close()
			return goui.ConnectionEntry{},nil,nil,fmt.Errorf("open remote Water session %s: %w",destination,err)
		}
		view:=goui.NewWorkspaceClientWithConnection(session,w.Invalidate,cfg,destination)
		entry:=goui.ConnectionEntry{
			ID:uuid.New(),
			Name:destination,
			Kind:"remote",
			Status:"connected",
			SocketPath:tunnel.LocalSocket(),
			RemoteSocketPath:tunnel.RemoteSocket(),
			Destination:destination,
		}
		closeRemote:=func(){
			_ = session.Close()
			_ = tunnel.Close()
		}
		return entry,view,closeRemote,nil
	}
	multi.SetRemoteConnector(remoteFactory)

	for _,rawDestination:=range sshDestinations{
		entry,view,closeRemote,err:=remoteFactory(rawDestination)
		if err!=nil{return fmt.Errorf("SSH destination %q: %w",rawDestination,err)}
		if err:=multi.AddConnection(entry,view,closeRemote,true);err!=nil{
			if closeRemote!=nil{closeRemote()}
			return err
		}
	}
	if len(multi.ConnectionEntries())==0{
		if localErr!=nil{return localErr}
		return fmt.Errorf("no Water connection is available")
	}

	if err:=multi.Bootstrap();err!=nil{return err}
	multi.Run()

	th:=material.NewTheme()
	var ops op.Ops
	for{
		switch e:=w.Event().(type){
		case app.DestroyEvent:
			return e.Err
		case app.FrameEvent:
			gtx:=app.NewContext(&ops,e)
			multi.Layout(gtx,th)
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
