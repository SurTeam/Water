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
	Kind   Kind `json:"kind"`
	Active bool `json:"active"`
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
	{GeminiCLI, "Gemini CLI", []string{"gemini"}, []string{"@google/gemini-cli"}, true},
	{Aider, "Aider", []string{"aider", "aider-chat"}, []string{"aider/chat", "aider-main"}, true},
	{CursorAgent, "Cursor Agent", []string{"cursor-agent"}, []string{"/cursor-agent"}, true},
	{Amp, "Amp", []string{"amp"}, []string{"@ampcode", "sourcegraph/amp"}, true},
	{Crush, "Crush", []string{"crush"}, []string{"charmbracelet/crush"}, true},
	{Goose, "Goose", []string{"goose"}, []string{"block/goose"}, true},
	{QwenCode, "Qwen Code", []string{"qwen", "qwen-code"}, []string{"qwen-code"}, true},
	{Droid, "Droid", []string{"droid"}, nil, true},
	{Grok, "Grok", []string{"grok"}, nil, true},
	{Pi, "Pi", []string{"pi"}, []string{"@earendil-works/pi-coding-agent", "@mariozechner/pi-coding-agent"}, false},
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
	tokens := make([]string, 0, len(cmdline)+2)
	tokens = append(tokens, normalizeToken(processName))
	if argv0 := shellExecArgv0(cmdline); argv0 != "" {
		tokens = append(tokens, normalizeToken(argv0))
	}
	for i, token := range cmdline {
		if i == 0 || strings.Contains(token, "/") {
			tokens = append(tokens, normalizeToken(token))
		}
	}
	for _, def := range definitions {
		for _, token := range tokens {
			for _, name := range def.names {
				if token == name {
					return &DetectedAgent{Kind: def.kind}
				}
			}
		}
	}
	for _, def := range definitions {
		if len(def.pathMarkers) == 0 {
			continue
		}
		for _, raw := range cmdline {
			lower := strings.ToLower(raw)
			if !strings.Contains(lower, "/") {
				continue
			}
			for _, marker := range def.pathMarkers {
				if strings.Contains(lower, marker) {
					return &DetectedAgent{Kind: def.kind}
				}
			}
		}
	}
	return nil
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
