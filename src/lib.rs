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
