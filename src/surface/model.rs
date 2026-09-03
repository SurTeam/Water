use serde::{Deserialize, Serialize};

use crate::agent::DetectedAgent;
use crate::ids::{SessionId, TerminalId};

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum SurfaceKind {
    Terminal,
    Agent,
    FileBrowser,
    ImagePreview,
    MarkdownPreview,
    Diff,
    Empty,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct EmptySurfaceState {
    pub label: String,
}

impl Default for EmptySurfaceState {
    fn default() -> Self {
        Self {
            label: "Empty".to_owned(),
        }
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum TerminalStatus {
    Running,
    Exited { code: Option<i32> },
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct TerminalSurfaceState {
    pub terminal_id: TerminalId,
    pub session_id: SessionId,
    pub program: String,
    pub title: Option<String>,
    /// The current foreground process name used by automatic tab titles.
    #[serde(default)]
    pub process_name: String,
    /// The current foreground process working directory. New tabs and panes
    /// inherit this value through the dispatcher.
    #[serde(default)]
    pub cwd: String,
    pub args: Vec<String>,
    pub status: TerminalStatus,
    pub columns: usize,
    pub lines: usize,
    pub last_output_revision: u64,
    /// The coding agent currently running in the terminal's foreground,
    /// reclassified on every process-metadata refresh. This is the canonical
    /// pane<->agent binding consumed by the sidebar, the state dump, and
    /// future agent surfaces; it is never a UI-side guess.
    #[serde(default)]
    pub agent: Option<DetectedAgent>,
    /// Optional user-facing label override for the currently detected agent;
    /// this is display metadata, not agent identity.
    #[serde(default)]
    pub agent_label: Option<String>,
}

/// Surface state remains an enum rather than a trait object so registry access
/// stays explicit and cheap while future surface kinds are added.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub enum SurfaceState {
    Empty(EmptySurfaceState),
    Terminal(TerminalSurfaceState),
}

impl SurfaceState {
    pub fn empty() -> Self {
        Self::Empty(EmptySurfaceState::default())
    }

    pub fn kind(&self) -> SurfaceKind {
        match self {
            Self::Empty(_) => SurfaceKind::Empty,
            Self::Terminal(_) => SurfaceKind::Terminal,
        }
    }
}
