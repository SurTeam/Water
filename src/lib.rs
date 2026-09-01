pub mod app;
pub mod automation;
pub mod command;
pub mod control;
pub mod event;
pub mod ids;
pub mod pane;
pub mod surface;
pub mod terminal;
pub mod ui;
pub mod workspace;

pub use app::{ApplicationModel, CommandClient, MemoryStats, ModelHost, ModelSnapshot, StateDump};
pub use command::{
    AppCommand, CommandDispatcher, CommandError, DispatchError, FocusDirection, OperationResult,
    OperationSnapshot, OperationStatus, PaneCommand, SplitDirection, SurfaceCommand, TabCommand,
    TerminalCommand, WorkspaceCommand,
};
pub use ids::{OperationId, PaneId, SessionId, SurfaceId, TabId, TerminalId, WorkspaceId};
pub use terminal::{
    MAX_COLUMNS, MAX_LINES, TerminalCell, TerminalCellFlags, TerminalColor, TerminalCursor,
    TerminalModes, TerminalProcessState, TerminalSize, TerminalSnapshot,
};
