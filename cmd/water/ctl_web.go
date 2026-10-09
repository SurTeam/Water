package main

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/SurTeam/Water/internal/goconfig"
	"github.com/google/uuid"
)

func runServerWeb(ctx cliContext, args []string) error {
	if len(args) == 0 {
		return errors.New("server web requires status, configure, start, stop, pair, cancel, devices or revoke")
	}
	method := ""
	params := map[string]any{}
	switch args[0] {
	case "status":
		method = "inspect"
	case "start", "stop":
		method = args[0]
	case "pair":
		method = "pair.create"
	case "cancel":
		if len(args) != 2 {
			return errors.New("server web cancel requires invitation UUID")
		}
		if _, err := uuid.Parse(args[1]); err != nil {
			return err
		}
		params["id"] = args[1]
		method = "pair.cancel"
	case "devices":
		method = "devices.list"
	case "revoke":
		if len(args) != 2 {
			return errors.New("server web revoke requires device UUID")
		}
		if _, err := uuid.Parse(args[1]); err != nil {
			return err
		}
		params["id"] = args[1]
		method = "devices.revoke"
	case "configure":
		cfg := goconfig.WebConfig{}
		portSet := false
		for i := 1; i < len(args); i++ {
			if i+1 >= len(args) {
				return fmt.Errorf("%s requires a value", args[i])
			}
			key, value := args[i], args[i+1]
			i++
			switch key {
			case "--listen-address":
				cfg.ListenAddress = value
			case "--listen-port":
				port, err := strconv.Atoi(value)
				if err != nil {
					return errors.New("--listen-port requires an integer")
				}
				cfg.ListenPort = port
				portSet = true
			case "--tls":
				if value != "true" && value != "false" {
					return errors.New("--tls requires true or false")
				}
				cfg.TLS = value == "true"
			case "--url":
				cfg.PublicURL = value
			case "--cert":
				cfg.TLSCertFile = value
			case "--key":
				cfg.TLSKeyFile = value
			case "--enabled":
				if value != "true" && value != "false" {
					return errors.New("--enabled requires true or false")
				}
				cfg.Enabled = value == "true"
			default:
				return fmt.Errorf("unknown Web setting %q", key)
			}
		}
		if cfg.PublicURL != "" && (cfg.ListenAddress != "" || portSet) {
			return errors.New("do not combine legacy --url with --listen-address or --listen-port")
		}
		if cfg.PublicURL == "" {
			if cfg.ListenAddress == "" {
				cfg.ListenAddress = "127.0.0.1"
			}
			if !portSet {
				cfg.ListenPort = 8080
			}
		}
		cfg = cfg.Normalized()
		if err := cfg.Validate(); err != nil {
			return err
		}
		var out any
		if err := ctx.client.CallTimeout("server.web.configure", cfg, &out, 10*time.Second); err != nil {
			return err
		}
		return printJSON(out)
	default:
		return fmt.Errorf("unknown Web command %q", args[0])
	}
	var out any
	if err := ctx.client.CallTimeout("server.web."+method, params, &out, 10*time.Second); err != nil {
		return err
	}
	return printJSON(out)
}
