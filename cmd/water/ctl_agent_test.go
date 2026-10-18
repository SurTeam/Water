package main

import (
	"strings"
	"testing"
)

func TestAgentControlCommandsRemoved(t *testing.T) {
	for _, action := range []string{"interrupt", "exit", "force-stop"} {
		t.Run(action, func(t *testing.T) {
			err := runAgent(cliContext{}, []string{action})
			if err == nil || !strings.Contains(err.Error(), "unknown Agent command") {
				t.Fatalf("removed command was not rejected locally: %v", err)
			}
		})
	}
}
