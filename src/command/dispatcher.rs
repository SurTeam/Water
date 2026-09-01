use crate::{
    app::model::{ApplicationModel, MemoryStats, PaneTreeDump, SplitRequest, StateDump},
    event::{AppEvent, AppEventKind, EventBus},
    ids::{IdAllocator, OperationId, PaneId, SessionId, TerminalId},
    pane::SplitAxis,
    surface::{SurfaceKind, SurfaceState, TerminalStatus, TerminalSurfaceState},
    terminal::{
        TerminalError, TerminalManager, TerminalRegistry, TerminalSize, TerminalSnapshot,
        default_shell_args, default_shell_program,
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
    events: EventBus,
    operations: OperationRegistry,
    terminals: TerminalManager,
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

    pub fn with_scrollback_lines(scrollback_lines: usize) -> Self {
        Self::with_operations_and_terminal_wakeup_and_scrollback(
            OperationRegistry::new(),
            None,
            scrollback_lines,
        )
    }

    pub(crate) fn with_operations(operations: OperationRegistry) -> Self {
        Self::with_operations_and_terminal_wakeup(operations, None)
    }

    pub(crate) fn with_operations_and_terminal_wakeup(
        operations: OperationRegistry,
        wakeup: Option<crate::terminal::WakeupCallback>,
    ) -> Self {
        Self::with_operations_and_terminal_wakeup_and_scrollback(
            operations,
            wakeup,
            crate::terminal::MAX_SCROLLBACK_LINES,
        )
    }

    pub(crate) fn with_operations_and_terminal_wakeup_and_scrollback(
        operations: OperationRegistry,
        wakeup: Option<crate::terminal::WakeupCallback>,
        scrollback_lines: usize,
    ) -> Self {
        Self {
            model: ApplicationModel::new(),
            ids: IdAllocator::new(),
            events: EventBus::default(),
            operations,
            terminals: TerminalManager::new_with_wakeup_and_scrollback(wakeup, scrollback_lines),
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
        operation_id
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
        if let Some(workspace) = state.workspace.as_mut() {
            for tab in &mut workspace.tabs {
                attach_terminal_snapshots(&mut tab.tree, &self.terminals.registry());
            }
        }
        state
    }

    pub fn memory_stats(&self) -> MemoryStats {
        let mut stats = self.model.memory_stats();
        stats.scrollback_lines =
            self.terminals.terminal_count() * self.terminals.scrollback_lines();
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
                    if let Ok(snapshot) = self.terminals.registry().snapshot(terminal_id)
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
                    }
                }
            }
        }
        changed
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
            AppCommand::Workspace(WorkspaceCommand::Create) => self.create_workspace(),
            AppCommand::Tab(command) => self.apply_tab(command),
            AppCommand::Pane(command) => self.apply_pane(command),
            AppCommand::Surface(command) => self.apply_surface(command),
            AppCommand::Terminal(command) => self.apply_terminal(command),
        }
    }

    fn create_workspace(&mut self) -> Result<OperationResult, CommandError> {
        if let Some(workspace) = self.model.workspace() {
            return Ok(OperationResult::WorkspaceCreated {
                workspace_id: workspace.id,
            });
        }

        let workspace_id = self.ids.alloc();
        if !self.model.create_workspace(workspace_id) {
            return Err(CommandError::new(
                "WORKSPACE_CREATE_FAILED",
                "workspace already exists",
            ));
        }
        self.emit(AppEventKind::WorkspaceCreated { workspace_id });
        Ok(OperationResult::WorkspaceCreated { workspace_id })
    }

    fn apply_tab(&mut self, command: TabCommand) -> Result<OperationResult, CommandError> {
        match command {
            TabCommand::New { title } => {
                if self.model.workspace().is_none() {
                    return Err(CommandError::new(
                        "WORKSPACE_NOT_FOUND",
                        "create a workspace before creating a tab",
                    ));
                }
                let tab_id = self.ids.alloc();
                let pane_id = self.ids.alloc();
                let empty_surface_id = self.ids.alloc();
                let title = title.unwrap_or_else(|| {
                    format!("Tab {}", self.model.tabs().count().saturating_add(1))
                });
                self.model
                    .create_tab(tab_id, title, pane_id, empty_surface_id)
                    .map_err(|message| CommandError::new("TAB_CREATE_FAILED", message))?;

                let program = default_shell_program();
                let args = default_shell_args(&program);
                let terminal = match self.spawn_terminal_for_pane(
                    pane_id,
                    program,
                    args,
                    TerminalSize::default(),
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
            TabCommand::Close { tab_id } => {
                let tab_id = self.resolve_tab(tab_id, None)?;
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
                let program = default_shell_program();
                let args = default_shell_args(&program);
                let terminal = match self.spawn_terminal_for_pane(
                    new_pane,
                    program,
                    args,
                    TerminalSize::default(),
                ) {
                    Ok(terminal) => terminal,
                    Err(error) => {
                        let _ = self.model.close_pane(tab_id, new_pane);
                        return Err(error);
                    }
                };
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
                self.model
                    .close_pane(tab_id, pane_id)
                    .map_err(|message| self.pane_error(message))?;
                if let Some(terminal_id) = terminal_id {
                    self.terminals.remove(terminal_id);
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
    ) -> Result<SpawnedTerminal, CommandError> {
        let old_terminal_id = self.model.terminal_id_for_pane(pane_id);
        let terminal_id: TerminalId = self.ids.alloc();
        let session_id: SessionId = self.ids.alloc();
        let surface_id = self.ids.alloc();
        self.terminals
            .spawn(terminal_id, program.clone(), args.clone(), size)
            .map_err(terminal_command_error)?;
        let terminal_surface = SurfaceState::Terminal(TerminalSurfaceState {
            terminal_id,
            session_id,
            program,
            title: None,
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
                let (_, pane_id) = self.resolve_pane(pane_id)?;
                let size = TerminalSize::new(columns, lines);
                let terminal = self.spawn_terminal_for_pane(pane_id, program, args, size)?;
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
        if let Some(pane_id) = pane_id {
            let (tab_id, _) = self.resolve_pane(Some(pane_id))?;
            return Ok((tab_id, pane_id));
        }

        let tab_id = self
            .model
            .active_tab_id()
            .ok_or_else(|| CommandError::new("TAB_NOT_FOUND", "there is no active tab"))?;
        let current = self
            .model
            .active_pane()
            .ok_or_else(|| CommandError::new("PANE_NOT_FOUND", "there is no active pane"))?;
        let Some(direction) = direction else {
            return Ok((tab_id, current));
        };
        let pane_ids = self.model.pane_ids_in_tab(tab_id).ok_or_else(|| {
            CommandError::new("TAB_NOT_FOUND", format!("tab {tab_id} does not exist"))
        })?;
        let index = pane_ids
            .iter()
            .position(|id| *id == current)
            .ok_or_else(|| {
                CommandError::new("PANE_NOT_FOUND", format!("pane {current} does not exist"))
            })?;
        let target_index = match direction {
            FocusDirection::Left | FocusDirection::Up => index.checked_sub(1),
            FocusDirection::Right | FocusDirection::Down => index.checked_add(1),
        };
        let target = target_index
            .and_then(|index| pane_ids.get(index).copied())
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

fn attach_terminal_snapshots(tree: &mut PaneTreeDump, registry: &TerminalRegistry) {
    match tree {
        PaneTreeDump::Leaf {
            surface_state,
            terminal_snapshot,
            ..
        } => {
            *terminal_snapshot = match surface_state {
                SurfaceState::Terminal(terminal) => {
                    registry.snapshot(terminal.terminal_id).ok().map(Box::new)
                }
                SurfaceState::Empty(_) => None,
            };
        }
        PaneTreeDump::Split { first, second, .. } => {
            attach_terminal_snapshots(first, registry);
            attach_terminal_snapshots(second, registry);
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
