package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/SurTeam/Water/internal/gobuild"
	"github.com/SurTeam/Water/internal/goclient"
	"github.com/SurTeam/Water/internal/goconfig"
	"github.com/SurTeam/Water/internal/gomodel"
	"github.com/SurTeam/Water/internal/goprotocol"
	"github.com/SurTeam/Water/internal/govt"
	"github.com/google/uuid"
)

var (
	buildVariant  = gobuild.Variant
	clientVersion = gobuild.Version
)

type cliContext struct {
	client *goclient.Client
	socket string
}

func run(arguments []string) error {
	socket, configPath, args, err := extractGlobal(arguments)
	if err != nil {
		return err
	}
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		printUsage()
		return nil
	}
	if args[0] == "ctl" {
		args = args[1:]
		if len(args) == 0 {
			printUsage()
			return nil
		}
	}
	if socket == "" {
		cfg, err := goconfig.Load(configPath)
		if err != nil {
			return fmt.Errorf("config: %w", err)
		}
		socket = resolveControlSocket(cfg)
	}
	ctx := cliContext{client: goclient.New(socket), socket: socket}
	return dispatchCLI(ctx, args)
}

func dispatchCLI(ctx cliContext, args []string) error {
	if len(args) == 0 {
		return errors.New("control command is required")
	}
	switch args[0] {
	case "state":
		var out any
		if err := ctx.client.Call("state.dump", map[string]any{}, &out); err != nil {
			return err
		}
		return printJSON(out)
	case "info":
		var server any
		if err := ctx.client.Call("server.info", map[string]any{}, &server); err != nil {
			return err
		}
		return printJSON(map[string]any{"client": clientInfo(), "server": server})
	case "version", "--version", "-V":
		return printJSON(clientInfo())
	case "client":
		if len(args) == 1 || args[1] == "info" || args[1] == "version" {
			return printJSON(clientInfo())
		}
		if args[1] == "connections" || args[1] == "connection" {
			return runConnections(ctx, args[2:])
		}
		return fmt.Errorf("unknown client command %q", args[1])
	case "connections", "connection", "sockets", "socket":
		return runConnections(ctx, args[1:])
	case "ui":
		return runUI(ctx, args[1:])
	case "server":
		return runServer(ctx, args[1:])
	case "debug":
		return runDebug(ctx, args[1:])
	case "workspace":
		return runWorkspace(ctx, args[1:])
	case "tab":
		return runTab(ctx, args[1:])
	case "pane":
		return runPane(ctx, args[1:])
	case "surface":
		return runSurface(ctx, args[1:])
	case "terminal":
		return runTerminal(ctx, args[1:])
	case "operation":
		return runOperation(ctx, args[1:])
	case "scenario":
		return runScenario(ctx, args[1:])
	case "ping":
		var out any
		if err := ctx.client.Call("ping", map[string]any{}, &out); err != nil {
			return err
		}
		fmt.Println("pong")
		return nil
	default:
		return fmt.Errorf("unknown control command %q", args[0])
	}
}

func clientInfo() map[string]any {
	return map[string]any{
		"build_variant":    buildVariant,
		"client_version":   clientVersion,
		"protocol_version": goprotocol.ProtocolVersion,
		"api_signature":    goprotocol.APISignature,
	}
}

func runConnections(ctx cliContext, args []string) error {
	if len(args) > 0 && args[0] != "list" {
		return fmt.Errorf("unknown connection command %q", args[0])
	}
	var out any
	if err := ctx.client.Call("connection.list", map[string]any{}, &out); err != nil {
		return err
	}
	return printJSON(out)
}

func runUI(ctx cliContext, args []string) error {
	if len(args) == 0 {
		return errors.New("ui requires key, state, screenshot, wheel, or click")
	}
	var out any
	switch args[0] {
	case "key", "keystroke":
		value, ok := option(args, "--keystroke")
		if !ok && len(args) > 1 && !strings.HasPrefix(args[1], "-") {
			value = args[1]
			ok = true
		}
		if !ok {
			return errors.New("ui key requires a keystroke")
		}
		if err := ctx.client.Call("ui.keystroke", map[string]any{"keystroke": value}, &out); err != nil {
			return err
		}
	case "state", "snapshot":
		if err := ctx.client.Call("ui.snapshot", map[string]any{}, &out); err != nil {
			return err
		}
	case "screenshot", "capture":
		path, _ := option(args, "--output")
		if path == "" && len(args) > 1 && !strings.HasPrefix(args[1], "-") {
			path = args[1]
		}
		if path == "" {
			path = "target/water-screenshot.png"
		}
		if err := ctx.client.Call("ui.screenshot", map[string]any{"path": path}, &out); err != nil {
			return err
		}
	case "wheel":
		x, err := optionFloatDefault(args, "--x", 120)
		if err != nil {
			return err
		}
		y, err := optionFloatDefault(args, "--y", 20)
		if err != nil {
			return err
		}
		dx, err := optionFloatDefault(args, "--dx", 0)
		if err != nil {
			return err
		}
		dy, err := optionFloatDefault(args, "--dy", 0)
		if err != nil {
			return err
		}
		if err := ctx.client.Call("ui.wheel", map[string]any{"x": x, "y": y, "dx": dx, "dy": dy}, &out); err != nil {
			return err
		}
	case "click":
		x, err := requiredFloat(args, "--x")
		if err != nil {
			return err
		}
		y, err := requiredFloat(args, "--y")
		if err != nil {
			return err
		}
		count, err := optionIntDefault(args, "--click-count", 1)
		if err != nil {
			return err
		}
		if err := ctx.client.Call("ui.click", map[string]any{"x": x, "y": y, "click_count": count}, &out); err != nil {
			return err
		}
	case "drag":
		x, err := requiredFloat(args, "--x")
		if err != nil {
			return err
		}
		y, err := requiredFloat(args, "--y")
		if err != nil {
			return err
		}
		toX, err := requiredFloat(args, "--to-x")
		if err != nil {
			return err
		}
		toY, err := requiredFloat(args, "--to-y")
		if err != nil {
			return err
		}
		if err := ctx.client.Call("ui.drag", map[string]any{"x": x, "y": y, "to_x": toX, "to_y": toY}, &out); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown ui command %q", args[0])
	}
	return printJSON(out)
}

func runServer(ctx cliContext, args []string) error {
	if len(args) == 0 {
		return errors.New("server requires info or shutdown")
	}
	var out any
	switch args[0] {
	case "info", "version":
		if err := ctx.client.Call("server.info", map[string]any{}, &out); err != nil {
			return err
		}
	case "connections", "connection", "sockets", "socket":
		return runConnections(ctx, args[1:])
	case "shutdown":
		if err := ctx.client.Call("server.shutdown", map[string]any{}, &out); err != nil {
			return err
		}
		fmt.Println("server shutdown requested")
		return nil
	default:
		return fmt.Errorf("unknown server command %q", args[0])
	}
	return printJSON(out)
}

func runDebug(ctx cliContext, args []string) error {
	if len(args) == 0 {
		return errors.New("debug requires memory or metrics")
	}
	var out any
	method := ""
	switch args[0] {
	case "memory":
		method = "debug.memory"
	case "metrics":
		method = "debug.metrics"
	default:
		return fmt.Errorf("unknown debug command %q", args[0])
	}
	if err := ctx.client.Call(method, map[string]any{}, &out); err != nil {
		return err
	}
	return printJSON(out)
}

func runWorkspace(ctx cliContext, args []string) error {
	if len(args) == 0 {
		return errors.New("workspace requires a command")
	}
	switch args[0] {
	case "create", "new":
		return dispatchAndPrint(ctx, map[string]any{"type": "workspace.new"})
	case "list":
		var state gomodel.StateDump
		if err := ctx.client.Call("state.dump", map[string]any{}, &state); err != nil {
			return err
		}
		return printJSON(state.Workspaces)
	case "activate":
		id, err := requiredIDOrBare(args, "--workspace", 1)
		if err != nil {
			return err
		}
		return dispatchAndPrint(ctx, map[string]any{"type": "workspace.activate", "workspace_id": id})
	case "rename":
		var id any
		if v, ok := optionalUUID(args, "--workspace"); ok {
			id = v
		}
		title, ok := option(args, "--title")
		if !ok {
			for _, v := range args[1:] {
				if !strings.HasPrefix(v, "-") {
					if _, e := uuid.Parse(v); e != nil {
						title = v
						break
					}
				}
			}
		}
		if title == "" {
			return errors.New("workspace rename requires --title")
		}
		return dispatchAndPrint(ctx, map[string]any{"type": "workspace.rename", "workspace_id": id, "title": title})
	case "reorder":
		id, err := requiredIDOrBare(args, "--workspace", 1)
		if err != nil {
			return err
		}
		index, err := requiredInt(args, "--index")
		if err != nil {
			return err
		}
		return dispatchAndPrint(ctx, map[string]any{"type": "workspace.reorder", "workspace_id": id, "index": index})
	case "close", "delete":
		var id any
		if v, ok := optionalUUID(args, "--workspace"); ok {
			id = v
		} else if v, ok := bareUUID(args[1:]); ok {
			id = v
		}
		return dispatchAndPrint(ctx, map[string]any{"type": "workspace.delete", "workspace_id": id})
	default:
		return fmt.Errorf("unknown workspace command %q", args[0])
	}
}

func runTab(ctx cliContext, args []string) error {
	if len(args) == 0 {
		return errors.New("tab requires a command")
	}
	switch args[0] {
	case "new":
		cmd := map[string]any{"type": "tab.new"}
		if title, ok := option(args, "--title"); ok {
			cmd["title"] = title
		}
		if wid, ok := optionalUUID(args, "--workspace"); ok {
			cmd["type"] = "tab.new_in_workspace"
			cmd["workspace_id"] = wid
		}
		return dispatchAndPrint(ctx, cmd)
	case "rename":
		cmd := map[string]any{"type": "tab.rename"}
		if id, ok := optionalUUID(args, "--tab"); ok {
			cmd["tab_id"] = id
		} else if id, ok := bareUUID(args[1:]); ok {
			cmd["tab_id"] = id
		}
		title, ok := option(args, "--title")
		if !ok {
			for _, v := range args[1:] {
				if !strings.HasPrefix(v, "-") {
					if _, e := uuid.Parse(v); e != nil {
						title = v
						break
					}
				}
			}
		}
		if title == "" {
			return errors.New("tab rename requires --title")
		}
		cmd["title"] = title
		return dispatchAndPrint(ctx, cmd)
	case "close":
		cmd := map[string]any{"type": "tab.close"}
		if id, ok := optionalUUID(args, "--tab"); ok {
			cmd["tab_id"] = id
		} else if id, ok := bareUUID(args[1:]); ok {
			cmd["tab_id"] = id
		}
		return dispatchAndPrint(ctx, cmd)
	case "activate":
		cmd := map[string]any{"type": "tab.activate"}
		if id, ok := optionalUUID(args, "--tab"); ok {
			cmd["tab_id"] = id
		} else if id, ok := bareUUID(args[1:]); ok {
			cmd["tab_id"] = id
		}
		if idx, ok, err := optionalInt(args, "--index"); err != nil {
			return err
		} else if ok {
			cmd["index"] = idx
		}
		if _, hasID := cmd["tab_id"]; !hasID {
			if _, hasIndex := cmd["index"]; !hasIndex {
				return errors.New("tab activate requires an ID or --index")
			}
		}
		return dispatchAndPrint(ctx, cmd)
	case "move-to-workspace":
		tabID, err := requiredIDOrBare(args, "--tab", 1)
		if err != nil {
			return err
		}
		wid, ok := optionalUUID(args, "--workspace")
		if !ok {
			return errors.New("tab move-to-workspace requires --workspace")
		}
		return dispatchAndPrint(ctx, map[string]any{"type": "tab.move_to_workspace", "tab_id": tabID, "workspace_id": wid})
	default:
		return fmt.Errorf("unknown tab command %q", args[0])
	}
}

func runPane(ctx cliContext, args []string) error {
	if len(args) == 0 {
		return errors.New("pane requires a command")
	}
	switch args[0] {
	case "split":
		direction := ""
		for _, d := range []string{"left", "right", "up", "down"} {
			if hasFlag(args, "--"+d) {
				direction = d
				break
			}
		}
		if direction == "" {
			return errors.New("pane split requires --left, --right, --up, or --down")
		}
		cmd := map[string]any{"type": "pane.split", "direction": direction}
		if id, ok := optionalUUID(args, "--pane"); ok {
			cmd["pane_id"] = id
		}
		return dispatchAndPrint(ctx, cmd)
	case "close":
		cmd := map[string]any{"type": "pane.close"}
		if id, ok := optionalUUID(args, "--pane"); ok {
			cmd["pane_id"] = id
		} else if id, ok := bareUUID(args[1:]); ok {
			cmd["pane_id"] = id
		}
		return dispatchAndPrint(ctx, cmd)
	case "focus":
		cmd := map[string]any{"type": "pane.focus"}
		if id, ok := optionalUUID(args, "--pane"); ok {
			cmd["pane_id"] = id
		} else if id, ok := bareUUID(args[1:]); ok {
			cmd["pane_id"] = id
		}
		for _, d := range []string{"left", "right", "up", "down"} {
			if hasFlag(args, "--"+d) {
				cmd["direction"] = d
				break
			}
		}
		if _, idOK := cmd["pane_id"]; !idOK {
			if _, dirOK := cmd["direction"]; !dirOK {
				return errors.New("pane focus requires an ID or direction")
			}
		}
		return dispatchAndPrint(ctx, cmd)
	case "resize":
		ratio, err := requiredFloat(args, "--ratio")
		if err != nil {
			return err
		}
		cmd := map[string]any{"type": "pane.resize", "ratio": ratio}
		if id, ok := optionalUUID(args, "--pane"); ok {
			cmd["pane_id"] = id
		}
		return dispatchAndPrint(ctx, cmd)
	case "resize-split":
		ratio, err := requiredFloat(args, "--ratio")
		if err != nil {
			return err
		}
		tabID, ok := optionalUUID(args, "--tab")
		if !ok {
			return errors.New("pane resize-split requires --tab")
		}
		var path []bool
		if raw, ok := option(args, "--path"); ok {
			for _, part := range strings.Split(raw, ",") {
				part = strings.TrimSpace(part)
				if part == "" {
					continue
				}
				switch part {
				case "0", "false":
					path = append(path, false)
				case "1", "true":
					path = append(path, true)
				default:
					return fmt.Errorf("invalid path segment %q", part)
				}
			}
		}
		return dispatchAndPrint(ctx, map[string]any{"type": "pane.resize_split", "tab_id": tabID, "path": path, "ratio": ratio})
	case "rename-agent", "agent-rename":
		cmd := map[string]any{"type": "pane.agent_rename"}
		if id, ok := optionalUUID(args, "--pane"); ok {
			cmd["pane_id"] = id
		}
		label, ok := option(args, "--label")
		if !ok && len(args) > 1 && !strings.HasPrefix(args[1], "-") {
			label = args[1]
		}
		if label == "" {
			return errors.New("pane rename-agent requires --label")
		}
		cmd["label"] = label
		return dispatchAndPrint(ctx, cmd)
	case "move-to-workspace":
		pid, err := requiredIDOrBare(args, "--pane", 1)
		if err != nil {
			return err
		}
		wid, ok := optionalUUID(args, "--workspace")
		if !ok {
			return errors.New("pane move-to-workspace requires --workspace")
		}
		return dispatchAndPrint(ctx, map[string]any{"type": "pane.move_to_workspace", "pane_id": pid, "workspace_id": wid})
	case "promote", "promote-to-tab":
		cmd := map[string]any{"type": "pane.promote_to_tab"}
		if id, ok := optionalUUID(args, "--pane"); ok {
			cmd["pane_id"] = id
		} else if id, ok := bareUUID(args[1:]); ok {
			cmd["pane_id"] = id
		}
		return dispatchAndPrint(ctx, cmd)
	case "input", "send":
		pid, err := requiredIDOrBare(args, "--pane", 1)
		if err != nil {
			return err
		}
		text, ok := option(args, "--text")
		if !ok {
			var values []string
			skipFirstBare := true
			for _, v := range args[1:] {
				if strings.HasPrefix(v, "-") {
					continue
				}
				if skipFirstBare {
					if _, e := uuid.Parse(v); e == nil {
						skipFirstBare = false
						continue
					}
					skipFirstBare = false
				}
				values = append(values, v)
			}
			text = strings.Join(values, " ")
		}
		if text == "" {
			return errors.New("pane input requires --text")
		}
		return dispatchAndPrint(ctx, map[string]any{"type": "terminal.send_text", "pane_id": pid, "text": text})
	case "content":
		return printPaneContent(ctx, args)
	default:
		return fmt.Errorf("unknown pane command %q", args[0])
	}
}

func runSurface(ctx cliContext, args []string) error {
	if len(args) == 0 || args[0] != "replace" {
		return errors.New("surface requires replace")
	}
	if !hasFlag(args, "--empty") {
		return errors.New("surface replace currently requires --empty")
	}
	cmd := map[string]any{"type": "surface.replace", "kind": "empty"}
	if id, ok := optionalUUID(args, "--pane"); ok {
		cmd["pane_id"] = id
	}
	return dispatchAndPrint(ctx, cmd)
}

func runTerminal(ctx cliContext, args []string) error {
	if len(args) == 0 {
		return errors.New("terminal requires a command")
	}
	switch args[0] {
	case "spawn":
		cmd := map[string]any{"type": "terminal.spawn"}
		if id, ok := optionalUUID(args, "--pane"); ok {
			cmd["pane_id"] = id
		}
		cols, err := optionIntDefault(args, "--columns", 80)
		if err != nil {
			return err
		}
		rows, err := optionIntDefault(args, "--lines", 24)
		if err != nil {
			return err
		}
		cmd["columns"] = cols
		cmd["lines"] = rows
		program, hasProgram := option(args, "--program")
		pos := terminalPositionals(args)
		if !hasProgram && len(pos) > 0 {
			program = pos[0]
			pos = pos[1:]
		}
		if program == "" {
			cfg, _, _ := goconfig.LoadDefault(buildVariant)
			program = cfg.Shell.Program
			if len(pos) == 0 {
				pos = append([]string(nil), cfg.Shell.Args...)
			}
		}
		cmd["program"] = program
		cmd["args"] = pos
		return dispatchAndPrint(ctx, cmd)
	case "send", "send-text":
		cmd := map[string]any{"type": "terminal.send_text"}
		if id, ok := optionalUUID(args, "--terminal"); ok {
			cmd["terminal_id"] = id
		}
		if id, ok := optionalUUID(args, "--pane"); ok {
			cmd["pane_id"] = id
		}
		text, ok := option(args, "--text")
		if !ok {
			text = strings.Join(terminalPositionals(args), " ")
		}
		if text == "" {
			return errors.New("terminal send requires text")
		}
		cmd["text"] = text
		return dispatchAndPrint(ctx, cmd)
	case "send-bytes":
		cmd := map[string]any{"type": "terminal.send_bytes"}
		if id, ok := optionalUUID(args, "--terminal"); ok {
			cmd["terminal_id"] = id
		}
		if id, ok := optionalUUID(args, "--pane"); ok {
			cmd["pane_id"] = id
		}
		raw, ok := option(args, "--hex")
		if !ok {
			raw = strings.Join(terminalPositionals(args), "")
		}
		bytes, err := parseHexBytes(raw)
		if err != nil {
			return err
		}
		if len(bytes) == 0 {
			return errors.New("terminal send-bytes requires hexadecimal bytes")
		}
		ints := make([]int, len(bytes))
		for i, b := range bytes {
			ints[i] = int(b)
		}
		cmd["bytes"] = ints
		return dispatchAndPrint(ctx, cmd)
	case "resize":
		cols, err := requiredInt(args, "--columns")
		if err != nil {
			return err
		}
		rows, err := requiredInt(args, "--lines")
		if err != nil {
			return err
		}
		cmd := map[string]any{"type": "terminal.resize", "columns": cols, "lines": rows, "cell_width": 0, "cell_height": 0}
		if id, ok := optionalUUID(args, "--terminal"); ok {
			cmd["terminal_id"] = id
		}
		if id, ok := optionalUUID(args, "--pane"); ok {
			cmd["pane_id"] = id
		}
		return dispatchAndPrint(ctx, cmd)
	case "scroll":
		lines, err := requiredInt(args, "--lines")
		if err != nil {
			return err
		}
		cmd := map[string]any{"type": "terminal.scroll", "lines": lines}
		if id, ok := optionalUUID(args, "--terminal"); ok {
			cmd["terminal_id"] = id
		}
		if id, ok := optionalUUID(args, "--pane"); ok {
			cmd["pane_id"] = id
		}
		return dispatchAndPrint(ctx, cmd)
	case "contains":
		id, ok := optionalUUID(args, "--terminal")
		if !ok {
			return errors.New("terminal contains requires --terminal")
		}
		text, ok := option(args, "--text")
		if !ok {
			text = strings.Join(terminalPositionals(args), " ")
		}
		timeout, err := optionIntDefault(args, "--timeout-ms", 5000)
		if err != nil {
			return err
		}
		var out any
		if err := ctx.client.Call("terminal.contains", map[string]any{"terminal_id": id, "text": text, "timeout_ms": timeout}, &out); err != nil {
			return err
		}
		return printJSON(out)
	case "wait-exit":
		id, ok := optionalUUID(args, "--terminal")
		if !ok {
			return errors.New("terminal wait-exit requires --terminal")
		}
		timeout, err := optionIntDefault(args, "--timeout-ms", 5000)
		if err != nil {
			return err
		}
		var out any
		if err := ctx.client.Call("terminal.wait_exit", map[string]any{"terminal_id": id, "timeout_ms": timeout}, &out); err != nil {
			return err
		}
		return printJSON(out)
	case "snapshot":
		id, ok := optionalUUID(args, "--terminal")
		if !ok {
			return errors.New("terminal snapshot requires --terminal")
		}
		var out any
		if err := ctx.client.Call("terminal.snapshot", map[string]any{"terminal_id": id}, &out); err != nil {
			return err
		}
		return printJSON(out)
	case "attach":
		id, ok := optionalUUID(args, "--terminal")
		if !ok && len(args) > 1 {
			id, ok = bareUUID(args[1:])
		}
		if !ok {
			return errors.New("terminal attach requires terminal ID")
		}
		return attachTerminal(ctx, id)
	default:
		return fmt.Errorf("unknown terminal command %q", args[0])
	}
}

func runOperation(ctx cliContext, args []string) error {
	if len(args) < 2 {
		return errors.New("operation requires get|wait OPERATION_ID")
	}
	id, err := uuid.Parse(args[1])
	if err != nil {
		return fmt.Errorf("invalid operation ID: %w", err)
	}
	method := ""
	switch args[0] {
	case "get":
		method = "operation.get"
	case "wait":
		method = "operation.wait"
	default:
		return fmt.Errorf("unknown operation command %q", args[0])
	}
	var out any
	if err := ctx.client.Call(method, map[string]any{"operation_id": id}, &out); err != nil {
		return err
	}
	return printJSON(out)
}

func runScenario(ctx cliContext, args []string) error {
	if len(args) < 2 || args[0] != "run" {
		return errors.New("scenario requires: scenario run PATH")
	}
	return runScenarioFile(ctx, args[1])
}

type cliOperation struct {
	ID     uuid.UUID            `json:"id"`
	Status string               `json:"status"`
	Result json.RawMessage      `json:"result,omitempty"`
	Error  *goprotocol.RPCError `json:"error,omitempty"`
}

func dispatchOperation(ctx cliContext, command any) (uuid.UUID, cliOperation, error) {
	var ack struct {
		OperationID uuid.UUID `json:"operation_id"`
	}
	if err := ctx.client.Call("command.dispatch", map[string]any{"command": command}, &ack); err != nil {
		return uuid.Nil, cliOperation{}, err
	}
	var op cliOperation
	if err := ctx.client.Call("operation.wait", map[string]any{"operation_id": ack.OperationID}, &op); err != nil {
		return ack.OperationID, cliOperation{}, err
	}
	if op.Status == "failed" {
		if op.Error != nil {
			return ack.OperationID, op, op.Error
		}
		return ack.OperationID, op, errors.New("operation failed")
	}
	return ack.OperationID, op, nil
}

func dispatchAndPrint(ctx cliContext, command any) error {
	_, op, err := dispatchOperation(ctx, command)
	if err != nil {
		return err
	}
	return printJSON(op)
}

type terminalReplay struct {
	TerminalID uuid.UUID                      `json:"terminal_id"`
	Size       goprotocol.TerminalSize        `json:"size"`
	Events     []goprotocol.WireTerminalEvent `json:"events"`
}

type cliPaneTree struct {
	Type     string    `json:"type"`
	PaneID   uuid.UUID `json:"pane_id"`
	Terminal *struct {
		Summary struct {
			TerminalID uuid.UUID `json:"terminal_id"`
		} `json:"summary"`
	} `json:"terminal"`
	First  *cliPaneTree `json:"first"`
	Second *cliPaneTree `json:"second"`
}

func printPaneContent(ctx cliContext, args []string) error {
	paneID, err := requiredIDOrBare(args, "--pane", 1)
	if err != nil {
		return err
	}
	var state gomodel.StateDump
	if err := ctx.client.Call("state.dump", map[string]any{}, &state); err != nil {
		return err
	}
	terminalID, ok := terminalForPaneState(state, paneID)
	if !ok {
		return errors.New("the requested pane does not contain a terminal")
	}
	var replay terminalReplay
	if err := ctx.client.Call("terminal.snapshot", map[string]any{"terminal_id": terminalID}, &replay); err != nil {
		return err
	}
	emu := govt.New(replay.Size.Columns, replay.Size.Lines, 10000)
	defer emu.Close()
	for _, ev := range replay.Events {
		applyCLIWireEvent(emu, ev)
	}
	snap := emu.Snapshot()
	row, _ := optionIntDefault(args, "--row", 0)
	col, _ := optionIntDefault(args, "--column", 0)
	rows, _ := optionIntDefault(args, "--rows", snap.Rows-row)
	cols, _ := optionIntDefault(args, "--columns", snap.Cols-col)
	if row < 0 {
		row = 0
	}
	if col < 0 {
		col = 0
	}
	rowEnd := minInt(snap.Rows, row+maxInt(rows, 0))
	colEnd := minInt(snap.Cols, col+maxInt(cols, 0))
	lines := make([]string, 0, maxInt(rowEnd-row, 0))
	for y := row; y < rowEnd; y++ {
		var b strings.Builder
		for x := col; x < colEnd; x++ {
			cell := snap.RowsData[y].Cells[x]
			if cell.Width == 0 {
				continue
			}
			if cell.Text == "" {
				b.WriteByte(' ')
			} else {
				b.WriteString(cell.Text)
			}
		}
		lines = append(lines, strings.TrimRight(b.String(), " "))
	}
	return printJSON(map[string]any{
		"pane_id": paneID, "terminal_id": terminalID,
		"row": row, "column": col, "rows": rowEnd - row, "columns": colEnd - col,
		"text": strings.Join(lines, "\n"), "lines": lines,
	})
}

func terminalForPaneState(state gomodel.StateDump, pane uuid.UUID) (uuid.UUID, bool) {
	for _, w := range state.Workspaces {
		for _, tab := range w.Tabs {
			var root cliPaneTree
			if json.Unmarshal(tab.Tree, &root) == nil {
				if id, ok := findTerminalInTree(&root, pane); ok {
					return id, true
				}
			}
		}
	}
	return uuid.Nil, false
}

func findTerminalInTree(n *cliPaneTree, pane uuid.UUID) (uuid.UUID, bool) {
	if n == nil {
		return uuid.Nil, false
	}
	if n.Type == "leaf" {
		if n.PaneID == pane && n.Terminal != nil && n.Terminal.Summary.TerminalID != uuid.Nil {
			return n.Terminal.Summary.TerminalID, true
		}
		return uuid.Nil, false
	}
	if id, ok := findTerminalInTree(n.First, pane); ok {
		return id, true
	}
	return findTerminalInTree(n.Second, pane)
}

func applyCLIWireEvent(emu *govt.Emulator, ev goprotocol.WireTerminalEvent) {
	switch ev.Type {
	case "output":
		if data, err := base64.StdEncoding.DecodeString(ev.Bytes); err == nil {
			emu.Write(data)
		}
	case "resize":
		emu.Resize(ev.Columns, ev.Lines)
	}
}

func attachTerminal(ctx cliContext, id uuid.UUID) error {
	session, err := ctx.client.OpenSession()
	if err != nil {
		return err
	}
	defer session.Close()
	var attached struct {
		Replay []goprotocol.WireTerminalEvent `json:"replay"`
	}
	if err := session.Attach(id, &attached); err != nil {
		return err
	}
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
		if push.Event.Kind == goprotocol.OutputEvent {
			_, _ = os.Stdout.Write(push.Event.Data)
		}
		if push.Event.Kind == goprotocol.ExitEvent {
			return nil
		}
	}
	return nil
}

func extractGlobal(args []string) (socket, config string, filtered []string, err error) {
	config = goconfig.DefaultLoadPath(buildVariant)
	for i := 0; i < len(args); {
		arg := args[i]
		switch {
		case arg == "--socket" || arg == "--control-socket":
			if i+1 >= len(args) {
				return "", "", nil, fmt.Errorf("%s requires a path", arg)
			}
			socket = args[i+1]
			i += 2
		case strings.HasPrefix(arg, "--socket="):
			socket = strings.TrimPrefix(arg, "--socket=")
			i++
		case strings.HasPrefix(arg, "--control-socket="):
			socket = strings.TrimPrefix(arg, "--control-socket=")
			i++
		case arg == "--config":
			if i+1 >= len(args) {
				return "", "", nil, errors.New("--config requires a path")
			}
			config = args[i+1]
			i += 2
		case strings.HasPrefix(arg, "--config="):
			config = strings.TrimPrefix(arg, "--config=")
			i++
		default:
			filtered = append(filtered, arg)
			i++
		}
	}
	return
}

func resolveControlSocket(cfg goconfig.AppConfig) string {
	if p := strings.TrimSpace(os.Getenv("WATER_CONTROL_SOCKET")); p != "" {
		return p
	}
	if p := strings.TrimSpace(os.Getenv("WATER_SOCKET")); p != "" {
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

func option(args []string, name string) (string, bool) {
	for i, arg := range args {
		if arg == name && i+1 < len(args) {
			return args[i+1], true
		}
		if strings.HasPrefix(arg, name+"=") {
			return strings.TrimPrefix(arg, name+"="), true
		}
	}
	return "", false
}

func hasFlag(args []string, name string) bool {
	for _, v := range args {
		if v == name {
			return true
		}
	}
	return false
}
func optionalUUID(args []string, name string) (uuid.UUID, bool) {
	v, ok := option(args, name)
	if !ok {
		return uuid.Nil, false
	}
	id, e := uuid.Parse(v)
	return id, e == nil
}
func bareUUID(args []string) (uuid.UUID, bool) {
	for _, v := range args {
		if strings.HasPrefix(v, "-") {
			continue
		}
		if id, e := uuid.Parse(v); e == nil {
			return id, true
		}
	}
	return uuid.Nil, false
}
func requiredIDOrBare(args []string, name string, start int) (uuid.UUID, error) {
	if id, ok := optionalUUID(args, name); ok {
		return id, nil
	}
	if start < len(args) {
		if id, ok := bareUUID(args[start:]); ok {
			return id, nil
		}
	}
	return uuid.Nil, fmt.Errorf("%s or an ID is required", name)
}
func optionalInt(args []string, name string) (int, bool, error) {
	v, ok := option(args, name)
	if !ok {
		return 0, false, nil
	}
	n, e := strconv.Atoi(v)
	if e != nil {
		return 0, false, fmt.Errorf("invalid value for %s", name)
	}
	return n, true, nil
}
func requiredInt(args []string, name string) (int, error) {
	v, ok, e := optionalInt(args, name)
	if e != nil {
		return 0, e
	}
	if !ok {
		return 0, fmt.Errorf("%s is required", name)
	}
	return v, nil
}
func optionIntDefault(args []string, name string, def int) (int, error) {
	v, ok, e := optionalInt(args, name)
	if e != nil {
		return 0, e
	}
	if !ok {
		return def, nil
	}
	return v, nil
}
func requiredFloat(args []string, name string) (float32, error) {
	v, ok := option(args, name)
	if !ok {
		return 0, fmt.Errorf("%s is required", name)
	}
	n, e := strconv.ParseFloat(v, 32)
	if e != nil {
		return 0, fmt.Errorf("invalid value for %s", name)
	}
	return float32(n), nil
}
func optionFloatDefault(args []string, name string, def float32) (float32, error) {
	v, ok := option(args, name)
	if !ok {
		return def, nil
	}
	n, e := strconv.ParseFloat(v, 32)
	if e != nil {
		return 0, fmt.Errorf("invalid value for %s", name)
	}
	return float32(n), nil
}

func terminalPositionals(args []string) []string {
	var out []string
	valueFlags := map[string]bool{"--pane": true, "--terminal": true, "--columns": true, "--lines": true, "--timeout-ms": true, "--program": true, "--text": true, "--hex": true}
	for i := 1; i < len(args); i++ {
		if valueFlags[args[i]] {
			i++
			continue
		}
		if strings.Contains(args[i], "=") && strings.HasPrefix(args[i], "--") {
			continue
		}
		if strings.HasPrefix(args[i], "-") {
			continue
		}
		out = append(out, args[i])
	}
	return out
}

func parseHexBytes(raw string) ([]byte, error) {
	var digits strings.Builder
	for _, r := range raw {
		if r == ' ' || r == '\t' || r == '\n' || r == ':' {
			continue
		}
		digits.WriteRune(r)
	}
	s := digits.String()
	if len(s)%2 != 0 {
		return nil, errors.New("hex byte input must contain pairs of digits")
	}
	out := make([]byte, len(s)/2)
	for i := 0; i < len(out); i++ {
		v, e := strconv.ParseUint(s[i*2:i*2+2], 16, 8)
		if e != nil {
			return nil, fmt.Errorf("invalid hexadecimal bytes: %w", e)
		}
		out[i] = byte(v)
	}
	return out, nil
}

func printJSON(v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func printUsage() {
	fmt.Print(`water ctl [--socket PATH] <command> ...

Introspection:
  water ctl state
  water ctl info
  water ctl version
  water ctl server info
  water ctl connections list
  water ctl debug memory
  water ctl debug metrics

UI:
  water ctl ui key cmd-t
  water ctl ui state
  water ctl ui screenshot --output target/water.png
  water ctl ui click --x 240 --y 100 --click-count 2
  water ctl ui drag --x 600 --y 300 --to-x 720 --to-y 300
  water ctl ui wheel --x 120 --y 20 --dx 0 --dy 3

Workspace/tab/pane:
  water ctl workspace new
  water ctl workspace list
  water ctl workspace rename --workspace UUID --title Dev
  water ctl workspace reorder --workspace UUID --index 0
  water ctl tab new --title Main
  water ctl tab activate --index 0
  water ctl pane split --right
  water ctl pane focus --left
  water ctl pane resize --ratio 0.6
  water ctl pane promote-to-tab
  water ctl pane content --pane UUID --row 0 --rows 4

Terminal:
  water ctl terminal spawn --program /bin/sh --columns 80 --lines 24
  water ctl terminal send --terminal UUID --text 'printf hello\\n'
  water ctl terminal send-bytes --terminal UUID --hex '03'
  water ctl terminal resize --terminal UUID --columns 120 --lines 40
  water ctl terminal contains --terminal UUID --text READY
  water ctl terminal wait-exit --terminal UUID
  water ctl terminal snapshot --terminal UUID
  water ctl terminal attach --terminal UUID

Operations/scenarios:
  water ctl operation get UUID
  water ctl operation wait UUID
  water ctl scenario run PATH
`)
}

type scenarioFile struct {
	Name  string         `json:"name"`
	Steps []scenarioStep `json:"steps"`
}
type scenarioStep struct {
	Command map[string]any     `json:"command,omitempty"`
	Assert  *scenarioAssertion `json:"assert,omitempty"`
	Wait    *scenarioWait      `json:"wait,omitempty"`
}
type scenarioAssertion struct {
	Type  string `json:"type"`
	Value uint64 `json:"value,omitempty"`
	Kind  string `json:"kind,omitempty"`
}
type scenarioWait struct {
	Type        string     `json:"type"`
	OperationID *uuid.UUID `json:"operation_id,omitempty"`
	EventType   string     `json:"event_type,omitempty"`
	Value       uint64     `json:"value,omitempty"`
	TerminalID  *uuid.UUID `json:"terminal_id,omitempty"`
	Text        string     `json:"text,omitempty"`
	TimeoutMS   int        `json:"timeout_ms,omitempty"`
}

func runScenarioFile(ctx cliContext, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var scenario scenarioFile
	if err := json.Unmarshal(data, &scenario); err != nil {
		return fmt.Errorf("invalid scenario: %w", err)
	}
	var lastOperation *uuid.UUID
	var lastTerminal *uuid.UUID
	var lastEvent uint64

	for index, step := range scenario.Steps {
		actions := 0
		if step.Command != nil {
			actions++
		}
		if step.Assert != nil {
			actions++
		}
		if step.Wait != nil {
			actions++
		}
		if actions != 1 {
			return fmt.Errorf("scenario step %d: each step must contain exactly one of command, assert, or wait", index)
		}

		if step.Command != nil {
			opID, op, err := dispatchOperation(ctx, step.Command)
			if err != nil {
				return fmt.Errorf("scenario step %d: %w", index, err)
			}
			lastOperation = &opID
			if len(op.Result) > 0 {
				var result map[string]any
				if json.Unmarshal(op.Result, &result) == nil {
					if raw, ok := result["terminal_id"].(string); ok {
						if id, e := uuid.Parse(raw); e == nil {
							lastTerminal = &id
						}
					}
				}
			}
			continue
		}
		if step.Assert != nil {
			if err := runScenarioAssertion(ctx, *step.Assert); err != nil {
				return fmt.Errorf("scenario assertion failed at step %d: %w", index, err)
			}
			continue
		}
		if err := runScenarioWait(ctx, *step.Wait, lastOperation, lastTerminal, &lastEvent); err != nil {
			return fmt.Errorf("scenario wait failed at step %d: %w", index, err)
		}
	}
	if scenario.Name == "" {
		scenario.Name = path
	}
	fmt.Printf("scenario %s: passed\n", scenario.Name)
	return nil
}

func runScenarioAssertion(ctx cliContext, a scenarioAssertion) error {
	var state gomodel.StateDump
	if err := ctx.client.Call("state.dump", map[string]any{}, &state); err != nil {
		return err
	}
	switch a.Type {
	case "pane_count":
		got := activePaneCountCLI(state)
		if uint64(got) != a.Value {
			return fmt.Errorf("pane count %d != %d", got, a.Value)
		}
	case "tab_count":
		w := activeWorkspaceCLI(state)
		got := 0
		if w != nil {
			got = len(w.Tabs)
		}
		if uint64(got) != a.Value {
			return fmt.Errorf("tab count %d != %d", got, a.Value)
		}
	case "active_surface_kind":
		kind := activeSurfaceKindCLI(state)
		if kind != a.Kind {
			return fmt.Errorf("active surface kind %q != %q", kind, a.Kind)
		}
	case "state_revision_at_least":
		if state.StateRevision < a.Value {
			return fmt.Errorf("state revision %d is below %d", state.StateRevision, a.Value)
		}
	default:
		return fmt.Errorf("unsupported assertion %q", a.Type)
	}
	return nil
}

func runScenarioWait(ctx cliContext, w scenarioWait, lastOperation, lastTerminal *uuid.UUID, lastEvent *uint64) error {
	timeout := w.TimeoutMS
	if timeout <= 0 {
		timeout = 5000
	}
	switch w.Type {
	case "operation_complete", "app_idle":
		var id *uuid.UUID
		if w.OperationID != nil {
			id = w.OperationID
		} else {
			id = lastOperation
		}
		if id == nil {
			if w.Type == "app_idle" {
				return nil
			}
			return errors.New("operation_complete requires a prior command or operation_id")
		}
		var op cliOperation
		if err := ctx.client.Call("operation.wait", map[string]any{"operation_id": *id}, &op); err != nil {
			return err
		}
		if op.Status == "failed" {
			if op.Error != nil {
				return op.Error
			}
			return errors.New("operation failed")
		}
		return nil
	case "state_revision_at_least":
		var state gomodel.StateDump
		if err := ctx.client.Call("state.dump", map[string]any{}, &state); err != nil {
			return err
		}
		if state.StateRevision < w.Value {
			return fmt.Errorf("state revision %d is below %d", state.StateRevision, w.Value)
		}
		return nil
	case "terminal_contains":
		id := w.TerminalID
		if id == nil {
			id = lastTerminal
		}
		if id == nil {
			return errors.New("terminal_contains requires terminal_id or prior terminal.spawn")
		}
		var out any
		return ctx.client.Call("terminal.contains", map[string]any{"terminal_id": *id, "text": w.Text, "timeout_ms": timeout}, &out)
	case "process_exit":
		id := w.TerminalID
		if id == nil {
			id = lastTerminal
		}
		if id == nil {
			return errors.New("process_exit requires terminal_id or prior terminal.spawn")
		}
		var out any
		return ctx.client.Call("terminal.wait_exit", map[string]any{"terminal_id": *id, "timeout_ms": timeout}, &out)
	case "event":
		var events []struct {
			Sequence uint64         `json:"sequence"`
			Kind     map[string]any `json:"kind"`
		}
		if err := ctx.client.Call("event.list", map[string]any{"after_sequence": *lastEvent}, &events); err != nil {
			return err
		}
		found := false
		for _, event := range events {
			if event.Sequence > *lastEvent {
				*lastEvent = event.Sequence
			}
			wire, _ := event.Kind["type"].(string)
			if eventTypeName(wire) == w.EventType {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("event %q was not observed", w.EventType)
		}
		return nil
	default:
		return fmt.Errorf("unsupported wait %q", w.Type)
	}
}

func eventTypeName(wire string) string {
	if i := strings.IndexByte(wire, '_'); i >= 0 {
		return wire[:i] + "." + wire[i+1:]
	}
	return wire
}

func activeWorkspaceCLI(state gomodel.StateDump) *gomodel.WorkspaceDump {
	if state.Workspace != nil {
		return state.Workspace
	}
	if state.ActiveWorkspace != nil {
		for i := range state.Workspaces {
			if state.Workspaces[i].ID == *state.ActiveWorkspace {
				return &state.Workspaces[i]
			}
		}
	}
	if len(state.Workspaces) > 0 {
		return &state.Workspaces[0]
	}
	return nil
}

func activePaneCountCLI(state gomodel.StateDump) int {
	w := activeWorkspaceCLI(state)
	if w == nil {
		return 0
	}
	var tab *gomodel.TabDump
	if w.ActiveTab != nil {
		for i := range w.Tabs {
			if w.Tabs[i].ID == *w.ActiveTab {
				tab = &w.Tabs[i]
				break
			}
		}
	}
	if tab == nil && len(w.Tabs) > 0 {
		tab = &w.Tabs[0]
	}
	if tab == nil {
		return 0
	}
	var root cliPaneTree
	if json.Unmarshal(tab.Tree, &root) != nil {
		return 0
	}
	return paneTreeCount(&root)
}

func paneTreeCount(n *cliPaneTree) int {
	if n == nil {
		return 0
	}
	if n.Type == "leaf" {
		return 1
	}
	return paneTreeCount(n.First) + paneTreeCount(n.Second)
}

func activeSurfaceKindCLI(state gomodel.StateDump) string {
	w := activeWorkspaceCLI(state)
	if w == nil || w.ActiveTab == nil {
		return ""
	}
	var tab *gomodel.TabDump
	for i := range w.Tabs {
		if w.Tabs[i].ID == *w.ActiveTab {
			tab = &w.Tabs[i]
			break
		}
	}
	if tab == nil {
		return ""
	}
	var root struct {
		Type        string          `json:"type"`
		PaneID      uuid.UUID       `json:"pane_id"`
		SurfaceKind string          `json:"surface_kind"`
		First       json.RawMessage `json:"first"`
		Second      json.RawMessage `json:"second"`
	}
	var walk func(json.RawMessage) string
	walk = func(raw json.RawMessage) string {
		var n struct {
			Type        string          `json:"type"`
			PaneID      uuid.UUID       `json:"pane_id"`
			SurfaceKind string          `json:"surface_kind"`
			First       json.RawMessage `json:"first"`
			Second      json.RawMessage `json:"second"`
		}
		if json.Unmarshal(raw, &n) != nil {
			return ""
		}
		if n.Type == "leaf" {
			if n.PaneID == tab.ActivePane {
				return n.SurfaceKind
			}
			return ""
		}
		if v := walk(n.First); v != "" {
			return v
		}
		return walk(n.Second)
	}
	raw := tab.Tree
	if json.Unmarshal(raw, &root) != nil {
		return ""
	}
	return walk(raw)
}
