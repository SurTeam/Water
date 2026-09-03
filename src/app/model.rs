use std::collections::BTreeMap;
use std::sync::Arc;

use serde::{Deserialize, Deserializer, Serialize};

use crate::{
    agent::{AgentKind, DetectedAgent, detect_agent},
    ids::{PaneId, SurfaceId, TabId, TerminalId, WorkspaceId},
    pane::{Pane, PaneDirection, PaneNode, SplitAxis},
    surface::{SurfaceKind, SurfaceState, TerminalStatus, TerminalSurfaceState},
    terminal::{TerminalSnapshot, TerminalSummary},
    workspace::{Tab, Workspace},
};

#[derive(Debug, Clone, PartialEq, Serialize)]
pub struct StateDump {
    pub state_revision: u64,
    /// Compatibility alias for the active workspace. New consumers should use
    /// `workspaces` and `active_workspace`.
    pub workspace: Option<WorkspaceDump>,
    #[serde(default)]
    pub workspaces: Vec<WorkspaceDump>,
    #[serde(default)]
    pub active_workspace: Option<WorkspaceId>,
    pub focused_pane: Option<PaneId>,
    /// Flattened pane<->agent bindings for every workspace and tab, ordered
    /// by workspace, tab, then pane-tree position. This is the stable data
    /// contract for the sidebar Agents section, `waterctl state.dump`, and
    /// future agent surfaces; consumers never re-walk the pane tree to find
    /// agents themselves.
    #[serde(default)]
    pub agents: Vec<AgentDump>,
}

impl<'de> Deserialize<'de> for StateDump {
    fn deserialize<D>(deserializer: D) -> Result<Self, D::Error>
    where
        D: Deserializer<'de>,
    {
        #[derive(Deserialize)]
        struct StateDumpFields {
            state_revision: u64,
            #[serde(default)]
            workspace: Option<WorkspaceDump>,
            #[serde(default)]
            workspaces: Vec<WorkspaceDump>,
            #[serde(default)]
            active_workspace: Option<WorkspaceId>,
            #[serde(default)]
            focused_pane: Option<PaneId>,
            #[serde(default)]
            agents: Vec<AgentDump>,
        }

        let fields = StateDumpFields::deserialize(deserializer)?;
        let active_workspace = fields
            .active_workspace
            .or_else(|| fields.workspace.as_ref().map(|workspace| workspace.id));
        let mut workspaces = fields.workspaces;
        if workspaces.is_empty()
            && let Some(workspace) = fields.workspace.as_ref()
        {
            workspaces.push(workspace.clone());
        }
        let workspace = fields.workspace.or_else(|| {
            active_workspace.and_then(|workspace_id| {
                workspaces
                    .iter()
                    .find(|workspace| workspace.id == workspace_id)
                    .cloned()
            })
        });

        Ok(Self {
            state_revision: fields.state_revision,
            workspace,
            workspaces,
            active_workspace,
            focused_pane: fields.focused_pane,
            agents: fields.agents,
        })
    }
}

pub type ModelSnapshot = StateDump;

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct WorkspaceDump {
    pub id: WorkspaceId,
    #[serde(default = "default_workspace_dump_title")]
    pub title: String,
    pub active_tab: Option<TabId>,
    pub tabs: Vec<TabDump>,
}

fn default_workspace_dump_title() -> String {
    "Workspace".to_owned()
}

/// A detected coding agent projected together with the full identity path
/// (workspace, tab, pane, terminal) that owns it. Activating the agent's
/// location is `pane.focus` on `pane_id`; no secondary lookup table exists.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct AgentDump {
    pub kind: AgentKind,
    pub label: String,
    #[serde(default)]
    pub active: bool,
    pub workspace_id: WorkspaceId,
    pub tab_id: TabId,
    pub pane_id: PaneId,
    pub terminal_id: TerminalId,
    #[serde(default)]
    pub cwd: String,
    pub status: TerminalStatus,
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct TabDump {
    pub id: TabId,
    pub title: String,
    #[serde(default)]
    pub title_override: Option<String>,
    pub active_pane: PaneId,
    pub tree: PaneTreeDump,
}

/// Terminal state carried by a projected pane leaf.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct TerminalProjection {
    pub summary: TerminalSummary,
    /// Shared (not copied) grid from the terminal registry. Present only on
    /// the displayed tab of each workspace; waiters and waterctl fetch the
    /// grid for any single terminal through the registry RPC instead.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub snapshot: Option<Arc<TerminalSnapshot>>,
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(tag = "type", rename_all = "snake_case")]
pub enum PaneTreeDump {
    Leaf {
        pane_id: PaneId,
        surface_id: SurfaceId,
        surface_kind: SurfaceKind,
        surface_state: SurfaceState,
        /// Cell-free metadata plus, for displayed tabs only, a shared
        /// reference to the registry's single grid snapshot. Hidden tabs
        /// never duplicate their grid into the projection layer.
        #[serde(default)]
        terminal: Option<Box<TerminalProjection>>,
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
    /// Configured per-terminal scrollback limit (focused rows).
    pub scrollback_lines: usize,
    /// Rows the focused terminal retains after losing focus.
    pub inactive_scrollback_lines: usize,
    /// Aggregate byte ceiling shared by all terminal scrollback grids.
    pub max_total_scrollback_bytes: usize,
    /// Currently retained scrollback across all workers (budget accounting).
    pub retained_scrollback_lines: usize,
    pub retained_scrollback_bytes: usize,
    /// Estimated heap bytes pinned by snapshot grids and recent-output
    /// buffers still held by the terminal registry (including retired
    /// terminals inside the auto-close race window).
    pub registry_snapshot_bytes: usize,
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
    workspaces: BTreeMap<WorkspaceId, Workspace>,
    active_workspace: Option<WorkspaceId>,
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
            workspaces: BTreeMap::new(),
            active_workspace: None,
            tabs: BTreeMap::new(),
            panes: BTreeMap::new(),
            surfaces: BTreeMap::new(),
            state_revision: 0,
        }
    }

    pub fn state_revision(&self) -> u64 {
        self.state_revision
    }

    /// Returns the active workspace. This keeps the Phase 1/2 query shape
    /// available while the model now owns multiple workspaces.
    pub fn workspace(&self) -> Option<&Workspace> {
        self.active_workspace
            .and_then(|workspace_id| self.workspaces.get(&workspace_id))
    }

    pub fn workspace_by_id(&self, workspace_id: WorkspaceId) -> Option<&Workspace> {
        self.workspaces.get(&workspace_id)
    }

    pub fn workspaces(&self) -> impl Iterator<Item = &Workspace> {
        self.workspaces.values()
    }

    pub fn active_workspace_id(&self) -> Option<WorkspaceId> {
        self.active_workspace
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

    pub(crate) fn create_workspace_with_title(
        &mut self,
        workspace_id: WorkspaceId,
        title: String,
    ) -> bool {
        if self.workspaces.contains_key(&workspace_id) {
            return false;
        }
        self.workspaces
            .insert(workspace_id, Workspace::new_with_title(workspace_id, title));
        self.active_workspace = Some(workspace_id);
        true
    }

    pub(crate) fn activate_workspace(
        &mut self,
        workspace_id: WorkspaceId,
    ) -> Result<bool, &'static str> {
        if !self.workspaces.contains_key(&workspace_id) {
            return Err("workspace not found");
        }
        if self.active_workspace == Some(workspace_id) {
            return Ok(false);
        }
        self.active_workspace = Some(workspace_id);
        Ok(true)
    }

    pub(crate) fn rename_workspace(
        &mut self,
        workspace_id: WorkspaceId,
        title: String,
    ) -> Result<bool, &'static str> {
        let workspace = self
            .workspaces
            .get_mut(&workspace_id)
            .ok_or("workspace not found")?;
        if workspace.title == title {
            return Ok(false);
        }
        workspace.title = title;
        Ok(true)
    }

    /// Removes a workspace and all of its tabs, panes, and surfaces. Terminal
    /// workers are retired by the dispatcher immediately after this model
    /// mutation, keeping process ownership outside the model.
    pub(crate) fn close_workspace(
        &mut self,
        workspace_id: WorkspaceId,
    ) -> Result<Workspace, &'static str> {
        let removed = self
            .workspaces
            .remove(&workspace_id)
            .ok_or("workspace not found")?;
        for tab_id in &removed.tabs {
            let Some(tab) = self.tabs.remove(tab_id) else {
                continue;
            };
            let mut pane_ids = Vec::new();
            tab.root.leaf_ids(&mut pane_ids);
            for pane_id in pane_ids {
                if let Some(pane) = self.panes.remove(&pane_id) {
                    self.surfaces.remove(&pane.surface);
                }
            }
        }
        if self.active_workspace == Some(workspace_id) {
            self.active_workspace = self.workspaces.keys().next_back().copied();
        }
        Ok(removed)
    }

    pub(crate) fn create_tab_with_title_mode(
        &mut self,
        tab_id: TabId,
        title: String,
        title_is_pinned: bool,
        pane_id: PaneId,
        surface_id: SurfaceId,
    ) -> Result<bool, &'static str> {
        let workspace_id = self.active_workspace.ok_or("workspace not found")?;
        self.create_tab_with_title_mode_in_workspace(
            workspace_id,
            tab_id,
            title,
            title_is_pinned,
            pane_id,
            surface_id,
        )
    }

    /// Creates a tab in an explicit workspace without changing the
    /// process-global active workspace. The target workspace's active tab is
    /// updated so a view scoped to that workspace can immediately render it.
    pub(crate) fn create_tab_with_title_mode_in_workspace(
        &mut self,
        workspace_id: WorkspaceId,
        tab_id: TabId,
        title: String,
        title_is_pinned: bool,
        pane_id: PaneId,
        surface_id: SurfaceId,
    ) -> Result<bool, &'static str> {
        if !self.workspaces.contains_key(&workspace_id) {
            return Err("workspace not found");
        }
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
        let title_override = title_is_pinned.then(|| title.clone());
        self.tabs.insert(
            tab_id,
            Tab::new_with_title(tab_id, title, pane_id, title_override),
        );
        let workspace = self
            .workspaces
            .get_mut(&workspace_id)
            .expect("workspace was validated before mutation");
        workspace.tabs.push(tab_id);
        workspace.active_tab = Some(tab_id);
        Ok(true)
    }

    pub(crate) fn close_tab(&mut self, tab_id: TabId) -> Result<Tab, &'static str> {
        let workspace_id = self.workspace_id_for_tab(tab_id).ok_or("tab not found")?;
        let tab = self.tabs.remove(&tab_id).ok_or("tab not found")?;
        let mut pane_ids = Vec::new();
        tab.root.leaf_ids(&mut pane_ids);
        for pane_id in pane_ids {
            if let Some(pane) = self.panes.remove(&pane_id) {
                self.surfaces.remove(&pane.surface);
            }
        }
        let workspace = self
            .workspaces
            .get_mut(&workspace_id)
            .ok_or("workspace not found")?;
        workspace.tabs.retain(|id| *id != tab_id);
        if workspace.active_tab == Some(tab_id) {
            workspace.active_tab = workspace.tabs.last().copied();
        }
        Ok(tab)
    }

    pub(crate) fn activate_tab(&mut self, tab_id: TabId) -> Result<bool, &'static str> {
        let workspace_id = self.workspace_id_for_tab(tab_id).ok_or("tab not found")?;
        let workspace = self
            .workspaces
            .get_mut(&workspace_id)
            .ok_or("workspace not found")?;
        let changed =
            self.active_workspace != Some(workspace_id) || workspace.active_tab != Some(tab_id);
        self.active_workspace = Some(workspace_id);
        workspace.active_tab = Some(tab_id);
        Ok(changed)
    }

    pub(crate) fn active_tab_id_for_workspace(&self, workspace_id: WorkspaceId) -> Option<TabId> {
        self.workspaces
            .get(&workspace_id)
            .and_then(|workspace| workspace.active_tab)
    }

    pub(crate) fn active_tab_id(&self) -> Option<TabId> {
        self.active_workspace
            .and_then(|workspace_id| self.active_tab_id_for_workspace(workspace_id))
    }

    pub(crate) fn tab_id_for_pane(&self, pane_id: PaneId) -> Option<TabId> {
        self.tabs
            .values()
            .find(|tab| tab.root.contains(pane_id))
            .map(|tab| tab.id)
    }

    pub(crate) fn workspace_id_for_pane(&self, pane_id: PaneId) -> Option<WorkspaceId> {
        self.tab_id_for_pane(pane_id)
            .and_then(|tab_id| self.workspace_id_for_tab(tab_id))
    }

    pub(crate) fn workspace_id_for_tab(&self, tab_id: TabId) -> Option<WorkspaceId> {
        self.workspaces
            .iter()
            .find(|(_, workspace)| workspace.tabs.contains(&tab_id))
            .map(|(workspace_id, _)| *workspace_id)
    }

    pub(crate) fn active_pane_for_workspace(&self, workspace_id: WorkspaceId) -> Option<PaneId> {
        self.active_tab_id_for_workspace(workspace_id)
            .and_then(|tab_id| self.tabs.get(&tab_id).map(|tab| tab.active_pane))
    }

    pub(crate) fn active_pane(&self) -> Option<PaneId> {
        self.active_workspace
            .and_then(|workspace_id| self.active_pane_for_workspace(workspace_id))
    }

    /// The terminal that currently owns the user's attention. Used to
    /// propagate focus to PTY workers so background tabs can be trimmed to
    /// their inactive scrollback tail.
    pub(crate) fn focused_terminal_id(&self) -> Option<TerminalId> {
        let pane_id = self.active_pane()?;
        let pane = self.panes.get(&pane_id)?;
        match self.surfaces.get(&pane.surface)? {
            SurfaceState::Terminal(terminal) => Some(terminal.terminal_id),
            SurfaceState::Empty(_) => None,
        }
    }

    pub(crate) fn pane_ids_in_tab(&self, tab_id: TabId) -> Option<Vec<PaneId>> {
        let tab = self.tabs.get(&tab_id)?;
        let mut pane_ids = Vec::new();
        tab.root.leaf_ids(&mut pane_ids);
        Some(pane_ids)
    }

    pub(crate) fn directional_pane(
        &self,
        tab_id: TabId,
        pane_id: PaneId,
        direction: PaneDirection,
    ) -> Option<PaneId> {
        self.tabs
            .get(&tab_id)
            .and_then(|tab| tab.root.directional_neighbor(pane_id, direction))
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
        if let Some(workspace_id) = self.workspace_id_for_tab(tab_id) {
            self.active_workspace = Some(workspace_id);
            self.workspaces
                .get_mut(&workspace_id)
                .expect("tab belongs to workspace")
                .active_tab = Some(tab_id);
        }
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
        let workspace_id = self.workspace_id_for_tab(tab_id).ok_or("tab not found")?;
        let tab = self.tabs.get_mut(&tab_id).ok_or("tab not found")?;
        if !tab.root.contains(pane_id) {
            return Err("pane not found");
        }
        let changed = tab.active_pane != pane_id
            || self.active_workspace != Some(workspace_id)
            || self
                .workspaces
                .get(&workspace_id)
                .and_then(|workspace| workspace.active_tab)
                != Some(tab_id);
        tab.active_pane = pane_id;
        self.active_workspace = Some(workspace_id);
        self.workspaces
            .get_mut(&workspace_id)
            .expect("tab belongs to workspace")
            .active_tab = Some(tab_id);
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

    pub(crate) fn pane_id_for_terminal(&self, terminal_id: TerminalId) -> Option<PaneId> {
        self.panes.values().find_map(|pane| {
            let surface = self.surfaces.get(&pane.surface)?;
            match surface {
                SurfaceState::Terminal(terminal) if terminal.terminal_id == terminal_id => {
                    Some(pane.id)
                }
                SurfaceState::Empty(_) | SurfaceState::Terminal(_) => None,
            }
        })
    }

    pub(crate) fn workspace_id_for_terminal(&self, terminal_id: TerminalId) -> Option<WorkspaceId> {
        self.pane_id_for_terminal(terminal_id)
            .and_then(|pane_id| self.workspace_id_for_pane(pane_id))
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

    /// Updates worker-owned terminal metadata and returns the affected pane if
    /// anything changed. The caller decides whether the corresponding tab is
    /// allowed to follow the process name. The agent binding is derived here,
    /// on the model thread, from every process-metadata refresh:
    /// reclassification of the foreground process (agent appears, changes, or
    /// exits) is a terminal-metadata update like any other, so the binding
    /// can never drift from the process actually running in the pane.
    pub(crate) fn set_terminal_process(
        &mut self,
        terminal_id: TerminalId,
        process_name: String,
        cwd: String,
        cmdline: Vec<String>,
        active: bool,
    ) -> Option<PaneId> {
        let agent =
            detect_agent(&process_name, &cmdline).map(|kind| DetectedAgent { kind, active });
        for pane in self.panes.values() {
            let Some(SurfaceState::Terminal(terminal)) = self.surfaces.get(&pane.surface) else {
                continue;
            };
            if terminal.terminal_id != terminal_id {
                continue;
            }
            let changed = terminal.process_name != process_name
                || terminal.cwd != cwd
                || terminal.agent != agent;
            if let Some(SurfaceState::Terminal(terminal)) = self.surfaces.get_mut(&pane.surface) {
                terminal.process_name = process_name;
                terminal.cwd = cwd;
                terminal.agent = agent;
            }
            return changed.then_some(pane.id);
        }
        None
    }

    pub(crate) fn rename_tab(
        &mut self,
        tab_id: TabId,
        title: String,
    ) -> Result<bool, &'static str> {
        let tab = self.tabs.get_mut(&tab_id).ok_or("tab not found")?;
        if tab.title == title && tab.title_override.as_deref() == Some(title.as_str()) {
            return Ok(false);
        }
        tab.title = title.clone();
        tab.title_override = Some(title);
        Ok(true)
    }

    pub(crate) fn update_tab_title_from_process(
        &mut self,
        tab_id: TabId,
        pane_id: PaneId,
        process_name: &str,
    ) -> bool {
        let Some(tab) = self.tabs.get_mut(&tab_id) else {
            return false;
        };
        if tab.active_pane != pane_id || tab.title_override.is_some() || tab.title == process_name {
            return false;
        }
        tab.title = process_name.to_owned();
        true
    }

    pub(crate) fn refresh_tab_title_from_active_pane(&mut self, tab_id: TabId) -> bool {
        let Some(tab) = self.tabs.get(&tab_id) else {
            return false;
        };
        let pane_id = tab.active_pane;
        let process_name = self
            .terminal_id_for_pane(pane_id)
            .and_then(|terminal_id| self.terminal_surface(terminal_id))
            .map(|terminal| terminal.process_name.clone())
            .filter(|name| !name.is_empty());
        let Some(process_name) = process_name else {
            return false;
        };
        self.update_tab_title_from_process(tab_id, pane_id, &process_name)
    }

    pub(crate) fn terminal_cwd(&self, terminal_id: TerminalId) -> Option<String> {
        self.terminal_surface(terminal_id)
            .map(|terminal| terminal.cwd.clone())
            .filter(|cwd| !cwd.is_empty())
    }

    pub fn snapshot(&self) -> StateDump {
        let workspaces: Vec<_> = self
            .workspaces
            .values()
            .map(|workspace| self.workspace_dump(workspace))
            .collect();
        let workspace = self
            .active_workspace
            .and_then(|workspace_id| self.workspaces.get(&workspace_id))
            .map(|workspace| self.workspace_dump(workspace));
        StateDump {
            state_revision: self.state_revision,
            workspace,
            workspaces,
            active_workspace: self.active_workspace,
            focused_pane: self.active_pane(),
            agents: self.collect_agents(),
        }
    }

    /// Flattens the pane<->agent bindings in stable order (workspace, tab,
    /// pane tree). Hidden tabs included: clicking a sidebar agent row jumps
    /// across tabs and workspaces through the regular command path.
    fn collect_agents(&self) -> Vec<AgentDump> {
        let mut agents = Vec::new();
        for workspace in self.workspaces.values() {
            for tab_id in &workspace.tabs {
                let Some(tab) = self.tabs.get(tab_id) else {
                    continue;
                };
                let mut pane_ids = Vec::new();
                tab.root.leaf_ids(&mut pane_ids);
                for pane_id in pane_ids {
                    let Some(SurfaceState::Terminal(terminal)) = self
                        .panes
                        .get(&pane_id)
                        .and_then(|pane| self.surfaces.get(&pane.surface))
                    else {
                        continue;
                    };
                    let Some(agent) = terminal.agent else {
                        continue;
                    };
                    agents.push(AgentDump {
                        kind: agent.kind,
                        label: agent.kind.label().to_owned(),
                        active: agent.active,
                        workspace_id: workspace.id,
                        tab_id: tab.id,
                        pane_id,
                        terminal_id: terminal.terminal_id,
                        cwd: terminal.cwd.clone(),
                        status: terminal.status,
                    });
                }
            }
        }
        agents
    }

    pub fn memory_stats(&self) -> MemoryStats {
        let terminal_count = self
            .surfaces
            .values()
            .filter(|surface| matches!(surface, SurfaceState::Terminal(_)))
            .count();
        MemoryStats {
            terminal_count,
            scrollback_lines: 0,
            inactive_scrollback_lines: 0,
            max_total_scrollback_bytes: 0,
            retained_scrollback_lines: 0,
            retained_scrollback_bytes: 0,
            registry_snapshot_bytes: 0,
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

    fn workspace_dump(&self, workspace: &Workspace) -> WorkspaceDump {
        WorkspaceDump {
            id: workspace.id,
            title: workspace.title.clone(),
            active_tab: workspace.active_tab,
            tabs: workspace
                .tabs
                .iter()
                .filter_map(|tab_id| self.tabs.get(tab_id))
                .map(|tab| self.tab_dump(tab))
                .collect(),
        }
    }

    fn tab_dump(&self, tab: &Tab) -> TabDump {
        TabDump {
            id: tab.id,
            title: tab.title.clone(),
            title_override: tab.title_override.clone(),
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
                    terminal: None,
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

#[cfg(test)]
mod tests {
    use super::*;
    use crate::ids::SessionId;

    fn model_with_terminal_pane() -> (ApplicationModel, PaneId, TerminalId, TabId, WorkspaceId) {
        let mut model = ApplicationModel::new();
        let workspace_id = WorkspaceId::new(1);
        let tab_id = TabId::new(2);
        let pane_id = PaneId::new(3);
        let surface_id = SurfaceId::new(4);
        let terminal_surface_id = SurfaceId::new(7);
        let terminal_id = TerminalId::new(5);
        model.create_workspace_with_title(workspace_id, "w".to_owned());
        model
            .create_tab_with_title_mode(tab_id, "t".to_owned(), false, pane_id, surface_id)
            .unwrap();
        model
            .replace_surface(
                pane_id,
                terminal_surface_id,
                SurfaceState::Terminal(TerminalSurfaceState {
                    terminal_id,
                    session_id: SessionId::new(6),
                    program: "/bin/zsh".to_owned(),
                    title: None,
                    process_name: "zsh".to_owned(),
                    cwd: "/tmp".to_owned(),
                    args: vec!["-l".to_owned()],
                    status: TerminalStatus::Running,
                    columns: 80,
                    lines: 24,
                    last_output_revision: 0,
                    agent: None,
                }),
            )
            .unwrap();
        (model, pane_id, terminal_id, tab_id, workspace_id)
    }

    #[test]
    fn agent_binding_follows_process_metadata_updates() {
        let (mut model, pane_id, terminal_id, tab_id, workspace_id) = model_with_terminal_pane();

        let update = model.set_terminal_process(
            terminal_id,
            "claude".to_owned(),
            "/proj".to_owned(),
            vec!["claude".to_owned(), "--resume".to_owned()],
            true,
        );
        assert_eq!(update, Some(pane_id));
        let snapshot = model.snapshot();
        assert_eq!(snapshot.agents.len(), 1);
        let agent = &snapshot.agents[0];
        assert_eq!(agent.kind, AgentKind::ClaudeCode);
        assert_eq!(agent.label, "Claude Code");
        assert!(agent.active);
        assert_eq!(agent.pane_id, pane_id);
        assert_eq!(agent.tab_id, tab_id);
        assert_eq!(agent.workspace_id, workspace_id);
        assert_eq!(agent.terminal_id, terminal_id);
        assert_eq!(agent.cwd, "/proj");

        // An activity-only flip is a change: the sidebar indicator needs it.
        assert_eq!(
            model.set_terminal_process(
                terminal_id,
                "claude".to_owned(),
                "/proj".to_owned(),
                vec!["claude".to_owned(), "--resume".to_owned()],
                false,
            ),
            Some(pane_id)
        );
        assert!(!model.snapshot().agents[0].active);

        // The agent exiting to the shell removes the binding.
        assert_eq!(
            model.set_terminal_process(
                terminal_id,
                "zsh".to_owned(),
                "/proj".to_owned(),
                vec!["-zsh".to_owned()],
                false,
            ),
            Some(pane_id)
        );
        assert!(model.snapshot().agents.is_empty());

        // An unchanged probe reports no update at all.
        assert_eq!(
            model.set_terminal_process(
                terminal_id,
                "zsh".to_owned(),
                "/proj".to_owned(),
                vec!["-zsh".to_owned()],
                false,
            ),
            None
        );
    }

    #[test]
    fn agents_field_is_backward_compatible_in_wire_json() {
        let (model, ..) = model_with_terminal_pane();
        let json = serde_json::to_string(&model.snapshot()).unwrap();
        let roundtrip: StateDump = serde_json::from_str(&json).unwrap();
        assert!(roundtrip.agents.is_empty());
        let legacy = r#"{"state_revision": 3, "workspace": null, "workspaces": [], "active_workspace": null, "focused_pane": null}"#;
        let parsed: StateDump = serde_json::from_str(legacy).unwrap();
        assert!(parsed.agents.is_empty());
    }
}
