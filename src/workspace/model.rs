use serde::{Deserialize, Serialize};

use crate::{
    ids::{PaneId, TabId, WorkspaceId},
    pane::PaneNode,
};

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Workspace {
    pub id: WorkspaceId,
    pub tabs: Vec<TabId>,
    pub active_tab: Option<TabId>,
}

impl Workspace {
    pub fn new(id: WorkspaceId) -> Self {
        Self {
            id,
            tabs: Vec::new(),
            active_tab: None,
        }
    }
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct Tab {
    pub id: TabId,
    pub title: String,
    pub root: PaneNode,
    pub active_pane: PaneId,
}

impl Tab {
    pub fn new(id: TabId, title: String, initial_pane: PaneId) -> Self {
        Self {
            id,
            title,
            root: PaneNode::leaf(initial_pane),
            active_pane: initial_pane,
        }
    }
}
