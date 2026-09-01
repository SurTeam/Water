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
    Create,
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub enum TabCommand {
    New {
        title: Option<String>,
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
    TabCreated {
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
    #[serde(rename = "tab.new")]
    TabNew { title: Option<String> },
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
            AppCommand::Tab(TabCommand::New { title }) => Self::TabNew {
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
            AppCommandWire::TabNew { title } => Self::Tab(TabCommand::New { title }),
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
            Self::Tab(TabCommand::New { .. }) => "tab.new",
            Self::Tab(TabCommand::Close { .. }) => "tab.close",
            Self::Tab(TabCommand::Activate { .. }) => "tab.activate",
            Self::Pane(PaneCommand::Split { .. }) => "pane.split",
            Self::Pane(PaneCommand::Close { .. }) => "pane.close",
            Self::Pane(PaneCommand::Focus { .. }) => "pane.focus",
            Self::Pane(PaneCommand::Resize { .. }) => "pane.resize",
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
