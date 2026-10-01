package main

import (
	"encoding/base64"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"gioui.org/app"
	"gioui.org/op"
	"gioui.org/unit"
	"gioui.org/widget/material"

	"github.com/SurTeam/Water/internal/goclient"
	"github.com/SurTeam/Water/internal/goui"
	"github.com/SurTeam/Water/internal/goprotocol"
	"github.com/SurTeam/Water/internal/govt"
	"github.com/google/uuid"
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
	terminal := flag.String("terminal", "", "terminal UUID to attach")
	flag.Parse()

	if *terminal == "" {
		fmt.Fprintln(os.Stderr, "water-gui requires --terminal <uuid> in the current Go rewrite milestone")
		os.Exit(2)
	}
	id, err := uuid.Parse(*terminal)
	if err != nil {
		panic(err)
	}

	go run(*socket, id)
	app.Main()
}

func run(socket string, id uuid.UUID) {
	w := new(app.Window)
	w.Option(app.Title("Water (Go)"), app.Size(unit.Dp(1100), unit.Dp(720)))

	client := goclient.New(socket)
	session, err := client.OpenSession()
	if err != nil {
		panic(err)
	}
	defer session.Close()

	var attached struct {
		Size   goprotocol.TerminalSize        `json:"size"`
		Replay []goprotocol.WireTerminalEvent `json:"replay"`
	}
	if err := session.Attach(id, &attached); err != nil {
		panic(err)
	}

	emu := govt.New(attached.Size.Columns, attached.Size.Lines, 2000)
	defer emu.Close()
	for _, ev := range attached.Replay {
		switch ev.Type {
		case "output":
			data, err := base64.StdEncoding.DecodeString(ev.Bytes)
			if err == nil {
				emu.Write(data)
			}
		case "resize":
			emu.Resize(ev.Columns, ev.Lines)
		}
	}
	flushVTResponses(session, id, emu)

	var mu sync.RWMutex
	screen := emu.Snapshot()

	go func() {
		for push := range session.Events {
			if push.TerminalID != id {
				continue
			}
			switch push.Event.Kind {
			case goprotocol.OutputEvent:
				emu.Write(push.Event.Data)
				flushVTResponses(session, id, emu)
			case goprotocol.ResizeEvent:
				emu.Resize(push.Event.Size.Columns, push.Event.Size.Lines)
			}
			snapshot := emu.Snapshot()
			mu.Lock()
			screen = snapshot
			mu.Unlock()
			w.Invalidate()
		}
	}()

	th := material.NewTheme()
	terminalView := goui.NewTerminalView()
	var ops op.Ops
	for {
		switch e := w.Event().(type) {
		case app.DestroyEvent:
			return
		case app.FrameEvent:
			gtx := app.NewContext(&ops, e)
			mu.RLock()
			snapshot := screen
			mu.RUnlock()
			terminalView.Layout(gtx, th, snapshot)
			e.Frame(&ops)
			ops.Reset()
		}
	}
}

func flushVTResponses(session *goclient.Session, terminalID uuid.UUID, emu *govt.Emulator) {
	for _, response := range emu.TakeResponses() {
		values := make([]int, len(response))
		for i, b := range response {
			values[i] = int(b)
		}
		if err := session.Dispatch(map[string]any{
			"type":        "terminal.send_bytes",
			"terminal_id": terminalID,
			"bytes":       values,
		}, nil); err != nil {
			return
		}
	}
}
