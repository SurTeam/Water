package goprotocol

import "testing"

func TestBilateralCompatibility(t *testing.T) {
	baseClient := Descriptor{BuildVariant: "dev", Version: "2.0", ProtocolVersion: ProtocolVersion, APISignature: APISignature, ServerRevision: "server-a", Capabilities: ClientCapabilities(), RequiredCapabilities: []string{"workspace/v1", "terminal-stream/v1"}}
	baseServer := Descriptor{BuildVariant: "dev", Version: "1.0", ProtocolVersion: ProtocolVersion, APISignature: APISignature, ServerRevision: "server-a", Capabilities: ServerCapabilities(), RequiredCapabilities: []string{"window-selection/v1"}}
	tests := []struct {
		name                         string
		change                       func(*Descriptor, *Descriptor)
		compatible, restart, legacy  bool
		missingServer, missingClient int
	}{
		{"GUI-only upgrade", func(c, s *Descriptor) { c.Version = "9.0" }, true, false, false, 0, 0},
		{"server changed with same API", func(c, s *Descriptor) { s.ServerRevision = "server-b" }, true, true, false, 0, 0},
		{"protocol changed", func(c, s *Descriptor) { s.ProtocolVersion++ }, false, false, false, 0, 0},
		{"signature changed", func(c, s *Descriptor) { s.APISignature = "different" }, false, false, false, 0, 0},
		{"variant mismatch", func(c, s *Descriptor) { s.BuildVariant = "release" }, false, false, false, 0, 0},
		{"server lacks GUI requirement", func(c, s *Descriptor) { c.RequiredCapabilities = []string{"future/v1"} }, false, false, false, 1, 0},
		{"GUI lacks server requirement", func(c, s *Descriptor) { s.RequiredCapabilities = []string{"future-client/v1"} }, false, false, false, 0, 1},
		{"legacy", func(c, s *Descriptor) { s.Capabilities = nil; s.RequiredCapabilities = nil; s.ServerRevision = "" }, true, true, true, 0, 0},
		{"legacy lacks future requirement", func(c, s *Descriptor) {
			s.Capabilities = nil
			s.RequiredCapabilities = nil
			c.RequiredCapabilities = []string{"future/v1"}
		}, false, false, true, 1, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, s := baseClient, baseServer
			tt.change(&c, &s)
			got := Assess(c, s)
			if got.Compatible != tt.compatible || got.RestartRecommended != tt.restart || got.Legacy != tt.legacy || len(got.MissingServer) != tt.missingServer || len(got.MissingClient) != tt.missingClient {
				t.Fatalf("unexpected assessment: %+v", got)
			}
		})
	}
}
