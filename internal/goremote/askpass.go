package goremote

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"
)

// Prompt is called only by an explicitly interactive connection attempt.
type Prompt func(string) (string, error)

type askpassReply struct {
	Value    string
	Canceled bool
}

// RunAskpass serves the private helper invocation before GUI initialization.
func RunAskpass(prompt string) error {
	c, err := net.DialTimeout("unix", os.Getenv("WATER_ASKPASS_SOCKET"), time.Second)
	if err != nil {
		return err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(3 * time.Minute))
	if err := json.NewEncoder(c).Encode(prompt); err != nil {
		return err
	}
	var reply askpassReply
	if err := json.NewDecoder(c).Decode(&reply); err != nil {
		return err
	}
	if reply.Canceled {
		return errors.New("authentication canceled")
	}
	_, err = fmt.Fprintln(os.Stdout, reply.Value)
	return err
}

func newAskpass(prompt Prompt) ([]string, func(), error) {
	dir, err := os.MkdirTemp(sshSocketDirectory(), "water-auth-")
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	executable, err := os.Executable()
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	helper := filepath.Join(dir, "askpass")
	if err = os.WriteFile(helper, []byte("#!/bin/sh\nexec "+shellQuote(executable)+" __ssh_askpass \"$@\"\n"), 0700); err != nil {
		cleanup()
		return nil, nil, err
	}
	socket := filepath.Join(dir, "s")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	go func() {
		for {
			c, err := listener.Accept()
			if err != nil {
				return
			}
			func() {
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(3 * time.Minute))
				var text string
				if json.NewDecoder(c).Decode(&text) != nil {
					return
				}
				value, err := prompt(text)
				_ = json.NewEncoder(c).Encode(askpassReply{value, err != nil})
			}()
		}
	}()
	env := append(os.Environ(), "SSH_ASKPASS="+helper, "SSH_ASKPASS_REQUIRE=force", "DISPLAY=water-askpass", "WATER_ASKPASS_SOCKET="+socket)
	return env, func() { _ = listener.Close(); cleanup() }, nil
}
