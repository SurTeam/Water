pub mod app;
pub mod automation;
pub mod command;
pub mod config;
pub mod control;
pub mod event;
pub mod ids;
pub mod pane;
pub mod surface;
pub mod terminal;
pub mod ui;
pub mod workspace;

pub use app::{
    ApplicationModel, CommandClient, MemoryStats, ModelHost, ModelSnapshot, ModelSnapshotReceiver,
    StateDump,
};
pub use command::{
    AppCommand, CommandDispatcher, CommandError, DispatchError, FocusDirection, OperationResult,
    OperationSnapshot, OperationStatus, PaneCommand, SplitDirection, SurfaceCommand, TabCommand,
    TerminalCommand, WorkspaceCommand,
};
pub use config::{AppConfig, ConfigError, FeatureConfig, TerminalConfig, ThemeColors, ThemeConfig};
pub use ids::{OperationId, PaneId, SessionId, SurfaceId, TabId, TerminalId, WorkspaceId};
pub use terminal::{
    MAX_COLUMNS, MAX_LINES, MAX_SCROLLBACK_LINES, MAX_TOTAL_SCROLLBACK_LINES, TerminalCell,
    TerminalCellFlags, TerminalColor, TerminalCursor, TerminalModes, TerminalProcessState,
    TerminalSize, TerminalSnapshot,
};
