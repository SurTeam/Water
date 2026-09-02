use std::path::Path;

mod model;
mod snapshot;
mod worker;

pub const HOMEBREW_ZSH: &str = "/opt/homebrew/bin/zsh";
pub const INTEL_HOMEBREW_ZSH: &str = "/usr/local/bin/zsh";
pub const SYSTEM_ZSH: &str = "/bin/zsh";

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

pub use model::{TerminalError, TerminalManager, TerminalRegistry};
pub(crate) use model::{TerminalManagerEvent, WakeupCallback};
pub use snapshot::{
    DEFAULT_COLUMNS, DEFAULT_LINES, MAX_COLUMNS, MAX_LINES, MAX_RECENT_OUTPUT_BYTES,
    MAX_SCROLLBACK_LINES, MAX_TOTAL_SCROLLBACK_LINES, TerminalCell, TerminalCellFlags,
    TerminalColor, TerminalCursor, TerminalModes, TerminalProcessState, TerminalSize,
    TerminalSnapshot,
};

#[cfg(test)]
mod tests {
    use super::{HOMEBREW_ZSH, SYSTEM_ZSH, default_shell_args};

    #[test]
    fn zsh_defaults_to_login_mode() {
        assert_eq!(default_shell_args(HOMEBREW_ZSH), vec!["-l"]);
        assert_eq!(default_shell_args(SYSTEM_ZSH), vec!["-l"]);
    }
}
