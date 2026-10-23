package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/SurTeam/Water/internal/goconfig"
	"github.com/SurTeam/Water/internal/goremote"
	"github.com/SurTeam/Water/internal/gotui"
	"github.com/SurTeam/Water/internal/gouiapp"
)

func main() {
	args := os.Args[1:]
	var err error
	if len(args) > 0 && args[0] == "__ssh_askpass" {
		prompt := "SSH authentication"
		if len(args) > 1 {
			prompt = args[1]
		}
		if goremote.RunAskpass(prompt) != nil {
			os.Exit(1)
		}
		return
	}
	if len(args) > 0 && args[0] == "tui" {
		err = runTUI(args[1:])
	} else if isControlInvocation(args) {
		err = run(args)
	} else {
		stopMemoryMonitor := configureGUIMemory()
		defer stopMemoryMonitor()
		err = gouiapp.Run(args, buildVariant)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "water:", err)
		os.Exit(1)
	}
}

func isControlArgument(first string) bool {
	switch first {
	case "ctl", "state", "info", "version", "--version", "-V", "client",
		"connections", "connection", "sockets", "socket", "ui", "server", "debug",
		"workspace", "tab", "pane", "surface", "terminal", "operation", "scenario", "ping":
		return true
	default:
		return false
	}
}

func isControlInvocation(args []string) bool {
	if len(args) == 0 {
		return false
	}
	_, _, filtered, err := extractGlobal(args)
	if err == nil && len(filtered) > 0 {
		return isControlArgument(filtered[0])
	}
	return isControlArgument(args[0])
}

func runTUI(args []string) error {
	flags := flag.NewFlagSet("water tui", flag.ContinueOnError)
	var o gotui.Options
	flags.StringVar(&o.Socket, "socket", os.Getenv("WATER_CONTROL_SOCKET"), "server socket")
	flags.StringVar(&o.Config, "config", goconfig.ConfiguredLoadPath(buildVariant), "configuration path")
	flags.StringVar(&o.SSH, "ssh", "", "SSH destination")
	flags.BoolVar(&o.Serve, "serve", false, "run headless server (internal)")
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected TUI arguments: %v", flags.Args())
	}
	if o.Socket == "" {
		cfg, err := goconfig.Load(o.Config)
		if err != nil {
			return err
		}
		o.Socket = resolveControlSocket(cfg)
	}
	return gotui.Run(o)
}
