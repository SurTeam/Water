package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/SurTeam/Water/internal/goagent"
	"github.com/google/uuid"
)

func runAgent(ctx cliContext, args []string) error {
	if len(args) == 0 {
		return errors.New("agent requires list, export, configure or test")
	}
	command := map[string]any{}
	var configPath string
	for i := 1; i < len(args); i++ {
		if i+1 >= len(args) {
			return fmt.Errorf("%s requires a value", args[i])
		}
		flag, value := args[i], args[i+1]
		i++
		switch flag {
		case "--pane", "--terminal":
			id, err := uuid.Parse(value)
			if err != nil {
				return err
			}
			if flag == "--pane" {
				command["pane_id"] = id
			} else {
				command["terminal_id"] = id
			}
		case "--file":
			configPath = value
		default:
			return fmt.Errorf("unknown Agent option %s", flag)
		}
	}
	switch args[0] {
	case "list", "export":
		if len(command) > 0 || configPath != "" {
			return errors.New("agent list/export do not accept pane, terminal or file options")
		}
		command["type"] = "agents.inspect"
	case "configure", "test":
		if args[0] == "configure" {
			command["type"] = "agents.configure"
			if configPath == "" {
				return errors.New("agent configure requires --file with an Agent config object")
			}
		} else {
			command["type"] = "agents.test"
		}
		if configPath != "" {
			data, err := os.ReadFile(configPath)
			if err != nil {
				return err
			}
			cfg, err := goagent.ParseConfig(data)
			if err != nil {
				return err
			}
			command["config"] = cfg
		}
	default:
		return fmt.Errorf("unknown Agent command %q", args[0])
	}
	var out any
	if err := ctx.client.Dispatch(command, &out); err != nil {
		return err
	}
	if args[0] == "export" {
		return printJSON(out.(map[string]any)["config"])
	}
	return printJSON(out)
}
