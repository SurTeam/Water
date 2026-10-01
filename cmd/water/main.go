package main

import (
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/SurTeam/Water/internal/goclient"
	"github.com/SurTeam/Water/internal/goprotocol"
	"github.com/google/uuid"
)

func defaultSocket() string {
	if p := os.Getenv("WATER_SOCKET"); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".water", "dev", "water-go.sock")
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: water-go [--socket path] info|ping|terminal ...")
	fmt.Fprintln(os.Stderr, "terminal spawn <program> [args...]")
	fmt.Fprintln(os.Stderr, "terminal send <uuid> <text>")
	fmt.Fprintln(os.Stderr, "terminal resize <uuid> <cols> <rows>")
	fmt.Fprintln(os.Stderr, "terminal attach <uuid>")
}

func main() {
	socket := flag.String("socket", defaultSocket(), "Unix control socket")
	flag.Parse()
	args := flag.Args()
	if len(args) == 0 {
		usage()
		os.Exit(2)
	}
	c := goclient.New(*socket)

	switch args[0] {
	case "info":
		var out any
		must(c.Call("server.info", map[string]any{}, &out))
		printJSON(out)
	case "ping":
		var out any
		must(c.Call("ping", map[string]any{}, &out))
		printJSON(out)
	case "terminal":
		runTerminal(c, args[1:])
	default:
		usage()
		os.Exit(2)
	}
}

func runTerminal(c *goclient.Client, args []string) {
	if len(args) == 0 {
		usage()
		os.Exit(2)
	}
	switch args[0] {
	case "spawn":
		if len(args) < 2 {
			usage()
			os.Exit(2)
		}
		var out struct {
			Type       string    `json:"type"`
			TerminalID uuid.UUID `json:"terminal_id"`
		}
		must(c.Dispatch(map[string]any{
			"type":    "terminal.spawn",
			"program": args[1],
			"args":    args[2:],
			"columns": 80,
			"lines":   24,
		}, &out))
		fmt.Println(out.TerminalID)

	case "send":
		if len(args) < 3 {
			usage()
			os.Exit(2)
		}
		id := mustUUID(args[1])
		must(c.Dispatch(map[string]any{
			"type":        "terminal.send_text",
			"terminal_id": id,
			"text":        args[2],
		}, nil))

	case "resize":
		if len(args) < 4 {
			usage()
			os.Exit(2)
		}
		id := mustUUID(args[1])
		cols := mustInt(args[2])
		rows := mustInt(args[3])
		must(c.Dispatch(map[string]any{
			"type":        "terminal.resize",
			"terminal_id": id,
			"columns":     cols,
			"lines":       rows,
			"cell_width":  0,
			"cell_height": 0,
		}, nil))

	case "attach":
		if len(args) < 2 {
			usage()
			os.Exit(2)
		}
		id := mustUUID(args[1])
		session, err := c.OpenSession()
		must(err)
		defer session.Close()

		var attached struct {
			Size   goprotocol.TerminalSize        `json:"size"`
			Replay []goprotocol.WireTerminalEvent `json:"replay"`
		}
		must(session.Attach(id, &attached))
		for _, ev := range attached.Replay {
			if ev.Type == "output" {
				data, err := base64.StdEncoding.DecodeString(ev.Bytes)
				if err == nil {
					_, _ = os.Stdout.Write(data)
				}
			}
		}
		for push := range session.Events {
			if push.TerminalID != id {
				continue
			}
			switch push.Event.Kind {
			case goprotocol.OutputEvent:
				_, _ = os.Stdout.Write(push.Event.Data)
			case goprotocol.ExitEvent:
				return
			}
		}

	default:
		usage()
		os.Exit(2)
	}
}

func printJSON(v any) {
	b, _ := json.MarshalIndent(v, "", "  ")
	fmt.Println(string(b))
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func mustUUID(s string) uuid.UUID {
	v, err := uuid.Parse(s)
	must(err)
	return v
}

func mustInt(s string) int {
	v, err := strconv.Atoi(s)
	must(err)
	return v
}
