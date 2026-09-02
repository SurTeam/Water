use std::collections::BTreeMap;
use std::ops::Range;
use std::sync::{Arc, Mutex};

use gpui::{
    AnyElement, App, Bounds, Context, CursorStyle, DispatchPhase, Entity, EntityInputHandler,
    FocusHandle, Focusable, InputHandler, KeyDownEvent, Keystroke, MouseButton, MouseDownEvent,
    MouseMoveEvent, MouseUpEvent, Point, ScrollDelta, ScrollWheelEvent, ShapedLine, SharedString,
    StrikethroughStyle, TextAlign, TextInputConfiguration, TextRun, UTF16Selection, UnderlineStyle,
    Window, WindowControlArea, anchored, canvas, deferred, div, fill, font, outline, point,
    prelude::*, px, relative, rgb, rgba, size,
};

use crate::app::model::{PaneTreeDump, TabDump, WorkspaceDump};
use crate::app::{CommandClient, ModelSnapshot};
use crate::command::{
    AppCommand, FocusDirection, PaneCommand, SplitDirection, TabCommand, TerminalCommand,
    WorkspaceCommand,
};
use crate::config::{AppConfig, ThemeColors};
use crate::ids::{PaneId, TabId, TerminalId, WorkspaceId};
use crate::pane::SplitAxis;
use crate::surface::SurfaceState;
use crate::terminal::{TerminalColor, TerminalModes, TerminalSize, TerminalSnapshot};

use super::application::{
    HideWindow, IgnoreQuit, MinimizeWindow, NewTerminalTab, NewWorkspace, RenameTab,
    RenameWorkspace, SplitDown, SplitRight, ToggleSidebar,
};

const DEFAULT_TERMINAL_CELL_WIDTH: f32 = 8.4;
const DEFAULT_TERMINAL_LINE_HEIGHT: f32 = 18.0;
const DEFAULT_SIDEBAR_WIDTH: f32 = 236.0;
const MIN_SIDEBAR_WIDTH: f32 = 170.0;
const MAX_SIDEBAR_WIDTH: f32 = 420.0;
const SIDEBAR_RESIZE_HANDLE_WIDTH: f32 = 6.0;

#[derive(Debug, Clone, Copy)]
struct TerminalMetrics {
    cell_width: f32,
    line_height: f32,
    scale_factor: f32,
}

impl Default for TerminalMetrics {
    fn default() -> Self {
        Self {
            cell_width: DEFAULT_TERMINAL_CELL_WIDTH,
            line_height: DEFAULT_TERMINAL_LINE_HEIGHT,
            scale_factor: 1.0,
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

#[derive(Debug, Clone, Copy, PartialEq)]
struct ContextMenuState {
    target: ContextMenuTarget,
    position: Point<gpui::Pixels>,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum ContextMenuTarget {
    Workspace(WorkspaceId),
    Tab(TabId),
}

/// In-window dialogs are kept as view state so their presentation and
/// dismissal share one path without introducing model-owned UI state.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum DialogState {
    ConfirmCloseWorkspace { workspace_id: WorkspaceId },
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
struct TerminalBackgroundSpan {
    start_column: usize,
    width_columns: usize,
    color: u32,
}

struct TerminalTextCell {
    text: String,
    run: TextRun,
    width_columns: usize,
}

struct TerminalTextChunk {
    start_column: usize,
    width_columns: usize,
    span_columns: usize,
    requires_cell_scaling: bool,
    text: String,
    runs: Vec<TextRun>,
    cells: Vec<TerminalTextCell>,
}

struct TerminalTextPaint {
    start_column: usize,
    line: ShapedLine,
}

struct TerminalRowPaint {
    text: Vec<TerminalTextPaint>,
    backgrounds: Vec<TerminalBackgroundSpan>,
}

struct TerminalPrepaintState {
    rows: Vec<TerminalRowPaint>,
    ime_line: Option<(ShapedLine, usize, usize)>,
}

struct TerminalRenderElement {
    snapshot: TerminalSnapshot,
    selection: Option<TerminalSelection>,
    options: TerminalRenderOptions,
    font_family: String,
    font_size: f32,
    ime_text: Option<String>,
    terminal_bounds: Arc<Mutex<BTreeMap<TerminalId, Bounds<gpui::Pixels>>>>,
    input_handler: Option<(Entity<WorkspaceView>, FocusHandle)>,
}

struct TerminalInputHandler {
    view: Entity<WorkspaceView>,
    terminal_id: TerminalId,
    element_bounds: Bounds<gpui::Pixels>,
    cursor: (usize, usize),
}

impl TerminalInputHandler {
    fn update_view<R>(
        &self,
        cx: &mut App,
        update: impl FnOnce(&mut WorkspaceView, &mut Context<WorkspaceView>) -> R,
    ) -> R {
        let fallback_terminal_id = self.terminal_id;
        self.view.update(cx, |view, cx| {
            let terminal_id = view.active_terminal_id().unwrap_or(fallback_terminal_id);
            view.ime_terminal = Some(terminal_id);
            update(view, cx)
        })
    }
}

impl InputHandler for TerminalInputHandler {
    fn selected_text_range(
        &mut self,
        ignore_disabled_input: bool,
        window: &mut Window,
        cx: &mut App,
    ) -> Option<UTF16Selection> {
        self.update_view(cx, |view, cx| {
            <WorkspaceView as EntityInputHandler>::selected_text_range(
                view,
                ignore_disabled_input,
                window,
                cx,
            )
        })
    }

    fn marked_text_range(&mut self, window: &mut Window, cx: &mut App) -> Option<Range<usize>> {
        self.update_view(cx, |view, cx| {
            <WorkspaceView as EntityInputHandler>::marked_text_range(view, window, cx)
        })
    }

    fn text_for_range(
        &mut self,
        range_utf16: Range<usize>,
        adjusted_range: &mut Option<Range<usize>>,
        window: &mut Window,
        cx: &mut App,
    ) -> Option<String> {
        self.update_view(cx, |view, cx| {
            <WorkspaceView as EntityInputHandler>::text_for_range(
                view,
                range_utf16,
                adjusted_range,
                window,
                cx,
            )
        })
    }

    fn replace_text_in_range(
        &mut self,
        replacement_range: Option<Range<usize>>,
        text: &str,
        window: &mut Window,
        cx: &mut App,
    ) {
        self.update_view(cx, |view, cx| {
            <WorkspaceView as EntityInputHandler>::replace_text_in_range(
                view,
                replacement_range,
                text,
                window,
                cx,
            )
        });
    }

    fn replace_and_mark_text_in_range(
        &mut self,
        range_utf16: Option<Range<usize>>,
        new_text: &str,
        new_selected_range: Option<Range<usize>>,
        window: &mut Window,
        cx: &mut App,
    ) {
        self.update_view(cx, |view, cx| {
            <WorkspaceView as EntityInputHandler>::replace_and_mark_text_in_range(
                view,
                range_utf16,
                new_text,
                new_selected_range,
                window,
                cx,
            )
        });
    }

    fn unmark_text(&mut self, window: &mut Window, cx: &mut App) {
        self.update_view(cx, |view, cx| {
            <WorkspaceView as EntityInputHandler>::unmark_text(view, window, cx)
        });
    }

    fn bounds_for_range(
        &mut self,
        _range_utf16: Range<usize>,
        _window: &mut Window,
        cx: &mut App,
    ) -> Option<Bounds<gpui::Pixels>> {
        let element_bounds = self.element_bounds;
        let fallback_terminal_id = self.terminal_id;
        let fallback_cursor = self.cursor;
        self.update_view(cx, |view, _cx| {
            let terminal_id = view.active_terminal_id().unwrap_or(fallback_terminal_id);
            let bounds = view
                .terminal_bounds_for(terminal_id)
                .unwrap_or(element_bounds);
            let (cursor, width_columns) = view
                .terminal_snapshot_for(terminal_id)
                .map(|snapshot| {
                    let cursor = terminal_cursor_position(snapshot);
                    let width_columns = snapshot
                        .cell(cursor.0, cursor.1)
                        .map(|cell| if cell.flags.wide { 2 } else { 1 })
                        .unwrap_or(1);
                    (cursor, width_columns)
                })
                .unwrap_or((fallback_cursor, 1));
            Some(terminal_cell_bounds(
                bounds,
                view.terminal_metrics,
                cursor.0,
                cursor.1,
                width_columns,
            ))
        })
    }

    fn character_index_for_point(
        &mut self,
        point: Point<gpui::Pixels>,
        window: &mut Window,
        cx: &mut App,
    ) -> Option<usize> {
        self.update_view(cx, |view, cx| {
            <WorkspaceView as EntityInputHandler>::character_index_for_point(
                view, point, window, cx,
            )
        })
    }

    fn set_selected_text_range(
        &mut self,
        range_utf16: Range<usize>,
        window: &mut Window,
        cx: &mut App,
    ) {
        self.update_view(cx, |view, cx| {
            <WorkspaceView as EntityInputHandler>::set_selected_text_range(
                view,
                range_utf16,
                window,
                cx,
            )
        });
    }

    fn element_bounds(
        &mut self,
        _window: &mut Window,
        _cx: &mut App,
    ) -> Option<Bounds<gpui::Pixels>> {
        Some(self.element_bounds)
    }

    fn text_length_utf16(&mut self, window: &mut Window, cx: &mut App) -> Option<usize> {
        self.update_view(cx, |view, cx| {
            <WorkspaceView as EntityInputHandler>::text_length_utf16(view, window, cx)
        })
    }

    fn apple_press_and_hold_enabled(&mut self) -> bool {
        false
    }

    fn accepts_text_input(&mut self, _window: &mut Window, cx: &mut App) -> bool {
        let view = self.view.read(cx);
        view.active_terminal_id().is_some() || view.ime_terminal == Some(self.terminal_id)
    }

    fn prefers_ime_for_printable_keys(&mut self, _window: &mut Window, cx: &mut App) -> bool {
        let view = self.view.read(cx);
        view.active_terminal_id().is_some() || view.ime_terminal == Some(self.terminal_id)
    }

    fn text_input_configuration(
        &mut self,
        _window: &mut Window,
        _cx: &mut App,
    ) -> TextInputConfiguration {
        TextInputConfiguration::default()
    }

    fn text_input_editable_range(
        &mut self,
        window: &mut Window,
        cx: &mut App,
    ) -> Option<Range<usize>> {
        self.update_view(cx, |view, cx| {
            <WorkspaceView as EntityInputHandler>::text_input_editable_range(view, window, cx)
        })
    }
}

pub struct WorkspaceView {
    client: CommandClient,
    snapshot: ModelSnapshot,
    config: AppConfig,
    terminal_metrics: TerminalMetrics,
    focus_handle: FocusHandle,
    resize_requests: Arc<Mutex<BTreeMap<TerminalId, TerminalSize>>>,
    terminal_bounds: Arc<Mutex<BTreeMap<TerminalId, Bounds<gpui::Pixels>>>>,
    input_handler_terminal: Option<TerminalId>,
    focused_pane: Option<PaneId>,
    selection: Option<TerminalSelection>,
    ime_terminal: Option<TerminalId>,
    ime_marked_text: String,
    ime_selected_range: Range<usize>,
    dragging_terminal: Option<TerminalId>,
    reported_mouse: Option<(TerminalId, MouseButton)>,
    last_reported_mouse_cell: Option<(TerminalId, TerminalCellPosition)>,
    sidebar_collapsed: bool,
    sidebar_width: f32,
    dragging_sidebar: bool,
    titlebar_dragging: bool,
    rename_target: Option<RenameTarget>,
    rename_value: String,
    context_menu: Option<ContextMenuState>,
    dialog: Option<DialogState>,
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
            input_handler_terminal: None,
            focused_pane,
            selection: None,
            ime_terminal: None,
            ime_marked_text: String::new(),
            ime_selected_range: 0..0,
            dragging_terminal: None,
            reported_mouse: None,
            last_reported_mouse_cell: None,
            sidebar_collapsed: false,
            sidebar_width: DEFAULT_SIDEBAR_WIDTH,
            dragging_sidebar: false,
            titlebar_dragging: false,
            rename_target: None,
            rename_value: String::new(),
            context_menu: None,
            dialog: None,
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

    fn has_transient_ui(&self) -> bool {
        self.rename_target.is_some() || self.context_menu.is_some() || self.dialog.is_some()
    }

    fn toggle_sidebar(&mut self, cx: &mut Context<Self>) {
        self.sidebar_collapsed = !self.sidebar_collapsed;
        cx.notify();
    }

    pub(crate) fn new_terminal_tab(&mut self, cx: &mut Context<Self>) {
        self.dispatch(AppCommand::Tab(TabCommand::New { title: None }), cx);
    }

    pub(crate) fn split_active_pane(&mut self, direction: SplitDirection, cx: &mut Context<Self>) {
        if let Some(pane_id) = self.focused_pane {
            self.dispatch(
                AppCommand::Pane(PaneCommand::Split {
                    pane_id: Some(pane_id),
                    direction,
                }),
                cx,
            );
        }
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

    fn workspace_by_id(&self, workspace_id: WorkspaceId) -> Option<WorkspaceDump> {
        self.workspace_dumps()
            .into_iter()
            .find(|workspace| workspace.id == workspace_id)
    }

    fn tab_by_id(&self, tab_id: TabId) -> Option<TabDump> {
        self.workspace_dumps()
            .into_iter()
            .flat_map(|workspace| workspace.tabs)
            .find(|tab| tab.id == tab_id)
    }

    fn context_menu_target_exists(&self, target: ContextMenuTarget) -> bool {
        match target {
            ContextMenuTarget::Workspace(workspace_id) => {
                self.workspace_by_id(workspace_id).is_some()
            }
            ContextMenuTarget::Tab(tab_id) => self.tab_by_id(tab_id).is_some(),
        }
    }

    fn rename_target_exists(&self, target: RenameTarget) -> bool {
        match target {
            RenameTarget::Workspace(workspace_id) => self.workspace_by_id(workspace_id).is_some(),
            RenameTarget::Tab(tab_id) => self.tab_by_id(tab_id).is_some(),
        }
    }

    fn dialog_target_exists(&self, dialog: DialogState) -> bool {
        match dialog {
            DialogState::ConfirmCloseWorkspace { workspace_id } => {
                self.workspace_by_id(workspace_id).is_some()
            }
        }
    }

    fn dispatch_close_workspace(&mut self, workspace_id: WorkspaceId, cx: &mut Context<Self>) {
        self.dispatch(
            AppCommand::Workspace(WorkspaceCommand::Close {
                workspace_id: Some(workspace_id),
            }),
            cx,
        );
    }

    fn request_close_workspace(
        &mut self,
        workspace_id: WorkspaceId,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        let Some(workspace) = self.workspace_by_id(workspace_id) else {
            return;
        };
        self.context_menu = None;
        if workspace.tabs.is_empty() {
            self.dispatch_close_workspace(workspace_id, cx);
            cx.notify();
        } else {
            self.dialog = Some(DialogState::ConfirmCloseWorkspace { workspace_id });
            self.focus_handle.focus(window, cx);
            cx.notify();
        }
    }

    fn confirm_dialog(&mut self, cx: &mut Context<Self>) {
        let Some(dialog) = self.dialog.take() else {
            return;
        };
        match dialog {
            DialogState::ConfirmCloseWorkspace { workspace_id } => {
                self.dispatch_close_workspace(workspace_id, cx);
            }
        }
        cx.notify();
    }

    fn cancel_dialog(&mut self, cx: &mut Context<Self>) {
        if self.dialog.take().is_some() {
            cx.notify();
        }
    }

    fn handle_dialog_key(&mut self, event: &KeyDownEvent, cx: &mut Context<Self>) -> bool {
        if self.dialog.is_none() {
            return false;
        }
        match event.keystroke.key.as_str() {
            "enter" | "return" => self.confirm_dialog(cx),
            "escape" => self.cancel_dialog(cx),
            _ => {}
        }
        true
    }

    fn begin_rename_active_workspace(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        if let Some(workspace_id) = self.active_workspace_id() {
            self.begin_rename_workspace(workspace_id, window, cx);
        }
    }

    fn begin_rename_active_tab(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        let tab_id = self
            .active_workspace_id()
            .and_then(|workspace_id| self.workspace_by_id(workspace_id))
            .and_then(|workspace| workspace.active_tab);
        if let Some(tab_id) = tab_id {
            self.begin_rename_tab(tab_id, window, cx);
        }
    }

    fn begin_rename_workspace(
        &mut self,
        workspace_id: WorkspaceId,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        if self.has_transient_ui() {
            return;
        }
        let Some(workspace) = self.workspace_by_id(workspace_id) else {
            return;
        };
        self.context_menu = None;
        self.rename_target = Some(RenameTarget::Workspace(workspace_id));
        self.rename_value = workspace.title.clone();
        self.focus_handle.focus(window, cx);
        cx.notify();
    }

    fn begin_rename_tab(&mut self, tab_id: TabId, window: &mut Window, cx: &mut Context<Self>) {
        if self.has_transient_ui() {
            return;
        }
        let Some(tab) = self
            .workspace_dumps()
            .into_iter()
            .flat_map(|workspace| workspace.tabs)
            .find(|tab| tab.id == tab_id)
        else {
            return;
        };
        self.context_menu = None;
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
            scale_factor: window.scale_factor(),
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

    fn ime_marked_text_for(&self, terminal_id: TerminalId) -> Option<String> {
        (self.ime_terminal == Some(terminal_id) && !self.ime_marked_text.is_empty())
            .then(|| self.ime_marked_text.clone())
    }

    fn clear_ime(&mut self) {
        self.ime_terminal = None;
        self.ime_marked_text.clear();
        self.ime_selected_range = 0..0;
    }

    fn reset_ime_marked_text(&mut self) {
        self.ime_marked_text.clear();
        self.ime_selected_range = 0..0;
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
        self.reset_ime_marked_text();
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
        self.clear_ime();
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
        canvas(
            move |bounds, _, _| {
                if bounds.size.width <= px(0.) || bounds.size.height <= px(0.) {
                    return;
                }
                let size = TerminalSize::new(
                    terminal_columns_for_width(bounds, metrics),
                    terminal_lines_for_height(bounds, metrics),
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
        if self.handle_dialog_key(event, cx) {
            cx.stop_propagation();
            return;
        }
        if self.context_menu.take().is_some() {
            cx.notify();
            cx.stop_propagation();
            return;
        }
        if self.handle_rename_key(event, cx) {
            cx.stop_propagation();
            return;
        }

        let keystroke = &event.keystroke;
        if keystroke.modifiers.platform {
            match (keystroke.key.as_str(), keystroke.modifiers.shift) {
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
        if terminal_key_uses_text_input_handler(keystroke) && self.input_handler_terminal.is_some()
        {
            // GPUI dispatches the key event before forwarding printable text
            // to the focused InputHandler. Let the handler own it so a
            // printable character is never sent to the PTY twice.
            return;
        }
        self.selection = None;
        self.clear_ime();
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

    pub(crate) fn install_snapshot(&mut self, snapshot: ModelSnapshot, cx: &mut Context<Self>) {
        if snapshot.state_revision <= self.snapshot.state_revision {
            return;
        }
        update_terminal_selection_for_snapshot(&mut self.selection, &self.snapshot, &snapshot);
        self.focused_pane = snapshot.focused_pane;
        self.snapshot = snapshot;
        if self
            .context_menu
            .is_some_and(|menu| !self.context_menu_target_exists(menu.target))
        {
            self.context_menu = None;
        }
        if self
            .rename_target
            .is_some_and(|target| !self.rename_target_exists(target))
        {
            self.rename_target = None;
            self.rename_value.clear();
        }
        if self
            .dialog
            .is_some_and(|dialog| !self.dialog_target_exists(dialog))
        {
            self.dialog = None;
        }
        if self.ime_terminal.is_some() && self.ime_terminal != self.active_terminal_id() {
            self.clear_ime();
        }
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
        let title = if self.rename_target == Some(RenameTarget::Tab(tab_id)) {
            format!("{}▌", self.rename_value)
        } else {
            title
        };
        div()
            .id(format!("tab-{tab_id}"))
            .h(px(28.))
            .px(px(10.))
            .items_center()
            .flex()
            .flex_none()
            .cursor_pointer()
            .hover(|style| style.bg(rgb(theme.tab_add_background)))
            .bg(background)
            .text_color(rgb(theme.terminal_foreground))
            .child(SharedString::from(title))
            .on_mouse_down(
                MouseButton::Left,
                cx.listener(move |this, _event: &MouseDownEvent, window, cx| {
                    if this.has_transient_ui() {
                        cx.stop_propagation();
                        return;
                    }
                    this.context_menu = None;
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
                    cx.stop_propagation();
                }),
            )
            .on_mouse_down(
                MouseButton::Right,
                cx.listener(move |this, event: &MouseDownEvent, window, cx| {
                    this.context_menu = Some(ContextMenuState {
                        target: ContextMenuTarget::Tab(tab_id),
                        position: event.position,
                    });
                    this.focus_handle.focus(window, cx);
                    cx.stop_propagation();
                    cx.notify();
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
        let workspace_hover_background = if active {
            theme.tab_active_background
        } else {
            theme.tab_add_background
        };
        let workspace_active_pane = workspace
            .active_tab
            .and_then(|tab_id| workspace.tabs.iter().find(|tab| tab.id == tab_id))
            .map(|tab| tab.active_pane);
        let workspace_activate = cx.listener(move |this, _event: &MouseDownEvent, window, cx| {
            this.context_menu = None;
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
        let workspace_row = div()
            .id(format!("workspace-{workspace_id}"))
            .h(px(32.))
            .w_full()
            .px(px(10.))
            .items_center()
            .flex()
            .cursor_pointer()
            .hover(|style| style.bg(rgb(workspace_hover_background)))
            .bg(workspace_background)
            .text_color(rgb(theme.ui_foreground))
            .on_mouse_down(MouseButton::Left, workspace_activate)
            .on_mouse_down(
                MouseButton::Right,
                cx.listener(move |this, event: &MouseDownEvent, window, cx| {
                    this.context_menu = Some(ContextMenuState {
                        target: ContextMenuTarget::Workspace(workspace_id),
                        position: event.position,
                    });
                    this.focus_handle.focus(window, cx);
                    cx.stop_propagation();
                    cx.notify();
                }),
            )
            .child(
                div()
                    .flex_1()
                    .min_w(px(0.))
                    .truncate()
                    .child(SharedString::from(workspace_title)),
            );

        workspace_row.into_any_element()
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
                            .h(px(36.))
                            .px(px(10.))
                            .items_center()
                            .flex()
                            .bg(rgb(theme.chrome_background))
                            .text_color(rgb(theme.inactive_pane_border))
                            .child("WORKSPACES"),
                    )
                    .child(list),
            )
            .child(self.sidebar_resize_handle(theme, cx))
            .into_any_element()
    }

    fn render_context_menu_item(
        &self,
        label: &'static str,
        theme: ThemeColors,
        listener: impl Fn(&mut Self, &MouseDownEvent, &mut Window, &mut Context<Self>) + 'static,
        cx: &mut Context<Self>,
    ) -> AnyElement {
        div()
            .h(px(30.))
            .w_full()
            .px(px(10.))
            .items_center()
            .flex()
            .cursor_pointer()
            .hover(|style| style.bg(rgb(theme.tab_inactive_background)))
            .text_color(rgb(theme.ui_foreground))
            .on_mouse_down(
                MouseButton::Left,
                cx.listener(move |this, event, window, cx| {
                    listener(this, event, window, cx);
                    cx.stop_propagation();
                }),
            )
            .child(label)
            .into_any_element()
    }

    fn render_context_menu(
        &self,
        theme: ThemeColors,
        cx: &mut Context<Self>,
    ) -> Option<AnyElement> {
        let context_menu = self.context_menu?;
        let target = context_menu.target;
        let rename = match target {
            ContextMenuTarget::Workspace(workspace_id) => self.render_context_menu_item(
                "Rename workspace",
                theme,
                move |this, _event, window, cx| {
                    this.context_menu = None;
                    this.begin_rename_workspace(workspace_id, window, cx);
                },
                cx,
            ),
            ContextMenuTarget::Tab(tab_id) => self.render_context_menu_item(
                "Rename tab",
                theme,
                move |this, _event, window, cx| {
                    this.context_menu = None;
                    this.begin_rename_tab(tab_id, window, cx);
                },
                cx,
            ),
        };
        let close = match target {
            ContextMenuTarget::Workspace(workspace_id) => self.render_context_menu_item(
                "Close workspace",
                theme,
                move |this, _event, window, cx| {
                    this.request_close_workspace(workspace_id, window, cx);
                },
                cx,
            ),
            ContextMenuTarget::Tab(tab_id) => self.render_context_menu_item(
                "Close tab",
                theme,
                move |this, _event, _window, cx| {
                    this.context_menu = None;
                    this.dispatch(
                        AppCommand::Tab(TabCommand::Close {
                            tab_id: Some(tab_id),
                        }),
                        cx,
                    );
                    cx.notify();
                },
                cx,
            ),
        };
        let menu = div()
            .id("workspace-context-menu")
            .w(px(190.))
            .p(px(4.))
            .flex()
            .flex_col()
            .gap(px(2.))
            .bg(rgb(theme.chrome_background))
            .border_1()
            .border_color(rgb(theme.inactive_pane_border))
            .child(rename)
            .child(close);
        let position = context_menu.position;
        Some(
            deferred(
                div()
                    .size_full()
                    .absolute()
                    .inset_0()
                    .on_mouse_down(
                        MouseButton::Left,
                        cx.listener(|this, _event: &MouseDownEvent, _window, cx| {
                            this.context_menu = None;
                            cx.stop_propagation();
                            cx.notify();
                        }),
                    )
                    .on_mouse_down(
                        MouseButton::Right,
                        cx.listener(|this, _event: &MouseDownEvent, _window, cx| {
                            this.context_menu = None;
                            cx.stop_propagation();
                            cx.notify();
                        }),
                    )
                    .child(
                        anchored()
                            .position(position)
                            .snap_to_window_with_margin(px(8.))
                            .child(menu),
                    ),
            )
            .with_priority(10)
            .into_any_element(),
        )
    }

    fn render_dialog(&self, theme: ThemeColors, cx: &mut Context<Self>) -> Option<AnyElement> {
        let dialog = self.dialog?;
        let DialogState::ConfirmCloseWorkspace { workspace_id } = dialog;
        let workspace = self.workspace_by_id(workspace_id)?;
        let tab_count = workspace.tabs.len();
        let tab_label = if tab_count == 1 { "tab" } else { "tabs" };
        let message = format!(
            "Close “{}” and its {tab_count} {tab_label}?",
            workspace.title
        );
        let cancel = div()
            .h(px(30.))
            .px(px(12.))
            .items_center()
            .justify_center()
            .flex()
            .text_color(rgb(theme.ui_foreground))
            .border_1()
            .border_color(rgb(theme.inactive_pane_border))
            .on_mouse_down(
                MouseButton::Left,
                cx.listener(|this, _event: &MouseDownEvent, _window, cx| {
                    this.cancel_dialog(cx);
                    cx.stop_propagation();
                }),
            )
            .child("Cancel");
        let confirm = div()
            .h(px(30.))
            .px(px(12.))
            .items_center()
            .justify_center()
            .flex()
            .bg(rgb(theme.tab_add_background))
            .text_color(rgb(theme.ui_foreground))
            .on_mouse_down(
                MouseButton::Left,
                cx.listener(|this, _event: &MouseDownEvent, _window, cx| {
                    this.confirm_dialog(cx);
                    cx.stop_propagation();
                }),
            )
            .child("Close workspace");
        let dialog = div()
            .id("close-workspace-dialog")
            .w(px(380.))
            .p(px(20.))
            .gap(px(12.))
            .flex()
            .flex_col()
            .bg(rgb(theme.chrome_background))
            .border_1()
            .border_color(rgb(theme.active_pane_border))
            .text_color(rgb(theme.ui_foreground))
            .child(div().text_lg().child("Close workspace?"))
            .child(SharedString::from(message))
            .child(
                div()
                    .w_full()
                    .gap(px(8.))
                    .justify_end()
                    .items_center()
                    .flex()
                    .child(cancel)
                    .child(confirm),
            );
        Some(
            deferred(
                div()
                    .size_full()
                    .absolute()
                    .inset_0()
                    .items_center()
                    .justify_center()
                    .bg(rgba(0x00000099))
                    .on_mouse_down(MouseButton::Left, |_event: &MouseDownEvent, _window, cx| {
                        cx.stop_propagation();
                    })
                    .on_mouse_down(
                        MouseButton::Right,
                        |_event: &MouseDownEvent, _window, cx| {
                            cx.stop_propagation();
                        },
                    )
                    .child(dialog),
            )
            .with_priority(20)
            .into_any_element(),
        )
    }

    fn render_titlebar_control(
        &self,
        color: u32,
        area: WindowControlArea,
        listener: impl Fn(&mut Self, &MouseDownEvent, &mut Window, &mut Context<Self>) + 'static,
        cx: &mut Context<Self>,
    ) -> AnyElement {
        div()
            .size(px(12.))
            .rounded(px(6.))
            .bg(rgb(color))
            .cursor_pointer()
            .occlude()
            .window_control_area(area)
            .hover(|style| style.opacity(0.75))
            .on_mouse_down(
                MouseButton::Left,
                cx.listener(move |this, event, window, cx| {
                    cx.stop_propagation();
                    listener(this, event, window, cx);
                }),
            )
            .into_any_element()
    }

    fn render_tab_bar(&self, theme: ThemeColors, cx: &mut Context<Self>) -> AnyElement {
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
        let mut tab_bar = div()
            .id("tab-bar")
            .h_full()
            .flex_1()
            .min_w(px(0.))
            .gap(px(2.))
            .items_center()
            .flex()
            .overflow_x_hidden();
        for (tab_id, title, active, active_pane) in tab_data {
            tab_bar = tab_bar.child(self.tab_button(tab_id, title, active, active_pane, theme, cx));
        }
        tab_bar
            .child(
                div()
                    .id("new-tab")
                    .h(px(28.))
                    .w(px(28.))
                    .items_center()
                    .justify_center()
                    .flex()
                    .flex_none()
                    .cursor_pointer()
                    .hover(|style| style.bg(rgb(theme.tab_add_background)))
                    .bg(rgb(theme.tab_inactive_background))
                    .text_color(rgb(theme.ui_foreground))
                    .child("+")
                    .on_mouse_down(
                        MouseButton::Left,
                        cx.listener(|this, _event: &MouseDownEvent, window, cx| {
                            if this.has_transient_ui() {
                                cx.stop_propagation();
                                return;
                            }
                            this.focus_handle.focus(window, cx);
                            this.dispatch(AppCommand::Tab(TabCommand::New { title: None }), cx);
                            cx.stop_propagation();
                        }),
                    ),
            )
            .into_any_element()
    }

    fn render_titlebar(&self, theme: ThemeColors, cx: &mut Context<Self>) -> AnyElement {
        let close = self.render_titlebar_control(
            0xff5f57,
            WindowControlArea::Close,
            |_, _event, window, _cx| window.remove_window(),
            cx,
        );
        let minimize = self.render_titlebar_control(
            0xfebc2e,
            WindowControlArea::Min,
            |_, _event, window, _cx| window.minimize_window(),
            cx,
        );
        let maximize = self.render_titlebar_control(
            0x28c840,
            WindowControlArea::Max,
            |_, _event, window, _cx| window.zoom_window(),
            cx,
        );
        let controls = div()
            .h_full()
            .gap(px(8.))
            .items_center()
            .flex()
            .flex_none()
            .child(close)
            .child(minimize)
            .child(maximize);
        let sidebar_toggle = div()
            .id("sidebar-toggle")
            .size(px(28.))
            .items_center()
            .justify_center()
            .flex()
            .flex_none()
            .cursor_pointer()
            .hover(|style| style.bg(rgb(theme.tab_add_background)))
            .text_color(rgb(theme.ui_foreground))
            .child("▤")
            .on_mouse_down(
                MouseButton::Left,
                cx.listener(|this, _event: &MouseDownEvent, _window, cx| {
                    this.toggle_sidebar(cx);
                    cx.stop_propagation();
                }),
            );
        div()
            .id("water-titlebar")
            .h(px(36.))
            .w_full()
            .gap(px(8.))
            .px(px(10.))
            .items_center()
            .flex()
            // The titlebar owns its drag gesture explicitly below. Only the
            // three control hitboxes use WindowControlArea so they are not
            // shadowed by a full-width Drag hitbox.
            .bg(rgb(theme.chrome_background))
            .text_color(rgb(theme.ui_foreground))
            .on_mouse_down_out(cx.listener(|this, _event, _window, _cx| {
                this.titlebar_dragging = false;
            }))
            .on_mouse_down(
                MouseButton::Left,
                cx.listener(|this, _event: &MouseDownEvent, _window, _cx| {
                    this.titlebar_dragging = true;
                }),
            )
            .on_mouse_up(
                MouseButton::Left,
                cx.listener(|this, _event: &MouseUpEvent, _window, _cx| {
                    this.titlebar_dragging = false;
                }),
            )
            .on_mouse_move(cx.listener(|this, _event: &MouseMoveEvent, window, _cx| {
                if this.titlebar_dragging {
                    this.titlebar_dragging = false;
                    window.start_window_move();
                }
            }))
            .child(controls)
            .child(sidebar_toggle)
            .child(self.render_tab_bar(theme, cx))
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
        let view = cx.entity();
        self.render_pane_tree_with_grow(tree, 1.0, window_active, metrics, theme, view, cx)
    }

    #[allow(clippy::too_many_arguments)]
    fn render_pane_tree_with_grow(
        &self,
        tree: &PaneTreeDump,
        grow: f32,
        window_active: bool,
        metrics: TerminalMetrics,
        theme: ThemeColors,
        view: Entity<WorkspaceView>,
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
                                self.ime_marked_text_for(snapshot.terminal_id),
                                self.terminal_bounds.clone(),
                                active.then(|| (view.clone(), self.focus_handle.clone())),
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
                        view.clone(),
                        cx,
                    ))
                    .child(self.render_pane_tree_with_grow(
                        second,
                        1.0 - ratio,
                        window_active,
                        metrics,
                        theme,
                        view,
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

impl EntityInputHandler for WorkspaceView {
    fn text_for_range(
        &mut self,
        range_utf16: Range<usize>,
        adjusted_range: &mut Option<Range<usize>>,
        _window: &mut Window,
        _cx: &mut Context<Self>,
    ) -> Option<String> {
        let range = normalize_utf16_range(range_utf16, utf16_len(&self.ime_marked_text));
        let start = utf16_offset_to_byte(&self.ime_marked_text, range.start);
        let end = utf16_offset_to_byte_end(&self.ime_marked_text, range.end);
        adjusted_range.replace(range);
        Some(self.ime_marked_text[start..end].to_owned())
    }

    fn selected_text_range(
        &mut self,
        _ignore_disabled_input: bool,
        _window: &mut Window,
        _cx: &mut Context<Self>,
    ) -> Option<UTF16Selection> {
        if self.active_terminal_id().is_none() && self.ime_terminal.is_none() {
            return None;
        }
        let range = normalize_utf16_range(
            self.ime_selected_range.clone(),
            utf16_len(&self.ime_marked_text),
        );
        Some(UTF16Selection {
            range: range.clone(),
            reversed: false,
        })
    }

    fn marked_text_range(
        &self,
        _window: &mut Window,
        _cx: &mut Context<Self>,
    ) -> Option<Range<usize>> {
        (!self.ime_marked_text.is_empty() && self.ime_terminal.is_some())
            .then(|| 0..utf16_len(&self.ime_marked_text))
    }

    fn unmark_text(&mut self, _window: &mut Window, cx: &mut Context<Self>) {
        self.reset_ime_marked_text();
        cx.notify();
    }

    fn replace_text_in_range(
        &mut self,
        _range_utf16: Option<Range<usize>>,
        new_text: &str,
        _window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        let Some(terminal_id) = self.ime_terminal.or_else(|| self.active_terminal_id()) else {
            return;
        };
        self.ime_terminal = Some(terminal_id);
        self.selection = None;
        self.reset_ime_marked_text();
        if !new_text.is_empty() {
            self.enqueue_terminal_command(
                terminal_id,
                TerminalCommand::SendText {
                    terminal_id: Some(terminal_id),
                    pane_id: None,
                    text: new_text.to_owned(),
                },
            );
        }
        cx.notify();
    }

    fn replace_and_mark_text_in_range(
        &mut self,
        range_utf16: Option<Range<usize>>,
        new_text: &str,
        new_selected_range: Option<Range<usize>>,
        _window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        let Some(terminal_id) = self.ime_terminal.or_else(|| self.active_terminal_id()) else {
            return;
        };
        self.ime_terminal = Some(terminal_id);
        let current = self.ime_marked_text.clone();
        let replacement = range_utf16
            .map(|range| normalize_utf16_range(range, utf16_len(&current)))
            .unwrap_or_else(|| 0..utf16_len(&current));
        let start = utf16_offset_to_byte(&current, replacement.start);
        let end = utf16_offset_to_byte_end(&current, replacement.end);
        let mut updated = String::with_capacity(
            current.len().saturating_sub(end.saturating_sub(start)) + new_text.len(),
        );
        updated.push_str(&current[..start]);
        updated.push_str(new_text);
        updated.push_str(&current[end..]);
        self.ime_marked_text = updated;
        let new_length = utf16_len(new_text);
        let replacement_start = replacement.start;
        self.ime_selected_range = new_selected_range
            .map(|range| {
                let range = normalize_utf16_range(range, new_length);
                replacement_start.saturating_add(range.start)
                    ..replacement_start.saturating_add(range.end)
            })
            .unwrap_or_else(|| {
                let end = replacement_start.saturating_add(new_length);
                end..end
            });
        self.selection = None;
        cx.notify();
    }

    fn bounds_for_range(
        &mut self,
        _range_utf16: Range<usize>,
        element_bounds: Bounds<gpui::Pixels>,
        _window: &mut Window,
        _cx: &mut Context<Self>,
    ) -> Option<Bounds<gpui::Pixels>> {
        let terminal_id = self.ime_terminal.or_else(|| self.active_terminal_id())?;
        let snapshot = self.terminal_snapshot_for(terminal_id);
        let (cursor, width_columns) = snapshot
            .map(|snapshot| {
                let cursor = terminal_cursor_position(snapshot);
                let width_columns = snapshot
                    .cell(cursor.0, cursor.1)
                    .map(|cell| if cell.flags.wide { 2 } else { 1 })
                    .unwrap_or(1);
                (cursor, width_columns)
            })
            .unwrap_or(((0, 0), 1));
        Some(terminal_cell_bounds(
            element_bounds,
            self.terminal_metrics,
            cursor.0,
            cursor.1,
            width_columns,
        ))
    }

    fn character_index_for_point(
        &mut self,
        point: Point<gpui::Pixels>,
        _window: &mut Window,
        _cx: &mut Context<Self>,
    ) -> Option<usize> {
        let terminal_id = self.ime_terminal.or_else(|| self.active_terminal_id())?;
        let bounds = self.terminal_bounds_for(terminal_id)?;
        let mouse = terminal_mouse_position(point, Some(bounds), self.terminal_metrics);
        let row = mouse.row.saturating_sub(1);
        let column = mouse.column.saturating_sub(1);
        let (cursor_row, cursor_column) = self
            .terminal_snapshot_for(terminal_id)
            .map(terminal_cursor_position)
            .unwrap_or((0, 0));
        let offset = if row == cursor_row && column >= cursor_column {
            utf16_len(&self.ime_marked_text).min(column - cursor_column)
        } else {
            0
        };
        Some(offset)
    }

    fn set_selected_text_range(
        &mut self,
        range_utf16: Range<usize>,
        _window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        self.ime_selected_range =
            normalize_utf16_range(range_utf16, utf16_len(&self.ime_marked_text));
        cx.notify();
    }

    fn text_length_utf16(
        &mut self,
        _window: &mut Window,
        _cx: &mut Context<Self>,
    ) -> Option<usize> {
        self.active_terminal_id()
            .or(self.ime_terminal)
            .map(|_| utf16_len(&self.ime_marked_text))
    }

    fn accepts_text_input(&self, _window: &mut Window, _cx: &mut Context<Self>) -> bool {
        self.active_terminal_id().is_some() || self.ime_terminal.is_some()
    }

    fn text_input_configuration(
        &mut self,
        _window: &mut Window,
        _cx: &mut Context<Self>,
    ) -> TextInputConfiguration {
        TextInputConfiguration::default()
    }

    fn text_input_editable_range(
        &mut self,
        _window: &mut Window,
        _cx: &mut Context<Self>,
    ) -> Option<Range<usize>> {
        self.active_terminal_id()
            .or(self.ime_terminal)
            .map(|_| 0..utf16_len(&self.ime_marked_text))
    }
}

fn utf16_len(text: &str) -> usize {
    text.encode_utf16().count()
}

fn utf16_offset_to_byte(text: &str, offset: usize) -> usize {
    let mut current = 0;
    for (byte, character) in text.char_indices() {
        if offset <= current {
            return byte;
        }
        let next = current + character.len_utf16();
        if offset < next {
            return byte;
        }
        current = next;
        if offset == current {
            return byte + character.len_utf8();
        }
    }
    text.len()
}

fn utf16_offset_to_byte_end(text: &str, offset: usize) -> usize {
    let mut current = 0;
    for (byte, character) in text.char_indices() {
        let next = current + character.len_utf16();
        if offset <= current {
            return byte;
        }
        if offset <= next {
            return byte + character.len_utf8();
        }
        current = next;
    }
    text.len()
}

fn normalize_utf16_range(range: Range<usize>, length: usize) -> Range<usize> {
    let start = range.start.min(length);
    let end = range.end.min(length);
    if start <= end { start..end } else { end..end }
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

fn terminal_snap_to_device_pixel(value: f32, scale_factor: f32) -> f32 {
    let scale_factor = if scale_factor.is_finite() && scale_factor > 0.0 {
        scale_factor
    } else {
        1.0
    };
    let scaled = value * scale_factor;
    (scaled.abs() - 0.5).ceil().copysign(scaled) / scale_factor
}

fn terminal_grid_edge(origin: f32, advance: f32, index: usize, scale_factor: f32) -> f32 {
    terminal_snap_to_device_pixel(origin + advance * index as f32, scale_factor)
}

fn terminal_grid_index_at(
    coordinate: f32,
    origin: f32,
    advance: f32,
    scale_factor: f32,
    extent: f32,
) -> (usize, TerminalSelectionSide) {
    if coordinate < terminal_grid_edge(origin, advance, 0, scale_factor) {
        return (0, TerminalSelectionSide::Left);
    }

    let max_index = (extent.max(0.0) / advance.max(f32::EPSILON)).ceil() as usize + 2;
    let mut index = 0;
    while index + 1 < max_index
        && coordinate >= terminal_grid_edge(origin, advance, index + 1, scale_factor)
    {
        index += 1;
    }
    let left = terminal_grid_edge(origin, advance, index, scale_factor);
    let right = terminal_grid_edge(origin, advance, index + 1, scale_factor);
    let side = if coordinate >= (left + right) / 2.0 {
        TerminalSelectionSide::Right
    } else {
        TerminalSelectionSide::Left
    };
    (index, side)
}

fn terminal_columns_for_width(bounds: Bounds<gpui::Pixels>, metrics: TerminalMetrics) -> usize {
    let origin = f32::from(bounds.origin.x);
    let right = terminal_snap_to_device_pixel(f32::from(bounds.right()), metrics.scale_factor);
    let extent =
        (right - terminal_grid_edge(origin, metrics.cell_width, 0, metrics.scale_factor)).max(0.0);
    let estimate = (extent / metrics.cell_width.max(f32::EPSILON)).ceil() as usize + 2;
    (0..estimate)
        .take_while(|column| {
            terminal_grid_edge(
                origin,
                metrics.cell_width,
                *column + 1,
                metrics.scale_factor,
            ) <= right
        })
        .count()
}

fn terminal_lines_for_height(bounds: Bounds<gpui::Pixels>, metrics: TerminalMetrics) -> usize {
    let origin = f32::from(bounds.origin.y);
    let bottom = terminal_snap_to_device_pixel(f32::from(bounds.bottom()), metrics.scale_factor);
    let extent = (bottom
        - terminal_grid_edge(origin, metrics.line_height, 0, metrics.scale_factor))
    .max(0.0);
    let estimate = (extent / metrics.line_height.max(f32::EPSILON)).ceil() as usize + 2;
    (0..estimate)
        .take_while(|row| {
            terminal_grid_edge(origin, metrics.line_height, *row + 1, metrics.scale_factor)
                <= bottom
        })
        .count()
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
    let position_x = f32::from(position.x);
    let position_y = f32::from(position.y);
    let origin_x = f32::from(bounds.origin.x);
    let origin_y = f32::from(bounds.origin.y);
    let (column, column_side) = terminal_grid_index_at(
        position_x,
        origin_x,
        metrics.cell_width,
        metrics.scale_factor,
        f32::from(bounds.size.width),
    );
    let (row, _) = terminal_grid_index_at(
        position_y,
        origin_y,
        metrics.line_height,
        metrics.scale_factor,
        f32::from(bounds.size.height),
    );
    let side = if position_x
        < terminal_grid_edge(origin_x, metrics.cell_width, 0, metrics.scale_factor)
        || position_y < terminal_grid_edge(origin_y, metrics.line_height, 0, metrics.scale_factor)
    {
        TerminalSelectionSide::Left
    } else if position_x
        >= terminal_snap_to_device_pixel(f32::from(bounds.right()), metrics.scale_factor)
        || position_y
            >= terminal_snap_to_device_pixel(f32::from(bounds.bottom()), metrics.scale_factor)
        || column_side == TerminalSelectionSide::Right
    {
        TerminalSelectionSide::Right
    } else {
        TerminalSelectionSide::Left
    };
    TerminalMousePosition {
        column: column + 1,
        row: row + 1,
        side,
    }
}

fn terminal_key_uses_text_input_handler(keystroke: &Keystroke) -> bool {
    (keystroke.key_char.as_deref().is_some_and(|character| {
        !character.is_empty() && character.chars().all(|character| !character.is_control())
    }) || keystroke.key.chars().count() == 1
        || keystroke.key == "space")
        && !keystroke.modifiers.control
        && !keystroke.modifiers.alt
        && !keystroke.modifiers.platform
        && !keystroke.modifiers.function
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
        "home" => Some(cursor_sequence('H', modifier, modes.application_cursor)),
        "end" => Some(cursor_sequence('F', modifier, modes.application_cursor)),
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

#[cfg(test)]
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

    while start < end {
        let Some(cell) = snapshot.cells.get(start) else {
            break;
        };
        if cell.flags.leading_wide_spacer {
            // This is the placeholder written at the end of a wrapped line
            // before a wide character starts on the next line. It is not the
            // second half of a character in this row.
            start = start.saturating_add(1);
        } else if cell.flags.wide_spacer {
            // A trailing spacer belongs to the wide base immediately before
            // it. Normalize an endpoint landing on either half to the base.
            if start > 0 && snapshot.cells[start - 1].flags.wide {
                start -= 1;
            } else {
                start = start.saturating_add(1);
            }
        } else {
            break;
        }
    }
    if end > start
        && snapshot
            .cells
            .get(end.saturating_sub(1))
            .is_some_and(|cell| cell.flags.leading_wide_spacer)
    {
        end = end.saturating_sub(1);
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
            if cell.flags.wide_spacer || cell.flags.leading_wide_spacer {
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

#[allow(clippy::too_many_arguments)]
fn render_terminal_snapshot(
    snapshot: &TerminalSnapshot,
    selection: Option<TerminalSelection>,
    options: TerminalRenderOptions,
    font_family: &str,
    font_size: f32,
    ime_text: Option<String>,
    terminal_bounds: Arc<Mutex<BTreeMap<TerminalId, Bounds<gpui::Pixels>>>>,
    input_handler: Option<(Entity<WorkspaceView>, FocusHandle)>,
) -> AnyElement {
    TerminalRenderElement {
        snapshot: snapshot.clone(),
        selection,
        options,
        font_family: font_family.to_owned(),
        font_size,
        ime_text,
        terminal_bounds,
        input_handler,
    }
    .into_any_element()
}

impl gpui::IntoElement for TerminalRenderElement {
    type Element = Self;

    fn into_element(self) -> Self::Element {
        self
    }
}

impl gpui::Element for TerminalRenderElement {
    type RequestLayoutState = ();
    type PrepaintState = TerminalPrepaintState;

    fn id(&self) -> Option<gpui::ElementId> {
        None
    }

    fn source_location(&self) -> Option<&'static std::panic::Location<'static>> {
        None
    }

    fn request_layout(
        &mut self,
        _id: Option<&gpui::GlobalElementId>,
        _inspector_id: Option<&gpui::InspectorElementId>,
        window: &mut Window,
        cx: &mut App,
    ) -> (gpui::LayoutId, Self::RequestLayoutState) {
        let mut style = gpui::Style::default();
        style.size.width = relative(1.).into();
        style.size.height =
            px(self.options.metrics.line_height * self.snapshot.size.lines as f32).into();
        (window.request_layout(style, [], cx), ())
    }

    fn prepaint(
        &mut self,
        _id: Option<&gpui::GlobalElementId>,
        _inspector_id: Option<&gpui::InspectorElementId>,
        bounds: Bounds<gpui::Pixels>,
        _request_layout: &mut Self::RequestLayoutState,
        window: &mut Window,
        _cx: &mut App,
    ) -> Self::PrepaintState {
        self.terminal_bounds
            .lock()
            .expect("terminal bounds poisoned")
            .insert(self.snapshot.terminal_id, bounds);

        let selected_bounds = self
            .selection
            .filter(|selection| selection.terminal_id == self.snapshot.terminal_id)
            .and_then(|selection| selection_bounds(&self.snapshot, selection));
        let mut rows = Vec::with_capacity(self.snapshot.size.lines);
        for row in 0..self.snapshot.size.lines {
            let (chunks, backgrounds) = terminal_row_data(
                &self.snapshot,
                row,
                selected_bounds,
                self.options,
                &self.font_family,
            );
            let mut text = Vec::new();
            for chunk in chunks {
                let target_width = f32::from(
                    terminal_cell_bounds(
                        bounds,
                        self.options.metrics,
                        row,
                        chunk.start_column,
                        chunk.span_columns,
                    )
                    .size
                    .width,
                );
                let line = (!chunk.requires_cell_scaling).then(|| {
                    shape_terminal_text_line(
                        window,
                        &chunk.text,
                        &chunk.runs,
                        self.font_size,
                        self.options.metrics.cell_width * chunk.width_columns as f32,
                        target_width,
                    )
                });
                let should_shape_cells = chunk.requires_cell_scaling
                    || line.as_ref().is_some_and(|line| {
                        let natural_width = f32::from(line.width());
                        natural_width.is_finite() && natural_width > target_width + 0.01
                    });
                if should_shape_cells {
                    let mut column = chunk.start_column;
                    for cell in chunk.cells {
                        let width = f32::from(
                            terminal_cell_bounds(
                                bounds,
                                self.options.metrics,
                                row,
                                column,
                                cell.width_columns,
                            )
                            .size
                            .width,
                        );
                        let line = shape_terminal_text_line(
                            window,
                            &cell.text,
                            std::slice::from_ref(&cell.run),
                            self.font_size,
                            width,
                            width,
                        );
                        text.push(TerminalTextPaint {
                            start_column: column,
                            line,
                        });
                        column += cell.width_columns;
                    }
                } else if let Some(line) = line {
                    text.push(TerminalTextPaint {
                        start_column: chunk.start_column,
                        line,
                    });
                }
            }
            rows.push(TerminalRowPaint { text, backgrounds });
        }

        let ime_line = self.ime_text.as_deref().and_then(|text| {
            if text.is_empty() || !self.snapshot.cursor.visible {
                return None;
            }
            let (row, column) = terminal_cursor_position(&self.snapshot);
            let color = rgb(self.options.theme.terminal_foreground).into();
            let run = TextRun {
                len: text.len(),
                font: font(self.font_family.clone()),
                color,
                background_color: None,
                underline: Some(UnderlineStyle {
                    thickness: px(1.),
                    color: Some(color),
                    wavy: false,
                }),
                strikethrough: None,
            };
            let line = window.text_system().shape_line(
                SharedString::from(text.to_owned()),
                px(self.font_size),
                &[run],
                None,
            );
            Some((line, row, column))
        });

        TerminalPrepaintState { rows, ime_line }
    }

    fn paint(
        &mut self,
        _id: Option<&gpui::GlobalElementId>,
        _inspector_id: Option<&gpui::InspectorElementId>,
        bounds: Bounds<gpui::Pixels>,
        _request_layout: &mut Self::RequestLayoutState,
        prepaint: &mut Self::PrepaintState,
        window: &mut Window,
        cx: &mut App,
    ) {
        let TerminalRenderOptions {
            metrics,
            theme,
            cursor_focused,
        } = self.options;
        window.paint_quad(fill(bounds, rgb(theme.terminal_background)));

        for (row, row_paint) in prepaint.rows.iter().enumerate() {
            for background in &row_paint.backgrounds {
                window.paint_quad(fill(
                    terminal_cell_bounds(
                        bounds,
                        metrics,
                        row,
                        background.start_column,
                        background.width_columns,
                    ),
                    rgb(background.color),
                ));
            }
            for text in &row_paint.text {
                let origin =
                    terminal_cell_bounds(bounds, metrics, row, text.start_column, 1).origin;
                let _ = text.line.paint(
                    origin,
                    px(metrics.line_height),
                    TextAlign::Left,
                    None,
                    window,
                    cx,
                );
            }
        }

        if let Some((line, row, column)) = prepaint.ime_line.as_ref() {
            let origin = terminal_cell_bounds(bounds, metrics, *row, *column, 1).origin;
            let _ = line.paint(
                origin,
                px(metrics.line_height),
                TextAlign::Left,
                None,
                window,
                cx,
            );
        }

        if self.snapshot.cursor.visible && !cursor_focused {
            let (row, column) = terminal_cursor_position(&self.snapshot);
            let width_columns = self
                .snapshot
                .cell(row, column)
                .map(|cell| if cell.flags.wide { 2 } else { 1 })
                .unwrap_or(1);
            window.paint_quad(outline(
                terminal_cell_bounds(bounds, metrics, row, column, width_columns),
                rgb(theme.inactive_cursor),
                gpui::BorderStyle::default(),
            ));
        }

        if let Some((view, focus_handle)) = self.input_handler.take() {
            window.handle_input(
                &focus_handle,
                TerminalInputHandler {
                    view,
                    terminal_id: self.snapshot.terminal_id,
                    element_bounds: bounds,
                    cursor: terminal_cursor_position(&self.snapshot),
                },
                cx,
            );
        }
    }
}

fn shape_terminal_text_line(
    window: &mut Window,
    text: &str,
    runs: &[TextRun],
    font_size: f32,
    force_width: f32,
    target_width: f32,
) -> ShapedLine {
    let text_system = window.text_system();
    let shape = |font_size: f32| {
        text_system.shape_line(
            SharedString::from(text.to_owned()),
            px(font_size),
            runs,
            Some(px(force_width)),
        )
    };
    let line = shape(font_size);
    let natural_width = f32::from(line.width());
    let scale = terminal_fit_scale(natural_width, target_width);
    if scale < 1.0 {
        shape((font_size * scale).max(0.5))
    } else {
        line
    }
}

fn terminal_fit_scale(natural_width: f32, target_width: f32) -> f32 {
    if target_width > 0.0 && natural_width.is_finite() && natural_width > target_width + 0.01 {
        (target_width / natural_width).clamp(0.05, 1.0)
    } else {
        1.0
    }
}

fn terminal_row_data(
    snapshot: &TerminalSnapshot,
    row: usize,
    selected_bounds: Option<(TerminalCellPosition, TerminalCellPosition)>,
    options: TerminalRenderOptions,
    font_family: &str,
) -> (Vec<TerminalTextChunk>, Vec<TerminalBackgroundSpan>) {
    let fonts = {
        let normal = font(font_family.to_owned());
        [
            normal.clone(),
            normal.clone().italic(),
            normal.clone().bold(),
            normal.bold().italic(),
        ]
    };
    let mut chunks = Vec::new();
    let mut current_text = String::new();
    let mut current_runs = Vec::new();
    let mut current_cells = Vec::new();
    let mut current_requires_cell_scaling = false;
    let mut current_start = 0;

    let flush_chunk = |chunks: &mut Vec<TerminalTextChunk>,
                       current_text: &mut String,
                       current_runs: &mut Vec<TextRun>,
                       current_cells: &mut Vec<TerminalTextCell>,
                       current_requires_cell_scaling: &mut bool,
                       current_start: &mut usize| {
        if !current_text.is_empty() {
            let span_columns = current_cells.iter().map(|cell| cell.width_columns).sum();
            chunks.push(TerminalTextChunk {
                start_column: *current_start,
                width_columns: 1,
                span_columns,
                requires_cell_scaling: *current_requires_cell_scaling,
                text: std::mem::take(current_text),
                runs: std::mem::take(current_runs),
                cells: std::mem::take(current_cells),
            });
            *current_requires_cell_scaling = false;
        }
    };
    let mut backgrounds = Vec::new();

    for column in 0..snapshot.size.columns {
        let Some(cell) = snapshot.cell(row, column) else {
            continue;
        };
        if cell.flags.wide_spacer {
            flush_chunk(
                &mut chunks,
                &mut current_text,
                &mut current_runs,
                &mut current_cells,
                &mut current_requires_cell_scaling,
                &mut current_start,
            );
            continue;
        }

        let (mut foreground, mut background) = terminal_cell_colors(cell, options.theme);
        let selected = selected_bounds.is_some_and(|(start, end)| {
            (start..=end).contains(&TerminalCellPosition {
                row: row as i32,
                column,
            })
        });
        if selected {
            background = theme_color(options.theme.selection_background);
        }
        let cursor_at_cell =
            snapshot.cursor.visible && terminal_cursor_position(snapshot) == (row, column);
        if cursor_at_cell && options.cursor_focused {
            foreground = theme_color(options.theme.cursor_foreground);
            background = theme_color(options.theme.cursor_background);
        }

        let width_columns =
            (if cell.flags.wide { 2 } else { 1 }).min(snapshot.size.columns.saturating_sub(column));
        let background_color = color_to_rgb(background, false, options.theme);
        if background_color != options.theme.terminal_background {
            push_terminal_background(&mut backgrounds, column, width_columns, background_color);
        }
        if cell.flags.leading_wide_spacer {
            flush_chunk(
                &mut chunks,
                &mut current_text,
                &mut current_runs,
                &mut current_cells,
                &mut current_requires_cell_scaling,
                &mut current_start,
            );
            continue;
        }

        let mut character = String::new();
        character.push(cell.character);
        character.extend(cell.zerowidth.iter().copied());
        let color = rgb(color_to_rgb(foreground, true, options.theme)).into();
        let run = TextRun {
            len: character.len(),
            font: fonts[usize::from(cell.flags.italic) + usize::from(cell.flags.bold) * 2].clone(),
            color,
            background_color: None,
            underline: cell.flags.underline.then(|| UnderlineStyle {
                thickness: px(1.),
                color: Some(color),
                wavy: false,
            }),
            strikethrough: cell.flags.strike.then(|| StrikethroughStyle {
                thickness: px(1.),
                color: Some(color),
            }),
        };
        let requires_cell_scaling = !character.is_ascii();
        let text_cell = TerminalTextCell {
            text: character.clone(),
            run: run.clone(),
            width_columns,
        };
        if cell.flags.wide {
            flush_chunk(
                &mut chunks,
                &mut current_text,
                &mut current_runs,
                &mut current_cells,
                &mut current_requires_cell_scaling,
                &mut current_start,
            );
            chunks.push(TerminalTextChunk {
                start_column: column,
                width_columns,
                span_columns: width_columns,
                requires_cell_scaling: true,
                text: character,
                runs: vec![run],
                cells: vec![text_cell],
            });
        } else {
            if current_text.is_empty() {
                current_start = column;
            }
            current_text.push_str(&character);
            append_terminal_text_run(&mut current_runs, run);
            current_cells.push(text_cell);
            current_requires_cell_scaling |= requires_cell_scaling;
        }
    }
    flush_chunk(
        &mut chunks,
        &mut current_text,
        &mut current_runs,
        &mut current_cells,
        &mut current_requires_cell_scaling,
        &mut current_start,
    );

    (chunks, backgrounds)
}

fn terminal_cell_colors(
    cell: &crate::terminal::TerminalCell,
    theme: ThemeColors,
) -> (TerminalColor, TerminalColor) {
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
    (foreground, background)
}

fn append_terminal_text_run(runs: &mut Vec<TextRun>, run: TextRun) {
    if let Some(previous) = runs.last_mut()
        && previous.font == run.font
        && previous.color == run.color
        && previous.background_color == run.background_color
        && previous.underline == run.underline
        && previous.strikethrough == run.strikethrough
    {
        previous.len += run.len;
    } else {
        runs.push(run);
    }
}

fn push_terminal_background(
    backgrounds: &mut Vec<TerminalBackgroundSpan>,
    start_column: usize,
    width_columns: usize,
    color: u32,
) {
    if width_columns == 0 {
        return;
    }
    if let Some(previous) = backgrounds.last_mut()
        && previous.color == color
        && previous.start_column + previous.width_columns == start_column
    {
        previous.width_columns += width_columns;
    } else {
        backgrounds.push(TerminalBackgroundSpan {
            start_column,
            width_columns,
            color,
        });
    }
}

fn terminal_cell_bounds(
    bounds: Bounds<gpui::Pixels>,
    metrics: TerminalMetrics,
    row: usize,
    column: usize,
    width_columns: usize,
) -> Bounds<gpui::Pixels> {
    let left = terminal_grid_edge(
        f32::from(bounds.origin.x),
        metrics.cell_width,
        column,
        metrics.scale_factor,
    );
    let right = terminal_grid_edge(
        f32::from(bounds.origin.x),
        metrics.cell_width,
        column.saturating_add(width_columns),
        metrics.scale_factor,
    );
    let top = terminal_grid_edge(
        f32::from(bounds.origin.y),
        metrics.line_height,
        row,
        metrics.scale_factor,
    );
    let bottom = terminal_grid_edge(
        f32::from(bounds.origin.y),
        metrics.line_height,
        row.saturating_add(1),
        metrics.scale_factor,
    );
    Bounds::new(
        point(px(left), px(top)),
        size(px((right - left).max(0.0)), px((bottom - top).max(0.0))),
    )
}

fn terminal_cursor_position(snapshot: &TerminalSnapshot) -> (usize, usize) {
    let row = snapshot
        .cursor
        .row
        .min(snapshot.size.lines.saturating_sub(1));
    let mut column = snapshot
        .cursor
        .column
        .min(snapshot.size.columns.saturating_sub(1));
    if snapshot
        .cell(row, column)
        .is_some_and(|cell| cell.flags.wide_spacer)
    {
        column = column.saturating_sub(1);
    }
    (row, column)
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
    // Match the 16-color palette from kitty_normal.conf so ANSI output and
    // the Water chrome share the same visual language.
    const COLORS: [u32; 16] = [
        0x000000, 0xaa0000, 0x00aa00, 0xf57900, 0x1e8acb, 0xaa00aa, 0x00aaaa, 0xaaaaaa, 0x555555,
        0xff5555, 0x339966, 0xffff55, 0x729fcf, 0xd530c6, 0x55ffff, 0xffffff,
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
        #[cfg(target_os = "macos")]
        if !window.is_fullscreen() {
            // AppKit keeps its standard traffic lights even with a transparent
            // titlebar. Park them off-canvas so the custom controls below are
            // the only visible controls in the integrated titlebar.
            window.set_traffic_light_position(point(px(-100.), px(9.)));
        }

        let metrics = self.measured_terminal_metrics(window);
        self.terminal_metrics = metrics;
        self.input_handler_terminal = self
            .active_terminal_snapshot()
            .map(|snapshot| snapshot.terminal_id);
        let theme = self.config.theme.colors();

        let window_active = window.is_window_active() && self.focus_handle.is_focused(window);
        let content = div()
            .flex_1()
            .flex()
            .flex_col()
            .min_w(px(0.))
            .min_h(px(0.))
            .overflow_hidden()
            .bg(rgb(theme.terminal_background))
            .child(self.render_active_tab(window_active, metrics, theme, cx));

        let action_view = cx.entity();
        let mut main_content = div().flex_1().min_w(px(0.)).min_h(px(0.)).flex();
        if !self.sidebar_collapsed {
            main_content = main_content.child(self.render_sidebar(theme, cx));
        }
        let main_content = main_content.child(content);
        let overlay = self
            .render_dialog(theme, cx)
            .or_else(|| self.render_context_menu(theme, cx));
        let mut root = div()
            .size_full()
            .flex()
            .flex_col()
            .on_action(|_: &HideWindow, window, _cx| {
                window.remove_window();
            })
            .on_action(|_: &MinimizeWindow, window, _cx| {
                window.minimize_window();
            })
            .on_action(|_: &IgnoreQuit, _window, _cx| {})
            .on_action({
                let view = action_view.clone();
                move |_: &NewTerminalTab, _window, cx| {
                    view.update(cx, |workspace, cx| {
                        if !workspace.has_transient_ui() {
                            workspace.new_terminal_tab(cx);
                        }
                    });
                }
            })
            .on_action({
                let view = action_view.clone();
                move |_: &SplitRight, _window, cx| {
                    view.update(cx, |workspace, cx| {
                        if !workspace.has_transient_ui() {
                            workspace.split_active_pane(SplitDirection::Right, cx);
                        }
                    });
                }
            })
            .on_action({
                let view = action_view.clone();
                move |_: &SplitDown, _window, cx| {
                    view.update(cx, |workspace, cx| {
                        if !workspace.has_transient_ui() {
                            workspace.split_active_pane(SplitDirection::Down, cx);
                        }
                    });
                }
            })
            .on_action({
                let view = action_view.clone();
                move |_: &NewWorkspace, _window, cx| {
                    view.update(cx, |workspace, cx| {
                        if !workspace.has_transient_ui() {
                            workspace.dispatch(AppCommand::Workspace(WorkspaceCommand::New), cx);
                        }
                    });
                }
            })
            .on_action({
                let view = action_view.clone();
                move |_: &ToggleSidebar, _window, cx| {
                    view.update(cx, |workspace, cx| {
                        if !workspace.has_transient_ui() {
                            workspace.toggle_sidebar(cx);
                        }
                    });
                }
            })
            .on_action(cx.listener(|workspace, _: &RenameWorkspace, window, cx| {
                workspace.begin_rename_active_workspace(window, cx);
            }))
            .on_action(cx.listener(|workspace, _: &RenameTab, window, cx| {
                workspace.begin_rename_active_tab(window, cx);
            }))
            .track_focus(&self.focus_handle)
            .on_key_down(cx.listener(|this, event: &KeyDownEvent, _window, cx| {
                this.handle_key_down(event, cx);
            }))
            .bg(rgb(theme.terminal_background))
            .text_color(rgb(theme.ui_foreground))
            .child(workspace_mouse_event_observer(cx.entity()))
            .child(self.render_titlebar(theme, cx))
            .child(main_content);
        if let Some(overlay) = overlay {
            root = root.child(overlay);
        }
        root
    }
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
        assert!(terminal_key_uses_text_input_handler(&keystroke(
            "a",
            Some("a"),
            Modifiers::none(),
        )));
        assert!(!terminal_key_uses_text_input_handler(&keystroke(
            "enter",
            Some("\r"),
            Modifiers::none(),
        )));
        assert!(!terminal_key_uses_text_input_handler(&keystroke(
            "return",
            None,
            Modifiers::none(),
        )));
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
    fn terminal_home_and_end_use_standard_and_application_sequences() {
        let normal = TerminalModes::default();
        assert_eq!(
            terminal_input_for_keystroke_with_modes(
                &keystroke("home", None, Modifiers::none()),
                normal,
            ),
            Some("\u{1b}[H".to_owned())
        );
        assert_eq!(
            terminal_input_for_keystroke_with_modes(
                &keystroke("end", None, Modifiers::none()),
                normal,
            ),
            Some("\u{1b}[F".to_owned())
        );

        let application = TerminalModes {
            application_cursor: true,
            ..normal
        };
        assert_eq!(
            terminal_input_for_keystroke_with_modes(
                &keystroke("home", None, Modifiers::none()),
                application,
            ),
            Some("\u{1b}OH".to_owned())
        );
        assert_eq!(
            terminal_input_for_keystroke_with_modes(
                &keystroke("end", None, Modifiers::none()),
                application,
            ),
            Some("\u{1b}OF".to_owned())
        );
        assert_eq!(
            terminal_input_for_keystroke_with_modes(
                &keystroke("home", None, Modifiers::shift()),
                application,
            ),
            Some("\u{1b}[1;2H".to_owned())
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
    fn leading_wide_placeholders_do_not_become_a_second_character() {
        let terminal_id = TerminalId::new(1);
        let mut snapshot = TerminalSnapshot::empty(terminal_id, TerminalSize::new(4, 2));
        snapshot.cells[3].flags.leading_wide_spacer = true;
        snapshot.cells[4].character = '界';
        snapshot.cells[4].flags.wide = true;
        snapshot.cells[5].flags.wide_spacer = true;

        let wrapped_wide_character = TerminalSelection {
            terminal_id,
            anchor: endpoint(0, 3, TerminalSelectionSide::Left),
            head: endpoint(1, 0, TerminalSelectionSide::Right),
        };
        let (start, end) = selection_bounds(&snapshot, wrapped_wide_character).unwrap();
        assert_eq!((start.row, start.column), (1, 0));
        assert_eq!((end.row, end.column), (1, 1));
        assert_eq!(
            selected_terminal_text(&snapshot, wrapped_wide_character),
            "界"
        );

        let placeholder_only = TerminalSelection {
            terminal_id,
            anchor: endpoint(0, 3, TerminalSelectionSide::Left),
            head: endpoint(0, 3, TerminalSelectionSide::Right),
        };
        assert!(selection_bounds(&snapshot, placeholder_only).is_none());
    }

    #[test]
    fn terminal_rows_shape_wide_cells_with_two_cell_advances() {
        let terminal_id = TerminalId::new(1);
        let mut snapshot = TerminalSnapshot::empty(terminal_id, TerminalSize::new(5, 1));
        snapshot.cells[0].character = 'a';
        snapshot.cells[1].character = '界';
        snapshot.cells[1].flags.wide = true;
        snapshot.cells[2].flags.wide_spacer = true;
        snapshot.cells[3].character = 'b';
        let options = TerminalRenderOptions {
            metrics: TerminalMetrics::default(),
            theme: ThemeColors {
                terminal_background: 0,
                terminal_foreground: 1,
                selection_background: 2,
                cursor_foreground: 3,
                cursor_background: 4,
                inactive_cursor: 5,
                inverse_foreground: 6,
                inverse_background: 7,
                pane_background: 8,
                active_pane_border: 9,
                inactive_pane_border: 10,
                chrome_background: 11,
                tab_active_background: 12,
                tab_inactive_background: 13,
                tab_add_background: 14,
                ui_foreground: 15,
            },
            cursor_focused: false,
        };
        let (chunks, _) =
            terminal_row_data(&snapshot, 0, None, options, "Sarasa Term SC Nerd Font");
        assert_eq!(
            chunks
                .iter()
                .map(|chunk| (chunk.start_column, chunk.width_columns, chunk.text.as_str()))
                .collect::<Vec<_>>(),
            vec![(0, 1, "a"), (1, 2, "界"), (3, 1, "b ")]
        );
    }

    #[test]
    fn oversized_unicode_uses_a_bounded_font_scale() {
        assert!((terminal_fit_scale(21.0, 14.0) - (2.0 / 3.0)).abs() < 1e-6);
        assert_eq!(terminal_fit_scale(14.0, 14.0), 1.0);
        assert_eq!(terminal_fit_scale(12.0, 14.0), 1.0);
        assert_eq!(terminal_fit_scale(f32::INFINITY, 14.0), 1.0);
    }

    #[test]
    fn terminal_grid_geometry_is_shared_by_bounds_and_mouse_hit_testing() {
        let metrics = TerminalMetrics {
            cell_width: 8.4,
            line_height: 17.3,
            scale_factor: 2.0,
        };
        let bounds = Bounds::new(point(px(0.3), px(1.2)), size(px(42.0), px(35.0)));
        let edges: Vec<_> = (0..8)
            .map(|column| {
                terminal_grid_edge(
                    f32::from(bounds.origin.x),
                    metrics.cell_width,
                    column,
                    metrics.scale_factor,
                )
            })
            .collect();
        assert!(edges.windows(2).all(|pair| pair[0] <= pair[1]));

        let first = terminal_cell_bounds(bounds, metrics, 0, 0, 1);
        let second = terminal_cell_bounds(bounds, metrics, 0, 1, 1);
        let first_x = f32::from(first.origin.x) + f32::from(first.size.width) * 0.75;
        let second_x = f32::from(second.origin.x) + f32::from(second.size.width) * 0.25;
        let y = f32::from(first.origin.y) + f32::from(first.size.height) * 0.5;

        let first_hit = terminal_mouse_position(point(px(first_x), px(y)), Some(bounds), metrics);
        assert_eq!(
            (first_hit.column, first_hit.side),
            (1, TerminalSelectionSide::Right)
        );
        let second_hit = terminal_mouse_position(point(px(second_x), px(y)), Some(bounds), metrics);
        assert_eq!(
            (second_hit.column, second_hit.side),
            (2, TerminalSelectionSide::Left)
        );
    }

    #[test]
    fn utf16_ime_offsets_follow_unicode_scalar_boundaries() {
        let text = "a界😀";
        assert_eq!(utf16_len(text), 4);
        assert_eq!(utf16_offset_to_byte(text, 0), 0);
        assert_eq!(utf16_offset_to_byte(text, 1), 1);
        assert_eq!(utf16_offset_to_byte(text, 2), 4);
        assert_eq!(utf16_offset_to_byte(text, 3), 4);
        assert_eq!(utf16_offset_to_byte(text, 4), text.len());
        assert_eq!(normalize_utf16_range(0..99, 4), 0..4);
        assert_eq!(
            normalize_utf16_range(std::ops::Range { start: 3, end: 1 }, 4),
            1..1
        );
    }

    #[gpui::test]
    fn ime_composition_state_commits_once_to_the_focused_terminal(cx: &mut gpui::TestAppContext) {
        let mut host = crate::app::ModelHost::start();
        let client = host.client();
        let workspace = client
            .dispatch(crate::command::AppCommand::Workspace(
                crate::command::WorkspaceCommand::Create,
            ))
            .unwrap();
        client.wait_operation(workspace).unwrap();
        let tab = client
            .dispatch(crate::command::AppCommand::Tab(
                crate::command::TabCommand::New { title: None },
            ))
            .unwrap();
        client.wait_operation(tab).unwrap();
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
        let terminal_id = match spawn.result.unwrap() {
            crate::command::OperationResult::TerminalSpawned { terminal_id } => terminal_id,
            result => panic!("unexpected result: {result:?}"),
        };
        let snapshot = client.state_dump().unwrap();
        let (view, cx) = cx.add_window_view(|_, cx| {
            WorkspaceView::new(client.clone(), snapshot.clone(), cx.focus_handle())
        });
        view.update_in(cx, |view, window, cx| {
            window.focus(&view.focus_handle, cx);
            view.ime_terminal = Some(terminal_id);
            <WorkspaceView as EntityInputHandler>::replace_and_mark_text_in_range(
                view,
                None,
                "拼音",
                Some(2..2),
                window,
                cx,
            );
        });
        let (marked_text, selected_range) = view.update_in(cx, |view, _, _| {
            (
                view.ime_marked_text.clone(),
                view.ime_selected_range.clone(),
            )
        });
        assert_eq!(marked_text, "拼音");
        assert_eq!(selected_range, 2..2);

        view.update_in(cx, |view, window, cx| {
            <WorkspaceView as EntityInputHandler>::replace_text_in_range(
                view,
                None,
                "print -r -- IME_中",
                window,
                cx,
            );
        });
        assert!(view.update_in(cx, |view, _, _| view.ime_marked_text.is_empty()));
        cx.simulate_keystrokes("enter");
        client
            .terminal_contains(terminal_id, "IME_中", std::time::Duration::from_secs(5))
            .unwrap();
        host.shutdown();
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
        cx.update(|cx| cx.bind_keys(crate::ui::application::window_key_bindings()));
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
