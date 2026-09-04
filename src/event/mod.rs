use std::collections::VecDeque;

use serde::{Deserialize, Serialize};

use crate::agent::AgentKind;
use crate::ids::{PaneId, SurfaceId, TabId, TerminalId, WorkspaceId};

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case", tag = "type")]
pub enum AppEventKind {
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
    WorkspaceReordered {
        workspace_id: WorkspaceId,
        index: usize,
    },
    TabCreated {
        tab_id: TabId,
    },
    TabRenamed {
        tab_id: TabId,
    },
    TabActivated {
        tab_id: TabId,
    },
    TabClosed {
        tab_id: TabId,
    },
    PaneCreated {
        pane_id: PaneId,
    },
    PaneSplit {
        target_pane: PaneId,
        new_pane: PaneId,
    },
    PaneFocused {
        pane_id: PaneId,
    },
    PaneClosed {
        pane_id: PaneId,
    },
    PaneResized {
        pane_id: PaneId,
        ratio: f32,
    },
    SplitResized {
        tab_id: TabId,
        ratio: f32,
    },
    PaneMovedToWorkspace {
        pane_id: PaneId,
        tab_id: TabId,
        workspace_id: WorkspaceId,
    },
    SurfaceChanged {
        pane_id: PaneId,
        surface_id: SurfaceId,
    },
    TerminalSpawned {
        terminal_id: TerminalId,
        pane_id: PaneId,
    },
    TerminalExited {
        terminal_id: TerminalId,
        exit_code: Option<i32>,
    },
    TerminalOutputChanged {
        terminal_id: TerminalId,
    },
    TerminalResized {
        terminal_id: TerminalId,
        columns: usize,
        lines: usize,
    },
    TerminalTitleChanged {
        terminal_id: TerminalId,
        title: String,
    },
    TerminalProcessChanged {
        terminal_id: TerminalId,
        process_name: String,
        cwd: String,
    },
    AgentStarted {
        terminal_id: TerminalId,
        pane_id: PaneId,
        kind: AgentKind,
    },
    AgentStopped {
        terminal_id: TerminalId,
        pane_id: PaneId,
        kind: AgentKind,
    },
    AgentRenamed {
        pane_id: PaneId,
        label: Option<String>,
    },
}

impl AppEventKind {
    pub fn type_name(&self) -> &'static str {
        match self {
            Self::WorkspaceCreated { .. } => "workspace.created",
            Self::WorkspaceClosed { .. } => "workspace.closed",
            Self::WorkspaceActivated { .. } => "workspace.activated",
            Self::WorkspaceRenamed { .. } => "workspace.renamed",
            Self::WorkspaceReordered { .. } => "workspace.reordered",
            Self::TabCreated { .. } => "tab.created",
            Self::TabRenamed { .. } => "tab.renamed",
            Self::TabActivated { .. } => "tab.activated",
            Self::TabClosed { .. } => "tab.closed",
            Self::PaneCreated { .. } => "pane.created",
            Self::PaneSplit { .. } => "pane.split",
            Self::PaneFocused { .. } => "pane.focused",
            Self::PaneClosed { .. } => "pane.closed",
            Self::PaneResized { .. } => "pane.resized",
            Self::SplitResized { .. } => "split.resized",
            Self::PaneMovedToWorkspace { .. } => "pane.moved_to_workspace",
            Self::SurfaceChanged { .. } => "surface.changed",
            Self::TerminalSpawned { .. } => "terminal.spawned",
            Self::TerminalExited { .. } => "terminal.exited",
            Self::TerminalOutputChanged { .. } => "terminal.output_changed",
            Self::TerminalResized { .. } => "terminal.resized",
            Self::TerminalTitleChanged { .. } => "terminal.title_changed",
            Self::TerminalProcessChanged { .. } => "terminal.process_changed",
            Self::AgentStarted { .. } => "agent.started",
            Self::AgentStopped { .. } => "agent.stopped",
            Self::AgentRenamed { .. } => "agent.renamed",
        }
    }
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct AppEvent {
    pub sequence: u64,
    pub state_revision: u64,
    pub kind: AppEventKind,
}

#[derive(Debug, Clone)]
pub struct EventBus {
    next_sequence: u64,
    max_events: usize,
    events: VecDeque<AppEvent>,
}

impl Default for EventBus {
    fn default() -> Self {
        Self::new(4096)
    }
}

impl EventBus {
    pub fn new(max_events: usize) -> Self {
        assert!(max_events > 0, "event history must be non-empty");
        Self {
            next_sequence: 1,
            max_events,
            events: VecDeque::with_capacity(max_events.min(256)),
        }
    }

    pub fn emit(&mut self, state_revision: u64, kind: AppEventKind) -> AppEvent {
        let event = AppEvent {
            sequence: self.next_sequence,
            state_revision,
            kind,
        };
        self.next_sequence = self
            .next_sequence
            .checked_add(1)
            .expect("water event sequence exhausted");
        if self.events.len() == self.max_events {
            self.events.pop_front();
        }
        self.events.push_back(event.clone());
        event
    }

    pub fn since(&self, sequence: u64) -> Vec<AppEvent> {
        self.events
            .iter()
            .filter(|event| event.sequence > sequence)
            .cloned()
            .collect()
    }

    pub fn all(&self) -> Vec<AppEvent> {
        self.events.iter().cloned().collect()
    }

    pub fn latest_sequence(&self) -> u64 {
        self.next_sequence.saturating_sub(1)
    }
}
