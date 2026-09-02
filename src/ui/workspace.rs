use std::collections::BTreeMap;
use std::sync::{Arc, Mutex};

use gpui::{
    AnyElement, App, Bounds, Context, CursorStyle, DispatchPhase, Entity, FocusHandle, Focusable,
    FontWeight, KeyDownEvent, Keystroke, MouseButton, MouseDownEvent, MouseMoveEvent, MouseUpEvent,
    Point, ScrollDelta, ScrollWheelEvent, SharedString, Task, Window, canvas, div, font,
    prelude::*, px, rgb,
};

use crate::app::model::{PaneTreeDump, TabDump, WorkspaceDump};
use crate::app::{CommandClient, ModelSnapshot, ModelSnapshotReceiver};
use crate::command::{
    AppCommand, FocusDirection, PaneCommand, SplitDirection, TabCommand, TerminalCommand,
    WorkspaceCommand,
};
use crate::config::{AppConfig, ThemeColors};
use crate::ids::{PaneId, TabId, TerminalId, WorkspaceId};
use crate::pane::SplitAxis;
use crate::surface::SurfaceState;
use crate::terminal::{
    TerminalCellFlags, TerminalColor, TerminalModes, TerminalSize, TerminalSnapshot,
};

const DEFAULT_TERMINAL_CELL_WIDTH: f32 = 8.4;
const DEFAULT_TERMINAL_LINE_HEIGHT: f32 = 18.0;
const DEFAULT_SIDEBAR_WIDTH: f32 = 236.0;
const COLLAPSED_SIDEBAR_WIDTH: f32 = 34.0;
const MIN_SIDEBAR_WIDTH: f32 = 170.0;
const MAX_SIDEBAR_WIDTH: f32 = 420.0;
const SIDEBAR_RESIZE_HANDLE_WIDTH: f32 = 6.0;

#[derive(Debug, Clone, Copy)]
struct TerminalMetrics {
    cell_width: f32,
    line_height: f32,
}

impl Default for TerminalMetrics {
    fn default() -> Self {
        Self {
            cell_width: DEFAULT_TERMINAL_CELL_WIDTH,
            line_height: DEFAULT_TERMINAL_LINE_HEIGHT,
        }
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, PartialOrd, Ord)]
struct TerminalCellPosition {
    /// Viewport-relative row. Signed so a selected cell can remain tracked
    /// while scrolling moves it outside the visible area.
    row: i32,
    column: usize,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum TerminalSelectionSide {
    Left,
    Right,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum RenameTarget {
    Workspace(WorkspaceId),
    Tab(TabId),
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
struct TerminalSelectionEndpoint {
    position: TerminalCellPosition,
    side: TerminalSelectionSide,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
struct TerminalSelection {
    terminal_id: TerminalId,
    anchor: TerminalSelectionEndpoint,
    head: TerminalSelectionEndpoint,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum TerminalMouseReportKind {
    Press,
    Release,
    Motion,
}

#[derive(Debug, Clone, Copy)]
struct TerminalMouseContext {
    modes: TerminalModes,
    bounds: Option<Bounds<gpui::Pixels>>,
    metrics: TerminalMetrics,
}

#[derive(Debug, Clone, Copy)]
struct TerminalMousePosition {
    column: usize,
    row: usize,
    side: TerminalSelectionSide,
}

#[derive(Debug, Clone, Copy)]
struct TerminalRenderOptions {
    metrics: TerminalMetrics,
    theme: ThemeColors,
    cursor_focused: bool,
}

#[derive(Debug, Clone, Copy)]
struct TerminalRunStyle {
    metrics: TerminalMetrics,
    theme: ThemeColors,
    cursor_hollow: bool,
}

pub struct WorkspaceView {
    client: CommandClient,
    snapshot: ModelSnapshot,
    config: AppConfig,
    terminal_metrics: TerminalMetrics,
    focus_handle: FocusHandle,
    resize_requests: Arc<Mutex<BTreeMap<TerminalId, TerminalSize>>>,
    terminal_bounds: Arc<Mutex<BTreeMap<TerminalId, Bounds<gpui::Pixels>>>>,
    focused_pane: Option<PaneId>,
    selection: Option<TerminalSelection>,
    dragging_terminal: Option<TerminalId>,
    reported_mouse: Option<(TerminalId, MouseButton)>,
    last_reported_mouse_cell: Option<(TerminalId, TerminalCellPosition)>,
    sidebar_collapsed: bool,
    sidebar_width: f32,
    dragging_sidebar: bool,
    rename_target: Option<RenameTarget>,
    rename_value: String,
}

impl WorkspaceView {
    pub fn new(client: CommandClient, snapshot: ModelSnapshot, focus_handle: FocusHandle) -> Self {
        Self::new_with_config(client, snapshot, focus_handle, AppConfig::default())
    }

    pub fn new_with_config(
        client: CommandClient,
        snapshot: ModelSnapshot,
        focus_handle: FocusHandle,
        config: AppConfig,
    ) -> Self {
        let focused_pane = snapshot.focused_pane;
        Self {
            client,
            snapshot,
            config: config.normalized(),
            terminal_metrics: TerminalMetrics::default(),
            focus_handle,
            resize_requests: Arc::new(Mutex::new(BTreeMap::new())),
            terminal_bounds: Arc::new(Mutex::new(BTreeMap::new())),
            focused_pane,
            selection: None,
            dragging_terminal: None,
            reported_mouse: None,
            last_reported_mouse_cell: None,
            sidebar_collapsed: false,
            sidebar_width: DEFAULT_SIDEBAR_WIDTH,
            dragging_sidebar: false,
            rename_target: None,
            rename_value: String::new(),
        }
    }

    fn workspace_dumps(&self) -> Vec<WorkspaceDump> {
        if self.snapshot.workspaces.is_empty() {
            self.snapshot.workspace.clone().into_iter().collect()
        } else {
            self.snapshot.workspaces.clone()
        }
    }

    fn active_workspace_id(&self) -> Option<WorkspaceId> {
        self.snapshot.active_workspace.or_else(|| {
            self.snapshot
                .workspace
                .as_ref()
                .map(|workspace| workspace.id)
        })
    }

    fn toggle_sidebar(&mut self, cx: &mut Context<Self>) {
        self.sidebar_collapsed = !self.sidebar_collapsed;
        cx.notify();
    }

    fn update_sidebar_width(&mut self, x: gpui::Pixels, cx: &mut Context<Self>) {
        if !self.dragging_sidebar || self.sidebar_collapsed {
            return;
        }
        let width = f32::from(x).clamp(MIN_SIDEBAR_WIDTH, MAX_SIDEBAR_WIDTH);
        if (self.sidebar_width - width).abs() > f32::EPSILON {
            self.sidebar_width = width;
            cx.notify();
        }
    }

    fn begin_rename_workspace(
        &mut self,
        workspace_id: WorkspaceId,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        let Some(workspace) = self
            .workspace_dumps()
            .into_iter()
            .find(|workspace| workspace.id == workspace_id)
        else {
            return;
        };
        self.rename_target = Some(RenameTarget::Workspace(workspace_id));
        self.rename_value = workspace.title.clone();
        self.focus_handle.focus(window, cx);
        cx.notify();
    }

    fn begin_rename_tab(&mut self, tab_id: TabId, window: &mut Window, cx: &mut Context<Self>) {
        let Some(tab) = self
            .workspace_dumps()
            .into_iter()
            .flat_map(|workspace| workspace.tabs)
            .find(|tab| tab.id == tab_id)
        else {
            return;
        };
        self.rename_target = Some(RenameTarget::Tab(tab_id));
        self.rename_value = tab.title.clone();
        self.focus_handle.focus(window, cx);
        cx.notify();
    }

    fn cancel_rename(&mut self, cx: &mut Context<Self>) {
        if self.rename_target.take().is_some() {
            self.rename_value.clear();
            cx.notify();
        }
    }

    fn commit_rename(&mut self, cx: &mut Context<Self>) {
        let Some(target) = self.rename_target.take() else {
            return;
        };
        let title = self.rename_value.trim().to_owned();
        self.rename_value.clear();
        if title.is_empty() {
            cx.notify();
            return;
        }
        let command = match target {
            RenameTarget::Workspace(workspace_id) => {
                AppCommand::Workspace(WorkspaceCommand::Rename {
                    workspace_id: Some(workspace_id),
                    title,
                })
            }
            RenameTarget::Tab(tab_id) => AppCommand::Tab(TabCommand::Rename {
                tab_id: Some(tab_id),
                title,
            }),
        };
        self.dispatch(command, cx);
        cx.notify();
    }

    fn handle_rename_key(&mut self, event: &KeyDownEvent, cx: &mut Context<Self>) -> bool {
        if self.rename_target.is_none() {
            return false;
        }
        let key = event.keystroke.key.as_str();
        match key {
            "enter" | "return" => self.commit_rename(cx),
            "escape" => self.cancel_rename(cx),
            "backspace" => {
                self.rename_value.pop();
                cx.notify();
            }
            _ if !event.keystroke.modifiers.platform
                && !event.keystroke.modifiers.control
                && !event.keystroke.modifiers.alt
                && event.keystroke.key_char.is_some() =>
            {
                if let Some(character) = event.keystroke.key_char.as_deref() {
                    self.rename_value.push_str(character);
                    cx.notify();
                }
            }
            _ => {}
        }
        true
    }

    fn measured_terminal_metrics(&self, window: &Window) -> TerminalMetrics {
        let font_size = px(self.config.terminal.font_size);
        let font = font(self.config.terminal.font_family.clone());
        let text_system = window.text_system();
        let cell_width = text_system
            .ch_advance(text_system.resolve_font(&font), font_size)
            .ok()
            .map(f32::from)
            .filter(|width| *width > 0.0)
            .unwrap_or(DEFAULT_TERMINAL_CELL_WIDTH);
        TerminalMetrics {
            cell_width,
            line_height: self.config.terminal.line_height,
        }
    }

    fn enqueue_terminal_command(&self, terminal_id: TerminalId, command: TerminalCommand) {
        if let Err(error) = self.client.enqueue(AppCommand::Terminal(command)) {
            tracing::warn!(
                target: "water::ui",
                terminal_id = %terminal_id,
                ?error,
                "could not enqueue terminal input"
            );
        }
    }

    fn active_terminal_id(&self) -> Option<TerminalId> {
        let focused_pane = self.focused_pane?;
        let workspace = self.snapshot.workspace.as_ref()?;
        let active_tab_id = workspace.active_tab?;
        let tab = workspace.tabs.iter().find(|tab| tab.id == active_tab_id)?;
        terminal_id_for_pane(&tab.tree, focused_pane)
    }

    fn active_terminal_snapshot(&self) -> Option<&TerminalSnapshot> {
        let focused_pane = self.focused_pane?;
        let workspace = self.snapshot.workspace.as_ref()?;
        let active_tab_id = workspace.active_tab?;
        let tab = workspace.tabs.iter().find(|tab| tab.id == active_tab_id)?;
        terminal_snapshot_for_pane(&tab.tree, focused_pane)
    }

    fn terminal_id_for_pane(&self, pane_id: PaneId) -> Option<TerminalId> {
        self.snapshot
            .workspace
            .as_ref()?
            .tabs
            .iter()
            .find_map(|tab| terminal_id_for_pane(&tab.tree, pane_id))
    }

    fn terminal_bounds_for(&self, terminal_id: TerminalId) -> Option<Bounds<gpui::Pixels>> {
        self.terminal_bounds
            .lock()
            .expect("terminal bounds poisoned")
            .get(&terminal_id)
            .copied()
    }

    fn terminal_snapshot_for(&self, terminal_id: TerminalId) -> Option<&TerminalSnapshot> {
        self.snapshot
            .workspace
            .as_ref()?
            .tabs
            .iter()
            .find_map(|tab| terminal_snapshot_for_id(&tab.tree, terminal_id))
    }

    fn terminal_selection_endpoint_at(
        &self,
        terminal_id: TerminalId,
        position: Point<gpui::Pixels>,
    ) -> Option<TerminalSelectionEndpoint> {
        let snapshot = self.terminal_snapshot_for(terminal_id)?;
        let mouse = terminal_mouse_position(
            position,
            self.terminal_bounds_for(terminal_id),
            self.terminal_metrics,
        );
        let row = mouse
            .row
            .saturating_sub(1)
            .min(snapshot.size.lines.saturating_sub(1));
        let column = mouse
            .column
            .saturating_sub(1)
            .min(snapshot.size.columns.saturating_sub(1));
        let side = if mouse.row > snapshot.size.lines || mouse.column > snapshot.size.columns {
            TerminalSelectionSide::Right
        } else {
            mouse.side
        };
        Some(TerminalSelectionEndpoint {
            position: TerminalCellPosition {
                row: row as i32,
                column,
            },
            side,
        })
    }

    fn terminal_cell_at(
        &self,
        terminal_id: TerminalId,
        position: Point<gpui::Pixels>,
    ) -> Option<TerminalCellPosition> {
        self.terminal_selection_endpoint_at(terminal_id, position)
            .map(|endpoint| endpoint.position)
    }

    fn begin_reported_mouse(
        &mut self,
        terminal_id: TerminalId,
        event: &MouseDownEvent,
        cx: &mut Context<Self>,
    ) -> bool {
        let Some(snapshot) = self.terminal_snapshot_for(terminal_id) else {
            return false;
        };
        if !self.config.features.mouse_reporting || !snapshot.modes.mouse_reporting {
            return false;
        }
        let mouse = TerminalMouseContext {
            modes: snapshot.modes,
            bounds: self.terminal_bounds_for(terminal_id),
            metrics: self.terminal_metrics,
        };
        let Some(text) = terminal_mouse_button_input(
            event.position,
            event.button,
            true,
            false,
            event.modifiers,
            mouse,
        ) else {
            return false;
        };
        self.enqueue_terminal_command(
            terminal_id,
            TerminalCommand::SendBytes {
                terminal_id: Some(terminal_id),
                pane_id: None,
                bytes: text,
            },
        );
        self.reported_mouse = Some((terminal_id, event.button));
        self.last_reported_mouse_cell = self
            .terminal_cell_at(terminal_id, event.position)
            .map(|point| (terminal_id, point));
        cx.notify();
        true
    }

    fn begin_terminal_selection(
        &mut self,
        terminal_id: TerminalId,
        position: Point<gpui::Pixels>,
        cx: &mut Context<Self>,
    ) {
        if !self.config.features.selection {
            return;
        }
        let Some(endpoint) = self.terminal_selection_endpoint_at(terminal_id, position) else {
            return;
        };
        self.selection = Some(TerminalSelection {
            terminal_id,
            anchor: endpoint,
            head: endpoint,
        });
        self.dragging_terminal = Some(terminal_id);
        cx.notify();
    }

    fn update_terminal_selection(&mut self, event: &MouseMoveEvent, cx: &mut Context<Self>) {
        if self.reported_mouse.is_some() {
            self.update_reported_mouse(event, cx);
            return;
        }
        let Some(terminal_id) = self.dragging_terminal else {
            return;
        };
        if event.pressed_button != Some(MouseButton::Left) {
            return;
        }
        let Some(endpoint) = self.terminal_selection_endpoint_at(terminal_id, event.position)
        else {
            return;
        };
        if let Some(selection) = self.selection.as_mut()
            && selection.head != endpoint
        {
            selection.head = endpoint;
            cx.notify();
        }
    }

    fn update_reported_mouse(&mut self, event: &MouseMoveEvent, cx: &mut Context<Self>) {
        let Some((terminal_id, button)) = self.reported_mouse else {
            return;
        };
        if event.pressed_button != Some(button) {
            return;
        }
        let Some(snapshot) = self.terminal_snapshot_for(terminal_id) else {
            return;
        };
        if !snapshot.modes.mouse_motion && !snapshot.modes.mouse_drag {
            return;
        }
        let Some(point) = self.terminal_cell_at(terminal_id, event.position) else {
            return;
        };
        if self.last_reported_mouse_cell == Some((terminal_id, point)) {
            return;
        }
        let mouse = TerminalMouseContext {
            modes: snapshot.modes,
            bounds: self.terminal_bounds_for(terminal_id),
            metrics: self.terminal_metrics,
        };
        let Some(text) =
            terminal_mouse_button_input(event.position, button, true, true, event.modifiers, mouse)
        else {
            return;
        };
        self.enqueue_terminal_command(
            terminal_id,
            TerminalCommand::SendBytes {
                terminal_id: Some(terminal_id),
                pane_id: None,
                bytes: text,
            },
        );
        self.last_reported_mouse_cell = Some((terminal_id, point));
        cx.stop_propagation();
    }

    fn finish_terminal_selection(&mut self, event: &MouseUpEvent, cx: &mut Context<Self>) {
        if let Some((terminal_id, button)) = self.reported_mouse.take() {
            if button == event.button
                && let Some(snapshot) = self.terminal_snapshot_for(terminal_id)
                && let Some(text) = terminal_mouse_button_input(
                    event.position,
                    button,
                    false,
                    false,
                    event.modifiers,
                    TerminalMouseContext {
                        modes: snapshot.modes,
                        bounds: self.terminal_bounds_for(terminal_id),
                        metrics: self.terminal_metrics,
                    },
                )
            {
                self.enqueue_terminal_command(
                    terminal_id,
                    TerminalCommand::SendBytes {
                        terminal_id: Some(terminal_id),
                        pane_id: None,
                        bytes: text,
                    },
                );
            }
            self.last_reported_mouse_cell = None;
            cx.stop_propagation();
        } else if event.button == MouseButton::Left
            && self.config.features.selection
            && let Some(terminal_id) = self.dragging_terminal
            && let Some(endpoint) = self.terminal_selection_endpoint_at(terminal_id, event.position)
            && let Some(selection) = self.selection.as_mut()
            && selection.terminal_id == terminal_id
            && selection.head != endpoint
        {
            // Mouse-up is the authoritative endpoint. A platform may omit a
            // final move event, so do not leave the copied range one cell
            // behind the pointer.
            selection.head = endpoint;
            cx.notify();
        }
        self.dragging_terminal = None;
    }

    fn copy_terminal_selection(&self, terminal_id: TerminalId, cx: &mut Context<Self>) -> bool {
        let Some(selection) = self.selection else {
            return false;
        };
        if selection.terminal_id != terminal_id {
            return false;
        }
        let Some(snapshot) = self.terminal_snapshot_for(terminal_id) else {
            return false;
        };
        if selection_bounds(snapshot, selection).is_none() {
            return false;
        }
        let text = selected_terminal_text(snapshot, selection);
        if text.is_empty() {
            return false;
        }
        cx.write_to_clipboard(gpui::ClipboardItem::new_string(text));
        true
    }

    fn terminal_resize_observer(
        &self,
        terminal_id: TerminalId,
        current_size: TerminalSize,
        metrics: TerminalMetrics,
    ) -> AnyElement {
        let client = self.client.clone();
        let resize_requests = self.resize_requests.clone();
        let terminal_bounds = self.terminal_bounds.clone();
        canvas(
            move |bounds, _, _| {
                terminal_bounds
                    .lock()
                    .expect("terminal bounds poisoned")
                    .insert(terminal_id, bounds);
                let width = f32::from(bounds.size.width);
                let height = f32::from(bounds.size.height);
                if width <= 0.0 || height <= 0.0 {
                    return;
                }
                let size = TerminalSize::new(
                    (width / metrics.cell_width).floor() as usize,
                    (height / metrics.line_height).floor() as usize,
                );
                if size == current_size {
                    return;
                }
                let should_enqueue = {
                    let mut requests = resize_requests
                        .lock()
                        .expect("terminal resize requests poisoned");
                    if requests.get(&terminal_id) == Some(&size) {
                        false
                    } else {
                        requests.insert(terminal_id, size);
                        true
                    }
                };
                if should_enqueue {
                    let _ = client.enqueue(AppCommand::Terminal(TerminalCommand::Resize {
                        terminal_id: Some(terminal_id),
                        pane_id: None,
                        columns: size.columns,
                        lines: size.lines,
                    }));
                }
            },
            |_bounds, _, _, _| {},
        )
        .size_full()
        .absolute()
        .inset_0()
        .into_any_element()
    }

    fn handle_key_down(&mut self, event: &KeyDownEvent, cx: &mut Context<Self>) {
        if self.handle_rename_key(event, cx) {
            return;
        }

        let keystroke = &event.keystroke;
        if keystroke.modifiers.platform {
            match (keystroke.key.as_str(), keystroke.modifiers.shift) {
                ("e", false) => {
                    self.toggle_sidebar(cx);
                    return;
                }
                ("e", true) => {
                    if let Some(workspace_id) = self.active_workspace_id() {
                        // The view owns only transient editor state; the
                        // committed title still travels through the command
                        // dispatcher like every other model mutation.
                        if let Some(workspace) = self
                            .workspace_dumps()
                            .into_iter()
                            .find(|workspace| workspace.id == workspace_id)
                        {
                            self.rename_target = Some(RenameTarget::Workspace(workspace_id));
                            self.rename_value = workspace.title.clone();
                            cx.notify();
                        }
                    }
                    return;
                }
                ("n", true) => {
                    self.dispatch(AppCommand::Workspace(WorkspaceCommand::New), cx);
                    return;
                }
                ("t", false) => {
                    self.dispatch(AppCommand::Tab(TabCommand::New { title: None }), cx);
                    return;
                }
                ("t", true) => {
                    if let Some(tab_id) = self
                        .snapshot
                        .workspace
                        .as_ref()
                        .and_then(|workspace| workspace.active_tab)
                        && let Some(tab) = self.snapshot.workspace.as_ref().and_then(|workspace| {
                            workspace.tabs.iter().find(|tab| tab.id == tab_id)
                        })
                    {
                        self.rename_target = Some(RenameTarget::Tab(tab_id));
                        self.rename_value = tab.title.clone();
                        cx.notify();
                    }
                    return;
                }
                ("h", false) | ("j", false) | ("k", false) | ("l", false) => {
                    if self.focused_pane.is_none() {
                        return;
                    }
                    let direction = match keystroke.key.as_str() {
                        "h" => FocusDirection::Left,
                        "j" => FocusDirection::Down,
                        "k" => FocusDirection::Up,
                        "l" => FocusDirection::Right,
                        _ => unreachable!(),
                    };
                    self.dispatch(
                        AppCommand::Pane(PaneCommand::Focus {
                            pane_id: None,
                            direction: Some(direction),
                        }),
                        cx,
                    );
                    return;
                }
                ("w", true) => {
                    if let Some(pane_id) = self.focused_pane {
                        self.dispatch(
                            AppCommand::Pane(PaneCommand::Close {
                                pane_id: Some(pane_id),
                            }),
                            cx,
                        );
                    }
                    return;
                }
                ("\\", false) => {
                    if let Some(focused_pane) = self.focused_pane {
                        self.dispatch(
                            AppCommand::Pane(PaneCommand::Split {
                                pane_id: Some(focused_pane),
                                direction: SplitDirection::Right,
                            }),
                            cx,
                        );
                    }
                    return;
                }
                ("-", false) => {
                    if let Some(focused_pane) = self.focused_pane {
                        self.dispatch(
                            AppCommand::Pane(PaneCommand::Split {
                                pane_id: Some(focused_pane),
                                direction: SplitDirection::Down,
                            }),
                            cx,
                        );
                    }
                    return;
                }
                ("v", false) => {
                    if let Some(terminal_id) = self.active_terminal_id() {
                        let modes = self
                            .active_terminal_snapshot()
                            .map(|snapshot| snapshot.modes)
                            .unwrap_or_default();
                        self.paste_into_terminal(
                            terminal_id,
                            modes.bracketed_paste && self.config.features.bracketed_paste,
                            cx,
                        );
                    }
                    return;
                }
                ("c", false) => {
                    if let Some(terminal_id) = self.active_terminal_id()
                        && !self.copy_terminal_selection(terminal_id, cx)
                    {
                        self.enqueue_terminal_command(
                            terminal_id,
                            TerminalCommand::SendText {
                                terminal_id: Some(terminal_id),
                                pane_id: None,
                                text: "\u{3}".to_owned(),
                            },
                        );
                    }
                    return;
                }
                ("d", false) => {
                    if let Some(terminal_id) = self.active_terminal_id() {
                        self.enqueue_terminal_command(
                            terminal_id,
                            TerminalCommand::SendText {
                                terminal_id: Some(terminal_id),
                                pane_id: None,
                                text: "\u{4}".to_owned(),
                            },
                        );
                    }
                    return;
                }
                _ => {}
            }
        }

        let Some(terminal_id) = self.active_terminal_id() else {
            return;
        };
        let modes = self
            .active_terminal_snapshot()
            .map(|snapshot| snapshot.modes)
            .unwrap_or_default();
        if keystroke.modifiers.shift && matches!(keystroke.key.as_str(), "pageup" | "pagedown") {
            let lines = if keystroke.key == "pageup" { 20 } else { -20 };
            self.enqueue_terminal_command(
                terminal_id,
                TerminalCommand::Scroll {
                    terminal_id: Some(terminal_id),
                    pane_id: None,
                    lines,
                },
            );
            return;
        }
        let Some(text) = terminal_input_for_keystroke_with_modes(keystroke, modes) else {
            return;
        };
        self.selection = None;
        self.enqueue_terminal_command(
            terminal_id,
            TerminalCommand::SendText {
                terminal_id: Some(terminal_id),
                pane_id: None,
                text,
            },
        );
    }

    fn paste_into_terminal(
        &self,
        terminal_id: TerminalId,
        bracketed_paste: bool,
        cx: &mut Context<Self>,
    ) {
        let client = self.client.clone();
        let clipboard = cx.read_from_clipboard_async();
        cx.spawn(async move |_entity, _cx| {
            let Ok(Some(item)) = clipboard.await else {
                return;
            };
            let Some(text) = clipboard_text(item) else {
                return;
            };
            let text = if bracketed_paste {
                format!("\u{1b}[200~{text}\u{1b}[201~")
            } else {
                text
            };
            let _ = client.enqueue(AppCommand::Terminal(TerminalCommand::SendText {
                terminal_id: Some(terminal_id),
                pane_id: None,
                text,
            }));
        })
        .detach();
    }

    fn install_snapshot(&mut self, snapshot: ModelSnapshot, cx: &mut Context<Self>) {
        if snapshot.state_revision <= self.snapshot.state_revision {
            return;
        }
        update_terminal_selection_for_snapshot(&mut self.selection, &self.snapshot, &snapshot);
        self.focused_pane = snapshot.focused_pane;
        self.snapshot = snapshot;
        cx.notify();
    }

    fn dispatch(&mut self, command: AppCommand, cx: &mut Context<Self>) {
        let command_name = command.type_name();
        let client = self.client.clone();
        cx.spawn(async move |entity, cx| {
            let result = cx
                .background_executor()
                .spawn(async move {
                    let operation_id = client.dispatch(command)?;
                    let operation = client.wait_operation(operation_id)?;
                    if operation.status.is_terminal() && operation.error.is_none() {
                        client.state_dump().map(Some)
                    } else {
                        tracing::warn!(
                            target: "water::ui",
                            command = command_name,
                            status = ?operation.status,
                            error = ?operation.error,
                            "UI command failed"
                        );
                        Ok(None)
                    }
                })
                .await;
            if let Ok(Some(snapshot)) = result {
                let _ = entity.update(cx, |view, cx| {
                    view.install_snapshot(snapshot, cx);
                });
            } else if let Err(error) = result {
                tracing::warn!(
                    target: "water::ui",
                    command = command_name,
                    ?error,
                    "could not complete UI command"
                );
            }
        })
        .detach();
    }

    fn tab_button(
        &self,
        tab_id: TabId,
        title: String,
        active: bool,
        active_pane: PaneId,
        theme: ThemeColors,
        cx: &mut Context<Self>,
    ) -> AnyElement {
        let background = if active {
            rgb(theme.tab_active_background)
        } else {
            rgb(theme.tab_inactive_background)
        };
        div()
            .h(px(32.))
            .px(px(12.))
            .items_center()
            .flex()
            .bg(background)
            .text_color(rgb(theme.terminal_foreground))
            .child(SharedString::from(title))
            .on_mouse_down(
                MouseButton::Left,
                cx.listener(move |this, _event: &MouseDownEvent, window, cx| {
                    this.focus_handle.focus(window, cx);
                    this.focused_pane = Some(active_pane);
                    this.selection = None;
                    this.dispatch(
                        AppCommand::Tab(TabCommand::Activate {
                            tab_id: Some(tab_id),
                            index: None,
                        }),
                        cx,
                    );
                }),
            )
            .into_any_element()
    }

    fn sidebar_resize_handle(&self, theme: ThemeColors, cx: &mut Context<Self>) -> AnyElement {
        div()
            .w(px(SIDEBAR_RESIZE_HANDLE_WIDTH))
            .h_full()
            .cursor(CursorStyle::ResizeLeftRight)
            .bg(rgb(theme.chrome_background))
            .on_mouse_down(
                MouseButton::Left,
                cx.listener(|this, _event: &MouseDownEvent, window, cx| {
                    this.dragging_sidebar = true;
                    this.focus_handle.focus(window, cx);
                    cx.stop_propagation();
                    cx.notify();
                }),
            )
            .into_any_element()
    }

    fn render_sidebar_action(
        &self,
        label: &'static str,
        theme: ThemeColors,
        listener: impl Fn(&mut Self, &MouseDownEvent, &mut Window, &mut Context<Self>) + 'static,
        cx: &mut Context<Self>,
    ) -> AnyElement {
        div()
            .w(px(22.))
            .h(px(24.))
            .items_center()
            .justify_center()
            .flex()
            .text_color(rgb(theme.terminal_foreground))
            .child(label)
            .on_mouse_down(MouseButton::Left, cx.listener(listener))
            .into_any_element()
    }

    fn render_sidebar_workspace(
        &self,
        workspace: WorkspaceDump,
        active_workspace: Option<WorkspaceId>,
        theme: ThemeColors,
        cx: &mut Context<Self>,
    ) -> AnyElement {
        let workspace_id = workspace.id;
        let active = active_workspace == Some(workspace_id);
        let workspace_title = if self.rename_target == Some(RenameTarget::Workspace(workspace_id)) {
            format!("{}▌", self.rename_value)
        } else {
            workspace.title.clone()
        };
        let workspace_background = if active {
            rgb(theme.tab_active_background)
        } else {
            rgb(theme.tab_inactive_background)
        };
        let workspace_active_pane = workspace
            .active_tab
            .and_then(|tab_id| workspace.tabs.iter().find(|tab| tab.id == tab_id))
            .map(|tab| tab.active_pane);
        let workspace_activate = cx.listener(move |this, _event: &MouseDownEvent, window, cx| {
            this.focus_handle.focus(window, cx);
            this.focused_pane = workspace_active_pane;
            this.selection = None;
            this.dispatch(
                AppCommand::Workspace(WorkspaceCommand::Activate {
                    workspace_id: Some(workspace_id),
                }),
                cx,
            );
        });
        let rename_workspace = self.render_sidebar_action(
            "✎",
            theme,
            move |this, _event: &MouseDownEvent, window, cx| {
                cx.stop_propagation();
                this.begin_rename_workspace(workspace_id, window, cx);
            },
            cx,
        );
        let delete_workspace = self.render_sidebar_action(
            "×",
            theme,
            move |this, _event: &MouseDownEvent, _window, cx| {
                cx.stop_propagation();
                this.dispatch(
                    AppCommand::Workspace(WorkspaceCommand::Delete {
                        workspace_id: Some(workspace_id),
                    }),
                    cx,
                );
            },
            cx,
        );

        let workspace_row = div()
            .h(px(30.))
            .w_full()
            .px(px(8.))
            .gap(px(2.))
            .items_center()
            .flex()
            .bg(workspace_background)
            .text_color(rgb(theme.ui_foreground))
            .on_mouse_down(MouseButton::Left, workspace_activate)
            .child(
                div()
                    .w(px(14.))
                    .items_center()
                    .justify_center()
                    .flex()
                    .child(if active { "▾" } else { "▸" }),
            )
            .child(
                div()
                    .flex_1()
                    .min_w(px(0.))
                    .truncate()
                    .child(SharedString::from(workspace_title)),
            )
            .child(rename_workspace)
            .child(delete_workspace);

        let mut tabs = div().w_full().flex_col();
        for tab in workspace.tabs {
            let tab_id = tab.id;
            let tab_active = active && workspace.active_tab == Some(tab_id);
            let tab_title = if self.rename_target == Some(RenameTarget::Tab(tab_id)) {
                format!("{}▌", self.rename_value)
            } else {
                tab.title.clone()
            };
            let tab_background = if tab_active {
                rgb(theme.tab_active_background)
            } else {
                rgb(theme.chrome_background)
            };
            let active_pane = tab.active_pane;
            let activate_tab = cx.listener(move |this, _event: &MouseDownEvent, window, cx| {
                this.focus_handle.focus(window, cx);
                this.focused_pane = Some(active_pane);
                this.selection = None;
                this.dispatch(
                    AppCommand::Tab(TabCommand::Activate {
                        tab_id: Some(tab_id),
                        index: None,
                    }),
                    cx,
                );
            });
            let rename_tab = self.render_sidebar_action(
                "✎",
                theme,
                move |this, _event: &MouseDownEvent, window, cx| {
                    cx.stop_propagation();
                    this.begin_rename_tab(tab_id, window, cx);
                },
                cx,
            );
            let close_tab = self.render_sidebar_action(
                "×",
                theme,
                move |this, _event: &MouseDownEvent, _window, cx| {
                    cx.stop_propagation();
                    this.dispatch(
                        AppCommand::Tab(TabCommand::Close {
                            tab_id: Some(tab_id),
                        }),
                        cx,
                    );
                },
                cx,
            );
            tabs = tabs.child(
                div()
                    .h(px(28.))
                    .w_full()
                    .pl(px(28.))
                    .pr(px(4.))
                    .gap(px(2.))
                    .items_center()
                    .flex()
                    .bg(tab_background)
                    .text_color(rgb(theme.terminal_foreground))
                    .on_mouse_down(MouseButton::Left, activate_tab)
                    .child(
                        div()
                            .flex_1()
                            .min_w(px(0.))
                            .truncate()
                            .child(SharedString::from(tab_title)),
                    )
                    .child(rename_tab)
                    .child(close_tab),
            );
        }

        div()
            .w_full()
            .flex()
            .flex_col()
            .child(workspace_row)
            .child(tabs)
            .into_any_element()
    }

    fn render_sidebar(&self, theme: ThemeColors, cx: &mut Context<Self>) -> AnyElement {
        let active_workspace = self.active_workspace_id();
        let workspaces = self.workspace_dumps();
        let mut list = div()
            .id("workspace-list")
            .flex_1()
            .min_h(px(0.))
            .overflow_y_scroll()
            .flex_col();
        for workspace in workspaces {
            list =
                list.child(self.render_sidebar_workspace(workspace, active_workspace, theme, cx));
        }
        div()
            .w(px(self.sidebar_width))
            .h_full()
            .flex()
            .flex_row()
            .bg(rgb(theme.chrome_background))
            .text_color(rgb(theme.ui_foreground))
            .child(
                div()
                    .flex_1()
                    .min_w(px(0.))
                    .h_full()
                    .flex()
                    .flex_col()
                    .child(
                        div()
                            .h(px(40.))
                            .px(px(10.))
                            .gap(px(4.))
                            .items_center()
                            .flex()
                            .bg(rgb(theme.chrome_background))
                            .child("WORKSPACES")
                            .child(div().flex_1())
                            .child(
                                div()
                                    .w(px(24.))
                                    .h(px(24.))
                                    .items_center()
                                    .justify_center()
                                    .flex()
                                    .child("+")
                                    .on_mouse_down(
                                        MouseButton::Left,
                                        cx.listener(
                                            |this, _event: &MouseDownEvent, _window, cx| {
                                                this.dispatch(
                                                    AppCommand::Workspace(WorkspaceCommand::New),
                                                    cx,
                                                );
                                            },
                                        ),
                                    ),
                            )
                            .child(
                                div()
                                    .w(px(24.))
                                    .h(px(24.))
                                    .items_center()
                                    .justify_center()
                                    .flex()
                                    .child("‹")
                                    .on_mouse_down(
                                        MouseButton::Left,
                                        cx.listener(
                                            |this, _event: &MouseDownEvent, _window, cx| {
                                                this.toggle_sidebar(cx);
                                            },
                                        ),
                                    ),
                            ),
                    )
                    .child(
                        div()
                            .h(px(32.))
                            .px(px(10.))
                            .items_center()
                            .flex()
                            .text_color(rgb(theme.terminal_foreground))
                            .child("New workspace: Cmd+Shift+N"),
                    )
                    .child(list),
            )
            .child(self.sidebar_resize_handle(theme, cx))
            .into_any_element()
    }

    fn render_collapsed_sidebar(&self, theme: ThemeColors, cx: &mut Context<Self>) -> AnyElement {
        div()
            .w(px(COLLAPSED_SIDEBAR_WIDTH))
            .h_full()
            .items_center()
            .flex()
            .flex_col()
            .bg(rgb(theme.chrome_background))
            .text_color(rgb(theme.ui_foreground))
            .child(
                div()
                    .h(px(40.))
                    .w_full()
                    .items_center()
                    .justify_center()
                    .flex()
                    .child("W")
                    .on_mouse_down(
                        MouseButton::Left,
                        cx.listener(|this, _event: &MouseDownEvent, _window, cx| {
                            this.toggle_sidebar(cx);
                        }),
                    ),
            )
            .child(div().flex_1())
            .into_any_element()
    }

    fn render_pane_tree(
        &self,
        tree: &PaneTreeDump,
        window_active: bool,
        metrics: TerminalMetrics,
        theme: ThemeColors,
        cx: &mut Context<Self>,
    ) -> AnyElement {
        self.render_pane_tree_with_grow(tree, 1.0, window_active, metrics, theme, cx)
    }

    fn render_pane_tree_with_grow(
        &self,
        tree: &PaneTreeDump,
        grow: f32,
        window_active: bool,
        metrics: TerminalMetrics,
        theme: ThemeColors,
        cx: &mut Context<Self>,
    ) -> AnyElement {
        match tree {
            PaneTreeDump::Leaf {
                pane_id,
                surface_kind,
                surface_state,
                terminal_snapshot,
                ..
            } => {
                let pane_id = *pane_id;
                let active = self.focused_pane == Some(pane_id);
                let border = if active {
                    rgb(theme.active_pane_border)
                } else {
                    rgb(theme.inactive_pane_border)
                };
                let label = match surface_kind {
                    crate::surface::SurfaceKind::Empty => "EmptySurface",
                    crate::surface::SurfaceKind::Terminal => "TerminalSurface",
                    crate::surface::SurfaceKind::Agent => "AgentSurface",
                    crate::surface::SurfaceKind::FileBrowser => "FileBrowserSurface",
                    crate::surface::SurfaceKind::ImagePreview => "ImagePreviewSurface",
                    crate::surface::SurfaceKind::MarkdownPreview => "MarkdownPreviewSurface",
                    crate::surface::SurfaceKind::Diff => "DiffSurface",
                };
                let terminal_id = match surface_state {
                    SurfaceState::Terminal(terminal) => Some(terminal.terminal_id),
                    SurfaceState::Empty(_) => None,
                };
                let mouse_modes = terminal_snapshot
                    .as_ref()
                    .map(|snapshot| snapshot.modes)
                    .unwrap_or_default();
                let content = if *surface_kind == crate::surface::SurfaceKind::Terminal {
                    terminal_snapshot
                        .as_ref()
                        .map(|snapshot| {
                            render_terminal_snapshot(
                                snapshot,
                                self.selection,
                                TerminalRenderOptions {
                                    metrics,
                                    theme,
                                    cursor_focused: active && window_active,
                                },
                                &self.config.terminal.font_family,
                                self.config.terminal.font_size,
                            )
                        })
                        .unwrap_or_else(|| {
                            div()
                                .text_color(rgb(theme.terminal_foreground))
                                .child("Starting terminal…")
                                .into_any_element()
                        })
                } else {
                    div()
                        .text_color(rgb(theme.terminal_foreground))
                        .child(SharedString::from(format!("Pane {pane_id} · {label}")))
                        .into_any_element()
                };
                let content = match surface_state {
                    SurfaceState::Terminal(terminal) => div()
                        .size_full()
                        .min_w(px(0.))
                        .min_h(px(0.))
                        .overflow_hidden()
                        .relative()
                        .child(content)
                        .child(self.terminal_resize_observer(
                            terminal.terminal_id,
                            TerminalSize::new(terminal.columns, terminal.lines),
                            metrics,
                        ))
                        .into_any_element(),
                    SurfaceState::Empty(_) => content,
                };
                div()
                    .flex_1()
                    .flex_grow(grow)
                    .min_w(px(0.))
                    .min_h(px(0.))
                    .overflow_hidden()
                    .m(px(4.))
                    .p(px(8.))
                    .border_1()
                    .border_color(border)
                    .bg(rgb(theme.pane_background))
                    .text_color(rgb(theme.terminal_foreground))
                    .child(content)
                    .on_mouse_down(
                        MouseButton::Left,
                        cx.listener(move |this, event: &MouseDownEvent, window, cx| {
                            this.focus_handle.focus(window, cx);
                            this.focused_pane = Some(pane_id);
                            if let Some(terminal_id) = terminal_id {
                                if this.begin_reported_mouse(terminal_id, event, cx) {
                                    cx.stop_propagation();
                                } else {
                                    this.begin_terminal_selection(terminal_id, event.position, cx);
                                }
                            } else {
                                this.selection = None;
                            }
                            this.dispatch(
                                AppCommand::Pane(PaneCommand::Focus {
                                    pane_id: Some(pane_id),
                                    direction: None,
                                }),
                                cx,
                            );
                        }),
                    )
                    .on_scroll_wheel(cx.listener(
                        move |this, event: &ScrollWheelEvent, window, cx| {
                            this.focus_handle.focus(window, cx);
                            if let Some(terminal_id) = this.terminal_id_for_pane(pane_id) {
                                let lines = terminal_scroll_lines(event, this.terminal_metrics);
                                if lines != 0 {
                                    if mouse_modes.mouse_reporting
                                        && this.config.features.mouse_reporting
                                    {
                                        if let Some(bytes) = terminal_mouse_input(
                                            event,
                                            TerminalMouseContext {
                                                modes: mouse_modes,
                                                bounds: this.terminal_bounds_for(terminal_id),
                                                metrics: this.terminal_metrics,
                                            },
                                        ) {
                                            this.enqueue_terminal_command(
                                                terminal_id,
                                                TerminalCommand::SendBytes {
                                                    terminal_id: Some(terminal_id),
                                                    pane_id: Some(pane_id),
                                                    bytes,
                                                },
                                            );
                                        }
                                    } else if mouse_modes.alternate_screen
                                        && mouse_modes.alternate_scroll
                                    {
                                        this.enqueue_terminal_command(
                                            terminal_id,
                                            TerminalCommand::SendText {
                                                terminal_id: Some(terminal_id),
                                                pane_id: Some(pane_id),
                                                text: terminal_alternate_scroll_input(
                                                    lines,
                                                    mouse_modes,
                                                ),
                                            },
                                        );
                                    } else {
                                        this.enqueue_terminal_command(
                                            terminal_id,
                                            TerminalCommand::Scroll {
                                                terminal_id: Some(terminal_id),
                                                pane_id: Some(pane_id),
                                                lines,
                                            },
                                        );
                                    }
                                    cx.stop_propagation();
                                }
                            }
                        },
                    ))
                    .into_any_element()
            }
            PaneTreeDump::Split {
                axis,
                ratio,
                first,
                second,
            } => {
                let ratio = ratio.clamp(0.05, 0.95);
                let mut container = div()
                    .flex_1()
                    .flex_grow(grow)
                    .flex()
                    .min_w(px(0.))
                    .min_h(px(0.))
                    .overflow_hidden();
                if *axis == SplitAxis::Horizontal {
                    container = container.flex_row();
                } else {
                    container = container.flex_col();
                }
                container
                    .child(self.render_pane_tree_with_grow(
                        first,
                        ratio,
                        window_active,
                        metrics,
                        theme,
                        cx,
                    ))
                    .child(self.render_pane_tree_with_grow(
                        second,
                        1.0 - ratio,
                        window_active,
                        metrics,
                        theme,
                        cx,
                    ))
                    .into_any_element()
            }
        }
    }

    fn render_active_tab(
        &self,
        window_active: bool,
        metrics: TerminalMetrics,
        theme: ThemeColors,
        cx: &mut Context<Self>,
    ) -> AnyElement {
        let Some(workspace) = self.snapshot.workspace.as_ref() else {
            return div()
                .flex_1()
                .items_center()
                .justify_center()
                .text_color(rgb(theme.terminal_foreground))
                .child("No workspace")
                .into_any_element();
        };
        let Some(active_tab_id) = workspace.active_tab else {
            return div()
                .flex_1()
                .items_center()
                .justify_center()
                .text_color(rgb(theme.terminal_foreground))
                .child("No tab")
                .into_any_element();
        };
        let Some(tab) = workspace.tabs.iter().find(|tab| tab.id == active_tab_id) else {
            return div()
                .flex_1()
                .items_center()
                .justify_center()
                .text_color(rgb(theme.terminal_foreground))
                .child("Active tab is unavailable")
                .into_any_element();
        };
        self.render_pane_tree(&tab.tree, window_active, metrics, theme, cx)
    }
}

impl Focusable for WorkspaceView {
    fn focus_handle(&self, _cx: &App) -> FocusHandle {
        self.focus_handle.clone()
    }
}

fn update_terminal_selection_for_snapshot(
    selection: &mut Option<TerminalSelection>,
    previous: &ModelSnapshot,
    next: &ModelSnapshot,
) {
    let Some(selection) = selection.as_mut() else {
        return;
    };
    let Some(previous_snapshot) = previous.workspace.as_ref().and_then(|workspace| {
        workspace
            .tabs
            .iter()
            .find_map(|tab| terminal_snapshot_for_id(&tab.tree, selection.terminal_id))
    }) else {
        return;
    };
    let Some(next_snapshot) = next.workspace.as_ref().and_then(|workspace| {
        workspace
            .tabs
            .iter()
            .find_map(|tab| terminal_snapshot_for_id(&tab.tree, selection.terminal_id))
    }) else {
        return;
    };
    update_terminal_selection_for_viewport(selection, previous_snapshot, next_snapshot);
}

fn update_terminal_selection_for_viewport(
    selection: &mut TerminalSelection,
    previous: &TerminalSnapshot,
    next: &TerminalSnapshot,
) {
    let delta = next
        .viewport_position
        .saturating_sub(previous.viewport_position);
    shift_terminal_selection_rows(selection, delta);
}

fn shift_terminal_selection_rows(selection: &mut TerminalSelection, delta: i64) {
    let delta = delta.clamp(i32::MIN as i64, i32::MAX as i64) as i32;
    selection.anchor.position.row = selection.anchor.position.row.saturating_add(delta);
    selection.head.position.row = selection.head.position.row.saturating_add(delta);
}

fn terminal_snapshot_for_id(
    tree: &PaneTreeDump,
    terminal_id: TerminalId,
) -> Option<&TerminalSnapshot> {
    match tree {
        PaneTreeDump::Leaf {
            surface_state,
            terminal_snapshot,
            ..
        } => match surface_state {
            SurfaceState::Terminal(terminal) if terminal.terminal_id == terminal_id => {
                terminal_snapshot.as_deref()
            }
            SurfaceState::Terminal(_) | SurfaceState::Empty(_) => None,
        },
        PaneTreeDump::Split { first, second, .. } => terminal_snapshot_for_id(first, terminal_id)
            .or_else(|| terminal_snapshot_for_id(second, terminal_id)),
    }
}

fn terminal_snapshot_for_pane(tree: &PaneTreeDump, pane_id: PaneId) -> Option<&TerminalSnapshot> {
    match tree {
        PaneTreeDump::Leaf {
            pane_id: leaf_id,
            terminal_snapshot,
            ..
        } if *leaf_id == pane_id => terminal_snapshot.as_deref(),
        PaneTreeDump::Leaf { .. } => None,
        PaneTreeDump::Split { first, second, .. } => terminal_snapshot_for_pane(first, pane_id)
            .or_else(|| terminal_snapshot_for_pane(second, pane_id)),
    }
}

fn terminal_id_for_pane(tree: &PaneTreeDump, pane_id: PaneId) -> Option<TerminalId> {
    match tree {
        PaneTreeDump::Leaf {
            pane_id: leaf_id,
            surface_state,
            ..
        } if *leaf_id == pane_id => match surface_state {
            SurfaceState::Terminal(terminal) => Some(terminal.terminal_id),
            SurfaceState::Empty(_) => None,
        },
        PaneTreeDump::Leaf { .. } => None,
        PaneTreeDump::Split { first, second, .. } => {
            terminal_id_for_pane(first, pane_id).or_else(|| terminal_id_for_pane(second, pane_id))
        }
    }
}

/// Registers window-level listeners during paint so a terminal drag keeps receiving
/// moves and release events after it crosses another pane or the root hitbox.
fn workspace_mouse_event_observer(entity: Entity<WorkspaceView>) -> AnyElement {
    canvas(
        |_bounds, _, _| {},
        move |_bounds, _, window, _| {
            let move_entity = entity.clone();
            window.on_mouse_event(move |event: &MouseMoveEvent, phase, _window, cx| {
                if phase != DispatchPhase::Capture {
                    return;
                }
                move_entity.update(cx, |view, cx| {
                    if view.dragging_sidebar {
                        view.update_sidebar_width(event.position.x, cx);
                    } else {
                        view.update_terminal_selection(event, cx);
                    }
                });
            });

            let up_entity = entity;
            window.on_mouse_event(move |event: &MouseUpEvent, phase, _window, cx| {
                if phase != DispatchPhase::Capture || event.button != MouseButton::Left {
                    return;
                }
                up_entity.update(cx, |view, cx| {
                    if view.dragging_sidebar {
                        view.dragging_sidebar = false;
                        cx.notify();
                    } else {
                        view.finish_terminal_selection(event, cx);
                    }
                });
            });
        },
    )
    .size_full()
    .absolute()
    .inset_0()
    .into_any_element()
}

fn terminal_scroll_lines(event: &ScrollWheelEvent, metrics: TerminalMetrics) -> i32 {
    let delta = match event.delta {
        ScrollDelta::Lines(delta) => delta.y,
        ScrollDelta::Pixels(delta) => f32::from(delta.y) / metrics.line_height,
    };
    if delta == 0.0 {
        return 0;
    }
    (if delta.abs() < 1.0 {
        delta.signum()
    } else {
        delta.round()
    } as i32)
        .clamp(-100, 100)
}

fn terminal_alternate_scroll_input(lines: i32, modes: TerminalModes) -> String {
    let final_character = if lines > 0 { 'A' } else { 'B' };
    let sequence = cursor_sequence(final_character, 1, modes.application_cursor);
    sequence.repeat(lines.unsigned_abs().min(100) as usize)
}

fn terminal_mouse_input(event: &ScrollWheelEvent, mouse: TerminalMouseContext) -> Option<Vec<u8>> {
    let lines = terminal_scroll_lines(event, mouse.metrics);
    if lines == 0 {
        return None;
    }
    let button = if lines > 0 { 64_u16 } else { 65_u16 };
    let count = lines.unsigned_abs().min(100) as usize;
    Some(terminal_mouse_sequence(
        button,
        count,
        event.position,
        event.modifiers,
        mouse,
        TerminalMouseReportKind::Press,
    ))
}

fn terminal_mouse_button_input(
    position: Point<gpui::Pixels>,
    button: MouseButton,
    pressed: bool,
    motion: bool,
    modifiers: gpui::Modifiers,
    mouse: TerminalMouseContext,
) -> Option<Vec<u8>> {
    let button = mouse_button_code(button)?;
    Some(terminal_mouse_sequence(
        button,
        1,
        position,
        modifiers,
        mouse,
        if pressed {
            if motion {
                TerminalMouseReportKind::Motion
            } else {
                TerminalMouseReportKind::Press
            }
        } else {
            TerminalMouseReportKind::Release
        },
    ))
}

fn terminal_mouse_sequence(
    button: u16,
    count: usize,
    position: Point<gpui::Pixels>,
    modifiers: gpui::Modifiers,
    mouse: TerminalMouseContext,
    kind: TerminalMouseReportKind,
) -> Vec<u8> {
    let release = kind == TerminalMouseReportKind::Release;
    let motion = kind == TerminalMouseReportKind::Motion;
    let modifier = u16::from(modifiers.shift) * 4
        + u16::from(modifiers.alt) * 8
        + u16::from(modifiers.control) * 16;
    let button = if release {
        3
    } else {
        button + if motion { 32 } else { 0 }
    } + modifier;
    let mouse_position = terminal_mouse_position(position, mouse.bounds, mouse.metrics);
    if mouse.modes.sgr_mouse {
        let suffix = if release { 'm' } else { 'M' };
        let mut input = Vec::with_capacity(count * 16);
        for _ in 0..count {
            input.extend_from_slice(
                format!(
                    "\u{1b}[<{button};{};{}{suffix}",
                    mouse_position.column, mouse_position.row,
                )
                .as_bytes(),
            );
        }
        return input;
    }

    let max_coordinate = if mouse.modes.utf8_mouse { 2_047 } else { 223 };
    let column = mouse_position.column.min(max_coordinate);
    let row = mouse_position.row.min(max_coordinate);
    let mut input = Vec::with_capacity(count * 6);
    for _ in 0..count {
        input.extend_from_slice(b"\x1b[M");
        push_mouse_coordinate(&mut input, 32 + button, mouse.modes.utf8_mouse);
        push_mouse_coordinate(&mut input, 32 + column as u16, mouse.modes.utf8_mouse);
        push_mouse_coordinate(&mut input, 32 + row as u16, mouse.modes.utf8_mouse);
    }
    input
}

fn push_mouse_coordinate(input: &mut Vec<u8>, value: u16, utf8: bool) {
    if utf8 {
        let mut buffer = [0; 4];
        let character = char::from_u32(u32::from(value)).expect("mouse coordinate is valid");
        input.extend_from_slice(character.encode_utf8(&mut buffer).as_bytes());
    } else {
        input.push(value as u8);
    }
}

fn mouse_button_code(button: MouseButton) -> Option<u16> {
    match button {
        MouseButton::Left => Some(0),
        MouseButton::Middle => Some(1),
        MouseButton::Right => Some(2),
        MouseButton::Navigate(_) => None,
    }
}

fn terminal_mouse_position(
    position: Point<gpui::Pixels>,
    bounds: Option<Bounds<gpui::Pixels>>,
    metrics: TerminalMetrics,
) -> TerminalMousePosition {
    let Some(bounds) = bounds else {
        return TerminalMousePosition {
            column: 1,
            row: 1,
            side: TerminalSelectionSide::Left,
        };
    };
    let relative_x = f32::from(position.x) - f32::from(bounds.origin.x);
    let relative_y = f32::from(position.y) - f32::from(bounds.origin.y);
    let x = relative_x.max(0.0);
    let y = relative_y.max(0.0);
    let side = if relative_x < 0.0 || relative_y < 0.0 {
        TerminalSelectionSide::Left
    } else if relative_x >= f32::from(bounds.size.width)
        || relative_y >= f32::from(bounds.size.height)
        || x % metrics.cell_width > metrics.cell_width / 2.0
    {
        TerminalSelectionSide::Right
    } else {
        TerminalSelectionSide::Left
    };
    TerminalMousePosition {
        column: (x / metrics.cell_width).floor() as usize + 1,
        row: (y / metrics.line_height).floor() as usize + 1,
        side,
    }
}

fn terminal_input_for_keystroke_with_modes(
    keystroke: &Keystroke,
    modes: TerminalModes,
) -> Option<String> {
    let modifiers = keystroke.modifiers;
    if modifiers.platform || modifiers.function {
        return None;
    }

    let key = keystroke.key.as_str();
    if let Some(input) = terminal_special_key_input_with_modes(key, modifiers, modes) {
        return Some(input);
    }
    if modifiers.control {
        return terminal_control_input(key, keystroke.key_char.as_deref());
    }

    let character = keystroke
        .key_char
        .as_deref()
        .filter(|character| !character.is_empty())
        .or_else(|| (key.chars().count() == 1).then_some(key))
        .or_else(|| (key == "space").then_some(" "))?;
    let mut input = String::new();
    if modifiers.alt {
        input.push('\u{1b}');
    }
    input.push_str(character);
    Some(input)
}

fn clipboard_text(item: gpui::ClipboardItem) -> Option<String> {
    item.entries.into_iter().find_map(|entry| match entry {
        gpui::ClipboardEntry::String(text) => Some(text.into_text()),
        gpui::ClipboardEntry::Image(_) | gpui::ClipboardEntry::ExternalPaths(_) => None,
    })
}

fn terminal_special_key_input_with_modes(
    key: &str,
    modifiers: gpui::Modifiers,
    modes: TerminalModes,
) -> Option<String> {
    let modifier = terminal_modifier_parameter(modifiers);
    match key {
        "enter" | "return" => Some("\r".to_owned()),
        "backspace" => Some(if modifiers.alt {
            "\u{1b}\u{7f}".to_owned()
        } else if modifiers.control {
            "\u{8}".to_owned()
        } else {
            "\u{7f}".to_owned()
        }),
        "tab" => {
            if modifiers.shift && !modifiers.control && !modifiers.alt {
                Some("\u{1b}[Z".to_owned())
            } else {
                Some("\t".to_owned())
            }
        }
        "escape" => Some("\u{1b}".to_owned()),
        "left" => Some(cursor_sequence('D', modifier, modes.application_cursor)),
        "right" => Some(cursor_sequence('C', modifier, modes.application_cursor)),
        "up" => Some(cursor_sequence('A', modifier, modes.application_cursor)),
        "down" => Some(cursor_sequence('B', modifier, modes.application_cursor)),
        "home" => Some(numbered_sequence("1", 'H', modifier)),
        "end" => Some(numbered_sequence("1", 'F', modifier)),
        "insert" => Some(numbered_sequence("2", '~', modifier)),
        "delete" => Some(numbered_sequence("3", '~', modifier)),
        "pageup" => Some(numbered_sequence("5", '~', modifier)),
        "pagedown" => Some(numbered_sequence("6", '~', modifier)),
        "f1" => Some(function_key_sequence(1, modifier)),
        "f2" => Some(function_key_sequence(2, modifier)),
        "f3" => Some(function_key_sequence(3, modifier)),
        "f4" => Some(function_key_sequence(4, modifier)),
        "f5" => Some(numbered_sequence("15", '~', modifier)),
        "f6" => Some(numbered_sequence("17", '~', modifier)),
        "f7" => Some(numbered_sequence("18", '~', modifier)),
        "f8" => Some(numbered_sequence("19", '~', modifier)),
        "f9" => Some(numbered_sequence("20", '~', modifier)),
        "f10" => Some(numbered_sequence("21", '~', modifier)),
        "f11" => Some(numbered_sequence("23", '~', modifier)),
        "f12" => Some(numbered_sequence("24", '~', modifier)),
        _ => None,
    }
}

fn terminal_control_input(key: &str, key_char: Option<&str>) -> Option<String> {
    let character = match key {
        "space" => ' ',
        _ => key_char
            .and_then(|value| value.chars().next())
            .or_else(|| key.chars().next())?,
    };
    let character = character.to_ascii_lowercase();
    let byte = match character {
        'a'..='z' => character as u8 & 0x1f,
        '@' | '2' | ' ' => 0,
        '[' | '3' => 0x1b,
        '\\' | '4' => 0x1c,
        ']' | '5' => 0x1d,
        '^' | '6' => 0x1e,
        '_' | '7' => 0x1f,
        '?' | '8' => 0x7f,
        _ => return None,
    };
    Some(char::from(byte).to_string())
}

fn terminal_modifier_parameter(modifiers: gpui::Modifiers) -> u8 {
    1 + u8::from(modifiers.shift) + 2 * u8::from(modifiers.alt) + 4 * u8::from(modifiers.control)
}

fn cursor_sequence(final_character: char, modifier: u8, application_cursor: bool) -> String {
    if modifier == 1 && application_cursor {
        format!("\u{1b}O{final_character}")
    } else if modifier == 1 {
        format!("\u{1b}[{final_character}")
    } else {
        format!("\u{1b}[1;{modifier}{final_character}")
    }
}

fn numbered_sequence(number: &str, final_character: char, modifier: u8) -> String {
    if modifier == 1 {
        format!("\u{1b}[{number}{final_character}")
    } else {
        format!("\u{1b}[{number};{modifier}{final_character}")
    }
}

fn function_key_sequence(number: u8, modifier: u8) -> String {
    if modifier == 1 {
        return match number {
            1 => "\u{1b}OP".to_owned(),
            2 => "\u{1b}OQ".to_owned(),
            3 => "\u{1b}OR".to_owned(),
            4 => "\u{1b}OS".to_owned(),
            _ => unreachable!("function key sequence only supports F1-F4"),
        };
    }
    let final_character = match number {
        1 => 'P',
        2 => 'Q',
        3 => 'R',
        4 => 'S',
        _ => unreachable!("function key sequence only supports F1-F4"),
    };
    format!("\u{1b}[1;{modifier}{final_character}")
}

fn is_terminal_cell_selected(
    snapshot: &TerminalSnapshot,
    selection: Option<TerminalSelection>,
    terminal_id: TerminalId,
    row: usize,
    column: usize,
) -> bool {
    let Some(selection) = selection else {
        return false;
    };
    if selection.terminal_id != terminal_id {
        return false;
    }
    let Some((start, end)) = selection_bounds(snapshot, selection) else {
        return false;
    };
    (start..=end).contains(&TerminalCellPosition {
        row: row as i32,
        column,
    })
}

fn selection_boundary_index(endpoint: TerminalSelectionEndpoint, columns: usize) -> i64 {
    i64::from(endpoint.position.row) * columns as i64
        + endpoint.position.column as i64
        + i64::from(endpoint.side == TerminalSelectionSide::Right)
}

fn selection_bounds(
    snapshot: &TerminalSnapshot,
    selection: TerminalSelection,
) -> Option<(TerminalCellPosition, TerminalCellPosition)> {
    let columns = snapshot.size.columns;
    let total_cells = snapshot.cells.len();
    let anchor_boundary = selection_boundary_index(selection.anchor, columns);
    let head_boundary = selection_boundary_index(selection.head, columns);
    let (start_endpoint, end_endpoint) = if anchor_boundary <= head_boundary {
        (selection.anchor, selection.head)
    } else {
        (selection.head, selection.anchor)
    };
    let total_cells_i64 = total_cells as i64;
    let mut start =
        selection_boundary_index(start_endpoint, columns).clamp(0, total_cells_i64) as usize;
    let mut end =
        selection_boundary_index(end_endpoint, columns).clamp(0, total_cells_i64) as usize;
    if start >= end {
        return None;
    }

    if snapshot
        .cells
        .get(start)
        .is_some_and(|cell| cell.flags.wide_spacer)
    {
        start = start.saturating_sub(1);
    }
    if end > start
        && snapshot
            .cells
            .get(end.saturating_sub(1))
            .is_some_and(|cell| cell.flags.wide)
    {
        end = end.saturating_add(1).min(total_cells);
    }
    if start >= end {
        return None;
    }
    Some((
        TerminalCellPosition {
            row: (start / columns) as i32,
            column: start % columns,
        },
        TerminalCellPosition {
            row: ((end - 1) / columns) as i32,
            column: (end - 1) % columns,
        },
    ))
}

fn selected_terminal_text(snapshot: &TerminalSnapshot, selection: TerminalSelection) -> String {
    let Some((start, end)) = selection_bounds(snapshot, selection) else {
        return String::new();
    };
    let mut text = String::new();
    for row in start.row..=end.row {
        let row_index = row as usize;
        let first_column = if row == start.row { start.column } else { 0 };
        let last_column = if row == end.row {
            end.column
        } else {
            snapshot.size.columns.saturating_sub(1)
        };
        let line_start = text.len();
        for column in first_column..=last_column {
            let Some(cell) = snapshot.cell(row_index, column) else {
                continue;
            };
            if cell.flags.wide_spacer {
                continue;
            }
            text.push(cell.character);
            text.extend(cell.zerowidth.iter().copied());
        }
        let line = &text[line_start..];
        let trimmed_len = line.trim_end_matches(' ').len();
        text.truncate(line_start + trimmed_len);
        if row != end.row {
            let wrapped = snapshot
                .cell(row_index, snapshot.size.columns.saturating_sub(1))
                .is_some_and(|cell| cell.flags.wrapline);
            if !wrapped {
                text.push('\n');
            }
        }
    }
    text
}

fn render_terminal_snapshot(
    snapshot: &TerminalSnapshot,
    selection: Option<TerminalSelection>,
    options: TerminalRenderOptions,
    font_family: &str,
    font_size: f32,
) -> AnyElement {
    let TerminalRenderOptions {
        metrics,
        theme,
        cursor_focused,
    } = options;
    let mut terminal = div()
        .size_full()
        .flex()
        .min_w(px(0.))
        .min_h(px(0.))
        .flex_col()
        .overflow_hidden()
        .font_family(font_family.to_owned())
        .text_size(px(font_size))
        .line_height(px(metrics.line_height))
        .whitespace_nowrap()
        .bg(rgb(theme.terminal_background));
    for row in 0..snapshot.size.lines {
        let mut row_element = div()
            .relative()
            .w(px(metrics.cell_width * snapshot.size.columns as f32))
            .h(px(metrics.line_height))
            .min_w(px(0.))
            .flex_none()
            .whitespace_nowrap();
        let mut current: Option<(
            usize,
            TerminalColor,
            TerminalColor,
            TerminalCellFlags,
            String,
            usize,
            bool,
        )> = None;
        for column in 0..snapshot.size.columns {
            let Some(cell) = snapshot.cell(row, column) else {
                continue;
            };
            if cell.flags.wide_spacer {
                continue;
            }
            let mut foreground = cell.fg;
            let mut background = cell.bg;
            let default_colors = cell.fg == TerminalColor::Named { value: 256 }
                && cell.bg == TerminalColor::Named { value: 257 };
            if cell.flags.inverse {
                std::mem::swap(&mut foreground, &mut background);
                if default_colors {
                    foreground = theme_color(theme.inverse_foreground);
                    background = theme_color(theme.inverse_background);
                }
            }
            if is_terminal_cell_selected(snapshot, selection, snapshot.terminal_id, row, column) {
                background = theme_color(theme.selection_background);
            }
            let cursor_at_cell = snapshot.cursor.visible
                && snapshot.cursor.row == row
                && snapshot.cursor.column == column;
            if cursor_at_cell && cursor_focused {
                foreground = theme_color(theme.cursor_foreground);
                background = theme_color(theme.cursor_background);
            }
            let cursor_hollow = cursor_at_cell && !cursor_focused;
            let mut character = String::new();
            character.push(cell.character);
            character.extend(cell.zerowidth.iter().copied());
            let width_columns = if cell.flags.wide { 2 } else { 1 };
            let should_merge = current
                .as_ref()
                .map(
                    |(_, current_fg, current_bg, current_flags, _, _, current_cursor_hollow)| {
                        *current_fg == foreground
                            && *current_bg == background
                            && *current_flags == cell.flags
                            && *current_cursor_hollow == cursor_hollow
                    },
                )
                .unwrap_or(false);
            if should_merge {
                if let Some((_, _, _, _, text, width_columns_total, _)) = current.as_mut() {
                    text.push_str(&character);
                    *width_columns_total += width_columns;
                }
            } else {
                if let Some((start_column, fg, bg, flags, text, width_columns, cursor_hollow)) =
                    current.take()
                {
                    row_element = row_element.child(render_terminal_run(
                        start_column,
                        fg,
                        bg,
                        flags,
                        text,
                        width_columns,
                        TerminalRunStyle {
                            metrics,
                            theme,
                            cursor_hollow,
                        },
                    ));
                }
                current = Some((
                    column,
                    foreground,
                    background,
                    cell.flags,
                    character,
                    width_columns,
                    cursor_hollow,
                ));
            }
        }
        if let Some((start_column, fg, bg, flags, text, width_columns, cursor_hollow)) = current {
            row_element = row_element.child(render_terminal_run(
                start_column,
                fg,
                bg,
                flags,
                text,
                width_columns,
                TerminalRunStyle {
                    metrics,
                    theme,
                    cursor_hollow,
                },
            ));
        }
        terminal = terminal.child(row_element);
    }
    terminal.into_any_element()
}

fn render_terminal_run(
    start_column: usize,
    foreground: TerminalColor,
    background: TerminalColor,
    flags: TerminalCellFlags,
    text: String,
    width_columns: usize,
    style: TerminalRunStyle,
) -> AnyElement {
    let TerminalRunStyle {
        metrics,
        theme,
        cursor_hollow,
    } = style;
    let mut run = div()
        .absolute()
        .left(px(metrics.cell_width * start_column as f32))
        .top(px(0.))
        .w(px(metrics.cell_width * width_columns as f32))
        .h(px(metrics.line_height))
        .flex_none()
        .text_color(rgb(color_to_rgb(foreground, true, theme)))
        .bg(rgb(color_to_rgb(background, false, theme)));
    if cursor_hollow {
        run = run.border_1().border_color(rgb(theme.inactive_cursor));
    }
    if flags.bold {
        run = run.font_weight(FontWeight::BOLD);
    }
    if flags.italic {
        run = run.italic();
    }
    if flags.underline {
        run = run.underline();
    }
    if flags.strike {
        run = run.line_through();
    }
    run.child(SharedString::from(text)).into_any_element()
}

fn color_to_rgb(color: TerminalColor, foreground: bool, theme: ThemeColors) -> u32 {
    match color {
        TerminalColor::Rgb { red, green, blue } => {
            (u32::from(red) << 16) | (u32::from(green) << 8) | u32::from(blue)
        }
        TerminalColor::Named { value } => match value {
            0..=15 => basic_color(value as usize),
            256 if foreground => theme.terminal_foreground,
            257 if !foreground => theme.terminal_background,
            258 => theme.terminal_foreground,
            259 => theme.terminal_background,
            _ => theme.ui_foreground,
        },
        TerminalColor::Indexed { value } => indexed_color(value),
    }
}

fn theme_color(value: u32) -> TerminalColor {
    TerminalColor::Rgb {
        red: ((value >> 16) & 0xff) as u8,
        green: ((value >> 8) & 0xff) as u8,
        blue: (value & 0xff) as u8,
    }
}

fn basic_color(index: usize) -> u32 {
    const COLORS: [u32; 16] = [
        0x1d2733, 0xcd3131, 0x0dbc79, 0xe5e510, 0x2472c8, 0xbc3fbc, 0x11a8cd, 0xe5e5e5, 0x666666,
        0xf14c4c, 0x23d18b, 0xf5f543, 0x3b8eea, 0xd670d6, 0x29b8db, 0xffffff,
    ];
    COLORS[index.min(COLORS.len() - 1)]
}

fn indexed_color(index: u8) -> u32 {
    if index < 16 {
        return basic_color(index as usize);
    }
    if (16..=231).contains(&index) {
        let index = index - 16;
        let red = index / 36;
        let green = (index % 36) / 6;
        let blue = index % 6;
        let channel = |value: u8| {
            if value == 0 {
                0
            } else {
                55 + 40 * u32::from(value)
            }
        };
        return (channel(red) << 16) | (channel(green) << 8) | channel(blue);
    }
    let gray = 8 + 10 * u32::from(index - 232);
    (gray << 16) | (gray << 8) | gray
}

impl Render for WorkspaceView {
    fn render(&mut self, window: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        let metrics = self.measured_terminal_metrics(window);
        self.terminal_metrics = metrics;
        let theme = self.config.theme.colors();

        let window_active = window.is_window_active() && self.focus_handle.is_focused(window);
        let tab_data: Vec<(TabId, String, bool, PaneId)> = self
            .snapshot
            .workspace
            .as_ref()
            .map(|workspace| {
                workspace
                    .tabs
                    .iter()
                    .map(|tab| {
                        (
                            tab.id,
                            tab.title.clone(),
                            workspace.active_tab == Some(tab.id),
                            tab.active_pane,
                        )
                    })
                    .collect()
            })
            .unwrap_or_default();
        let workspace_title = self
            .snapshot
            .workspace
            .as_ref()
            .map(|workspace| workspace.title.clone())
            .unwrap_or_else(|| "No workspace".to_owned());
        let workspace_id = self
            .snapshot
            .workspace
            .as_ref()
            .map(|workspace| workspace.id.to_string())
            .unwrap_or_else(|| "none".to_owned());
        let revision = self.snapshot.state_revision;

        let tab_bar = div()
            .h(px(40.))
            .px(px(8.))
            .gap(px(4.))
            .items_center()
            .flex()
            .bg(rgb(theme.chrome_background))
            .children(
                tab_data
                    .into_iter()
                    .map(|(tab_id, title, active, active_pane)| {
                        self.tab_button(tab_id, title, active, active_pane, theme, cx)
                    }),
            )
            .child(
                div()
                    .h(px(32.))
                    .w(px(32.))
                    .items_center()
                    .justify_center()
                    .flex()
                    .bg(rgb(theme.tab_add_background))
                    .text_color(rgb(theme.ui_foreground))
                    .child("+")
                    .on_mouse_down(
                        MouseButton::Left,
                        cx.listener(|this, _event: &MouseDownEvent, window, cx| {
                            this.focus_handle.focus(window, cx);
                            this.dispatch(AppCommand::Tab(TabCommand::New { title: None }), cx);
                        }),
                    ),
            );

        let content = div()
            .flex_1()
            .flex()
            .flex_col()
            .min_w(px(0.))
            .min_h(px(0.))
            .bg(rgb(theme.terminal_background))
            .child(
                div()
                    .h(px(36.))
                    .px(px(12.))
                    .items_center()
                    .flex()
                    .bg(rgb(theme.chrome_background))
                    .child(SharedString::from(format!(
                        "Water · {workspace_title} ({workspace_id}) · revision {revision}"
                    )))
                    .child(div().flex_1())
                    .child("Cmd+E sidebar"),
            )
            .child(tab_bar)
            .child(
                div()
                    .flex_1()
                    .flex()
                    .min_w(px(0.))
                    .min_h(px(0.))
                    .overflow_hidden()
                    .child(self.render_active_tab(window_active, metrics, theme, cx)),
            );

        let sidebar = if self.sidebar_collapsed {
            self.render_collapsed_sidebar(theme, cx)
        } else {
            self.render_sidebar(theme, cx)
        };

        div()
            .size_full()
            .flex()
            .track_focus(&self.focus_handle)
            .on_key_down(cx.listener(|this, event: &KeyDownEvent, _window, cx| {
                this.handle_key_down(event, cx);
            }))
            .bg(rgb(theme.terminal_background))
            .text_color(rgb(theme.ui_foreground))
            .child(workspace_mouse_event_observer(cx.entity()))
            .child(sidebar)
            .child(content)
    }
}

/// Bridges the model thread's change-only snapshot stream into the GPUI entity.
/// The channel receive blocks only inside GPUI's background executor; the UI
/// thread is resumed only when a new model revision exists.
pub fn spawn_snapshot_listener(
    cx: &mut App,
    entity: Entity<WorkspaceView>,
    receiver: ModelSnapshotReceiver,
) -> Task<()> {
    let receiver = Arc::new(receiver);
    cx.spawn(async move |cx| {
        loop {
            let receiver_for_worker = receiver.clone();
            let snapshot = cx
                .background_executor()
                .spawn(async move { receiver_for_worker.recv().ok() })
                .await;
            let Some(snapshot) = snapshot else {
                break;
            };
            entity.update(cx, |view, cx| {
                view.install_snapshot(snapshot, cx);
            });
        }
    })
}

#[allow(dead_code)]
fn _pane_id_is_explicitly_typed(_pane_id: PaneId) {}

#[allow(dead_code)]
fn _tab_dump_is_a_projection(_tab: &TabDump) {}

#[cfg(test)]
mod tests {
    use super::*;
    use gpui::Modifiers;

    fn keystroke(key: &str, key_char: Option<&str>, modifiers: Modifiers) -> Keystroke {
        Keystroke {
            key: key.to_owned(),
            key_char: key_char.map(str::to_owned),
            modifiers,
        }
    }

    fn endpoint(
        row: usize,
        column: usize,
        side: TerminalSelectionSide,
    ) -> TerminalSelectionEndpoint {
        TerminalSelectionEndpoint {
            position: TerminalCellPosition {
                row: row as i32,
                column,
            },
            side,
        }
    }

    #[test]
    fn terminal_input_preserves_printable_text_and_control_bytes() {
        assert_eq!(
            terminal_input_for_keystroke_with_modes(
                &keystroke("a", Some("a"), Modifiers::none()),
                TerminalModes::default(),
            ),
            Some("a".to_owned())
        );
        assert_eq!(
            terminal_input_for_keystroke_with_modes(
                &keystroke("c", Some("c"), Modifiers::control()),
                TerminalModes::default(),
            ),
            Some("\u{3}".to_owned())
        );
        assert_eq!(
            terminal_input_for_keystroke_with_modes(
                &keystroke("backspace", None, Modifiers::none()),
                TerminalModes::default(),
            ),
            Some("\u{7f}".to_owned())
        );
        assert_eq!(
            terminal_input_for_keystroke_with_modes(
                &keystroke("enter", None, Modifiers::none()),
                TerminalModes::default(),
            ),
            Some("\r".to_owned())
        );
    }

    #[test]
    fn terminal_selection_extracts_text_without_wide_spacers_or_padding() {
        let terminal_id = TerminalId::new(1);
        let mut snapshot = TerminalSnapshot::empty(terminal_id, TerminalSize::new(8, 2));
        for (column, character) in "hello".chars().enumerate() {
            snapshot.cells[column].character = character;
        }
        for (column, character) in "world".chars().enumerate() {
            snapshot.cells[snapshot.size.columns + column].character = character;
        }
        snapshot.cells[5].flags.wide_spacer = true;
        snapshot.cells[4].character = '界';
        snapshot.cells[4].flags.wide = true;
        let selection = TerminalSelection {
            terminal_id,
            anchor: endpoint(0, 0, TerminalSelectionSide::Left),
            head: endpoint(1, 7, TerminalSelectionSide::Right),
        };
        assert_eq!(
            selected_terminal_text(&snapshot, selection),
            "hell界\nworld"
        );
    }

    #[test]
    fn selection_sides_choose_only_fully_covered_cells() {
        let terminal_id = TerminalId::new(1);
        let mut snapshot = TerminalSnapshot::empty(terminal_id, TerminalSize::new(4, 1));
        for (column, character) in "abcd".chars().enumerate() {
            snapshot.cells[column].character = character;
        }

        let first_cell = TerminalSelection {
            terminal_id,
            anchor: endpoint(0, 0, TerminalSelectionSide::Left),
            head: endpoint(0, 1, TerminalSelectionSide::Left),
        };
        assert_eq!(selection_bounds(&snapshot, first_cell).unwrap().1.column, 0);

        let boundary_only = TerminalSelection {
            terminal_id,
            anchor: endpoint(0, 0, TerminalSelectionSide::Right),
            head: endpoint(0, 1, TerminalSelectionSide::Left),
        };
        assert!(selection_bounds(&snapshot, boundary_only).is_none());

        let second_cell = TerminalSelection {
            terminal_id,
            anchor: endpoint(0, 0, TerminalSelectionSide::Right),
            head: endpoint(0, 1, TerminalSelectionSide::Right),
        };
        let (start, end) = selection_bounds(&snapshot, second_cell).unwrap();
        assert_eq!((start.column, end.column), (1, 1));
    }

    #[test]
    fn selection_follows_user_viewport_scroll() {
        let terminal_id = TerminalId::new(1);
        let mut previous = TerminalSnapshot::empty(terminal_id, TerminalSize::new(8, 4));
        previous.display_offset = 2;
        previous.viewport_position = 2;
        let mut next = previous.clone();
        next.display_offset = 6;
        next.viewport_position = 6;

        let mut selection = TerminalSelection {
            terminal_id,
            anchor: endpoint(1, 2, TerminalSelectionSide::Left),
            head: endpoint(2, 4, TerminalSelectionSide::Right),
        };
        update_terminal_selection_for_viewport(&mut selection, &previous, &next);

        assert_eq!(selection.anchor.position.row, 5);
        assert_eq!(selection.head.position.row, 6);
    }

    #[test]
    fn output_growth_does_not_move_selection_in_a_pinned_viewport() {
        let terminal_id = TerminalId::new(1);
        let mut previous = TerminalSnapshot::empty(terminal_id, TerminalSize::new(8, 4));
        previous.display_offset = 2;
        previous.viewport_position = 2;
        let mut next = previous.clone();
        next.display_offset = 6;
        // Output can increase display_offset while the pinned viewport stays
        // on the same visible cells. It must not move the selection highlight.
        next.viewport_position = previous.viewport_position;

        let mut selection = TerminalSelection {
            terminal_id,
            anchor: endpoint(1, 2, TerminalSelectionSide::Left),
            head: endpoint(2, 4, TerminalSelectionSide::Right),
        };
        update_terminal_selection_for_viewport(&mut selection, &previous, &next);

        assert_eq!(selection.anchor.position.row, 1);
        assert_eq!(selection.head.position.row, 2);
    }

    #[test]
    fn wide_character_selection_expands_to_both_grid_cells() {
        let terminal_id = TerminalId::new(1);
        let mut snapshot = TerminalSnapshot::empty(terminal_id, TerminalSize::new(5, 1));
        snapshot.cells[1].character = '界';
        snapshot.cells[1].flags.wide = true;
        snapshot.cells[2].flags.wide_spacer = true;
        let selection = TerminalSelection {
            terminal_id,
            anchor: endpoint(0, 0, TerminalSelectionSide::Left),
            head: endpoint(0, 1, TerminalSelectionSide::Right),
        };

        let (_, end) = selection_bounds(&snapshot, selection).unwrap();
        assert_eq!(end.column, 2);
        assert!(is_terminal_cell_selected(
            &snapshot,
            Some(selection),
            terminal_id,
            0,
            1,
        ));
        assert!(is_terminal_cell_selected(
            &snapshot,
            Some(selection),
            terminal_id,
            0,
            2,
        ));

        let half_cell_drag = TerminalSelection {
            terminal_id,
            anchor: endpoint(0, 1, TerminalSelectionSide::Left),
            head: endpoint(0, 2, TerminalSelectionSide::Right),
        };
        assert!(is_terminal_cell_selected(
            &snapshot,
            Some(half_cell_drag),
            terminal_id,
            0,
            1,
        ));
        assert!(is_terminal_cell_selected(
            &snapshot,
            Some(half_cell_drag),
            terminal_id,
            0,
            2,
        ));
    }

    #[test]
    fn terminal_input_honors_application_cursor_and_mouse_reporting_modes() {
        assert_eq!(
            terminal_input_for_keystroke_with_modes(
                &keystroke("up", None, Modifiers::none()),
                TerminalModes {
                    application_cursor: true,
                    ..TerminalModes::default()
                },
            ),
            Some("\u{1b}OA".to_owned())
        );
        let event = ScrollWheelEvent {
            delta: ScrollDelta::Lines(Point { x: 0.0, y: 1.0 }),
            ..ScrollWheelEvent::default()
        };
        assert_eq!(
            terminal_mouse_input(
                &event,
                TerminalMouseContext {
                    modes: TerminalModes {
                        mouse_reporting: true,
                        sgr_mouse: true,
                        ..TerminalModes::default()
                    },
                    bounds: None,
                    metrics: TerminalMetrics::default(),
                },
            ),
            Some(b"\x1b[<64;1;1M".to_vec())
        );
    }

    #[test]
    fn terminal_input_encodes_navigation_and_alt_sequences() {
        assert_eq!(
            terminal_input_for_keystroke_with_modes(
                &keystroke("up", None, Modifiers::none()),
                TerminalModes::default(),
            ),
            Some("\u{1b}[A".to_owned())
        );
        assert_eq!(
            terminal_input_for_keystroke_with_modes(
                &keystroke(
                    "left",
                    None,
                    Modifiers {
                        control: true,
                        ..Modifiers::none()
                    },
                ),
                TerminalModes::default(),
            ),
            Some("\u{1b}[1;5D".to_owned())
        );
        assert_eq!(
            terminal_input_for_keystroke_with_modes(
                &keystroke(
                    "x",
                    Some("x"),
                    Modifiers {
                        alt: true,
                        ..Modifiers::none()
                    },
                ),
                TerminalModes::default(),
            ),
            Some("\u{1b}x".to_owned())
        );
        assert_eq!(
            terminal_input_for_keystroke_with_modes(
                &keystroke(
                    "tab",
                    None,
                    Modifiers {
                        shift: true,
                        ..Modifiers::none()
                    },
                ),
                TerminalModes::default(),
            ),
            Some("\u{1b}[Z".to_owned())
        );
    }

    #[gpui::test]
    fn focused_workspace_routes_keystrokes_to_real_zsh(cx: &mut gpui::TestAppContext) {
        let mut host = crate::app::ModelHost::start();
        let client = host.client();
        let workspace = client
            .dispatch(crate::command::AppCommand::Workspace(
                crate::command::WorkspaceCommand::Create,
            ))
            .unwrap();
        assert_eq!(
            client.wait_operation(workspace).unwrap().status,
            crate::command::OperationStatus::Succeeded
        );
        let tab = client
            .dispatch(crate::command::AppCommand::Tab(
                crate::command::TabCommand::New { title: None },
            ))
            .unwrap();
        assert_eq!(
            client.wait_operation(tab).unwrap().status,
            crate::command::OperationStatus::Succeeded
        );
        let program = crate::terminal::default_shell_program();
        let spawn = client
            .dispatch(crate::command::AppCommand::Terminal(
                crate::command::TerminalCommand::Spawn {
                    pane_id: None,
                    args: crate::terminal::default_shell_args(&program),
                    program,
                    columns: 80,
                    lines: 24,
                },
            ))
            .unwrap();
        let spawn = client.wait_operation(spawn).unwrap();
        let _initial_terminal_id = match spawn.result.unwrap() {
            crate::command::OperationResult::TerminalSpawned { terminal_id } => terminal_id,
            result => panic!("unexpected result: {result:?}"),
        };
        let snapshot = client.state_dump().unwrap();
        let (view, cx) = cx.add_window_view(|_, cx| {
            WorkspaceView::new(client.clone(), snapshot.clone(), cx.focus_handle())
        });
        view.update_in(cx, |view, window, cx| {
            window.focus(&view.focus_handle, cx);
        });
        cx.simulate_keystrokes("cmd-\\");
        let split_state = client.state_dump().unwrap();
        let split_tree = &split_state.workspace.as_ref().unwrap().tabs[0].tree;
        assert_eq!(split_tree.pane_count(), 2);
        assert_tree_is_terminal(split_tree);

        cx.simulate_keystrokes("cmd--");
        let vertical_split_state = client.state_dump().unwrap();
        let vertical_split_tab = &vertical_split_state.workspace.as_ref().unwrap().tabs[0];
        let vertical_split_tree = &vertical_split_tab.tree;
        assert_eq!(vertical_split_tree.pane_count(), 3);
        assert_tree_is_terminal(vertical_split_tree);
        let active_terminal_id =
            terminal_id_for_pane(vertical_split_tree, vertical_split_tab.active_pane).unwrap();

        cx.simulate_input("print -r -- UI_KEYBOARD_READY");
        cx.simulate_keystrokes("enter");
        client
            .terminal_contains(
                active_terminal_id,
                "UI_KEYBOARD_READY",
                std::time::Duration::from_secs(5),
            )
            .unwrap();

        cx.simulate_keystrokes("cmd-t");
        let tab_state = client.state_dump().unwrap();
        let workspace = tab_state.workspace.as_ref().unwrap();
        assert_eq!(workspace.tabs.len(), 2);
        assert_tree_is_terminal(&workspace.tabs[1].tree);

        host.shutdown();
    }

    fn assert_tree_is_terminal(tree: &PaneTreeDump) {
        match tree {
            PaneTreeDump::Leaf {
                surface_kind,
                surface_state,
                ..
            } => {
                assert_eq!(*surface_kind, crate::surface::SurfaceKind::Terminal);
                assert!(matches!(surface_state, SurfaceState::Terminal(_)));
            }
            PaneTreeDump::Split { first, second, .. } => {
                assert_tree_is_terminal(first);
                assert_tree_is_terminal(second);
            }
        }
    }
}
