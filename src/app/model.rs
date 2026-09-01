use std::collections::BTreeMap;

use serde::{Deserialize, Serialize};

use crate::{
    ids::{PaneId, SurfaceId, TabId, TerminalId, WorkspaceId},
    pane::{Pane, PaneNode, SplitAxis},
    surface::{SurfaceKind, SurfaceState, TerminalStatus, TerminalSurfaceState},
    terminal::TerminalSnapshot,
    workspace::{Tab, Workspace},
};

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct StateDump {
    pub state_revision: u64,
    pub workspace: Option<WorkspaceDump>,
    pub focused_pane: Option<PaneId>,
}

pub type ModelSnapshot = StateDump;

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct WorkspaceDump {
    pub id: WorkspaceId,
    pub active_tab: Option<TabId>,
    pub tabs: Vec<TabDump>,
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct TabDump {
    pub id: TabId,
    pub title: String,
    pub active_pane: PaneId,
    pub tree: PaneTreeDump,
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(tag = "type", rename_all = "snake_case")]
pub enum PaneTreeDump {
    Leaf {
        pane_id: PaneId,
        surface_id: SurfaceId,
        surface_kind: SurfaceKind,
        surface_state: SurfaceState,
        terminal_snapshot: Option<Box<TerminalSnapshot>>,
    },
    Split {
        axis: SplitAxis,
        ratio: f32,
        first: Box<PaneTreeDump>,
        second: Box<PaneTreeDump>,
    },
}

impl PaneTreeDump {
    pub fn pane_count(&self) -> usize {
        match self {
            Self::Leaf { .. } => 1,
            Self::Split { first, second, .. } => first.pane_count() + second.pane_count(),
        }
    }

    pub fn surface_kind_for(&self, pane_id: PaneId) -> Option<SurfaceKind> {
        match self {
            Self::Leaf {
                pane_id: leaf_id,
                surface_kind,
                ..
            } if *leaf_id == pane_id => Some(*surface_kind),
            Self::Leaf { .. } => None,
            Self::Split { first, second, .. } => first
                .surface_kind_for(pane_id)
                .or_else(|| second.surface_kind_for(pane_id)),
        }
    }
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct MemoryStats {
    pub terminal_count: usize,
    pub scrollback_lines: usize,
    pub visible_cells: usize,
    pub surface_count: usize,
    pub shape_cache_entries: usize,
    pub image_cache_bytes: usize,
}

/// The application-domain state. It is owned by the model thread and is only
/// changed through `CommandDispatcher`; views receive `StateDump` projections.
#[derive(Debug)]
pub(crate) struct SplitRequest {
    pub tab_id: TabId,
    pub target_pane: PaneId,
    pub axis: SplitAxis,
    pub ratio: f32,
    pub new_pane: PaneId,
    pub new_surface: SurfaceId,
    pub new_first: bool,
}

#[derive(Debug)]
pub struct ApplicationModel {
    workspace: Option<Workspace>,
    tabs: BTreeMap<TabId, Tab>,
    panes: BTreeMap<PaneId, Pane>,
    surfaces: BTreeMap<SurfaceId, SurfaceState>,
    state_revision: u64,
}

impl Default for ApplicationModel {
    fn default() -> Self {
        Self::new()
    }
}

impl ApplicationModel {
    pub fn new() -> Self {
        Self {
            workspace: None,
            tabs: BTreeMap::new(),
            panes: BTreeMap::new(),
            surfaces: BTreeMap::new(),
            state_revision: 0,
        }
    }

    pub fn state_revision(&self) -> u64 {
        self.state_revision
    }

    pub fn workspace(&self) -> Option<&Workspace> {
        self.workspace.as_ref()
    }

    pub fn tab(&self, tab_id: TabId) -> Option<&Tab> {
        self.tabs.get(&tab_id)
    }

    pub fn pane(&self, pane_id: PaneId) -> Option<&Pane> {
        self.panes.get(&pane_id)
    }

    pub fn surface(&self, surface_id: SurfaceId) -> Option<&SurfaceState> {
        self.surfaces.get(&surface_id)
    }

    pub fn tabs(&self) -> impl Iterator<Item = &Tab> {
        self.tabs.values()
    }

    pub fn panes(&self) -> impl Iterator<Item = &Pane> {
        self.panes.values()
    }

    pub(crate) fn bump_revision(&mut self) -> u64 {
        self.state_revision = self
            .state_revision
            .checked_add(1)
            .expect("water state revision exhausted");
        self.state_revision
    }

    pub(crate) fn create_workspace(&mut self, workspace_id: WorkspaceId) -> bool {
        if self.workspace.is_some() {
            return false;
        }
        self.workspace = Some(Workspace::new(workspace_id));
        true
    }

    pub(crate) fn create_tab(
        &mut self,
        tab_id: TabId,
        title: String,
        pane_id: PaneId,
        surface_id: SurfaceId,
    ) -> Result<bool, &'static str> {
        let workspace = self.workspace.as_mut().ok_or("workspace not found")?;
        if self.tabs.contains_key(&tab_id) {
            return Ok(false);
        }
        self.panes.insert(
            pane_id,
            Pane {
                id: pane_id,
                surface: surface_id,
            },
        );
        self.surfaces.insert(surface_id, SurfaceState::empty());
        self.tabs.insert(tab_id, Tab::new(tab_id, title, pane_id));
        workspace.tabs.push(tab_id);
        workspace.active_tab = Some(tab_id);
        Ok(true)
    }

    pub(crate) fn close_tab(&mut self, tab_id: TabId) -> Result<Tab, &'static str> {
        let workspace = self.workspace.as_mut().ok_or("workspace not found")?;
        if !workspace.tabs.contains(&tab_id) {
            return Err("tab not found");
        }
        let tab = self.tabs.remove(&tab_id).ok_or("tab not found")?;
        let mut pane_ids = Vec::new();
        tab.root.leaf_ids(&mut pane_ids);
        for pane_id in pane_ids {
            if let Some(pane) = self.panes.remove(&pane_id) {
                self.surfaces.remove(&pane.surface);
            }
        }
        workspace.tabs.retain(|id| *id != tab_id);
        if workspace.active_tab == Some(tab_id) {
            workspace.active_tab = workspace.tabs.last().copied();
        }
        Ok(tab)
    }

    pub(crate) fn activate_tab(&mut self, tab_id: TabId) -> Result<bool, &'static str> {
        let workspace = self.workspace.as_mut().ok_or("workspace not found")?;
        if !workspace.tabs.contains(&tab_id) {
            return Err("tab not found");
        }
        if workspace.active_tab == Some(tab_id) {
            return Ok(false);
        }
        workspace.active_tab = Some(tab_id);
        Ok(true)
    }

    pub(crate) fn active_tab_id(&self) -> Option<TabId> {
        self.workspace
            .as_ref()
            .and_then(|workspace| workspace.active_tab)
    }

    pub(crate) fn tab_id_for_pane(&self, pane_id: PaneId) -> Option<TabId> {
        self.tabs
            .values()
            .find(|tab| tab.root.contains(pane_id))
            .map(|tab| tab.id)
    }

    pub(crate) fn active_pane(&self) -> Option<PaneId> {
        self.active_tab_id()
            .and_then(|tab_id| self.tabs.get(&tab_id).map(|tab| tab.active_pane))
    }

    pub(crate) fn pane_ids_in_tab(&self, tab_id: TabId) -> Option<Vec<PaneId>> {
        let tab = self.tabs.get(&tab_id)?;
        let mut pane_ids = Vec::new();
        tab.root.leaf_ids(&mut pane_ids);
        Some(pane_ids)
    }

    pub(crate) fn split_pane(&mut self, request: SplitRequest) -> Result<(), &'static str> {
        let SplitRequest {
            tab_id,
            target_pane,
            axis,
            ratio,
            new_pane,
            new_surface,
            new_first,
        } = request;
        let tab = self.tabs.get_mut(&tab_id).ok_or("tab not found")?;
        if !tab.root.contains(target_pane) {
            return Err("pane not found");
        }
        if !tab
            .root
            .split_leaf(target_pane, axis, ratio, new_pane, new_first)
        {
            return Err("pane not found");
        }
        self.panes.insert(
            new_pane,
            Pane {
                id: new_pane,
                surface: new_surface,
            },
        );
        self.surfaces.insert(new_surface, SurfaceState::empty());
        tab.active_pane = new_pane;
        Ok(())
    }

    pub(crate) fn close_pane(
        &mut self,
        tab_id: TabId,
        pane_id: PaneId,
    ) -> Result<Pane, &'static str> {
        let tab = self.tabs.get_mut(&tab_id).ok_or("tab not found")?;
        if !tab.root.contains(pane_id) {
            return Err("pane not found");
        }
        if tab.root.pane_count() == 1 {
            return Err("cannot close the final pane");
        }
        if !tab.root.close_leaf(pane_id) {
            return Err("pane not found");
        }
        let removed = self.panes.remove(&pane_id).ok_or("pane not found")?;
        self.surfaces.remove(&removed.surface);
        if tab.active_pane == pane_id {
            let mut remaining = Vec::new();
            tab.root.leaf_ids(&mut remaining);
            tab.active_pane = remaining
                .first()
                .copied()
                .expect("a non-final pane close leaves a pane");
        }
        Ok(removed)
    }

    pub(crate) fn focus_pane(
        &mut self,
        tab_id: TabId,
        pane_id: PaneId,
    ) -> Result<bool, &'static str> {
        let workspace = self.workspace.as_mut().ok_or("workspace not found")?;
        let tab = self.tabs.get_mut(&tab_id).ok_or("tab not found")?;
        if !tab.root.contains(pane_id) {
            return Err("pane not found");
        }
        let changed = tab.active_pane != pane_id || workspace.active_tab != Some(tab_id);
        tab.active_pane = pane_id;
        workspace.active_tab = Some(tab_id);
        Ok(changed)
    }

    pub(crate) fn resize_pane(
        &mut self,
        tab_id: TabId,
        pane_id: PaneId,
        ratio: f32,
    ) -> Result<(), &'static str> {
        let tab = self.tabs.get_mut(&tab_id).ok_or("tab not found")?;
        if !tab.root.resize_nearest(pane_id, ratio) {
            return Err("split not found for pane");
        }
        Ok(())
    }

    pub(crate) fn replace_surface(
        &mut self,
        pane_id: PaneId,
        new_surface_id: SurfaceId,
        new_surface: SurfaceState,
    ) -> Result<SurfaceId, &'static str> {
        let pane = self.panes.get_mut(&pane_id).ok_or("pane not found")?;
        let old_surface_id = pane.surface;
        self.surfaces.insert(new_surface_id, new_surface);
        pane.surface = new_surface_id;
        self.surfaces.remove(&old_surface_id);
        Ok(old_surface_id)
    }

    pub(crate) fn terminal_id_for_pane(&self, pane_id: PaneId) -> Option<TerminalId> {
        let pane = self.panes.get(&pane_id)?;
        match self.surfaces.get(&pane.surface)? {
            SurfaceState::Terminal(terminal) => Some(terminal.terminal_id),
            SurfaceState::Empty(_) => None,
        }
    }

    pub(crate) fn terminal_surface(
        &self,
        terminal_id: TerminalId,
    ) -> Option<&TerminalSurfaceState> {
        self.surfaces.values().find_map(|surface| match surface {
            SurfaceState::Terminal(terminal) if terminal.terminal_id == terminal_id => {
                Some(terminal)
            }
            SurfaceState::Empty(_) | SurfaceState::Terminal(_) => None,
        })
    }

    pub(crate) fn set_terminal_output_revision(
        &mut self,
        terminal_id: TerminalId,
        revision: u64,
    ) -> bool {
        for surface in self.surfaces.values_mut() {
            if let SurfaceState::Terminal(terminal) = surface
                && terminal.terminal_id == terminal_id
            {
                terminal.last_output_revision = revision;
                return true;
            }
        }
        false
    }

    pub(crate) fn set_terminal_status(
        &mut self,
        terminal_id: TerminalId,
        status: TerminalStatus,
    ) -> bool {
        for surface in self.surfaces.values_mut() {
            if let SurfaceState::Terminal(terminal) = surface
                && terminal.terminal_id == terminal_id
            {
                terminal.status = status;
                return true;
            }
        }
        false
    }

    pub(crate) fn set_terminal_title(&mut self, terminal_id: TerminalId, title: String) -> bool {
        for surface in self.surfaces.values_mut() {
            if let SurfaceState::Terminal(terminal) = surface
                && terminal.terminal_id == terminal_id
            {
                terminal.title = if title.is_empty() { None } else { Some(title) };
                return true;
            }
        }
        false
    }

    pub(crate) fn set_terminal_size(
        &mut self,
        terminal_id: TerminalId,
        columns: usize,
        lines: usize,
    ) -> bool {
        for surface in self.surfaces.values_mut() {
            if let SurfaceState::Terminal(terminal) = surface
                && terminal.terminal_id == terminal_id
            {
                terminal.columns = columns;
                terminal.lines = lines;
                return true;
            }
        }
        false
    }

    pub fn snapshot(&self) -> StateDump {
        let workspace = self.workspace.as_ref().map(|workspace| WorkspaceDump {
            id: workspace.id,
            active_tab: workspace.active_tab,
            tabs: workspace
                .tabs
                .iter()
                .filter_map(|tab_id| self.tabs.get(tab_id))
                .map(|tab| self.tab_dump(tab))
                .collect(),
        });
        StateDump {
            state_revision: self.state_revision,
            workspace,
            focused_pane: self.active_pane(),
        }
    }

    pub fn memory_stats(&self) -> MemoryStats {
        let terminal_count = self
            .surfaces
            .values()
            .filter(|surface| matches!(surface, SurfaceState::Terminal(_)))
            .count();
        MemoryStats {
            terminal_count,
            scrollback_lines: terminal_count * crate::terminal::MAX_SCROLLBACK_LINES,
            visible_cells: self
                .surfaces
                .values()
                .filter_map(|surface| match surface {
                    SurfaceState::Terminal(terminal) => Some(terminal.columns * terminal.lines),
                    SurfaceState::Empty(_) => None,
                })
                .sum(),
            surface_count: self.surfaces.len(),
            shape_cache_entries: 0,
            image_cache_bytes: 0,
        }
    }

    fn tab_dump(&self, tab: &Tab) -> TabDump {
        TabDump {
            id: tab.id,
            title: tab.title.clone(),
            active_pane: tab.active_pane,
            tree: self.pane_tree_dump(&tab.root),
        }
    }

    fn pane_tree_dump(&self, node: &PaneNode) -> PaneTreeDump {
        match node {
            PaneNode::Leaf(pane_id) => {
                let pane = self
                    .panes
                    .get(pane_id)
                    .expect("pane tree references a pane");
                let surface = self
                    .surfaces
                    .get(&pane.surface)
                    .expect("pane references a surface");
                PaneTreeDump::Leaf {
                    pane_id: *pane_id,
                    surface_id: pane.surface,
                    surface_kind: surface.kind(),
                    surface_state: surface.clone(),
                    terminal_snapshot: None,
                }
            }
            PaneNode::Split {
                axis,
                ratio,
                first,
                second,
            } => PaneTreeDump::Split {
                axis: *axis,
                ratio: *ratio,
                first: Box::new(self.pane_tree_dump(first)),
                second: Box::new(self.pane_tree_dump(second)),
            },
        }
    }
}
