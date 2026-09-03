use std::path::PathBuf;

use crate::{
    app::model::{
        ApplicationModel, MemoryStats, PaneTreeDump, SplitRequest, StateDump, TerminalProjection,
    },
    config::AppConfig,
    event::{AppEvent, AppEventKind, EventBus},
    ids::{IdAllocator, OperationId, PaneId, SessionId, TerminalId, WorkspaceId},
    pane::SplitAxis,
    surface::{SurfaceKind, SurfaceState, TerminalStatus, TerminalSurfaceState},
    terminal::{
        TerminalError, TerminalLimits, TerminalManager, TerminalRegistry, TerminalSize,
        TerminalSnapshot, TerminalTheme,
    },
};

use super::{
    AppCommand, CommandError, DispatchError, FocusDirection, OperationRegistry, OperationResult,
    PaneCommand, SplitDirection, SurfaceCommand, TabCommand, TerminalCommand, WorkspaceCommand,
};
use super::{OperationSnapshot, OperationStatus};

#[derive(Debug)]
pub struct CommandDispatcher {
    model: ApplicationModel,
    ids: IdAllocator,
    next_workspace_number: usize,
    events: EventBus,
    operations: OperationRegistry,
    terminals: TerminalManager,
    shell_program: String,
    shell_args: Vec<String>,
    default_cwd: Option<PathBuf>,
    default_terminal_size: TerminalSize,
}

struct SpawnedTerminal {
    terminal_id: TerminalId,
    surface_id: crate::ids::SurfaceId,
}

impl Default for CommandDispatcher {
    fn default() -> Self {
        Self::new()
    }
}

impl CommandDispatcher {
    pub fn new() -> Self {
        Self::with_operations(OperationRegistry::new())
    }

    /// Creates a dispatcher with the effective application configuration.
    /// User settings are captured by the model thread and are used for every
    /// subsequently created default terminal.
    pub fn with_config(config: AppConfig) -> Self {
        Self::with_operations_and_config(OperationRegistry::new(), None, config)
    }

    pub(crate) fn with_operations_and_config(
        operations: OperationRegistry,
        wakeup: Option<crate::terminal::WakeupCallback>,
        config: AppConfig,
    ) -> Self {
        let config = config.normalized();
        let theme = config.theme.colors();
        Self::with_operations_and_terminal_wakeup_and_limits_and_config(
            operations,
            wakeup,
            TerminalLimits {
                scrollback_lines: config.terminal.scrollback_lines,
                inactive_scrollback_lines: config.terminal.inactive_scrollback_lines,
                max_total_scrollback_bytes: config.terminal.max_total_scrollback_bytes,
            },
            TerminalTheme::new(
                theme.terminal_foreground,
                theme.terminal_background,
                theme.cursor_background,
            ),
            config,
        )
    }

    pub fn with_scrollback_lines(scrollback_lines: usize) -> Self {
        Self::with_operations_and_limits(
            OperationRegistry::new(),
            None,
            TerminalLimits {
                scrollback_lines,
                ..TerminalLimits::default()
            },
        )
    }

    pub(crate) fn with_operations(operations: OperationRegistry) -> Self {
        Self::with_operations_and_terminal_wakeup(operations, None)
    }

    pub(crate) fn with_operations_and_terminal_wakeup(
        operations: OperationRegistry,
        wakeup: Option<crate::terminal::WakeupCallback>,
    ) -> Self {
        Self::with_operations_and_limits(operations, wakeup, TerminalLimits::default())
    }

    pub(crate) fn with_operations_and_limits(
        operations: OperationRegistry,
        wakeup: Option<crate::terminal::WakeupCallback>,
        limits: TerminalLimits,
    ) -> Self {
        Self::with_operations_and_terminal_wakeup_and_limits_and_config(
            operations,
            wakeup,
            limits,
            TerminalTheme::default(),
            AppConfig::default(),
        )
    }

    pub(crate) fn with_operations_and_terminal_wakeup_and_limits_and_config(
        operations: OperationRegistry,
        wakeup: Option<crate::terminal::WakeupCallback>,
        limits: TerminalLimits,
        theme: TerminalTheme,
        config: AppConfig,
    ) -> Self {
        let config = config.normalized();
        let default_terminal_size = TerminalSize::new(
            config.terminal.default_columns,
            config.terminal.default_lines,
        );
        let default_cwd = config.default_cwd_path();
        let shell_program = config.shell.program.clone();
        let shell_args = config.shell.args.clone();
        Self {
            model: ApplicationModel::new(),
            ids: IdAllocator::new(),
            next_workspace_number: 1,
            events: EventBus::default(),
            operations,
            terminals: TerminalManager::new_with_wakeup_and_limits(wakeup, limits, theme),
            shell_program,
            shell_args,
            default_cwd,
            default_terminal_size,
        }
    }

    pub fn model(&self) -> &ApplicationModel {
        &self.model
    }

    pub fn dispatch(&mut self, command: AppCommand) -> OperationId {
        self.pump_background_events();
        let operation_id = self.operations.begin(command.clone());
        self.operations.start(operation_id);
        let before_revision = self.model.state_revision();
        let result = self.apply(command.clone());
        let after_revision = self.model.state_revision();
        tracing::debug!(
            target: "water::command",
            operation_id = %operation_id,
            command = command.type_name(),
            before_revision,
            after_revision,
            status = ?if result.is_ok() {
                OperationStatus::Succeeded
            } else {
                OperationStatus::Failed
            },
            "command completed"
        );
        self.operations.finish(operation_id, result);
        self.sync_focused_terminal();
        operation_id
    }

    /// Propagates the model's focused pane to the terminal workers. Called
    /// after every dispatched command and background event drain so a
    /// backgrounded tab's worker can trim its scrollback tail promptly.
    fn sync_focused_terminal(&mut self) {
        let focused = self.model.focused_terminal_id();
        self.terminals.set_focused_terminal(focused);
    }

    pub fn get_operation(&self, operation_id: OperationId) -> Option<OperationSnapshot> {
        self.operations.get(operation_id)
    }

    pub fn wait_operation(
        &self,
        operation_id: OperationId,
    ) -> Result<OperationSnapshot, DispatchError> {
        self.operations.wait(operation_id).ok_or_else(|| {
            DispatchError::Command(CommandError::new(
                "OPERATION_NOT_FOUND",
                format!("operation {operation_id} does not exist"),
            ))
        })
    }

    pub fn state_dump(&self) -> StateDump {
        let mut state = self.model.snapshot();
        let registry = self.terminals.registry();
        // Every workspace projects its displayed tab with grid cells so a
        // window can select an inactive workspace without racing a separate
        // terminal query. Hidden tabs project summary metadata only; their
        // grids remain in the registry (the single source of truth) and are
        // fetched per terminal by waterctl waiters.
        for workspace in &mut state.workspaces {
            let active_tab = workspace.active_tab;
            for tab in &mut workspace.tabs {
                attach_terminal_projections(&mut tab.tree, &registry, active_tab == Some(tab.id));
            }
        }
        state.workspace = state
            .active_workspace
            .and_then(|workspace_id| {
                state
                    .workspaces
                    .iter()
                    .find(|workspace| workspace.id == workspace_id)
            })
            .cloned();
        state
    }

    pub fn memory_stats(&self) -> MemoryStats {
        let mut stats = self.model.memory_stats();
        stats.scrollback_lines = self.terminals.scrollback_lines();
        stats.inactive_scrollback_lines = self.terminals.inactive_scrollback_lines();
        stats.max_total_scrollback_bytes = self.terminals.max_total_scrollback_bytes();
        stats.retained_scrollback_lines = self.terminals.retained_scrollback_lines();
        stats.retained_scrollback_bytes = self.terminals.retained_scrollback_bytes();
        stats.registry_snapshot_bytes = self.terminals.retained_snapshot_bytes();
        stats
    }

    pub fn terminal_registry(&self) -> TerminalRegistry {
        self.terminals.registry()
    }

    pub fn terminal_snapshot(
        &self,
        terminal_id: TerminalId,
    ) -> Result<TerminalSnapshot, DispatchError> {
        self.terminals
            .registry()
            .snapshot(terminal_id)
            .map_err(terminal_dispatch_error)
    }

    pub fn wait_terminal_contains(
        &self,
        terminal_id: TerminalId,
        text: &str,
        timeout: std::time::Duration,
    ) -> Result<TerminalSnapshot, DispatchError> {
        self.terminals
            .registry()
            .contains_text(terminal_id, text, timeout)
            .map_err(terminal_dispatch_error)
    }

    pub fn wait_terminal_exit(
        &self,
        terminal_id: TerminalId,
        timeout: std::time::Duration,
    ) -> Result<TerminalSnapshot, DispatchError> {
        self.terminals
            .registry()
            .wait_process_exit(terminal_id, timeout)
            .map_err(terminal_dispatch_error)
    }

    /// Applies worker notifications on the model thread. The return value is
    /// true when a projected state revision changed and a fresh snapshot should
    /// be sent to the UI.
    pub fn pump_background_events(&mut self) -> bool {
        let mut changed = false;
        for event in self.terminals.drain_events() {
            match event {
                crate::terminal::TerminalManagerEvent::OutputChanged { terminal_id } => {
                    if let Ok(snapshot) =
                        self.terminals.registry().take_output_snapshot(terminal_id)
                        && self
                            .model
                            .set_terminal_output_revision(terminal_id, snapshot.revision)
                    {
                        let size = snapshot.size;
                        self.model
                            .set_terminal_size(terminal_id, size.columns, size.lines);
                        self.emit(AppEventKind::TerminalOutputChanged { terminal_id });
                        changed = true;
                    }
                }
                crate::terminal::TerminalManagerEvent::TitleChanged { terminal_id, title } => {
                    if self.model.set_terminal_title(terminal_id, title.clone()) {
                        self.emit(AppEventKind::TerminalTitleChanged { terminal_id, title });
                        changed = true;
                    }
                }
                crate::terminal::TerminalManagerEvent::ProcessChanged {
                    terminal_id,
                    process_name,
                    cwd,
                } => {
                    if let Some(pane_id) = self.model.set_terminal_process(
                        terminal_id,
                        process_name.clone(),
                        cwd.clone(),
                    ) {
                        if let Some(tab_id) = self.model.tab_id_for_pane(pane_id)
                            && self.model.update_tab_title_from_process(
                                tab_id,
                                pane_id,
                                &process_name,
                            )
                        {
                            self.emit(AppEventKind::TabRenamed { tab_id });
                        }
                        self.emit(AppEventKind::TerminalProcessChanged {
                            terminal_id,
                            process_name,
                            cwd,
                        });
                        changed = true;
                    }
                }
                crate::terminal::TerminalManagerEvent::Exited { terminal_id, code } => {
                    if self
                        .model
                        .set_terminal_status(terminal_id, TerminalStatus::Exited { code })
                    {
                        self.emit(AppEventKind::TerminalExited {
                            terminal_id,
                            exit_code: code,
                        });
                        changed = true;

                        self.close_exited_terminal_pane(terminal_id);
                    }
                }
            }
        }
        // Auto-close and exit handling can move focus away from a pane, so
        // the workers must observe the new focused terminal before the next
        // state dump is projected.
        self.sync_focused_terminal();
        changed
    }

    fn close_exited_terminal_pane(&mut self, terminal_id: TerminalId) {
        let Some(workspace_id) = self.model.workspace_id_for_terminal(terminal_id) else {
            return;
        };
        let Some(pane_id) = self.model.pane_id_for_terminal(terminal_id) else {
            return;
        };
        let Some(tab_id) = self.model.tab_id_for_pane(pane_id) else {
            return;
        };
        let Some(pane_ids) = self.model.pane_ids_in_tab(tab_id) else {
            return;
        };

        if pane_ids.len() == 1 {
            let was_active_tab =
                self.model.active_tab_id_for_workspace(workspace_id) == Some(tab_id);
            let terminal_ids: Vec<_> = pane_ids
                .iter()
                .filter_map(|pane_id| self.model.terminal_id_for_pane(*pane_id))
                .collect();
            if let Err(message) = self.model.close_tab(tab_id) {
                tracing::warn!(
                    target: "water::command",
                    terminal_id = %terminal_id,
                    pane_id = %pane_id,
                    tab_id = %tab_id,
                    %message,
                    "could not close tab after terminal exit"
                );
                return;
            }
            for exited_terminal_id in terminal_ids {
                if exited_terminal_id == terminal_id {
                    self.terminals.retire(exited_terminal_id);
                } else {
                    self.terminals.remove(exited_terminal_id);
                }
            }
            for pane_id in pane_ids {
                self.emit(AppEventKind::PaneClosed { pane_id });
            }
            self.emit(AppEventKind::TabClosed { tab_id });
            if was_active_tab
                && let Some(tab_id) = self.model.active_tab_id_for_workspace(workspace_id)
            {
                self.emit(AppEventKind::TabActivated { tab_id });
            }
            return;
        }

        let was_focused = self.model.active_pane_for_workspace(workspace_id) == Some(pane_id);
        match self.model.close_pane(tab_id, pane_id) {
            Ok(_) => {
                self.terminals.retire(terminal_id);
                if was_focused
                    && let Some(pane_id) = self.model.active_pane_for_workspace(workspace_id)
                {
                    self.emit(AppEventKind::PaneFocused { pane_id });
                }
                if self.model.refresh_tab_title_from_active_pane(tab_id) {
                    self.emit(AppEventKind::TabRenamed { tab_id });
                }
                self.emit(AppEventKind::PaneClosed { pane_id });
            }
            Err(message) => {
                tracing::warn!(
                    target: "water::command",
                    terminal_id = %terminal_id,
                    pane_id = %pane_id,
                    %message,
                    "could not close pane after terminal exit"
                );
            }
        }
    }

    pub fn events_since(&self, sequence: u64) -> Vec<AppEvent> {
        self.events.since(sequence)
    }

    pub fn all_events(&self) -> Vec<AppEvent> {
        self.events.all()
    }

    pub fn latest_event_sequence(&self) -> u64 {
        self.events.latest_sequence()
    }

    fn apply(&mut self, command: AppCommand) -> Result<OperationResult, CommandError> {
        match command {
            AppCommand::Workspace(command) => self.apply_workspace(command),
            AppCommand::Tab(command) => self.apply_tab(command),
            AppCommand::Pane(command) => self.apply_pane(command),
            AppCommand::Surface(command) => self.apply_surface(command),
            AppCommand::Terminal(command) => self.apply_terminal(command),
        }
    }

    fn apply_workspace(
        &mut self,
        command: WorkspaceCommand,
    ) -> Result<OperationResult, CommandError> {
        match command {
            WorkspaceCommand::Create => self.create_workspace(),
            WorkspaceCommand::New => self.create_workspace_with_terminal(),
            WorkspaceCommand::Ensure => {
                if let Some(workspace) = self.model.workspace() {
                    return Ok(OperationResult::WorkspaceCreated {
                        workspace_id: workspace.id,
                    });
                }
                self.create_workspace()
            }
            WorkspaceCommand::Close { workspace_id }
            | WorkspaceCommand::Delete { workspace_id } => {
                let workspace_id = self.resolve_workspace(workspace_id)?;
                let was_active = self.model.active_workspace_id() == Some(workspace_id);
                let tab_ids = self
                    .model
                    .workspace_by_id(workspace_id)
                    .map(|workspace| workspace.tabs.clone())
                    .unwrap_or_default();
                let pane_ids: Vec<_> = tab_ids
                    .iter()
                    .flat_map(|tab_id| self.model.pane_ids_in_tab(*tab_id).unwrap_or_default())
                    .collect();
                let terminal_ids: Vec<_> = pane_ids
                    .iter()
                    .filter_map(|pane_id| self.model.terminal_id_for_pane(*pane_id))
                    .collect();
                let workspace = self
                    .model
                    .close_workspace(workspace_id)
                    .map_err(|message| CommandError::new("WORKSPACE_NOT_FOUND", message))?;
                for terminal_id in terminal_ids {
                    self.terminals.remove(terminal_id);
                }
                for pane_id in pane_ids {
                    self.emit(AppEventKind::PaneClosed { pane_id });
                }
                for tab_id in tab_ids {
                    self.emit(AppEventKind::TabClosed { tab_id });
                }
                self.emit(AppEventKind::WorkspaceClosed { workspace_id });
                if was_active && let Some(workspace_id) = self.model.active_workspace_id() {
                    self.emit(AppEventKind::WorkspaceActivated { workspace_id });
                }
                Ok(OperationResult::WorkspaceClosed {
                    workspace_id: workspace.id,
                })
            }
            WorkspaceCommand::Activate { workspace_id } => {
                let workspace_id = self.resolve_workspace(workspace_id)?;
                let changed = self
                    .model
                    .activate_workspace(workspace_id)
                    .map_err(|message| CommandError::new("WORKSPACE_NOT_FOUND", message))?;
                if changed {
                    self.emit(AppEventKind::WorkspaceActivated { workspace_id });
                }
                Ok(OperationResult::WorkspaceActivated { workspace_id })
            }
            WorkspaceCommand::Rename {
                workspace_id,
                title,
            } => {
                let title = normalize_title(title)?;
                let workspace_id = self.resolve_workspace(workspace_id)?;
                let changed = self
                    .model
                    .rename_workspace(workspace_id, title)
                    .map_err(|message| CommandError::new("WORKSPACE_NOT_FOUND", message))?;
                if changed {
                    self.emit(AppEventKind::WorkspaceRenamed { workspace_id });
                }
                Ok(OperationResult::WorkspaceRenamed { workspace_id })
            }
        }
    }

    fn create_workspace(&mut self) -> Result<OperationResult, CommandError> {
        let workspace_id = self.ids.alloc();
        let title = format!("Workspace {}", self.next_workspace_number);
        self.next_workspace_number = self.next_workspace_number.saturating_add(1);
        if !self.model.create_workspace_with_title(workspace_id, title) {
            return Err(CommandError::new(
                "WORKSPACE_CREATE_FAILED",
                "workspace ID already exists",
            ));
        }
        self.emit(AppEventKind::WorkspaceCreated { workspace_id });
        Ok(OperationResult::WorkspaceCreated { workspace_id })
    }

    /// Creates the user-facing workspace shape atomically: one workspace,
    /// one tab, one pane, and one configured shell terminal. No lifecycle
    /// events are emitted until the terminal has been installed successfully,
    /// so a spawn failure cannot leave a misleading partial history.
    fn create_workspace_with_terminal(&mut self) -> Result<OperationResult, CommandError> {
        let previous_active_workspace = self.model.active_workspace_id();
        let workspace_id = self.ids.alloc();
        let title = format!("Workspace {}", self.next_workspace_number);
        self.next_workspace_number = self.next_workspace_number.saturating_add(1);
        if !self.model.create_workspace_with_title(workspace_id, title) {
            return Err(CommandError::new(
                "WORKSPACE_CREATE_FAILED",
                "workspace ID already exists",
            ));
        }

        let program = self.shell_program.clone();
        let args = self.shell_args.clone();
        let tab_id = self.ids.alloc();
        let pane_id = self.ids.alloc();
        let empty_surface_id = self.ids.alloc();
        let title = default_process_name(&program);
        let inherited_cwd = self.current_working_directory_for_workspace(workspace_id);
        if let Err(message) = self.model.create_tab_with_title_mode_in_workspace(
            workspace_id,
            tab_id,
            title,
            false,
            pane_id,
            empty_surface_id,
        ) {
            self.rollback_workspace_creation(workspace_id, previous_active_workspace);
            return Err(CommandError::new("TAB_CREATE_FAILED", message));
        }

        let terminal = match self.spawn_terminal_for_pane(
            pane_id,
            program,
            args,
            self.default_terminal_size,
            inherited_cwd,
        ) {
            Ok(terminal) => terminal,
            Err(error) => {
                let _ = self.model.close_tab(tab_id);
                self.rollback_workspace_creation(workspace_id, previous_active_workspace);
                return Err(error);
            }
        };

        self.emit(AppEventKind::WorkspaceCreated { workspace_id });
        self.emit(AppEventKind::TabCreated { tab_id });
        self.emit(AppEventKind::PaneCreated { pane_id });
        self.emit(AppEventKind::SurfaceChanged {
            pane_id,
            surface_id: terminal.surface_id,
        });
        self.emit(AppEventKind::TerminalSpawned {
            terminal_id: terminal.terminal_id,
            pane_id,
        });
        // The model creation above made this workspace active and no other
        // command can interleave on the model thread, so this is an activated
        // workspace by construction.
        Ok(OperationResult::WorkspaceCreated { workspace_id })
    }

    fn rollback_workspace_creation(
        &mut self,
        workspace_id: WorkspaceId,
        previous_active_workspace: Option<WorkspaceId>,
    ) {
        let _ = self.model.close_workspace(workspace_id);
        if let Some(previous_active_workspace) = previous_active_workspace
            && self
                .model
                .workspace_by_id(previous_active_workspace)
                .is_some()
        {
            let _ = self.model.activate_workspace(previous_active_workspace);
        }
    }

    fn create_tab_in_workspace(
        &mut self,
        workspace_id: WorkspaceId,
        title: Option<String>,
    ) -> Result<OperationResult, CommandError> {
        let inherited_cwd = self.current_working_directory_for_workspace(workspace_id);
        let program = self.shell_program.clone();
        let args = self.shell_args.clone();
        let title_is_pinned = title.is_some();
        let title = title
            .map(normalize_title)
            .transpose()?
            .unwrap_or_else(|| default_process_name(&program));
        let tab_id = self.ids.alloc();
        let pane_id = self.ids.alloc();
        let empty_surface_id = self.ids.alloc();
        let create_result = if self.model.active_workspace_id() == Some(workspace_id) {
            self.model.create_tab_with_title_mode(
                tab_id,
                title,
                title_is_pinned,
                pane_id,
                empty_surface_id,
            )
        } else {
            self.model.create_tab_with_title_mode_in_workspace(
                workspace_id,
                tab_id,
                title,
                title_is_pinned,
                pane_id,
                empty_surface_id,
            )
        };
        create_result.map_err(|message| CommandError::new("TAB_CREATE_FAILED", message))?;

        let terminal = match self.spawn_terminal_for_pane(
            pane_id,
            program,
            args,
            self.default_terminal_size,
            inherited_cwd,
        ) {
            Ok(terminal) => terminal,
            Err(error) => {
                let _ = self.model.close_tab(tab_id);
                return Err(error);
            }
        };
        self.emit(AppEventKind::TabCreated { tab_id });
        self.emit(AppEventKind::PaneCreated { pane_id });
        self.emit(AppEventKind::SurfaceChanged {
            pane_id,
            surface_id: terminal.surface_id,
        });
        self.emit(AppEventKind::TerminalSpawned {
            terminal_id: terminal.terminal_id,
            pane_id,
        });
        Ok(OperationResult::TabCreated { tab_id })
    }

    fn apply_tab(&mut self, command: TabCommand) -> Result<OperationResult, CommandError> {
        match command {
            TabCommand::New { title } => {
                let workspace_id = self.resolve_workspace(None)?;
                self.create_tab_in_workspace(workspace_id, title)
            }
            TabCommand::NewInWorkspace {
                workspace_id,
                title,
            } => {
                let workspace_id = self.resolve_workspace(Some(workspace_id))?;
                self.create_tab_in_workspace(workspace_id, title)
            }
            TabCommand::Rename { tab_id, title } => {
                let tab_id = self.resolve_tab(tab_id, None)?;
                let title = normalize_title(title)?;
                let changed = self
                    .model
                    .rename_tab(tab_id, title)
                    .map_err(|message| CommandError::new("TAB_NOT_FOUND", message))?;
                if changed {
                    self.emit(AppEventKind::TabRenamed { tab_id });
                }
                Ok(OperationResult::TabRenamed { tab_id })
            }
            TabCommand::Close { tab_id } => {
                let tab_id = self.resolve_tab(tab_id, None)?;
                let was_active = self.model.active_tab_id() == Some(tab_id);
                let pane_ids = self.model.pane_ids_in_tab(tab_id).ok_or_else(|| {
                    CommandError::new("TAB_NOT_FOUND", format!("tab {tab_id} does not exist"))
                })?;
                let terminal_ids: Vec<_> = pane_ids
                    .iter()
                    .filter_map(|pane_id| self.model.terminal_id_for_pane(*pane_id))
                    .collect();
                self.model
                    .close_tab(tab_id)
                    .map_err(|message| CommandError::new("TAB_NOT_FOUND", message))?;
                for terminal_id in terminal_ids {
                    self.terminals.remove(terminal_id);
                }
                for pane_id in pane_ids {
                    self.emit(AppEventKind::PaneClosed { pane_id });
                }
                self.emit(AppEventKind::TabClosed { tab_id });
                if was_active && let Some(tab_id) = self.model.active_tab_id() {
                    self.emit(AppEventKind::TabActivated { tab_id });
                }
                Ok(OperationResult::TabClosed { tab_id })
            }
            TabCommand::Activate { tab_id, index } => {
                let tab_id = self.resolve_tab(tab_id, index)?;
                let changed = self
                    .model
                    .activate_tab(tab_id)
                    .map_err(|message| CommandError::new("TAB_NOT_FOUND", message))?;
                if changed {
                    self.emit(AppEventKind::TabActivated { tab_id });
                }
                Ok(OperationResult::TabActivated { tab_id })
            }
        }
    }

    fn apply_pane(&mut self, command: PaneCommand) -> Result<OperationResult, CommandError> {
        match command {
            PaneCommand::Split { pane_id, direction } => {
                let (tab_id, target_pane) = self.resolve_pane(pane_id)?;
                // Capture the source cwd before splitting because the model
                // makes the new pane active as part of the topology mutation.
                let inherited_cwd = self.working_directory_for_pane(target_pane);
                let (axis, new_first) = match direction {
                    SplitDirection::Left => (SplitAxis::Horizontal, true),
                    SplitDirection::Right => (SplitAxis::Horizontal, false),
                    SplitDirection::Up => (SplitAxis::Vertical, true),
                    SplitDirection::Down => (SplitAxis::Vertical, false),
                };
                let new_pane = self.ids.alloc();
                let new_surface = self.ids.alloc();
                self.model
                    .split_pane(SplitRequest {
                        tab_id,
                        target_pane,
                        axis,
                        ratio: 0.5,
                        new_pane,
                        new_surface,
                        new_first,
                    })
                    .map_err(|message| self.pane_error(message))?;
                let program = self.shell_program.clone();
                let args = self.shell_args.clone();
                let terminal = match self.spawn_terminal_for_pane(
                    new_pane,
                    program,
                    args,
                    self.default_terminal_size,
                    inherited_cwd,
                ) {
                    Ok(terminal) => terminal,
                    Err(error) => {
                        let _ = self.model.close_pane(tab_id, new_pane);
                        return Err(error);
                    }
                };
                if self.model.refresh_tab_title_from_active_pane(tab_id) {
                    self.emit(AppEventKind::TabRenamed { tab_id });
                }
                self.emit(AppEventKind::PaneCreated { pane_id: new_pane });
                self.emit(AppEventKind::PaneSplit {
                    target_pane,
                    new_pane,
                });
                self.emit(AppEventKind::SurfaceChanged {
                    pane_id: new_pane,
                    surface_id: terminal.surface_id,
                });
                self.emit(AppEventKind::TerminalSpawned {
                    terminal_id: terminal.terminal_id,
                    pane_id: new_pane,
                });
                Ok(OperationResult::PaneCreated { pane_id: new_pane })
            }
            PaneCommand::Close { pane_id } => {
                let (tab_id, pane_id) = self.resolve_pane(pane_id)?;
                let terminal_id = self.model.terminal_id_for_pane(pane_id);
                let was_active_tab = self.model.active_tab_id() == Some(tab_id);
                let was_focused_pane = self.model.active_pane() == Some(pane_id);
                let pane_ids = self.model.pane_ids_in_tab(tab_id).ok_or_else(|| {
                    CommandError::new("TAB_NOT_FOUND", format!("tab {tab_id} does not exist"))
                })?;
                if pane_ids.len() == 1 {
                    // Closing the final pane is the natural way to close its
                    // tab. The workspace remains as an empty container so the
                    // user can explicitly delete it from the sidebar.
                    self.model
                        .close_tab(tab_id)
                        .map_err(|message| self.pane_error(message))?;
                    if let Some(terminal_id) = terminal_id {
                        self.terminals.remove(terminal_id);
                    }
                    self.emit(AppEventKind::PaneClosed { pane_id });
                    self.emit(AppEventKind::TabClosed { tab_id });
                    if was_active_tab && let Some(tab_id) = self.model.active_tab_id() {
                        self.emit(AppEventKind::TabActivated { tab_id });
                    }
                    return Ok(OperationResult::PaneClosed { pane_id });
                }
                self.model
                    .close_pane(tab_id, pane_id)
                    .map_err(|message| self.pane_error(message))?;
                if let Some(terminal_id) = terminal_id {
                    self.terminals.remove(terminal_id);
                }
                if was_focused_pane && let Some(pane_id) = self.model.active_pane() {
                    self.emit(AppEventKind::PaneFocused { pane_id });
                }
                if self.model.refresh_tab_title_from_active_pane(tab_id) {
                    self.emit(AppEventKind::TabRenamed { tab_id });
                }
                self.emit(AppEventKind::PaneClosed { pane_id });
                Ok(OperationResult::PaneClosed { pane_id })
            }
            PaneCommand::Focus { pane_id, direction } => {
                let (tab_id, target_pane) = self.resolve_focus_target(pane_id, direction)?;
                let changed = self
                    .model
                    .focus_pane(tab_id, target_pane)
                    .map_err(|message| self.pane_error(message))?;
                if changed {
                    self.emit(AppEventKind::PaneFocused {
                        pane_id: target_pane,
                    });
                }
                if self.model.refresh_tab_title_from_active_pane(tab_id) {
                    self.emit(AppEventKind::TabRenamed { tab_id });
                }
                Ok(OperationResult::PaneFocused {
                    pane_id: target_pane,
                })
            }
            PaneCommand::Resize { pane_id, ratio } => {
                if !ratio.is_finite() || !(0.05..=0.95).contains(&ratio) {
                    return Err(CommandError::new(
                        "INVALID_SPLIT",
                        "split ratio must be finite and between 0.05 and 0.95",
                    ));
                }
                let (tab_id, target_pane) = self.resolve_pane(pane_id)?;
                self.model
                    .resize_pane(tab_id, target_pane, ratio)
                    .map_err(|message| self.pane_error(message))?;
                self.emit(AppEventKind::PaneResized {
                    pane_id: target_pane,
                    ratio,
                });
                Ok(OperationResult::PaneResized {
                    pane_id: target_pane,
                    ratio,
                })
            }
        }
    }

    fn apply_surface(&mut self, command: SurfaceCommand) -> Result<OperationResult, CommandError> {
        match command {
            SurfaceCommand::Replace { pane_id, kind } => {
                if kind != SurfaceKind::Empty {
                    return Err(CommandError::new(
                        "SURFACE_NOT_AVAILABLE",
                        "only EmptySurface is implemented in Phase 1",
                    ));
                }
                let (_, pane_id) = self.resolve_pane(pane_id)?;
                let old_terminal_id = self.model.terminal_id_for_pane(pane_id);
                let surface_id = self.ids.alloc();
                self.model
                    .replace_surface(pane_id, surface_id, SurfaceState::empty())
                    .map_err(|message| self.pane_error(message))?;
                if let Some(terminal_id) = old_terminal_id {
                    self.terminals.remove(terminal_id);
                }
                self.emit(AppEventKind::SurfaceChanged {
                    pane_id,
                    surface_id,
                });
                Ok(OperationResult::SurfaceReplaced { surface_id })
            }
        }
    }

    fn spawn_terminal_for_pane(
        &mut self,
        pane_id: PaneId,
        program: String,
        args: Vec<String>,
        size: TerminalSize,
        working_directory: Option<PathBuf>,
    ) -> Result<SpawnedTerminal, CommandError> {
        let old_terminal_id = self.model.terminal_id_for_pane(pane_id);
        let terminal_id: TerminalId = self.ids.alloc();
        let session_id: SessionId = self.ids.alloc();
        let surface_id = self.ids.alloc();
        let process_name = default_process_name(&program);
        let working_directory = working_directory
            .or_else(|| self.default_cwd.clone())
            .filter(|path| path.is_dir());
        let cwd = working_directory
            .clone()
            .or_else(|| std::env::current_dir().ok())
            .unwrap_or_else(|| PathBuf::from("."));
        self.terminals
            .spawn_with_working_directory(
                terminal_id,
                program.clone(),
                args.clone(),
                size,
                working_directory,
            )
            .map_err(terminal_command_error)?;
        let terminal_surface = SurfaceState::Terminal(TerminalSurfaceState {
            terminal_id,
            session_id,
            program,
            title: None,
            process_name,
            cwd: cwd.display().to_string(),
            args,
            status: TerminalStatus::Running,
            columns: size.columns,
            lines: size.lines,
            last_output_revision: 0,
        });
        if let Err(message) = self
            .model
            .replace_surface(pane_id, surface_id, terminal_surface)
        {
            self.terminals.remove(terminal_id);
            return Err(self.pane_error(message));
        }
        if let Some(old_terminal_id) = old_terminal_id {
            self.terminals.remove(old_terminal_id);
        }
        Ok(SpawnedTerminal {
            terminal_id,
            surface_id,
        })
    }

    fn apply_terminal(
        &mut self,
        command: TerminalCommand,
    ) -> Result<OperationResult, CommandError> {
        match command {
            TerminalCommand::Spawn {
                pane_id,
                program,
                args,
                columns,
                lines,
            } => {
                let (tab_id, pane_id) = self.resolve_pane(pane_id)?;
                let inherited_cwd = self.working_directory_for_pane(pane_id);
                let size = TerminalSize::new(columns, lines);
                let terminal =
                    self.spawn_terminal_for_pane(pane_id, program, args, size, inherited_cwd)?;
                if self.model.refresh_tab_title_from_active_pane(tab_id) {
                    self.emit(AppEventKind::TabRenamed { tab_id });
                }
                self.emit(AppEventKind::SurfaceChanged {
                    pane_id,
                    surface_id: terminal.surface_id,
                });
                self.emit(AppEventKind::TerminalSpawned {
                    terminal_id: terminal.terminal_id,
                    pane_id,
                });
                Ok(OperationResult::TerminalSpawned {
                    terminal_id: terminal.terminal_id,
                })
            }
            TerminalCommand::SendText {
                terminal_id,
                pane_id,
                text,
            } => {
                let terminal_id = self.resolve_terminal_target(terminal_id, pane_id)?;
                self.terminals
                    .send_text(terminal_id, text)
                    .map_err(terminal_command_error)?;
                Ok(OperationResult::TerminalTextSent { terminal_id })
            }
            TerminalCommand::SendBytes {
                terminal_id,
                pane_id,
                bytes,
            } => {
                let terminal_id = self.resolve_terminal_target(terminal_id, pane_id)?;
                self.terminals
                    .send_bytes(terminal_id, bytes)
                    .map_err(terminal_command_error)?;
                Ok(OperationResult::TerminalBytesSent { terminal_id })
            }
            TerminalCommand::Resize {
                terminal_id,
                pane_id,
                columns,
                lines,
            } => {
                let terminal_id = self.resolve_terminal_target(terminal_id, pane_id)?;
                let size = TerminalSize::new(columns, lines);
                self.terminals
                    .resize(terminal_id, size)
                    .map_err(terminal_command_error)?;
                self.model
                    .set_terminal_size(terminal_id, size.columns, size.lines);
                self.emit(AppEventKind::TerminalResized {
                    terminal_id,
                    columns: size.columns,
                    lines: size.lines,
                });
                Ok(OperationResult::TerminalResized {
                    terminal_id,
                    columns: size.columns,
                    lines: size.lines,
                })
            }
            TerminalCommand::Scroll {
                terminal_id,
                pane_id,
                lines,
            } => {
                let terminal_id = self.resolve_terminal_target(terminal_id, pane_id)?;
                self.terminals
                    .scroll(terminal_id, lines)
                    .map_err(terminal_command_error)?;
                Ok(OperationResult::TerminalScrolled { terminal_id, lines })
            }
        }
    }

    fn resolve_workspace(
        &self,
        workspace_id: Option<crate::ids::WorkspaceId>,
    ) -> Result<crate::ids::WorkspaceId, CommandError> {
        let workspace_id = workspace_id
            .or_else(|| self.model.active_workspace_id())
            .ok_or_else(|| {
                CommandError::new("WORKSPACE_NOT_FOUND", "there is no active workspace")
            })?;
        if self.model.workspace_by_id(workspace_id).is_some() {
            Ok(workspace_id)
        } else {
            Err(CommandError::new(
                "WORKSPACE_NOT_FOUND",
                format!("workspace {workspace_id} does not exist"),
            ))
        }
    }

    fn resolve_tab(
        &self,
        tab_id: Option<crate::ids::TabId>,
        index: Option<usize>,
    ) -> Result<crate::ids::TabId, CommandError> {
        if let Some(tab_id) = tab_id {
            if self.model.tab(tab_id).is_some() {
                return Ok(tab_id);
            }
            return Err(CommandError::new(
                "TAB_NOT_FOUND",
                format!("tab {tab_id} does not exist"),
            ));
        }
        if let Some(index) = index {
            return self
                .model
                .workspace()
                .and_then(|workspace| workspace.tabs.get(index).copied())
                .ok_or_else(|| {
                    CommandError::new("TAB_NOT_FOUND", format!("tab index {index} does not exist"))
                });
        }
        self.model
            .active_tab_id()
            .ok_or_else(|| CommandError::new("TAB_NOT_FOUND", "there is no active tab"))
    }

    fn resolve_pane(
        &self,
        pane_id: Option<PaneId>,
    ) -> Result<(crate::ids::TabId, PaneId), CommandError> {
        let pane_id = match pane_id {
            Some(pane_id) => pane_id,
            None => self
                .model
                .active_pane()
                .ok_or_else(|| CommandError::new("PANE_NOT_FOUND", "there is no active pane"))?,
        };
        let tab_id = self.model.tab_id_for_pane(pane_id).ok_or_else(|| {
            CommandError::new("PANE_NOT_FOUND", format!("pane {pane_id} does not exist"))
        })?;
        Ok((tab_id, pane_id))
    }

    fn resolve_terminal_target(
        &self,
        terminal_id: Option<TerminalId>,
        pane_id: Option<PaneId>,
    ) -> Result<TerminalId, CommandError> {
        if let Some(terminal_id) = terminal_id {
            if self.model.terminal_surface(terminal_id).is_some() {
                return Ok(terminal_id);
            }
            return Err(CommandError::new(
                "TERMINAL_NOT_FOUND",
                format!("terminal {terminal_id} does not exist"),
            ));
        }
        let pane_id = pane_id
            .or_else(|| self.model.active_pane())
            .ok_or_else(|| CommandError::new("PANE_NOT_FOUND", "there is no active pane"))?;
        self.model.terminal_id_for_pane(pane_id).ok_or_else(|| {
            CommandError::new(
                "TERMINAL_NOT_FOUND",
                format!("pane {pane_id} does not contain a terminal"),
            )
        })
    }

    fn resolve_focus_target(
        &self,
        pane_id: Option<PaneId>,
        direction: Option<FocusDirection>,
    ) -> Result<(crate::ids::TabId, PaneId), CommandError> {
        let (tab_id, current) = if let Some(pane_id) = pane_id {
            let (tab_id, pane_id) = self.resolve_pane(Some(pane_id))?;
            (tab_id, pane_id)
        } else {
            let tab_id = self
                .model
                .active_tab_id()
                .ok_or_else(|| CommandError::new("TAB_NOT_FOUND", "there is no active tab"))?;
            let current = self
                .model
                .active_pane()
                .ok_or_else(|| CommandError::new("PANE_NOT_FOUND", "there is no active pane"))?;
            (tab_id, current)
        };
        let Some(direction) = direction else {
            return Ok((tab_id, current));
        };
        let direction = match direction {
            FocusDirection::Left => crate::pane::PaneDirection::Left,
            FocusDirection::Right => crate::pane::PaneDirection::Right,
            FocusDirection::Up => crate::pane::PaneDirection::Up,
            FocusDirection::Down => crate::pane::PaneDirection::Down,
        };
        let target = self
            .model
            .directional_pane(tab_id, current, direction)
            .ok_or_else(|| {
                CommandError::new(
                    "FOCUS_TARGET_NOT_FOUND",
                    format!("no pane is available in the {direction:?} direction"),
                )
            })?;
        Ok((tab_id, target))
    }

    fn pane_error(&self, message: &str) -> CommandError {
        let code = if message.contains("final pane") {
            "FINAL_PANE"
        } else if message.contains("split") {
            "INVALID_SPLIT"
        } else if message.contains("tab") {
            "TAB_NOT_FOUND"
        } else {
            "PANE_NOT_FOUND"
        };
        CommandError::new(code, message)
    }

    fn emit(&mut self, kind: AppEventKind) {
        let state_revision = self.model.bump_revision();
        let event = self.events.emit(state_revision, kind);
        tracing::trace!(
            target: "water::event",
            sequence = event.sequence,
            state_revision = event.state_revision,
            "application event"
        );
    }
    fn current_working_directory_for_workspace(
        &self,
        workspace_id: WorkspaceId,
    ) -> Option<PathBuf> {
        self.model
            .active_pane_for_workspace(workspace_id)
            .and_then(|pane_id| self.working_directory_for_pane(pane_id))
    }

    fn working_directory_for_pane(&self, pane_id: PaneId) -> Option<PathBuf> {
        self.model
            .terminal_id_for_pane(pane_id)
            .and_then(|terminal_id| self.model.terminal_cwd(terminal_id))
            .map(PathBuf::from)
            .filter(|path| path.is_dir())
            .or_else(|| self.default_cwd.clone().filter(|path| path.is_dir()))
            .or_else(|| std::env::current_dir().ok())
    }
}

fn normalize_title(title: String) -> Result<String, CommandError> {
    let title = title.trim().to_owned();
    if title.is_empty() {
        return Err(CommandError::new(
            "INVALID_TITLE",
            "title must not be empty",
        ));
    }
    Ok(title)
}

fn default_process_name(program: &str) -> String {
    std::path::Path::new(program)
        .file_name()
        .and_then(|name| name.to_str())
        .filter(|name| !name.is_empty())
        .unwrap_or("shell")
        .trim_start_matches('-')
        .to_owned()
}

fn terminal_dispatch_error(error: TerminalError) -> DispatchError {
    DispatchError::Command(terminal_command_error(error))
}

fn terminal_command_error(error: TerminalError) -> CommandError {
    let message = error.to_string();
    let code = match &error {
        TerminalError::SpawnFailed(_) => "TERMINAL_SPAWN_FAILED",
        TerminalError::NotFound(_) => "TERMINAL_NOT_FOUND",
        TerminalError::NotRunning(_) | TerminalError::WorkerClosed(_) => "TERMINAL_NOT_RUNNING",
        TerminalError::Timeout(_) => "TERMINAL_TIMEOUT",
        TerminalError::ProcessExited(_) => "TERMINAL_EXITED",
    };
    CommandError::new(code, message)
}

fn attach_terminal_projections(
    tree: &mut PaneTreeDump,
    registry: &TerminalRegistry,
    include_grid: bool,
) {
    match tree {
        PaneTreeDump::Leaf {
            surface_state,
            terminal,
            ..
        } => {
            *terminal = match surface_state {
                SurfaceState::Terminal(terminal) => registry
                    .snapshot_arc(terminal.terminal_id)
                    .ok()
                    .map(|snapshot| {
                        Box::new(TerminalProjection {
                            summary: snapshot.summary(),
                            snapshot: include_grid.then_some(snapshot),
                        })
                    }),
                SurfaceState::Empty(_) => None,
            };
        }
        PaneTreeDump::Split { first, second, .. } => {
            attach_terminal_projections(first, registry, include_grid);
            attach_terminal_projections(second, registry, include_grid);
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::command::{PaneCommand, TabCommand};

    fn create_dispatcher() -> CommandDispatcher {
        let mut dispatcher = CommandDispatcher::new();
        let operation = dispatcher.dispatch(AppCommand::Workspace(WorkspaceCommand::Create));
        let snapshot = dispatcher.wait_operation(operation).unwrap();
        assert_eq!(snapshot.status, OperationStatus::Succeeded);
        dispatcher
    }

    #[test]
    fn configured_shell_cwd_and_terminal_size_are_used_for_new_tabs() {
        let mut config = AppConfig::default();
        config.startup.default_cwd = Some("/tmp".to_owned());
        config.shell.program = "/bin/sh".to_owned();
        config.shell.args = vec!["-c".to_owned(), "exec sleep 30".to_owned()];
        config.terminal.default_columns = 40;
        config.terminal.default_lines = 8;
        let mut dispatcher = CommandDispatcher::with_config(config);
        let workspace = dispatcher.dispatch(AppCommand::Workspace(WorkspaceCommand::Create));
        assert_eq!(
            dispatcher.wait_operation(workspace).unwrap().status,
            OperationStatus::Succeeded
        );
        let tab = dispatcher.dispatch(AppCommand::Tab(TabCommand::New { title: None }));
        assert_eq!(
            dispatcher.wait_operation(tab).unwrap().status,
            OperationStatus::Succeeded
        );

        let state = dispatcher.state_dump();
        let tree = &state.workspace.as_ref().unwrap().tabs[0].tree;
        let PaneTreeDump::Leaf {
            surface_state: SurfaceState::Terminal(terminal),
            ..
        } = tree
        else {
            panic!("configured tab did not contain a terminal: {tree:?}");
        };
        assert_eq!(terminal.program, "/bin/sh");
        assert_eq!(terminal.args, ["-c", "exec sleep 30"]);
        assert_eq!(terminal.cwd, "/tmp");
        assert_eq!(terminal.columns, 40);
        assert_eq!(terminal.lines, 8);
    }

    #[test]
    fn new_tab_split_focus_and_close_use_stable_ids() {
        let mut dispatcher = create_dispatcher();
        let tab_operation = dispatcher.dispatch(AppCommand::Tab(TabCommand::New {
            title: Some("Main".to_owned()),
        }));
        let tab_result = dispatcher.wait_operation(tab_operation).unwrap();
        let tab_id = match tab_result.result.unwrap() {
            OperationResult::TabCreated { tab_id } => tab_id,
            result => panic!("unexpected result: {result:?}"),
        };
        assert_eq!(
            dispatcher
                .state_dump()
                .workspace
                .as_ref()
                .unwrap()
                .tabs
                .len(),
            1
        );

        let split_operation = dispatcher.dispatch(AppCommand::Pane(PaneCommand::Split {
            pane_id: None,
            direction: SplitDirection::Right,
        }));
        let split_result = dispatcher.wait_operation(split_operation).unwrap();
        let new_pane = match split_result.result.unwrap() {
            OperationResult::PaneCreated { pane_id } => pane_id,
            result => panic!("unexpected result: {result:?}"),
        };
        assert_ne!(new_pane.get(), 0);
        let state = dispatcher.state_dump();
        let tab = state
            .workspace
            .unwrap()
            .tabs
            .into_iter()
            .find(|tab| tab.id == tab_id)
            .unwrap();
        assert_eq!(tab.tree.pane_count(), 2);
        assert_eq!(tab.active_pane, new_pane);

        let focus_operation = dispatcher.dispatch(AppCommand::Pane(PaneCommand::Focus {
            pane_id: None,
            direction: Some(FocusDirection::Left),
        }));
        let focus_result = dispatcher.wait_operation(focus_operation).unwrap();
        assert_eq!(focus_result.status, OperationStatus::Succeeded);

        let close_operation = dispatcher.dispatch(AppCommand::Pane(PaneCommand::Close {
            pane_id: Some(new_pane),
        }));
        assert_eq!(
            dispatcher.wait_operation(close_operation).unwrap().status,
            OperationStatus::Succeeded
        );
        let state = dispatcher.state_dump();
        assert_eq!(state.workspace.unwrap().tabs[0].tree.pane_count(), 1);
    }

    #[test]
    fn explicit_focus_source_pane_targets_its_workspace() {
        let mut dispatcher = create_dispatcher();
        let tab_operation = dispatcher.dispatch(AppCommand::Tab(TabCommand::New { title: None }));
        let tab_result = dispatcher.wait_operation(tab_operation).unwrap();
        assert_eq!(tab_result.status, OperationStatus::Succeeded);
        let first_state = dispatcher.state_dump();
        let first_workspace = first_state.workspace.as_ref().unwrap().id;
        let first_pane = first_state.workspace.as_ref().unwrap().tabs[0].active_pane;

        let split_operation = dispatcher.dispatch(AppCommand::Pane(PaneCommand::Split {
            pane_id: Some(first_pane),
            direction: SplitDirection::Right,
        }));
        let split_result = dispatcher.wait_operation(split_operation).unwrap();
        let right_pane = match split_result.result.unwrap() {
            OperationResult::PaneCreated { pane_id } => pane_id,
            result => panic!("unexpected result: {result:?}"),
        };

        let second_workspace_operation =
            dispatcher.dispatch(AppCommand::Workspace(WorkspaceCommand::Create));
        assert_eq!(
            dispatcher
                .wait_operation(second_workspace_operation)
                .unwrap()
                .status,
            OperationStatus::Succeeded
        );
        let second_tab_operation =
            dispatcher.dispatch(AppCommand::Tab(TabCommand::New { title: None }));
        assert_eq!(
            dispatcher
                .wait_operation(second_tab_operation)
                .unwrap()
                .status,
            OperationStatus::Succeeded
        );
        assert_ne!(
            dispatcher.state_dump().active_workspace,
            Some(first_workspace)
        );

        let focus_operation = dispatcher.dispatch(AppCommand::Pane(PaneCommand::Focus {
            pane_id: Some(first_pane),
            direction: Some(FocusDirection::Right),
        }));
        let focus_result = dispatcher.wait_operation(focus_operation).unwrap();
        assert_eq!(focus_result.status, OperationStatus::Succeeded);
        assert_eq!(
            focus_result.result,
            Some(OperationResult::PaneFocused {
                pane_id: right_pane
            })
        );
        let state = dispatcher.state_dump();
        assert_eq!(state.active_workspace, Some(first_workspace));
        assert_eq!(state.focused_pane, Some(right_pane));
    }

    #[test]
    fn failed_commands_are_observable_operations() {
        let mut dispatcher = create_dispatcher();
        let operation = dispatcher.dispatch(AppCommand::Pane(PaneCommand::Close { pane_id: None }));
        let snapshot = dispatcher.wait_operation(operation).unwrap();
        assert_eq!(snapshot.status, OperationStatus::Failed);
        assert_eq!(snapshot.error.unwrap().code, "PANE_NOT_FOUND");
        assert_eq!(dispatcher.model().state_revision(), 1);
    }

    #[test]
    fn events_have_strict_sequences_and_revisions() {
        let mut dispatcher = create_dispatcher();
        let operation = dispatcher.dispatch(AppCommand::Tab(TabCommand::New { title: None }));
        dispatcher.wait_operation(operation).unwrap();
        let events = dispatcher.all_events();
        assert!(
            events
                .windows(2)
                .all(|pair| pair[0].sequence < pair[1].sequence)
        );
        assert!(
            events
                .windows(2)
                .all(|pair| pair[0].state_revision < pair[1].state_revision)
        );
        assert_eq!(
            events.last().unwrap().state_revision,
            dispatcher.model().state_revision()
        );
    }
}
