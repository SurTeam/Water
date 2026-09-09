use std::path::Path;

mod emulator;
mod model;
mod replay;
mod shell_integration;
mod snapshot;
mod stream;
mod worker;

pub const HOMEBREW_ZSH: &str = "/opt/homebrew/bin/zsh";
pub const INTEL_HOMEBREW_ZSH: &str = "/usr/local/bin/zsh";
pub const SYSTEM_ZSH: &str = "/bin/zsh";

/// Colors used when answering terminal dynamic-color queries (OSC 10/11/12).
/// The GUI-owned emulator uses these values for dynamic-color responses,
/// keeping query replies aligned with the rendered palette.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct TerminalTheme {
    pub foreground: u32,
    pub background: u32,
    pub cursor: u32,
}

impl TerminalTheme {
    pub const fn new(foreground: u32, background: u32, cursor: u32) -> Self {
        Self {
            foreground,
            background,
            cursor,
        }
    }
}

impl Default for TerminalTheme {
    fn default() -> Self {
        Self::new(0xe4e4e4, 0x2c2c2c, 0xe4e4e4)
    }
}

/// Select the real zsh executable used for terminals when no program is supplied.
/// `WATER_SHELL` is an explicit override for development and integration tests.
pub fn default_shell_program() -> String {
    if let Ok(program) = std::env::var("WATER_SHELL")
        && !program.is_empty()
    {
        return program;
    }

    [HOMEBREW_ZSH, INTEL_HOMEBREW_ZSH, SYSTEM_ZSH]
        .into_iter()
        .find(|program| Path::new(program).is_file())
        .map(str::to_owned)
        .unwrap_or_else(|| "zsh".to_owned())
}

/// Launch zsh as a login shell so the user's normal environment and startup
/// configuration are available in every default Water terminal.
pub fn default_shell_args(program: &str) -> Vec<String> {
    if Path::new(program)
        .file_name()
        .and_then(|name| name.to_str())
        == Some("zsh")
    {
        vec!["-l".to_owned()]
    } else {
        Vec::new()
    }
}

pub use emulator::{EmulatorEffect, TerminalEmulator, snapshot_from_replay};
pub use model::{
    TerminalAttachment, TerminalError, TerminalLimits, TerminalManager, TerminalRegistry,
    TerminalReplay,
};
pub(crate) use model::{TerminalManagerEvent, WakeupCallback};
pub use replay::{MAX_REPLAY_BYTES, ReplayRing};
pub use snapshot::{
    DEFAULT_COLUMNS, DEFAULT_INACTIVE_SCROLLBACK_LINES, DEFAULT_LINES,
    DEFAULT_MAX_TOTAL_SCROLLBACK_BYTES, DEFAULT_REPLAY_HISTORY_BYTES, DEFAULT_SCROLLBACK_LINES,
    MAX_COLUMNS, MAX_LINES, MAX_RECENT_OUTPUT_BYTES, MAX_SCROLLBACK_LINES,
    MAX_TOTAL_SCROLLBACK_BYTES, MIN_MAX_TOTAL_SCROLLBACK_BYTES, MIN_REPLAY_HISTORY_BYTES,
    TerminalCell, TerminalCellFlags, TerminalColor, TerminalCursor, TerminalModes,
    TerminalProcessState, TerminalRowSnapshot, TerminalSize, TerminalSnapshot, TerminalSummary,
    scrollback_row_bytes,
};
pub use stream::{TerminalSeq, TerminalStreamEvent, WireTerminalEvent};

#[cfg(test)]
mod tests {
    use super::{HOMEBREW_ZSH, SYSTEM_ZSH, default_shell_args};

    #[test]
    fn zsh_defaults_to_login_mode() {
        assert_eq!(default_shell_args(HOMEBREW_ZSH), vec!["-l"]);
        assert_eq!(default_shell_args(SYSTEM_ZSH), vec!["-l"]);
    }
}
