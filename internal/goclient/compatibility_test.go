package goclient

import (
	"errors"
	"testing"

	"github.com/SurTeam/Water/internal/goprotocol"
)

func TestDiagnosticSessionBlocksLegacyModelCommandsBeforeTransport(t *testing.T) {
	session := &Session{Diagnostic: true, Compatibility: goprotocol.Compatibility{Reason: "unsupported API"}}
	for _, method := range []string{"command.dispatch", "state.dump", "terminal.attach"} {
		err := session.Notify(method, map[string]any{})
		var rpc *goprotocol.RPCError
		if !errors.As(err, &rpc) || rpc.Code != "INCOMPATIBLE_SERVER" {
			t.Fatalf("%s was not rejected before transport: %v", method, err)
		}
	}
}
