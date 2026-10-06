package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"

	"github.com/SurTeam/Water/internal/gobuild"
	"github.com/SurTeam/Water/internal/goconfig"
	"github.com/SurTeam/Water/internal/goprotocol"
	"github.com/SurTeam/Water/internal/goserver"
)

var (
	buildVariant  = gobuild.Variant
	serverVersion = gobuild.Version
)

func main() {
	var socket string
	var configPath string
	var noInitialTerminal bool
	var emptyWorkspace bool
	var daemonize bool
	var printVariant bool
	var printVersion bool
	var printServerInfo bool

	flags := flag.NewFlagSet(os.Args[0], flag.ExitOnError)
	flags.StringVar(&socket, "socket", "", "Unix control socket (compatibility alias)")
	flags.StringVar(&socket, "control-socket", "", "Unix control socket")
	flags.StringVar(&configPath, "config", goconfig.DefaultLoadPath(buildVariant), "Water config JSON")
	flags.BoolVar(&noInitialTerminal, "no-initial-terminal", false, "create the initial workspace without a terminal")
	flags.BoolVar(&emptyWorkspace, "empty-workspace", false, "start without an initial workspace")
	flags.BoolVar(&emptyWorkspace, "no-initial-workspace", false, "start without an initial workspace")
	flags.BoolVar(&daemonize, "daemonize", false, "detach the server process")
	flags.BoolVar(&printVariant, "build-variant", false, "print build variant and exit")
	flags.BoolVar(&printVersion, "version", false, "print version and exit")
	flags.BoolVar(&printServerInfo, "server-info", false, "print server compatibility descriptor and exit")
	flags.BoolVar(&printVersion, "V", false, "print version and exit")
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), "water-server [--control-socket PATH] [--config PATH] [--no-initial-terminal] [--empty-workspace] [--daemonize]")
		flags.PrintDefaults()
	}
	_ = flags.Parse(os.Args[1:])

	if printServerInfo {
		_ = json.NewEncoder(os.Stdout).Encode(goprotocol.Descriptor{BuildVariant: buildVariant, Version: serverVersion, ServerRevision: gobuild.ServerRevision, ProtocolVersion: goprotocol.ProtocolVersion, APISignature: goprotocol.APISignature, Capabilities: goprotocol.ServerCapabilities()})
		return
	}
	if printVariant {
		fmt.Println(buildVariant)
		return
	}
	if printVersion {
		fmt.Printf("water-server %s\n", serverVersion)
		return
	}

	// The child has --daemonize removed. An inherited environment marker must
	// never turn a launcher into the long-lived server (and hang its caller).
	if daemonize {
		if err := startDetached(os.Args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, "water-server:", err)
			os.Exit(1)
		}
		return
	}

	cfg, err := goconfig.Load(configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "water-server: config:", err)
		os.Exit(1)
	}
	socket = resolveSocket(socket, cfg)
	initialWorkspace := cfg.Startup.InitialWorkspace && !emptyWorkspace
	initialTerminal := cfg.Startup.InitialTerminal && !noInitialTerminal && initialWorkspace

	srv := goserver.NewWithConfig(socket, cfg)
	srv.Build = buildVariant
	srv.Version = serverVersion
	if err := srv.Initialize(initialWorkspace, initialTerminal); err != nil {
		fmt.Fprintln(os.Stderr, "water-server: initialize:", err)
		os.Exit(1)
	}

	sig := make(chan os.Signal, 2)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	signal.Ignore(syscall.SIGHUP)
	go func() { <-sig; _ = srv.Close() }()

	fmt.Fprintf(os.Stderr, "water-server(go): %s\n", socket)
	if err := srv.ListenAndServe(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func resolveSocket(explicit string, cfg goconfig.AppConfig) string {
	if explicit != "" {
		return explicit
	}
	if p := os.Getenv("WATER_CONTROL_SOCKET"); p != "" {
		return p
	}
	if p := os.Getenv("WATER_SOCKET"); p != "" {
		return p
	}
	if cfg.Server.SocketPath != nil && strings.TrimSpace(*cfg.Server.SocketPath) != "" {
		return strings.TrimSpace(*cfg.Server.SocketPath)
	}
	if cfg.Startup.ControlSocket != nil && strings.TrimSpace(*cfg.Startup.ControlSocket) != "" {
		return strings.TrimSpace(*cfg.Startup.ControlSocket)
	}
	if buildVariant == "release" {
		return "/tmp/water.sock"
	}
	return "/tmp/water-dev.sock"
}

func startDetached(args []string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	childArgs := make([]string, 0, len(args))
	for _, arg := range args {
		if arg == "--daemonize" {
			continue
		}
		childArgs = append(childArgs, arg)
	}
	cmd := exec.Command(exe, childArgs...)
	cmd.Env = make([]string, 0, len(os.Environ()))
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "WATER_GO_DAEMON_CHILD=") {
			cmd.Env = append(cmd.Env, value)
		}
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return cmd.Start()
}
