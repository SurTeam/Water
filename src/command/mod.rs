mod dispatcher;
mod operation;

use serde::{Deserialize, Deserializer, Serialize, Serializer};
use thiserror::Error;

use crate::ids::{PaneId, SurfaceId, TabId, TerminalId, WorkspaceId};
use crate::surface::SurfaceKind;

pub use dispatcher::CommandDispatcher;
pub(crate) use operation::OperationRegistry;
pub use operation::{OperationSnapshot, OperationStatus};

#[derive(Debug, Clone, PartialEq)]
pub enum AppCommand {
    Workspace(WorkspaceCommand),
    Tab(TabCommand),
    Pane(PaneCommand),
    Surface(SurfaceCommand),
    Terminal(TerminalCommand),
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub enum WorkspaceCommand {
    /// Creates and activates an empty workspace. Startup flows retain this
    /// primitive so they can opt into creating the initial terminal
    /// separately.
    Create,
    /// Creates and activates a workspace with one configured terminal tab.
    New,
    /// Idempotent compatibility command for old startup/control callers.
    Ensure,
    Close {
        workspace_id: Option<WorkspaceId>,
    },
    /// Alias for `Close` retained as a more explicit public command name.
    Delete {
        workspace_id: Option<WorkspaceId>,
    },
    Activate {
        workspace_id: Option<WorkspaceId>,
    },
    Rename {
        workspace_id: Option<WorkspaceId>,
        title: String,
    },
    /// Moves a workspace to its final position in the order after removal.
    /// `None` targets the active workspace and out-of-range indices clamp to
    /// the available positions.
    Reorder {
        workspace_id: Option<WorkspaceId>,
        index: usize,
    },
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub enum TabCommand {
    New {
        title: Option<String>,
    },
    /// Creates a terminal tab in the specified workspace without changing the
    /// process-global active workspace compatibility alias.
    NewInWorkspace {
        workspace_id: WorkspaceId,
        title: Option<String>,
    },
    Rename {
        tab_id: Option<TabId>,
        title: String,
    },
    Close {
        tab_id: Option<TabId>,
    },
    Activate {
        tab_id: Option<TabId>,
        index: Option<usize>,
    },
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum SplitDirection {
    Left,
    Right,
    Up,
    Down,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum FocusDirection {
    Left,
    Right,
    Up,
    Down,
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub enum PaneCommand {
    Split {
        pane_id: Option<PaneId>,
        direction: SplitDirection,
    },
    Close {
        pane_id: Option<PaneId>,
    },
    Focus {
        pane_id: Option<PaneId>,
        direction: Option<FocusDirection>,
    },
    Resize {
        pane_id: Option<PaneId>,
        ratio: f32,
    },
    /// Resizes the split at an exact path from the tab's pane-tree root
    /// (`false` = first child, `true` = second; empty path = root split).
    /// Unlike `Resize` (nearest split of a pane), this commits the precise
    /// divider dragged in the UI, including ancestor splits.
    ResizeSplit {
        tab_id: TabId,
        path: Vec<bool>,
        ratio: f32,
    },
    /// Moves a pane into a new tab in another workspace without replacing its
    /// pane or terminal surface. `None` targets the active pane.
    MoveToWorkspace {
        pane_id: Option<PaneId>,
        workspace_id: WorkspaceId,
    },
    /// Sets the display label for the detected agent running in a pane.
    /// An empty label clears the override; `None` targets the active pane.
    RenameAgent {
        pane_id: Option<PaneId>,
        label: String,
    },
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub enum SurfaceCommand {
    Replace {
        pane_id: Option<PaneId>,
        kind: SurfaceKind,
    },
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub enum TerminalCommand {
    Spawn {
        pane_id: Option<PaneId>,
        program: String,
        args: Vec<String>,
        columns: usize,
        lines: usize,
    },
    SendText {
        terminal_id: Option<TerminalId>,
        pane_id: Option<PaneId>,
        text: String,
    },
    SendBytes {
        terminal_id: Option<TerminalId>,
        pane_id: Option<PaneId>,
        bytes: Vec<u8>,
    },
    Resize {
        terminal_id: Option<TerminalId>,
        pane_id: Option<PaneId>,
        columns: usize,
        lines: usize,
    },
    Scroll {
        terminal_id: Option<TerminalId>,
        pane_id: Option<PaneId>,
        lines: i32,
    },
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(tag = "type", rename_all = "snake_case")]
pub enum OperationResult {
    None,
    WorkspaceCreated {
        workspace_id: WorkspaceId,
    },
    WorkspaceClosed {
        workspace_id: WorkspaceId,
    },
    WorkspaceActivated {
        workspace_id: WorkspaceId,
    },
    WorkspaceRenamed {
        workspace_id: WorkspaceId,
    },
    TabCreated {
        tab_id: TabId,
    },
    TabRenamed {
        tab_id: TabId,
    },
    TabClosed {
        tab_id: TabId,
    },
    TabActivated {
        tab_id: TabId,
    },
    PaneCreated {
        pane_id: PaneId,
    },
    PaneClosed {
        pane_id: PaneId,
    },
    PaneFocused {
        pane_id: PaneId,
    },
    PaneResized {
        pane_id: PaneId,
        ratio: f32,
    },
    SurfaceReplaced {
        surface_id: SurfaceId,
    },
    TerminalSpawned {
        terminal_id: TerminalId,
    },
    TerminalTextSent {
        terminal_id: TerminalId,
    },
    TerminalBytesSent {
        terminal_id: TerminalId,
    },
    TerminalResized {
        terminal_id: TerminalId,
        columns: usize,
        lines: usize,
    },
    TerminalScrolled {
        terminal_id: TerminalId,
        lines: i32,
    },
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, Error)]
#[error("{code}: {message}")]
pub struct CommandError {
    pub code: String,
    pub message: String,
}

impl CommandError {
    pub fn new(code: impl Into<String>, message: impl Into<String>) -> Self {
        Self {
            code: code.into(),
            message: message.into(),
        }
    }
}

/// The wire representation keeps the public JSON protocol compact and stable:
/// `{ "type": "pane.split", "direction": "right" }`.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(tag = "type")]
enum AppCommandWire {
    #[serde(rename = "workspace.create")]
    WorkspaceCreate,
    #[serde(rename = "workspace.new")]
    WorkspaceNew,
    #[serde(rename = "workspace.ensure")]
    WorkspaceEnsure,
    #[serde(rename = "workspace.close")]
    WorkspaceClose { workspace_id: Option<WorkspaceId> },
    #[serde(rename = "workspace.delete")]
    WorkspaceDelete { workspace_id: Option<WorkspaceId> },
    #[serde(rename = "workspace.activate")]
    WorkspaceActivate { workspace_id: Option<WorkspaceId> },
    #[serde(rename = "workspace.rename")]
    WorkspaceRename {
        workspace_id: Option<WorkspaceId>,
        title: String,
    },
    #[serde(rename = "workspace.reorder")]
    WorkspaceReorder {
        workspace_id: Option<WorkspaceId>,
        index: usize,
    },
    #[serde(rename = "tab.new")]
    TabNew { title: Option<String> },
    #[serde(rename = "tab.new_in_workspace")]
    TabNewInWorkspace {
        workspace_id: WorkspaceId,
        title: Option<String>,
    },
    #[serde(rename = "tab.rename")]
    TabRename {
        tab_id: Option<TabId>,
        title: String,
    },
    #[serde(rename = "tab.close")]
    TabClose { tab_id: Option<TabId> },
    #[serde(rename = "tab.activate")]
    TabActivate {
        tab_id: Option<TabId>,
        index: Option<usize>,
    },
    #[serde(rename = "pane.split")]
    PaneSplit {
        pane_id: Option<PaneId>,
        direction: SplitDirection,
    },
    #[serde(rename = "pane.close")]
    PaneClose { pane_id: Option<PaneId> },
    #[serde(rename = "pane.focus")]
    PaneFocus {
        pane_id: Option<PaneId>,
        direction: Option<FocusDirection>,
    },
    #[serde(rename = "pane.resize")]
    PaneResize { pane_id: Option<PaneId>, ratio: f32 },
    #[serde(rename = "pane.resize_split")]
    PaneResizeSplit {
        tab_id: TabId,
        path: Vec<bool>,
        ratio: f32,
    },
    #[serde(rename = "pane.move_to_workspace")]
    PaneMoveToWorkspace {
        pane_id: Option<PaneId>,
        workspace_id: WorkspaceId,
    },
    #[serde(rename = "pane.agent_rename")]
    PaneAgentRename {
        pane_id: Option<PaneId>,
        label: String,
    },
    #[serde(rename = "surface.replace")]
    SurfaceReplace {
        pane_id: Option<PaneId>,
        kind: SurfaceKind,
    },
    #[serde(rename = "terminal.spawn")]
    TerminalSpawn {
        pane_id: Option<PaneId>,
        #[serde(default = "default_terminal_program")]
        program: String,
        #[serde(default)]
        args: Vec<String>,
        #[serde(default = "default_terminal_columns")]
        columns: usize,
        #[serde(default = "default_terminal_lines")]
        lines: usize,
    },
    #[serde(rename = "terminal.send_text")]
    TerminalSendText {
        terminal_id: Option<TerminalId>,
        pane_id: Option<PaneId>,
        text: String,
    },
    #[serde(rename = "terminal.send_bytes")]
    TerminalSendBytes {
        terminal_id: Option<TerminalId>,
        pane_id: Option<PaneId>,
        bytes: Vec<u8>,
    },
    #[serde(rename = "terminal.resize")]
    TerminalResize {
        terminal_id: Option<TerminalId>,
        pane_id: Option<PaneId>,
        columns: usize,
        lines: usize,
    },
    #[serde(rename = "terminal.scroll")]
    TerminalScroll {
        terminal_id: Option<TerminalId>,
        pane_id: Option<PaneId>,
        lines: i32,
    },
}

fn default_terminal_program() -> String {
    crate::terminal::default_shell_program()
}

fn default_terminal_columns() -> usize {
    crate::terminal::DEFAULT_COLUMNS
}

fn default_terminal_lines() -> usize {
    crate::terminal::DEFAULT_LINES
}

impl From<&AppCommand> for AppCommandWire {
    fn from(command: &AppCommand) -> Self {
        match command {
            AppCommand::Workspace(WorkspaceCommand::Create) => Self::WorkspaceCreate,
            AppCommand::Workspace(WorkspaceCommand::New) => Self::WorkspaceNew,
            AppCommand::Workspace(WorkspaceCommand::Ensure) => Self::WorkspaceEnsure,
            AppCommand::Workspace(WorkspaceCommand::Close { workspace_id }) => {
                Self::WorkspaceClose {
                    workspace_id: *workspace_id,
                }
            }
            AppCommand::Workspace(WorkspaceCommand::Delete { workspace_id }) => {
                Self::WorkspaceDelete {
                    workspace_id: *workspace_id,
                }
            }
            AppCommand::Workspace(WorkspaceCommand::Activate { workspace_id }) => {
                Self::WorkspaceActivate {
                    workspace_id: *workspace_id,
                }
            }
            AppCommand::Workspace(WorkspaceCommand::Rename {
                workspace_id,
                title,
            }) => Self::WorkspaceRename {
                workspace_id: *workspace_id,
                title: title.clone(),
            },
            AppCommand::Workspace(WorkspaceCommand::Reorder {
                workspace_id,
                index,
            }) => Self::WorkspaceReorder {
                workspace_id: *workspace_id,
                index: *index,
            },
            AppCommand::Tab(TabCommand::New { title }) => Self::TabNew {
                title: title.clone(),
            },
            AppCommand::Tab(TabCommand::NewInWorkspace {
                workspace_id,
                title,
            }) => Self::TabNewInWorkspace {
                workspace_id: *workspace_id,
                title: title.clone(),
            },
            AppCommand::Tab(TabCommand::Rename { tab_id, title }) => Self::TabRename {
                tab_id: *tab_id,
                title: title.clone(),
            },
            AppCommand::Tab(TabCommand::Close { tab_id }) => Self::TabClose { tab_id: *tab_id },
            AppCommand::Tab(TabCommand::Activate { tab_id, index }) => Self::TabActivate {
                tab_id: *tab_id,
                index: *index,
            },
            AppCommand::Pane(PaneCommand::Split { pane_id, direction }) => Self::PaneSplit {
                pane_id: *pane_id,
                direction: *direction,
            },
            AppCommand::Pane(PaneCommand::Close { pane_id }) => {
                Self::PaneClose { pane_id: *pane_id }
            }
            AppCommand::Pane(PaneCommand::Focus { pane_id, direction }) => Self::PaneFocus {
                pane_id: *pane_id,
                direction: *direction,
            },
            AppCommand::Pane(PaneCommand::Resize { pane_id, ratio }) => Self::PaneResize {
                pane_id: *pane_id,
                ratio: *ratio,
            },
            AppCommand::Pane(PaneCommand::ResizeSplit {
                tab_id,
                path,
                ratio,
            }) => Self::PaneResizeSplit {
                tab_id: *tab_id,
                path: path.clone(),
                ratio: *ratio,
            },
            AppCommand::Pane(PaneCommand::MoveToWorkspace {
                pane_id,
                workspace_id,
            }) => Self::PaneMoveToWorkspace {
                pane_id: *pane_id,
                workspace_id: *workspace_id,
            },
            AppCommand::Pane(PaneCommand::RenameAgent { pane_id, label }) => {
                Self::PaneAgentRename {
                    pane_id: *pane_id,
                    label: label.clone(),
                }
            }
            AppCommand::Surface(SurfaceCommand::Replace { pane_id, kind }) => {
                Self::SurfaceReplace {
                    pane_id: *pane_id,
                    kind: *kind,
                }
            }
            AppCommand::Terminal(TerminalCommand::Spawn {
                pane_id,
                program,
                args,
                columns,
                lines,
            }) => Self::TerminalSpawn {
                pane_id: *pane_id,
                program: program.clone(),
                args: args.clone(),
                columns: *columns,
                lines: *lines,
            },
            AppCommand::Terminal(TerminalCommand::SendText {
                terminal_id,
                pane_id,
                text,
            }) => Self::TerminalSendText {
                terminal_id: *terminal_id,
                pane_id: *pane_id,
                text: text.clone(),
            },
            AppCommand::Terminal(TerminalCommand::SendBytes {
                terminal_id,
                pane_id,
                bytes,
            }) => Self::TerminalSendBytes {
                terminal_id: *terminal_id,
                pane_id: *pane_id,
                bytes: bytes.clone(),
            },
            AppCommand::Terminal(TerminalCommand::Resize {
                terminal_id,
                pane_id,
                columns,
                lines,
            }) => Self::TerminalResize {
                terminal_id: *terminal_id,
                pane_id: *pane_id,
                columns: *columns,
                lines: *lines,
            },
            AppCommand::Terminal(TerminalCommand::Scroll {
                terminal_id,
                pane_id,
                lines,
            }) => Self::TerminalScroll {
                terminal_id: *terminal_id,
                pane_id: *pane_id,
                lines: *lines,
            },
        }
    }
}

impl From<AppCommandWire> for AppCommand {
    fn from(command: AppCommandWire) -> Self {
        match command {
            AppCommandWire::WorkspaceCreate => Self::Workspace(WorkspaceCommand::Create),
            AppCommandWire::WorkspaceNew => Self::Workspace(WorkspaceCommand::New),
            AppCommandWire::WorkspaceEnsure => Self::Workspace(WorkspaceCommand::Ensure),
            AppCommandWire::WorkspaceClose { workspace_id } => {
                Self::Workspace(WorkspaceCommand::Close { workspace_id })
            }
            AppCommandWire::WorkspaceDelete { workspace_id } => {
                Self::Workspace(WorkspaceCommand::Delete { workspace_id })
            }
            AppCommandWire::WorkspaceActivate { workspace_id } => {
                Self::Workspace(WorkspaceCommand::Activate { workspace_id })
            }
            AppCommandWire::WorkspaceRename {
                workspace_id,
                title,
            } => Self::Workspace(WorkspaceCommand::Rename {
                workspace_id,
                title,
            }),
            AppCommandWire::WorkspaceReorder {
                workspace_id,
                index,
            } => Self::Workspace(WorkspaceCommand::Reorder {
                workspace_id,
                index,
            }),
            AppCommandWire::TabNew { title } => Self::Tab(TabCommand::New { title }),
            AppCommandWire::TabNewInWorkspace {
                workspace_id,
                title,
            } => Self::Tab(TabCommand::NewInWorkspace {
                workspace_id,
                title,
            }),
            AppCommandWire::TabRename { tab_id, title } => {
                Self::Tab(TabCommand::Rename { tab_id, title })
            }
            AppCommandWire::TabClose { tab_id } => Self::Tab(TabCommand::Close { tab_id }),
            AppCommandWire::TabActivate { tab_id, index } => {
                Self::Tab(TabCommand::Activate { tab_id, index })
            }
            AppCommandWire::PaneSplit { pane_id, direction } => {
                Self::Pane(PaneCommand::Split { pane_id, direction })
            }
            AppCommandWire::PaneClose { pane_id } => Self::Pane(PaneCommand::Close { pane_id }),
            AppCommandWire::PaneFocus { pane_id, direction } => {
                Self::Pane(PaneCommand::Focus { pane_id, direction })
            }
            AppCommandWire::PaneResize { pane_id, ratio } => {
                Self::Pane(PaneCommand::Resize { pane_id, ratio })
            }
            AppCommandWire::PaneResizeSplit {
                tab_id,
                path,
                ratio,
            } => Self::Pane(PaneCommand::ResizeSplit {
                tab_id,
                path,
                ratio,
            }),
            AppCommandWire::PaneMoveToWorkspace {
                pane_id,
                workspace_id,
            } => Self::Pane(PaneCommand::MoveToWorkspace {
                pane_id,
                workspace_id,
            }),
            AppCommandWire::PaneAgentRename { pane_id, label } => {
                Self::Pane(PaneCommand::RenameAgent { pane_id, label })
            }
            AppCommandWire::SurfaceReplace { pane_id, kind } => {
                Self::Surface(SurfaceCommand::Replace { pane_id, kind })
            }
            AppCommandWire::TerminalSpawn {
                pane_id,
                program,
                args,
                columns,
                lines,
            } => {
                let args = if args.is_empty() {
                    let defaults = crate::terminal::default_shell_args(&program);
                    if defaults.is_empty() { args } else { defaults }
                } else {
                    args
                };
                Self::Terminal(TerminalCommand::Spawn {
                    pane_id,
                    program,
                    args,
                    columns,
                    lines,
                })
            }
            AppCommandWire::TerminalSendText {
                terminal_id,
                pane_id,
                text,
            } => Self::Terminal(TerminalCommand::SendText {
                terminal_id,
                pane_id,
                text,
            }),
            AppCommandWire::TerminalSendBytes {
                terminal_id,
                pane_id,
                bytes,
            } => Self::Terminal(TerminalCommand::SendBytes {
                terminal_id,
                pane_id,
                bytes,
            }),
            AppCommandWire::TerminalResize {
                terminal_id,
                pane_id,
                columns,
                lines,
            } => Self::Terminal(TerminalCommand::Resize {
                terminal_id,
                pane_id,
                columns,
                lines,
            }),
            AppCommandWire::TerminalScroll {
                terminal_id,
                pane_id,
                lines,
            } => Self::Terminal(TerminalCommand::Scroll {
                terminal_id,
                pane_id,
                lines,
            }),
        }
    }
}

impl Serialize for AppCommand {
    fn serialize<S>(&self, serializer: S) -> Result<S::Ok, S::Error>
    where
        S: Serializer,
    {
        AppCommandWire::from(self).serialize(serializer)
    }
}

impl<'de> Deserialize<'de> for AppCommand {
    fn deserialize<D>(deserializer: D) -> Result<Self, D::Error>
    where
        D: Deserializer<'de>,
    {
        AppCommandWire::deserialize(deserializer).map(Into::into)
    }
}

impl AppCommand {
    pub fn type_name(&self) -> &'static str {
        match self {
            Self::Workspace(WorkspaceCommand::Create) => "workspace.create",
            Self::Workspace(WorkspaceCommand::New) => "workspace.new",
            Self::Workspace(WorkspaceCommand::Ensure) => "workspace.ensure",
            Self::Workspace(WorkspaceCommand::Close { .. }) => "workspace.close",
            Self::Workspace(WorkspaceCommand::Delete { .. }) => "workspace.delete",
            Self::Workspace(WorkspaceCommand::Activate { .. }) => "workspace.activate",
            Self::Workspace(WorkspaceCommand::Rename { .. }) => "workspace.rename",
            Self::Workspace(WorkspaceCommand::Reorder { .. }) => "workspace.reorder",
            Self::Tab(TabCommand::New { .. }) => "tab.new",
            Self::Tab(TabCommand::NewInWorkspace { .. }) => "tab.new_in_workspace",
            Self::Tab(TabCommand::Rename { .. }) => "tab.rename",
            Self::Tab(TabCommand::Close { .. }) => "tab.close",
            Self::Tab(TabCommand::Activate { .. }) => "tab.activate",
            Self::Pane(PaneCommand::Split { .. }) => "pane.split",
            Self::Pane(PaneCommand::Close { .. }) => "pane.close",
            Self::Pane(PaneCommand::Focus { .. }) => "pane.focus",
            Self::Pane(PaneCommand::Resize { .. }) => "pane.resize",
            Self::Pane(PaneCommand::ResizeSplit { .. }) => "pane.resize_split",
            Self::Pane(PaneCommand::MoveToWorkspace { .. }) => "pane.move_to_workspace",
            Self::Pane(PaneCommand::RenameAgent { .. }) => "pane.agent_rename",
            Self::Surface(SurfaceCommand::Replace { .. }) => "surface.replace",
            Self::Terminal(TerminalCommand::Spawn { .. }) => "terminal.spawn",
            Self::Terminal(TerminalCommand::SendText { .. }) => "terminal.send_text",
            Self::Terminal(TerminalCommand::SendBytes { .. }) => "terminal.send_bytes",
            Self::Terminal(TerminalCommand::Resize { .. }) => "terminal.resize",
            Self::Terminal(TerminalCommand::Scroll { .. }) => "terminal.scroll",
        }
    }
}

#[derive(Debug, Clone, PartialEq, Eq, Error)]
pub enum DispatchError {
    #[error("{0}")]
    Command(CommandError),
    #[error("command channel closed")]
    ChannelClosed,
}

impl From<CommandError> for DispatchError {
    fn from(error: CommandError) -> Self {
        Self::Command(error)
    }
}
