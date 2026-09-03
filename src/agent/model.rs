use serde::{Deserialize, Serialize};

/// A coding-agent CLI recognized as a terminal's foreground process.
///
/// Recognition is intentionally a closed registry: the sidebar, the state
/// dump, and future agent surfaces all key off this enum instead of raw
/// process names. Adding an agent means adding a variant plus its match
/// signatures below; nothing else in the stack needs to know the CLI's
/// binary naming.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash, PartialOrd, Ord, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum AgentKind {
    ClaudeCode,
    Codex,
    OpenCode,
    GeminiCli,
    Aider,
    CursorAgent,
    Amp,
    Crush,
    Goose,
    QwenCode,
    Droid,
    Grok,
    Pi,
}

impl AgentKind {
    pub const fn all() -> [AgentKind; 13] {
        [
            Self::ClaudeCode,
            Self::Codex,
            Self::OpenCode,
            Self::GeminiCli,
            Self::Aider,
            Self::CursorAgent,
            Self::Amp,
            Self::Crush,
            Self::Goose,
            Self::QwenCode,
            Self::Droid,
            Self::Grok,
            Self::Pi,
        ]
    }

    /// Display label rendered in the sidebar and reused by future agent UIs.
    pub const fn label(self) -> &'static str {
        match self {
            Self::ClaudeCode => "Claude Code",
            Self::Codex => "Codex",
            Self::OpenCode => "OpenCode",
            Self::GeminiCli => "Gemini CLI",
            Self::Aider => "Aider",
            Self::CursorAgent => "Cursor Agent",
            Self::Amp => "Amp",
            Self::Crush => "Crush",
            Self::Goose => "Goose",
            Self::QwenCode => "Qwen Code",
            Self::Droid => "Droid",
            Self::Grok => "Grok",
            Self::Pi => "Pi",
        }
    }

    /// Exact executable/argv basenames that identify this agent.
    const fn names(self) -> &'static [&'static str] {
        match self {
            Self::ClaudeCode => &["claude"],
            Self::Codex => &["codex"],
            Self::OpenCode => &["opencode"],
            Self::GeminiCli => &["gemini"],
            Self::Aider => &["aider", "aider-chat"],
            Self::CursorAgent => &["cursor-agent"],
            Self::Amp => &["amp"],
            Self::Crush => &["crush"],
            Self::Goose => &["goose"],
            Self::QwenCode => &["qwen", "qwen-code"],
            Self::Droid => &["droid"],
            Self::Grok => &["grok"],
            Self::Pi => &["pi"],
        }
    }

    /// Path fragments that identify a node/python-launched installation even
    /// when the foreground process is the interpreter's script path.
    const fn path_markers(self) -> &'static [&'static str] {
        match self {
            Self::ClaudeCode => &["@anthropic-ai/claude-code"],
            Self::Codex => &["@openai/codex"],
            Self::OpenCode => &["@sst/opencode", "opencode-ai"],
            Self::GeminiCli => &["@google/gemini-cli"],
            Self::Aider => &["aider/chat", "aider-main"],
            Self::CursorAgent => &["/cursor-agent"],
            Self::Amp => &["@ampcode", "sourcegraph/amp"],
            Self::Crush => &["charmbracelet/crush"],
            Self::Goose => &["block/goose"],
            Self::QwenCode => &["qwen-code"],
            Self::Droid => &[],
            Self::Grok => &[],
            // pi is a node CLI: the package moved scopes, match both homes.
            Self::Pi => &[
                "@earendil-works/pi-coding-agent",
                "@mariozechner/pi-coding-agent",
            ],
        }
    }
}

/// A detected coding agent attached to the terminal surface that runs it.
///
/// `active` reflects recent PTY output (the agent produced something within
/// the worker's activity window); it is the first "running state" Water
/// exposes and is the hook where richer per-agent states (waiting for input,
/// tool running, ...) from future adapters should attach.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
pub struct DetectedAgent {
    pub kind: AgentKind,
    #[serde(default)]
    pub active: bool,
}

impl DetectedAgent {
    pub fn label(&self) -> &'static str {
        self.kind.label()
    }
}

/// Classify a terminal's foreground process as a known coding agent.
///
/// `process_name` is the probed foreground process (or the spawn fallback),
/// and `cmdline` is its argv when the probe could retrieve one. Both are
/// matched case-insensitively against the registry above.
pub fn detect_agent(process_name: &str, cmdline: &[String]) -> Option<AgentKind> {
    let tokens = std::iter::once(process_name)
        .chain(cmdline.iter().map(String::as_str))
        .map(normalize_token)
        .collect::<Vec<_>>();
    AgentKind::all()
        .into_iter()
        .find(|kind| {
            tokens
                .iter()
                .any(|token| kind.names().contains(&token.as_str()))
        })
        .or_else(|| {
            AgentKind::all().into_iter().find(|kind| {
                !kind.path_markers().is_empty()
                    && cmdline.iter().any(|token| {
                        let token = token.to_ascii_lowercase();
                        token.contains('/')
                            && kind
                                .path_markers()
                                .iter()
                                .any(|marker| token.contains(marker))
                    })
            })
        })
}

/// Lowercase a command token and reduce path-like values to their basename so
/// `/opt/homebrew/bin/claude`, `-claude` (login shell style), and `claude`
/// all compare equal.
fn normalize_token(token: &str) -> String {
    let token = token.trim();
    let base = token.rsplit('/').next().unwrap_or(token);
    base.trim_start_matches('-').to_ascii_lowercase()
}

#[cfg(test)]
mod tests {
    use super::*;

    fn args(values: &[&str]) -> Vec<String> {
        values.iter().map(|value| value.to_string()).collect()
    }

    #[test]
    fn native_agent_binaries_are_detected() {
        for (name, kind) in [
            ("claude", AgentKind::ClaudeCode),
            ("codex", AgentKind::Codex),
            ("opencode", AgentKind::OpenCode),
            ("gemini", AgentKind::GeminiCli),
            ("aider", AgentKind::Aider),
            ("cursor-agent", AgentKind::CursorAgent),
            ("goose", AgentKind::Goose),
            ("crush", AgentKind::Crush),
            ("pi", AgentKind::Pi),
        ] {
            assert_eq!(detect_agent(name, &args(&[])), Some(kind), "{name}");
        }
    }

    #[test]
    fn interpreter_launches_are_detected_via_argv_paths() {
        assert_eq!(
            detect_agent(
                "node",
                &args(&[
                    "/opt/homebrew/bin/node",
                    "/opt/homebrew/lib/node_modules/@anthropic-ai/claude-code/cli.js",
                ]),
            ),
            Some(AgentKind::ClaudeCode)
        );
        assert_eq!(
            detect_agent("python3", &args(&["/opt/homebrew/bin/aider", "--yes"])),
            Some(AgentKind::Aider)
        );
        assert_eq!(
            detect_agent(
                "node",
                &args(&[
                    "/usr/local/bin/node",
                    "/usr/local/lib/node_modules/@earendil-works/pi-coding-agent/dist/cli.js",
                ]),
            ),
            Some(AgentKind::Pi)
        );
    }

    #[test]
    fn shells_and_unrelated_tools_are_not_agents() {
        for (name, cmdline) in [
            ("zsh", args(&["-zsh"])),
            ("node", args(&["/app/server.js"])),
            ("vim", args(&[])),
            ("claude-context", args(&[])),
            ("myclaudetool", args(&[])),
        ] {
            assert_eq!(detect_agent(name, &cmdline), None, "{name}");
        }
    }

    #[test]
    fn detection_is_case_insensitive() {
        assert_eq!(
            detect_agent("Claude", &args(&["/usr/local/bin/Claude"])),
            Some(AgentKind::ClaudeCode)
        );
    }
}
