package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"gioui.org/app"
	"gioui.org/op"
	"gioui.org/unit"
	"gioui.org/widget/material"

	"github.com/SurTeam/Water/internal/goclient"
	"github.com/SurTeam/Water/internal/goui"
)

func defaultSocket() string {
	if p := os.Getenv("WATER_SOCKET"); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".water", "dev", "water-go.sock")
}

func main() {
	socket := flag.String("socket", defaultSocket(), "Unix control socket")
	flag.Parse()

	go run(*socket)
	app.Main()
}

func run(socket string) {
	w := new(app.Window)
	w.Option(app.Title("Water"), app.Size(unit.Dp(1200), unit.Dp(760)))

	client := goclient.New(socket)
	session, err := client.OpenSession()
	if err != nil {
		fmt.Fprintln(os.Stderr, "water-gui:", err)
		return
	}
	defer session.Close()

	view := goui.NewWorkspaceClient(session, w.Invalidate)
	defer view.Close()
	if err := view.Bootstrap(); err != nil {
		fmt.Fprintln(os.Stderr, "water-gui:", err)
		return
	}
	go view.Run()

	th := material.NewTheme()
	var ops op.Ops
	for {
		switch e := w.Event().(type) {
		case app.DestroyEvent:
			return
		case app.FrameEvent:
			gtx := app.NewContext(&ops, e)
			view.Layout(gtx, th)
			e.Frame(&ops)
			ops.Reset()
		}
	}
}
