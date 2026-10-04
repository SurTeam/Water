package goagent

import "testing"

func TestDetectAgentParity(t *testing.T) {
	tests := []struct {
		process string
		args    []string
		want    Kind
	}{
		{"claude", nil, ClaudeCode},
		{"/usr/local/bin/codex", nil, Codex},
		{"opencode", nil, OpenCode},
		{"pi", nil, Pi},
		{"node", []string{"/opt/homebrew/lib/node_modules/@anthropic-ai/claude-code/cli.js"}, ClaudeCode},
		{"python3", []string{"/opt/homebrew/bin/aider"}, Aider},
		{"node", []string{"/usr/local/lib/node_modules/@earendil-works/pi-coding-agent/dist/cli.js"}, Pi},
		{"/bin/zsh", []string{"-c", "exec -a claude sleep 2"}, ClaudeCode},
	}
	for _, tt := range tests {
		got := Detect(tt.process, tt.args)
		if got == nil || got.Kind != tt.want {
			t.Fatalf("Detect(%q,%q)=%#v want %s", tt.process, tt.args, got, tt.want)
		}
	}
	for _, name := range []string{"zsh", "vim", "claude-context", "myclaudetool"} {
		if got := Detect(name, nil); got != nil {
			t.Fatalf("%q detected as %#v", name, got)
		}
	}
	if Detect("echo", []string{"echo", "codex"}) != nil {
		t.Fatal("ordinary argument detected as agent")
	}
}
