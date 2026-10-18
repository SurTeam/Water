package goagent

import (
	"path/filepath"
	"strings"
)

type Kind string

const (
	ClaudeCode  Kind = "claude_code"
	Codex       Kind = "codex"
	OpenCode    Kind = "opencode"
	GeminiCLI   Kind = "gemini_cli"
	Aider       Kind = "aider"
	CursorAgent Kind = "cursor_agent"
	Amp         Kind = "amp"
	Crush       Kind = "crush"
	Goose       Kind = "goose"
	QwenCode    Kind = "qwen_code"
	Droid       Kind = "droid"
	Grok        Kind = "grok"
	Pi          Kind = "pi"
)

type DetectedAgent struct {
	Kind    Kind     `json:"kind"`
	Active  bool     `json:"active"`
	Run     *Process `json:"run,omitempty"`
	Profile *Profile `json:"profile,omitempty"`
}

type definition struct {
	kind                    Kind
	label                   string
	names                   []string
	pathMarkers             []string
	scrollbackOnClearScreen bool
}

var definitions = []definition{
	{ClaudeCode, "Claude Code", []string{"claude"}, []string{"@anthropic-ai/claude-code"}, true},
	{Codex, "Codex", []string{"codex"}, []string{"@openai/codex"}, true},
	{OpenCode, "OpenCode", []string{"opencode"}, []string{"@sst/opencode", "opencode-ai"}, true},
	{Pi, "Pi Coding Agent", []string{"pi"}, []string{"@earendil-works/pi-coding-agent", "@mariozechner/pi-coding-agent"}, false},
}

func Label(kind Kind) string {
	for _, def := range definitions {
		if def.kind == kind {
			return def.label
		}
	}
	return string(kind)
}

// ScrollbackOnClearScreen reports whether clearing the live terminal viewport
// should retain its previous contents in scrollback for this Agent kind.
func ScrollbackOnClearScreen(kind Kind) bool {
	for _, def := range definitions {
		if def.kind == kind {
			return def.scrollbackOnClearScreen
		}
	}
	return true
}

func Detect(processName string, cmdline []string) *DetectedAgent {
	registry, _ := Compile(Config{})
	return registry.Detect(Process{Name: processName, Args: cmdline})
}

func (a *DetectedAgent) DisplayName() string {
	if a == nil {
		return ""
	}
	if a.Profile != nil {
		return a.Profile.Name
	}
	return Label(a.Kind)
}
func (a *DetectedAgent) PreserveClear() bool {
	if a.Profile != nil {
		return a.Profile.ScrollbackOnClearScreen
	}
	return ScrollbackOnClearScreen(a.Kind)
}

func normalizeToken(token string) string {
	token = strings.TrimSpace(token)
	base := filepath.Base(token)
	return strings.ToLower(strings.TrimLeft(base, "-"))
}

func shellExecArgv0(cmdline []string) string {
	for _, raw := range cmdline {
		fields := strings.Fields(raw)
		for i := 0; i+2 < len(fields); i++ {
			if fields[i] != "exec" || fields[i+1] != "-a" {
				continue
			}
			return strings.Trim(fields[i+2], "\"'")
		}
	}
	return ""
}
