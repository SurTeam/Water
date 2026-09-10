pub mod agent;
pub mod app;
pub mod automation;
pub mod command;
pub mod config;
pub mod control;
#[cfg(feature = "gui")]
mod embedded_servers;
pub mod event;
pub mod ids;
pub mod metrics;
pub mod pane;
#[cfg(feature = "gui")]
pub mod remote;
pub mod server;
pub mod surface;
pub mod terminal;
pub mod ui;
pub mod workspace;

/// Set the visible process name (comm / progname) for unambiguous
/// ps/pkill targeting. Dev builds use `water-dev` / `water-srv-dev`
/// to avoid killing the production server by accident.
#[cfg(target_os = "linux")]
pub fn set_process_name(name: &str) {
    unsafe {
        let mut bytes = name.as_bytes().to_vec();
        bytes.push(0);
        if bytes.len() > 16 {
            bytes.truncate(15);
            bytes.push(0);
        }
        let _ = libc::prctl(15, bytes.as_ptr() as libc::c_ulong, 0, 0, 0);
    }
}

#[cfg(target_os = "macos")]
pub fn set_process_name(name: &str) {
    unsafe extern "C" {
        fn setprogname(name: *const libc::c_char);
    }
    let c_name = std::ffi::CString::new(name).unwrap_or_default();
    unsafe {
        setprogname(c_name.as_ptr());
    }
}

#[cfg(not(any(target_os = "linux", target_os = "macos")))]
pub fn set_process_name(_name: &str) {}

pub use agent::{AgentKind, DetectedAgent, detect_agent};
pub use app::{
    ApplicationModel, CommandClient, CommandTransport, MemoryStats, ModelHost, ModelSnapshot,
    ModelSnapshotReceiver, StateDump,
};
pub use command::{
    AppCommand, CommandDispatcher, CommandError, DispatchError, FocusDirection, OperationResult,
    OperationSnapshot, OperationStatus, PaneCommand, SplitDirection, SurfaceCommand, TabCommand,
    TerminalCommand, WorkspaceCommand,
};
pub use config::{
    AppConfig, AppConfigOverrides, ConfigError, ConfigOverrides, FeatureConfig,
    FeatureConfigOverrides, ServerConfig, ServerConfigOverrides, ShellConfig, ShellConfigOverrides,
    ShortcutConfig, ShortcutConfigOverrides, StartupConfig, StartupConfigOverrides, TerminalConfig,
    TerminalConfigOverrides, ThemeColors, ThemeConfig, ThemeConfigOverrides, UiConfig,
    UiConfigOverrides,
};
pub use ids::{OperationId, PaneId, SessionId, SurfaceId, TabId, TerminalId, WorkspaceId};
pub use terminal::{
    DEFAULT_INACTIVE_SCROLLBACK_LINES, DEFAULT_MAX_TOTAL_SCROLLBACK_BYTES,
    DEFAULT_SCROLLBACK_LINES, MAX_COLUMNS, MAX_LINES, MAX_SCROLLBACK_LINES,
    MAX_TOTAL_SCROLLBACK_BYTES, MIN_MAX_TOTAL_SCROLLBACK_BYTES, TerminalCell, TerminalCellFlags,
    TerminalColor, TerminalCursor, TerminalLimits, TerminalModes, TerminalProcessState,
    TerminalSize, TerminalSnapshot, TerminalSummary, scrollback_row_bytes,
};
