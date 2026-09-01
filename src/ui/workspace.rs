use std::collections::BTreeMap;
use std::sync::{Arc, Mutex};

use gpui::{
    AnyElement, App, Bounds, Context, Entity, FocusHandle, Focusable, FontWeight, KeyDownEvent,
    Keystroke, MouseButton, MouseDownEvent, MouseMoveEvent, MouseUpEvent, Point, ScrollDelta,
    ScrollWheelEvent, SharedString, Task, Window, canvas, div, prelude::*, px, rgb,
};

use crate::app::model::{PaneTreeDump, TabDump};
use crate::app::{CommandClient, ModelSnapshot, ModelSnapshotReceiver};
use crate::command::{AppCommand, PaneCommand, SplitDirection, TabCommand, TerminalCommand};
use crate::ids::{PaneId, TabId, TerminalId};
use crate::pane::SplitAxis;
use crate::surface::SurfaceState;
use crate::terminal::{
    TerminalCellFlags, TerminalColor, TerminalModes, TerminalSize, TerminalSnapshot,
};

const TERMINAL_CELL_WIDTH: f32 = 8.4;
const TERMINAL_LINE_HEIGHT: f32 = 18.0;

#[derive(Debug, Clone, Copy, PartialEq, Eq, PartialOrd, Ord)]
struct TerminalCellPosition {
    row: usize,
    column: usize,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
struct TerminalSelection {
    terminal_id: TerminalId,
    anchor: TerminalCellPosition,
    head: TerminalCellPosition,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum TerminalMouseReportKind {
    Press,
    Release,
    Motion,
}

pub struct WorkspaceView {
    client: CommandClient,
    snapshot: ModelSnapshot,
    focus_handle: FocusHandle,
    resize_requests: Arc<Mutex<BTreeMap<TerminalId, TerminalSize>>>,
    terminal_bounds: Arc<Mutex<BTreeMap<TerminalId, Bounds<gpui::Pixels>>>>,
    focused_pane: Option<PaneId>,
    selection: Option<TerminalSelection>,
    dragging_terminal: Option<TerminalId>,
    reported_mouse: Option<(TerminalId, MouseButton)>,
    last_reported_mouse_cell: Option<(TerminalId, TerminalCellPosition)>,
}

impl WorkspaceView {
    pub fn new(client: CommandClient, snapshot: ModelSnapshot, focus_handle: FocusHandle) -> Self {
        let focused_pane = snapshot.focused_pane;
        Self {
            client,
            snapshot,
            focus_handle,
            resize_requests: Arc::new(Mutex::new(BTreeMap::new())),
            terminal_bounds: Arc::new(Mutex::new(BTreeMap::new())),
            focused_pane,
            selection: None,
            dragging_terminal: None,
            reported_mouse: None,
            last_reported_mouse_cell: None,
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

    fn terminal_cell_at(
        &self,
        terminal_id: TerminalId,
        position: Point<gpui::Pixels>,
    ) -> Option<TerminalCellPosition> {
        let snapshot = self.terminal_snapshot_for(terminal_id)?;
        let (column, row) =
            terminal_mouse_position(position, self.terminal_bounds_for(terminal_id));
        Some(TerminalCellPosition {
            row: row
                .saturating_sub(1)
                .min(snapshot.size.lines.saturating_sub(1)),
            column: column
                .saturating_sub(1)
                .min(snapshot.size.columns.saturating_sub(1)),
        })
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
        if !snapshot.modes.mouse_reporting {
            return false;
        }
        let Some(text) = terminal_mouse_button_input(
            event.position,
            event.button,
            true,
            false,
            event.modifiers,
            snapshot.modes,
            self.terminal_bounds_for(terminal_id),
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
        let Some(point) = self.terminal_cell_at(terminal_id, position) else {
            return;
        };
        self.selection = Some(TerminalSelection {
            terminal_id,
            anchor: point,
            head: point,
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
        let Some(point) = self.terminal_cell_at(terminal_id, event.position) else {
            return;
        };
        if let Some(selection) = self.selection.as_mut()
            && selection.head != point
        {
            selection.head = point;
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
        let Some(text) = terminal_mouse_button_input(
            event.position,
            button,
            true,
            true,
            event.modifiers,
            snapshot.modes,
            self.terminal_bounds_for(terminal_id),
        ) else {
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
                    snapshot.modes,
                    self.terminal_bounds_for(terminal_id),
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
        }
        self.dragging_terminal = None;
    }

    fn copy_terminal_selection(&self, terminal_id: TerminalId, cx: &mut Context<Self>) -> bool {
        let Some(selection) = self.selection else {
            return false;
        };
        if selection.terminal_id != terminal_id || selection.anchor == selection.head {
            return false;
        }
        let Some(snapshot) = self.terminal_snapshot_for(terminal_id) else {
            return false;
        };
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
                    (width / TERMINAL_CELL_WIDTH).floor() as usize,
                    (height / TERMINAL_LINE_HEIGHT).floor() as usize,
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
        .into_any_element()
    }

    fn handle_key_down(&mut self, event: &KeyDownEvent, cx: &mut Context<Self>) {
        let Some(focused_pane) = self.focused_pane else {
            return;
        };
        let keystroke = &event.keystroke;
        if keystroke.modifiers.platform {
            match keystroke.key.as_str() {
                "\\" => self.dispatch(
                    AppCommand::Pane(PaneCommand::Split {
                        pane_id: Some(focused_pane),
                        direction: SplitDirection::Right,
                    }),
                    cx,
                ),
                "-" => self.dispatch(
                    AppCommand::Pane(PaneCommand::Split {
                        pane_id: Some(focused_pane),
                        direction: SplitDirection::Down,
                    }),
                    cx,
                ),
                "t" => self.dispatch(AppCommand::Tab(TabCommand::New { title: None }), cx),
                "v" => {
                    if let Some(terminal_id) = self.active_terminal_id() {
                        let modes = self
                            .active_terminal_snapshot()
                            .map(|snapshot| snapshot.modes)
                            .unwrap_or_default();
                        self.paste_into_terminal(terminal_id, modes.bracketed_paste, cx);
                    }
                }
                "c" => {
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
                }
                "d" => {
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
                }
                _ => {}
            }
            return;
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
                    if snapshot.state_revision > view.snapshot.state_revision {
                        view.focused_pane = snapshot.focused_pane;
                        view.snapshot = snapshot;
                        cx.notify();
                    }
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
        cx: &mut Context<Self>,
    ) -> AnyElement {
        let background = if active { rgb(0x29435c) } else { rgb(0x1d2733) };
        div()
            .h(px(32.))
            .px(px(12.))
            .items_center()
            .flex()
            .bg(background)
            .text_color(rgb(0xd8e2ef))
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

    fn render_pane_tree(&self, tree: &PaneTreeDump, cx: &mut Context<Self>) -> AnyElement {
        self.render_pane_tree_with_grow(tree, 1.0, cx)
    }

    fn render_pane_tree_with_grow(
        &self,
        tree: &PaneTreeDump,
        grow: f32,
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
                let border = if active { rgb(0x61b3ff) } else { rgb(0x364656) };
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
                        .map(|snapshot| render_terminal_snapshot(snapshot, self.selection))
                        .unwrap_or_else(|| {
                            div()
                                .text_color(rgb(0xaab8c7))
                                .child("Starting terminal…")
                                .into_any_element()
                        })
                } else {
                    div()
                        .text_color(rgb(0xc9d6e3))
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
                    .bg(rgb(0x101820))
                    .text_color(rgb(0xc9d6e3))
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
                                let lines = terminal_scroll_lines(event);
                                if lines != 0 {
                                    if mouse_modes.mouse_reporting {
                                        if let Some(bytes) = terminal_mouse_input(
                                            event,
                                            mouse_modes,
                                            this.terminal_bounds_for(terminal_id),
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
                    .child(self.render_pane_tree_with_grow(first, ratio, cx))
                    .child(self.render_pane_tree_with_grow(second, 1.0 - ratio, cx))
                    .into_any_element()
            }
        }
    }

    fn render_active_tab(&self, cx: &mut Context<Self>) -> AnyElement {
        let Some(workspace) = self.snapshot.workspace.as_ref() else {
            return div()
                .flex_1()
                .items_center()
                .justify_center()
                .text_color(rgb(0xaab8c7))
                .child("No workspace")
                .into_any_element();
        };
        let Some(active_tab_id) = workspace.active_tab else {
            return div()
                .flex_1()
                .items_center()
                .justify_center()
                .text_color(rgb(0xaab8c7))
                .child("No tab")
                .into_any_element();
        };
        let Some(tab) = workspace.tabs.iter().find(|tab| tab.id == active_tab_id) else {
            return div()
                .flex_1()
                .items_center()
                .justify_center()
                .text_color(rgb(0xaab8c7))
                .child("Active tab is unavailable")
                .into_any_element();
        };
        self.render_pane_tree(&tab.tree, cx)
    }
}

impl Focusable for WorkspaceView {
    fn focus_handle(&self, _cx: &App) -> FocusHandle {
        self.focus_handle.clone()
    }
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

fn terminal_scroll_lines(event: &ScrollWheelEvent) -> i32 {
    let delta = match event.delta {
        ScrollDelta::Lines(delta) => delta.y,
        ScrollDelta::Pixels(delta) => f32::from(delta.y) / TERMINAL_LINE_HEIGHT,
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

fn terminal_mouse_input(
    event: &ScrollWheelEvent,
    modes: TerminalModes,
    bounds: Option<Bounds<gpui::Pixels>>,
) -> Option<Vec<u8>> {
    let lines = terminal_scroll_lines(event);
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
        modes,
        bounds,
        TerminalMouseReportKind::Press,
    ))
}

fn terminal_mouse_button_input(
    position: Point<gpui::Pixels>,
    button: MouseButton,
    pressed: bool,
    motion: bool,
    modifiers: gpui::Modifiers,
    modes: TerminalModes,
    bounds: Option<Bounds<gpui::Pixels>>,
) -> Option<Vec<u8>> {
    let button = mouse_button_code(button)?;
    Some(terminal_mouse_sequence(
        button,
        1,
        position,
        modifiers,
        modes,
        bounds,
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
    modes: TerminalModes,
    bounds: Option<Bounds<gpui::Pixels>>,
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
    let (column, row) = terminal_mouse_position(position, bounds);
    if modes.sgr_mouse {
        let suffix = if release { 'm' } else { 'M' };
        let mut input = Vec::with_capacity(count * 16);
        for _ in 0..count {
            input.extend_from_slice(format!("\u{1b}[<{button};{column};{row}{suffix}").as_bytes());
        }
        return input;
    }

    let max_coordinate = if modes.utf8_mouse { 2_047 } else { 223 };
    let column = column.min(max_coordinate);
    let row = row.min(max_coordinate);
    let mut input = Vec::with_capacity(count * 6);
    for _ in 0..count {
        input.extend_from_slice(b"\x1b[M");
        push_mouse_coordinate(&mut input, 32 + button, modes.utf8_mouse);
        push_mouse_coordinate(&mut input, 32 + column as u16, modes.utf8_mouse);
        push_mouse_coordinate(&mut input, 32 + row as u16, modes.utf8_mouse);
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
) -> (usize, usize) {
    let Some(bounds) = bounds else {
        return (1, 1);
    };
    let x = (f32::from(position.x) - f32::from(bounds.origin.x)).max(0.0);
    let y = (f32::from(position.y) - f32::from(bounds.origin.y)).max(0.0);
    (
        (x / TERMINAL_CELL_WIDTH).floor() as usize + 1,
        (y / TERMINAL_LINE_HEIGHT).floor() as usize + 1,
    )
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
    selection: Option<TerminalSelection>,
    terminal_id: TerminalId,
    row: usize,
    column: usize,
) -> bool {
    let Some(selection) = selection else {
        return false;
    };
    if selection.terminal_id != terminal_id || selection.anchor == selection.head {
        return false;
    }
    let start = selection.anchor.min(selection.head);
    let end = selection.anchor.max(selection.head);
    (start..=end).contains(&TerminalCellPosition { row, column })
}

fn selected_terminal_text(snapshot: &TerminalSnapshot, selection: TerminalSelection) -> String {
    let start = selection.anchor.min(selection.head);
    let end = selection.anchor.max(selection.head);
    let mut text = String::new();
    for row in start.row..=end.row {
        let first_column = if row == start.row { start.column } else { 0 };
        let last_column = if row == end.row {
            end.column
        } else {
            snapshot.size.columns.saturating_sub(1)
        };
        let line_start = text.len();
        for column in first_column..=last_column {
            let Some(cell) = snapshot.cell(row, column) else {
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
                .cell(row, snapshot.size.columns.saturating_sub(1))
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
) -> AnyElement {
    let mut terminal = div()
        .flex_1()
        .flex()
        .min_w(px(0.))
        .min_h(px(0.))
        .flex_col()
        .overflow_hidden()
        .font_family("Menlo")
        .text_sm()
        .whitespace_nowrap()
        .bg(rgb(0x0b1117));
    for row in 0..snapshot.size.lines {
        let mut row_element = div()
            .h(px(TERMINAL_LINE_HEIGHT))
            .min_w(px(0.))
            .flex_none()
            .flex()
            .flex_nowrap()
            .whitespace_nowrap();
        let mut current: Option<(
            TerminalColor,
            TerminalColor,
            TerminalCellFlags,
            String,
            usize,
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
            if cell.flags.inverse {
                std::mem::swap(&mut foreground, &mut background);
            }
            if is_terminal_cell_selected(selection, snapshot.terminal_id, row, column) {
                background = TerminalColor::Rgb {
                    red: 51,
                    green: 93,
                    blue: 122,
                };
            }
            if snapshot.cursor.visible
                && snapshot.cursor.row == row
                && snapshot.cursor.column == column
            {
                foreground = TerminalColor::Rgb {
                    red: 11,
                    green: 17,
                    blue: 23,
                };
                background = TerminalColor::Rgb {
                    red: 156,
                    green: 200,
                    blue: 239,
                };
            }
            let mut character = String::new();
            character.push(cell.character);
            character.extend(cell.zerowidth.iter().copied());
            let width_columns = if cell.flags.wide { 2 } else { 1 };
            let should_merge = current
                .as_ref()
                .map(|(current_fg, current_bg, current_flags, _, _)| {
                    *current_fg == foreground
                        && *current_bg == background
                        && *current_flags == cell.flags
                })
                .unwrap_or(false);
            if should_merge {
                if let Some((_, _, _, text, width_columns_total)) = current.as_mut() {
                    text.push_str(&character);
                    *width_columns_total += width_columns;
                }
            } else {
                if let Some((fg, bg, flags, text, width_columns)) = current.take() {
                    row_element =
                        row_element.child(render_terminal_run(fg, bg, flags, text, width_columns));
                }
                current = Some((foreground, background, cell.flags, character, width_columns));
            }
        }
        if let Some((fg, bg, flags, text, width_columns)) = current {
            row_element =
                row_element.child(render_terminal_run(fg, bg, flags, text, width_columns));
        }
        terminal = terminal.child(row_element);
    }
    terminal.into_any_element()
}

fn render_terminal_run(
    foreground: TerminalColor,
    background: TerminalColor,
    flags: TerminalCellFlags,
    text: String,
    width_columns: usize,
) -> AnyElement {
    let mut run = div()
        .w(px(TERMINAL_CELL_WIDTH * width_columns as f32))
        .h(px(TERMINAL_LINE_HEIGHT))
        .flex_none()
        .text_color(rgb(color_to_rgb(foreground, true)))
        .bg(rgb(color_to_rgb(background, false)));
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

fn color_to_rgb(color: TerminalColor, foreground: bool) -> u32 {
    match color {
        TerminalColor::Rgb { red, green, blue } => {
            (u32::from(red) << 16) | (u32::from(green) << 8) | u32::from(blue)
        }
        TerminalColor::Named { value } => match value {
            0..=15 => basic_color(value as usize),
            256 if foreground => 0xd8e2ef,
            257 if !foreground => 0x101820,
            258 => 0xd8e2ef,
            259 => 0x101820,
            _ => 0xc9d6e3,
        },
        TerminalColor::Indexed { value } => indexed_color(value),
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
    fn render(&mut self, _window: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
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
            .bg(rgb(0x151e28))
            .children(
                tab_data
                    .into_iter()
                    .map(|(tab_id, title, active, active_pane)| {
                        self.tab_button(tab_id, title, active, active_pane, cx)
                    }),
            )
            .child(
                div()
                    .h(px(32.))
                    .w(px(32.))
                    .items_center()
                    .justify_center()
                    .flex()
                    .bg(rgb(0x263544))
                    .text_color(rgb(0xe6eef7))
                    .child("+")
                    .on_mouse_down(
                        MouseButton::Left,
                        cx.listener(|this, _event: &MouseDownEvent, window, cx| {
                            this.focus_handle.focus(window, cx);
                            this.dispatch(AppCommand::Tab(TabCommand::New { title: None }), cx);
                        }),
                    ),
            );

        div()
            .size_full()
            .flex()
            .flex_col()
            .track_focus(&self.focus_handle)
            .on_key_down(cx.listener(|this, event: &KeyDownEvent, _window, cx| {
                this.handle_key_down(event, cx);
            }))
            .on_mouse_move(cx.listener(|this, event: &MouseMoveEvent, _window, cx| {
                this.update_terminal_selection(event, cx);
            }))
            .on_mouse_up(
                MouseButton::Left,
                cx.listener(|this, event: &MouseUpEvent, _window, cx| {
                    this.finish_terminal_selection(event, cx);
                }),
            )
            .bg(rgb(0x0b1117))
            .text_color(rgb(0xe6eef7))
            .child(
                div()
                    .h(px(36.))
                    .px(px(12.))
                    .items_center()
                    .flex()
                    .bg(rgb(0x0f161e))
                    .child(SharedString::from(format!(
                        "Water · Workspace {workspace_id} · revision {revision}"
                    ))),
            )
            .child(tab_bar)
            .child(
                div()
                    .flex_1()
                    .flex()
                    .min_w(px(0.))
                    .min_h(px(0.))
                    .overflow_hidden()
                    .child(self.render_active_tab(cx)),
            )
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
                if snapshot.state_revision > view.snapshot.state_revision {
                    view.focused_pane = snapshot.focused_pane;
                    view.snapshot = snapshot;
                    cx.notify();
                }
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
            anchor: TerminalCellPosition { row: 0, column: 0 },
            head: TerminalCellPosition { row: 1, column: 7 },
        };
        assert_eq!(
            selected_terminal_text(&snapshot, selection),
            "hell界\nworld"
        );
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
                TerminalModes {
                    mouse_reporting: true,
                    sgr_mouse: true,
                    ..TerminalModes::default()
                },
                None,
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
