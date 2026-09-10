use std::cell::RefCell;
use std::path::PathBuf;
use std::rc::Rc;
use std::sync::Arc;
use std::sync::atomic::{AtomicBool, Ordering};

use gpui::{
    App, AppContext, Bounds, DispatchEventResult, Focusable, KeyBinding, Keystroke, Menu, MenuItem,
    Modifiers, PlatformInput, QuitMode, ScrollDelta, ScrollWheelEvent, Size, SystemMenuType, Task,
    TitlebarOptions, TouchPhase, WeakEntity, WindowBounds, WindowDecorations, WindowHandle,
    WindowOptions, actions, point, px, size,
};

use crate::app::{CommandTransport, ModelSnapshot};
use crate::config::{AppConfig, switch_tab_binding};
use crate::control::{
    ControlClient, RemoteCommandClient, WaterSession, connect_water_session,
    spawn_state_polling_fallback,
};
use crate::ids::{ConnectionId, TerminalId};
use crate::remote::SshTunnel;
use crate::terminal::{
    MAX_OUTPUT_EVENT_BYTES, TerminalEmulator, TerminalStreamEvent, TerminalTheme,
};

#[cfg(feature = "runtime-screenshot")]
use super::control::UiScreenshot;
use super::control::{
    UiControlReceiver, UiControlRequest, UiKeystrokeResult, UiSnapshot, UiWheelResult,
};
use super::settings::SettingsView;
use super::workspace::{WorkspaceConnection, WorkspaceConnectionKind};
use super::{UiControlClient, WorkspaceView};

actions!(
    water,
    [
        NewWindow,
        HideWindow,
        MinimizeWindow,
        IgnoreQuit,
        NewTerminalTab,
        NewWorkspace,
        ToggleSidebar,
        RenameWorkspace,
        RenameTab,
        ActivateTab1,
        ActivateTab2,
        ActivateTab3,
        ActivateTab4,
        ActivateTab5,
        ActivateTab6,
        ActivateTab7,
        ActivateTab8,
        ActivateTab9,
        ActivateTab10,
        NextTab,
        PreviousTab,
        NextWorkspace,
        PreviousWorkspace,
        SplitRight,
        SplitDown,
        OpenSettings,
        QuitApplication,
        QuitApplicationAndServer
    ]
);

#[derive(Clone)]
pub struct WaterApplication {
    state: Rc<WaterApplicationState>,
}

struct WaterApplicationState {
    connections: RefCell<Vec<ManagedConnection>>,
    ui_control_client: RefCell<Option<UiControlClient>>,
    config: RefCell<AppConfig>,
    config_path: PathBuf,
    views: RefCell<Vec<WeakEntity<WorkspaceView>>>,
    settings_window: RefCell<Option<WindowHandle<SettingsView>>>,
    shutdown_server: RefCell<Option<Arc<dyn Fn() + Send + Sync>>>,
    // Startup window dimensions are fixed for this process. Settings keeps
    // the newly saved values visible, but they take effect after restarting
    // Water (or for brand-new windows opened in time).
    window_width: f32,
    window_height: f32,
    window_min_width: f32,
    window_min_height: f32,
}

struct ManagedConnection {
    projection: WorkspaceConnection,
    _tunnel: Option<SshTunnel>,
    local_socket: Option<PathBuf>,
    last_snapshot_apply: std::time::Instant,
    /// Raw-stream terminal plane: session handle plus worker-owned emulator
    /// controllers and immutable snapshots consumed by GPUI.
    terminal: Option<TerminalConnection>,
}

/// One connection's terminal plane. Attachment threads own the emulators;
/// GPUI sends viewport commands and consumes immutable snapshots.
struct TerminalConnection {
    session: Arc<WaterSession>,
    emulator_commands: std::collections::BTreeMap<
        TerminalId,
        std::sync::mpsc::SyncSender<TerminalEmulatorCommand>,
    >,
    snapshots: std::collections::BTreeMap<TerminalId, Arc<crate::terminal::TerminalSnapshot>>,
    pending_attachments: std::collections::BTreeSet<TerminalId>,
    attachments: std::collections::BTreeMap<TerminalId, Arc<TerminalAttachmentState>>,
    events_tx: std::sync::mpsc::SyncSender<TerminalEventMsg>,
    scrollback_lines: usize,
    theme: TerminalTheme,
}

enum TerminalEmulatorCommand {
    ScrollBy(i64),
    ScrollTo(i64),
}

struct TerminalAttachmentState {
    active: AtomicBool,
}

impl TerminalAttachmentState {
    fn new() -> Self {
        Self {
            active: AtomicBool::new(true),
        }
    }

    fn is_active(&self) -> bool {
        self.active.load(Ordering::Acquire)
    }

    fn cancel(&self) {
        self.active.store(false, Ordering::Release);
    }
}

enum TerminalEventMsg {
    Attached {
        terminal_id: TerminalId,
        attachment: Arc<TerminalAttachmentState>,
        snapshot: Arc<crate::terminal::TerminalSnapshot>,
    },
    LiveSnapshot {
        terminal_id: TerminalId,
        attachment: Arc<TerminalAttachmentState>,
        snapshot: Arc<crate::terminal::TerminalSnapshot>,
        pty_writes: Vec<Vec<u8>>,
    },
    ReplayProgress {
        terminal_id: TerminalId,
        attachment: Arc<TerminalAttachmentState>,
        snapshot: Arc<crate::terminal::TerminalSnapshot>,
    },
    Restart {
        terminal_id: TerminalId,
        attachment: Arc<TerminalAttachmentState>,
    },
    /// The terminal's pump ended (process exit, detach, session end).
    Detached {
        terminal_id: TerminalId,
        attachment: Arc<TerminalAttachmentState>,
    },
}

/// Maximum decoded terminal output parsed by GPUI in one foreground turn.
/// Dense ANSI streams are CPU-heavy even when the PTY and socket finish
/// quickly, so the event loop must regain control between bounded slices.
const MAX_TERMINAL_BYTES_PER_UI_TURN: usize = MAX_OUTPUT_EVENT_BYTES;
const MAX_TERMINAL_MESSAGES_PER_UI_TURN: usize = 64;
const TERMINAL_UI_YIELD: std::time::Duration = std::time::Duration::from_millis(1);
const TERMINAL_SNAPSHOT_MIN_INTERVAL: std::time::Duration = std::time::Duration::from_millis(16);

impl TerminalEventMsg {
    fn terminal_id(&self) -> TerminalId {
        match self {
            Self::Attached { terminal_id, .. }
            | Self::LiveSnapshot { terminal_id, .. }
            | Self::ReplayProgress { terminal_id, .. }
            | Self::Restart { terminal_id, .. }
            | Self::Detached { terminal_id, .. } => *terminal_id,
        }
    }

    fn attachment(&self) -> &TerminalAttachmentState {
        match self {
            Self::Attached { attachment, .. }
            | Self::LiveSnapshot { attachment, .. }
            | Self::ReplayProgress { attachment, .. }
            | Self::Restart { attachment, .. }
            | Self::Detached { attachment, .. } => attachment,
        }
    }

    fn is_active(&self) -> bool {
        self.attachment().is_active()
    }

    fn ui_cost_bytes(&self) -> usize {
        if !self.is_active() {
            return 0;
        }
        match self {
            Self::LiveSnapshot { .. } | Self::ReplayProgress { .. } => {
                MAX_TERMINAL_BYTES_PER_UI_TURN
            }
            Self::Attached { .. } | Self::Restart { .. } | Self::Detached { .. } => 0,
        }
    }
}

/// Receives one strictly bounded foreground batch. The first event may exceed
/// the byte budget, but it is then processed alone; an event that would cross
/// the limit is carried into the next turn instead of being lost or reordered.
fn recv_terminal_event_batch(
    receiver: &std::sync::mpsc::Receiver<TerminalEventMsg>,
    pending: Option<TerminalEventMsg>,
) -> Option<(Vec<TerminalEventMsg>, Option<TerminalEventMsg>)> {
    let first = pending.or_else(|| receiver.recv().ok())?;
    let mut bytes = first.ui_cost_bytes();
    let mut batch = vec![first];

    while batch.len() < MAX_TERMINAL_MESSAGES_PER_UI_TURN {
        let Ok(message) = receiver.try_recv() else {
            break;
        };
        let message_bytes = message.ui_cost_bytes();
        if bytes > 0 && bytes.saturating_add(message_bytes) > MAX_TERMINAL_BYTES_PER_UI_TURN {
            return Some((batch, Some(message)));
        }
        bytes = bytes.saturating_add(message_bytes);
        batch.push(message);
    }
    Some((batch, None))
}

fn publish_replay_progress(
    sender: &std::sync::mpsc::SyncSender<TerminalEventMsg>,
    terminal_id: TerminalId,
    attachment: &Arc<TerminalAttachmentState>,
    emulator: &mut TerminalEmulator,
    events: &mut Vec<TerminalStreamEvent>,
    publish_snapshot: bool,
) -> bool {
    if events.is_empty() {
        return attachment.is_active();
    }
    emulator.apply_batch(events);
    events.clear();
    let _ = emulator.take_dirty();
    if !attachment.is_active() {
        return false;
    }
    if !publish_snapshot {
        return true;
    }
    sender
        .send(TerminalEventMsg::ReplayProgress {
            terminal_id,
            attachment: attachment.clone(),
            snapshot: Arc::new(emulator.snapshot(None)),
        })
        .is_ok()
}

fn apply_emulator_commands(
    receiver: &std::sync::mpsc::Receiver<TerminalEmulatorCommand>,
    emulator: &mut TerminalEmulator,
) -> bool {
    let mut changed = false;
    while let Ok(command) = receiver.try_recv() {
        match command {
            TerminalEmulatorCommand::ScrollBy(delta) => emulator.scroll_by(delta),
            TerminalEmulatorCommand::ScrollTo(target) => emulator.scroll_to(target),
        }
        changed = true;
    }
    changed
}

fn publish_live_snapshot(
    sender: &std::sync::mpsc::SyncSender<TerminalEventMsg>,
    terminal_id: TerminalId,
    attachment: &Arc<TerminalAttachmentState>,
    emulator: &mut TerminalEmulator,
) -> bool {
    let _ = emulator.take_dirty();
    if !attachment.is_active() {
        return false;
    }
    sender
        .send(TerminalEventMsg::LiveSnapshot {
            terminal_id,
            attachment: attachment.clone(),
            snapshot: Arc::new(emulator.snapshot(None)),
            pty_writes: emulator.pty_writes(),
        })
        .is_ok()
}

struct RemoteConnectionSetup {
    destination: String,
    client: Arc<dyn CommandTransport>,
    snapshot: ModelSnapshot,
    snapshot_receiver: crate::app::SnapshotStream,
    terminal_session: Option<WaterSession>,
    tunnel: SshTunnel,
    local_socket: PathBuf,
}

impl WaterApplication {
    pub fn new(
        client: std::sync::Arc<dyn CommandTransport>,
        snapshot: ModelSnapshot,
        config: AppConfig,
    ) -> Self {
        Self::new_with_config_path(client, snapshot, config, AppConfig::default_load_path())
    }

    pub fn new_with_config_path(
        client: std::sync::Arc<dyn CommandTransport>,
        snapshot: ModelSnapshot,
        config: AppConfig,
        config_path: PathBuf,
    ) -> Self {
        let config = config.normalized();
        let local_connection = WorkspaceConnection {
            id: ConnectionId::new(1),
            title: "Local".to_owned(),
            kind: WorkspaceConnectionKind::Local,
            client,
            snapshot,
        };
        Self {
            state: Rc::new(WaterApplicationState {
                window_width: config.startup.window_width,
                window_height: config.startup.window_height,
                window_min_width: config.startup.window_min_width,
                window_min_height: config.startup.window_min_height,
                connections: RefCell::new(vec![ManagedConnection {
                    projection: local_connection,
                    _tunnel: None,
                    local_socket: None,
                    last_snapshot_apply: std::time::Instant::now() - Self::SNAPSHOT_MIN_INTERVAL,
                    terminal: None,
                }]),
                ui_control_client: RefCell::new(None),
                config: RefCell::new(config),
                config_path,
                views: RefCell::new(Vec::new()),
                settings_window: RefCell::new(None),
                shutdown_server: RefCell::new(None),
            }),
        }
    }

    pub(crate) fn config(&self) -> AppConfig {
        self.state.config.borrow().clone()
    }

    /// Marks the startup transport as SSH-backed. Normal launches keep the
    /// first connection named Local; the legacy `--ssh` entry point remains
    /// useful and now projects the correct remote title in the same sidebar.
    pub fn set_initial_remote_connection(
        &self,
        destination: String,
        tunnel: SshTunnel,
        local_socket: PathBuf,
    ) {
        let mut connections = self.state.connections.borrow_mut();
        let Some(connection) = connections.first_mut() else {
            return;
        };
        connection.projection.title = destination;
        connection.projection.kind = WorkspaceConnectionKind::Remote;
        connection._tunnel = Some(tunnel);
        connection.local_socket = Some(local_socket);
    }

    fn connection_projections(&self) -> Vec<WorkspaceConnection> {
        self.state
            .connections
            .borrow()
            .iter()
            .map(|connection| connection.projection.clone())
            .collect()
    }

    pub(crate) fn connect_remote(
        &self,
        destination: String,
        origin: WeakEntity<WorkspaceView>,
        cx: &mut App,
    ) {
        if let Some(connection_id) = self
            .state
            .connections
            .borrow()
            .iter()
            .find(|connection| {
                connection.projection.kind == WorkspaceConnectionKind::Remote
                    && connection.projection.title == destination
            })
            .map(|connection| connection.projection.id)
        {
            let _ = origin.update(cx, |view, cx| {
                view.finish_remote_connection(Ok(connection_id), cx);
            });
            return;
        }

        let Some(ui_control_client) = self.state.ui_control_client.borrow().clone() else {
            let _ = origin.update(cx, |view, cx| {
                view.finish_remote_connection(
                    Err("Water UI control session is not ready".to_owned()),
                    cx,
                );
            });
            return;
        };
        let application = self.clone();
        cx.spawn(async move |cx| {
            let connection_destination = destination.clone();
            let setup = cx
                .background_executor()
                .spawn(async move {
                    let tunnel = SshTunnel::connect(&connection_destination)
                        .map_err(|error| error.to_string())?;
                    let local_socket = tunnel.local_socket().to_path_buf();
                    let client: Arc<dyn CommandTransport> = Arc::new(
                        RemoteCommandClient::connect(&local_socket)
                            .map_err(|error| error.to_string())?,
                    );
                    let snapshot = client.state_dump().map_err(|error| error.to_string())?;
                    let (snapshot_receiver, terminal_session) =
                        match connect_water_session(&local_socket, ui_control_client) {
                            Ok(session) => (session.snapshot_stream().clone(), Some(session)),
                            Err(error) => {
                                tracing::warn!(
                                    target: "water::workspace",
                                    ?error,
                                    destination = %connection_destination,
                                    "remote session push unavailable; falling back to state polling"
                                );
                                (spawn_state_polling_fallback(client.clone()), None)
                            }
                        };
                    Ok::<_, String>(RemoteConnectionSetup {
                        destination: connection_destination,
                        client,
                        snapshot,
                        snapshot_receiver,
                        terminal_session,
                        tunnel,
                        local_socket,
                    })
                })
                .await;

            let result = match setup {
                Ok(setup) => Ok(cx.update(|cx| application.register_remote_connection(setup, cx))),
                Err(error) => Err(error),
            };
            let _ = origin.update(cx, |view, cx| {
                view.finish_remote_connection(result, cx);
            });
        })
        .detach();
    }

    fn register_remote_connection(
        &self,
        setup: RemoteConnectionSetup,
        cx: &mut App,
    ) -> ConnectionId {
        if let Some(existing) = self
            .state
            .connections
            .borrow()
            .iter()
            .find(|connection| {
                connection.projection.kind == WorkspaceConnectionKind::Remote
                    && connection.projection.title == setup.destination
            })
            .map(|connection| connection.projection.id)
        {
            return existing;
        }
        let connection_id = ConnectionId::from(uuid::Uuid::new_v4());
        let projection = WorkspaceConnection {
            id: connection_id,
            title: setup.destination,
            kind: WorkspaceConnectionKind::Remote,
            client: setup.client,
            snapshot: setup.snapshot,
        };
        let terminal = setup
            .terminal_session
            .map(|session| self.build_terminal_connection(cx, connection_id, session));
        self.state.connections.borrow_mut().push(ManagedConnection {
            projection: projection.clone(),
            _tunnel: Some(setup.tunnel),
            local_socket: Some(setup.local_socket),
            last_snapshot_apply: std::time::Instant::now() - Self::SNAPSHOT_MIN_INTERVAL,
            terminal,
        });
        for terminal_id in terminal_ids_in_snapshot(&projection.snapshot) {
            self.ensure_terminal_attached(connection_id, terminal_id);
        }
        let views = self.state.views.borrow().clone();
        let mut live_views = Vec::with_capacity(views.len());
        for view in views {
            if view
                .update(cx, |workspace, cx| {
                    workspace.install_connection(projection.clone(), cx);
                })
                .is_ok()
            {
                live_views.push(view);
            }
        }
        self.state.views.replace(live_views);
        self.spawn_snapshot_listener(cx, connection_id, setup.snapshot_receiver)
            .detach();
        connection_id
    }

    pub(crate) fn disconnect_connection(&self, connection_id: ConnectionId, cx: &mut App) {
        self.defer_remote_connection_removal(connection_id, false, cx);
    }

    pub(crate) fn kill_connection(&self, connection_id: ConnectionId, cx: &mut App) {
        self.defer_remote_connection_removal(connection_id, true, cx);
    }

    fn defer_remote_connection_removal(
        &self,
        connection_id: ConnectionId,
        kill_server: bool,
        cx: &mut App,
    ) {
        let application = self.clone();
        // These actions are commonly dispatched from a WorkspaceView mouse
        // callback. Fan-out updates must wait until that entity has been
        // returned to GPUI, otherwise updating the originating view re-enters
        // its active mutable borrow and panics.
        cx.defer(move |cx| {
            application.remove_remote_connection(connection_id, kill_server, cx);
        });
    }

    fn remove_remote_connection(
        &self,
        connection_id: ConnectionId,
        kill_server: bool,
        cx: &mut App,
    ) {
        let removed = {
            let mut connections = self.state.connections.borrow_mut();
            let Some(index) = connections.iter().position(|connection| {
                connection.projection.id == connection_id
                    && connection.projection.kind == WorkspaceConnectionKind::Remote
            }) else {
                return;
            };
            connections.remove(index)
        };
        let views = self.state.views.borrow().clone();
        let mut live_views = Vec::with_capacity(views.len());
        for view in views {
            if view
                .update(cx, |workspace, cx| {
                    workspace.remove_connection(connection_id, cx);
                })
                .is_ok()
            {
                live_views.push(view);
            }
        }
        self.state.views.replace(live_views);

        let _ = std::thread::Builder::new()
            .name("water-ssh-disconnect".to_owned())
            .spawn(move || {
                if kill_server
                    && let Some(socket_path) = removed.local_socket.as_ref()
                    && let Err(error) = ControlClient::new(socket_path).server_shutdown()
                {
                    tracing::warn!(
                        target: "water::workspace",
                        ?error,
                        "failed to stop disconnected remote server"
                    );
                }
                drop(removed);
            });
    }

    /// Installs the explicit lifecycle action used by the application menu.
    /// Ordinary window closes and `Quit Water` retain the configured detach
    /// behavior; only `Quit GUI and Server` invokes this callback.
    pub fn set_server_shutdown_handler(&self, handler: impl Fn() + Send + Sync + 'static) {
        self.state.shutdown_server.replace(Some(Arc::new(handler)));
    }

    /// The configured minimum window size, fixed for this process like the
    /// startup window dimensions.
    fn window_min_size(&self) -> Size<gpui::Pixels> {
        size(
            px(self.state.window_min_width),
            px(self.state.window_min_height),
        )
    }

    pub(crate) fn config_path(&self) -> PathBuf {
        self.state.config_path.clone()
    }

    /// Applies the UI-safe portion of a newly saved config immediately. Shell,
    /// startup, and PTY history settings are intentionally left to the next
    /// process start; the settings page reports that restart requirement.
    pub(crate) fn apply_config(&self, config: AppConfig, cx: &mut App) {
        let config = config.normalized();
        self.state.config.replace(config.clone());
        cx.clear_key_bindings();
        cx.bind_keys(configured_window_key_bindings(&config));

        let views = self.state.views.borrow().clone();
        let mut live_views = Vec::with_capacity(views.len());
        for view in views {
            if view
                .update(cx, |workspace, cx| {
                    workspace.apply_config(config.clone(), cx);
                })
                .is_ok()
            {
                live_views.push(view);
            }
        }
        self.state.views.replace(live_views);
    }

    /// Keep terminal output at display cadence. Upstream mailboxes already
    /// collapse intermediate revisions, so this is only a guard against
    /// applying multiple full snapshots within one fast display frame.
    const SNAPSHOT_MIN_INTERVAL: std::time::Duration = std::time::Duration::from_millis(8);

    pub fn install(
        &self,
        cx: &mut App,
        snapshot_receiver: crate::app::SnapshotStream,
        ui_control_client: UiControlClient,
        ui_control_receiver: UiControlReceiver,
        terminal_session: Option<WaterSession>,
    ) {
        self.state
            .ui_control_client
            .replace(Some(ui_control_client));
        cx.set_quit_mode(QuitMode::Explicit);
        let config = self.config();
        cx.clear_key_bindings();
        cx.bind_keys(configured_window_key_bindings(&config));

        let application = self.clone();
        cx.on_action(move |_: &NewWindow, cx| application.open_window(cx));
        cx.on_action(|_: &QuitApplication, cx| cx.quit());
        let state = self.state.clone();
        cx.on_action(move |_: &QuitApplicationAndServer, cx| {
            if let Some(shutdown_server) = state.shutdown_server.borrow().as_ref() {
                shutdown_server();
            }
            cx.quit();
        });
        let application = self.clone();
        cx.on_action(move |_: &OpenSettings, cx| application.open_settings(cx));
        let state = self.state.clone();
        let _ = cx.intercept_keystrokes(move |event, _window, cx| {
            let ignore_quit = state.config.borrow().shortcuts.ignore_quit.clone();
            if shortcut_matches_or_default(&ignore_quit, "cmd-q", &event.keystroke) {
                cx.stop_propagation();
            }
        });
        cx.set_menus(application_menus());

        if let Some(session) = terminal_session {
            let terminal = self.build_terminal_connection(cx, ConnectionId::new(1), session);
            self.state
                .connections
                .borrow_mut()
                .iter_mut()
                .find(|connection| connection.projection.id == ConnectionId::new(1))
                .map(|connection| connection.terminal = Some(terminal));
            let terminal_ids = self
                .state
                .connections
                .borrow()
                .iter()
                .find(|connection| connection.projection.id == ConnectionId::new(1))
                .map(|connection| terminal_ids_in_snapshot(&connection.projection.snapshot))
                .unwrap_or_default();
            for terminal_id in terminal_ids {
                self.ensure_terminal_attached(ConnectionId::new(1), terminal_id);
            }
        }
        self.spawn_snapshot_listener(cx, ConnectionId::new(1), snapshot_receiver)
            .detach();
        self.spawn_ui_control_listener(cx, ui_control_receiver)
            .detach();
        self.open_window(cx);
        cx.activate(true);
    }

    pub fn reopen(&self, cx: &mut App) {
        let has_workspace_window = cx
            .windows()
            .iter()
            .any(|window| window.downcast::<WorkspaceView>().is_some());
        if !has_workspace_window {
            self.open_window(cx);
        }
        cx.activate(true);
    }

    pub fn open_window(&self, cx: &mut App) {
        let config = self.config();
        let connections = self.connection_projections();
        let active_connection = connections
            .first()
            .map_or(ConnectionId::new(1), |connection| connection.id);
        let application = self.clone();
        let root = cx.new(|cx| {
            WorkspaceView::new_with_connections(
                Some(application),
                connections,
                active_connection,
                cx.focus_handle(),
                config.clone(),
            )
        });
        let weak_root = root.downgrade();
        let focus_handle = root.read(cx).focus_handle(cx);
        let bounds = Bounds::centered(
            None,
            size(px(self.state.window_width), px(self.state.window_height)),
            cx,
        );
        let min_size = self.window_min_size();
        match cx.open_window(water_window_options(bounds, min_size), move |window, cx| {
            window.activate_window();
            window.focus(&focus_handle, cx);
            root
        }) {
            Ok(_) => {
                hide_native_window_buttons();
                self.state.views.borrow_mut().push(weak_root);
            }
            Err(error) => tracing::error!(
                target: "water::ui",
                ?error,
                "failed to open water window"
            ),
        }
    }

    pub fn open_settings(&self, cx: &mut App) {
        let settings_window = *self.state.settings_window.borrow();
        if let Some(window) = settings_window {
            let any_handle: gpui::AnyWindowHandle = window.into();
            let update_result = window.update(cx, |_, window, _cx| window.activate_window());
            if update_result.is_ok() || cx.windows().contains(&any_handle) {
                return;
            }
            // A WindowHandle can outlive its native window. Clear it before
            // recreating the singleton so a closed settings window never
            // blocks the shortcut from opening a replacement. A transient
            // update failure for a still-live window is deliberately ignored.
            self.state.settings_window.replace(None);
        }

        let root = cx.new(|cx| SettingsView::new(self.clone(), cx.focus_handle()));
        let focus_handle = root.read(cx).focus_handle(cx);
        let bounds = Bounds::centered(None, size(px(980.), px(760.)), cx);
        let min_size = self.window_min_size();
        match cx.open_window(
            settings_window_options(bounds, min_size),
            move |window, cx| {
                window.activate_window();
                window.focus(&focus_handle, cx);
                root
            },
        ) {
            Ok(window) => {
                hide_native_window_buttons();
                self.state.settings_window.replace(Some(window));
            }
            Err(error) => tracing::error!(
                target: "water::ui",
                ?error,
                "failed to open settings window"
            ),
        }
    }

    fn spawn_snapshot_listener(
        &self,
        cx: &mut App,
        connection_id: ConnectionId,
        receiver: crate::app::SnapshotStream,
    ) -> Task<()> {
        let receiver = std::sync::Arc::new(receiver);
        let state = self.state.clone();
        let application = self.clone();
        cx.spawn(async move |cx| {
            loop {
                let receiver_for_worker = receiver.clone();
                let snapshot = cx
                    .background_executor()
                    .spawn(async move { receiver_for_worker.recv() })
                    .await;
                let Some(snapshot) = snapshot else {
                    break;
                };
                // Pace snapshot application independently from parser work.
                // Intermediate revisions are already dropped upstream, while
                // an 8 ms ceiling keeps output continuous on 60/120 Hz panels.
                let Some(elapsed) = state
                    .connections
                    .borrow()
                    .iter()
                    .find(|connection| connection.projection.id == connection_id)
                    .map(|connection| connection.last_snapshot_apply.elapsed())
                else {
                    break;
                };
                if elapsed < Self::SNAPSHOT_MIN_INTERVAL {
                    cx.background_executor()
                        .spawn(async move {
                            std::thread::sleep(Self::SNAPSHOT_MIN_INTERVAL - elapsed);
                        })
                        .await;
                }
                let installed = {
                    let mut connections = state.connections.borrow_mut();
                    let Some(connection) = connections
                        .iter_mut()
                        .find(|connection| connection.projection.id == connection_id)
                    else {
                        break;
                    };
                    if snapshot.state_revision <= connection.projection.snapshot.state_revision {
                        false
                    } else {
                        connection.last_snapshot_apply = std::time::Instant::now();
                        connection.projection.snapshot = snapshot.clone();
                        true
                    }
                };
                if !installed {
                    continue;
                }
                for terminal_id in terminal_ids_in_snapshot(&snapshot) {
                    application.ensure_terminal_attached(connection_id, terminal_id);
                }
                let views = state.views.borrow().clone();
                let mut live_views = Vec::with_capacity(views.len());
                for view in views {
                    if view
                        .update(cx, |workspace, cx| {
                            workspace.install_connection_snapshot(
                                connection_id,
                                snapshot.clone(),
                                cx,
                            );
                        })
                        .is_ok()
                    {
                        live_views.push(view);
                    }
                }
                state.views.replace(live_views);
            }
        })
    }

    /// Builds the terminal plane for a connection: owns the session, starts
    /// the main-thread snapshot listener, and holds emulator controllers.
    fn build_terminal_connection(
        &self,
        cx: &mut App,
        connection_id: ConnectionId,
        session: WaterSession,
    ) -> TerminalConnection {
        let (events_tx, events_rx) = std::sync::mpsc::sync_channel(1024);
        let config = self.config();
        let colors = config.theme.colors();
        let theme = TerminalTheme::new(
            colors.terminal_foreground,
            colors.terminal_background,
            colors.cursor_background,
        );
        let connection = TerminalConnection {
            session: Arc::new(session),
            emulator_commands: std::collections::BTreeMap::new(),
            snapshots: std::collections::BTreeMap::new(),
            pending_attachments: std::collections::BTreeSet::new(),
            attachments: std::collections::BTreeMap::new(),
            events_tx,
            scrollback_lines: config.terminal.scrollback_lines,
            theme,
        };
        self.spawn_terminal_event_listener(cx, connection_id, events_rx)
            .detach();
        connection
    }

    /// Main-thread listener for already-rendered terminal snapshots. ANSI
    /// parsing stays on the attachment threads; this path only installs the
    /// newest immutable projection and forwards PTY query responses.
    fn spawn_terminal_event_listener(
        &self,
        cx: &mut App,
        connection_id: ConnectionId,
        receiver: std::sync::mpsc::Receiver<TerminalEventMsg>,
    ) -> Task<()> {
        let receiver = Arc::new(std::sync::Mutex::new(receiver));
        let application = self.clone();
        cx.spawn(async move |cx| {
            let mut pending = None;
            loop {
                let receiver_for_worker = receiver.clone();
                let pending_for_worker = pending.take();
                let received = cx
                    .background_executor()
                    .spawn(async move {
                        let receiver = receiver_for_worker
                            .lock()
                            .expect("terminal event receiver poisoned");
                        recv_terminal_event_batch(&receiver, pending_for_worker)
                    })
                    .await;
                let Some((batch, next_pending)) = received else {
                    break;
                };
                pending = next_pending;
                cx.update(|cx| application.apply_terminal_events(connection_id, batch, cx));
                cx.background_executor().timer(TERMINAL_UI_YIELD).await;
            }
        })
    }

    /// Installs a batch of worker-produced snapshots and refreshes views for
    /// the changed terminals.
    fn apply_terminal_events(
        &self,
        connection_id: ConnectionId,
        batch: Vec<TerminalEventMsg>,
        cx: &mut App,
    ) {
        use crate::command::{AppCommand, TerminalCommand};

        let mut changed = std::collections::BTreeSet::new();
        let mut resync = Vec::new();
        {
            let mut connections = self.state.connections.borrow_mut();
            let Some(connection) = connections
                .iter_mut()
                .find(|connection| connection.projection.id == connection_id)
            else {
                return;
            };
            let Some(terminal) = connection.terminal.as_mut() else {
                return;
            };
            let mut pty_writes: Vec<(TerminalId, Vec<u8>)> = Vec::new();
            let mut grouped =
                std::collections::BTreeMap::<TerminalId, Vec<TerminalEventMsg>>::new();
            for message in batch {
                if !message.is_active() {
                    continue;
                }
                let terminal_id = message.terminal_id();
                grouped.entry(terminal_id).or_default().push(message);
            }
            for (terminal_id, messages) in grouped {
                let mut latest_snapshot = None;
                let mut detached = false;
                for message in messages {
                    match message {
                        TerminalEventMsg::Attached { snapshot, .. } => {
                            terminal.pending_attachments.remove(&terminal_id);
                            latest_snapshot = Some(snapshot);
                        }
                        TerminalEventMsg::LiveSnapshot {
                            snapshot,
                            pty_writes: writes,
                            ..
                        } => {
                            latest_snapshot = Some(snapshot);
                            pty_writes.extend(writes.into_iter().map(|bytes| (terminal_id, bytes)));
                        }
                        TerminalEventMsg::ReplayProgress { snapshot, .. } => {
                            latest_snapshot = Some(snapshot);
                        }
                        TerminalEventMsg::Restart { .. } => resync.push(terminal_id),
                        TerminalEventMsg::Detached { .. } => detached = true,
                    }
                }
                if let Some(snapshot) = latest_snapshot {
                    terminal.snapshots.insert(terminal_id, snapshot);
                    changed.insert(terminal_id);
                }
                if detached {
                    terminal.pending_attachments.remove(&terminal_id);
                    terminal.attachments.remove(&terminal_id);
                    terminal.emulator_commands.remove(&terminal_id);
                    terminal.snapshots.remove(&terminal_id);
                    changed.insert(terminal_id);
                }
            }
            let client = connection.projection.client.clone();
            for (terminal_id, bytes) in pty_writes {
                let _ = client.enqueue(AppCommand::Terminal(TerminalCommand::SendBytes {
                    terminal_id: Some(terminal_id),
                    pane_id: None,
                    bytes,
                }));
            }
        }
        for terminal_id in resync {
            self.restart_terminal_attachment(connection_id, terminal_id);
        }
        if changed.is_empty() {
            return;
        }
        let changed = changed.into_iter().collect::<Vec<_>>();
        let views = self.state.views.borrow().clone();
        let mut live_views = Vec::with_capacity(views.len());
        for view in views {
            if view
                .update(cx, |workspace, cx| {
                    workspace.apply_terminal_events(&changed, cx)
                })
                .is_ok()
            {
                live_views.push(view);
            }
        }
        self.state.views.replace(live_views);
    }

    fn restart_terminal_attachment(&self, connection_id: ConnectionId, terminal_id: TerminalId) {
        let session = {
            let mut connections = self.state.connections.borrow_mut();
            let Some(terminal) = connections
                .iter_mut()
                .find(|connection| connection.projection.id == connection_id)
                .and_then(|connection| connection.terminal.as_mut())
            else {
                return;
            };
            if let Some(attachment) = terminal.attachments.remove(&terminal_id) {
                attachment.cancel();
            }
            terminal.pending_attachments.remove(&terminal_id);
            terminal.emulator_commands.remove(&terminal_id);
            terminal.session.clone()
        };
        session.detach(terminal_id);
        self.ensure_terminal_attached(connection_id, terminal_id);
    }

    /// Attaches the raw stream and starts its worker-owned emulator. Replay
    /// and live parsing both publish immutable snapshots; GPUI never mutates
    /// or waits on the emulator.
    pub(crate) fn ensure_terminal_attached(
        &self,
        connection_id: ConnectionId,
        terminal_id: TerminalId,
    ) {
        let terminal_exists = self
            .state
            .connections
            .borrow()
            .iter()
            .find(|connection| connection.projection.id == connection_id)
            .and_then(|connection| {
                terminal_size_in_snapshot(&connection.projection.snapshot, terminal_id)
            })
            .is_some();
        if !terminal_exists {
            return;
        }
        let (session, events_tx, scrollback_lines, theme, attachment, emulator_commands) = {
            let mut connections = self.state.connections.borrow_mut();
            let Some(connection) = connections
                .iter_mut()
                .find(|connection| connection.projection.id == connection_id)
            else {
                return;
            };
            let Some(terminal) = connection.terminal.as_mut() else {
                return;
            };
            if terminal.emulator_commands.contains_key(&terminal_id)
                || !terminal.pending_attachments.insert(terminal_id)
            {
                return;
            }
            let attachment = Arc::new(TerminalAttachmentState::new());
            let (emulator_commands_tx, emulator_commands) = std::sync::mpsc::sync_channel(64);
            terminal.attachments.insert(terminal_id, attachment.clone());
            terminal
                .emulator_commands
                .insert(terminal_id, emulator_commands_tx);
            (
                terminal.session.clone(),
                terminal.events_tx.clone(),
                terminal.scrollback_lines,
                terminal.theme,
                attachment,
                emulator_commands,
            )
        };
        let spawn_result = std::thread::Builder::new()
            .name(format!("water-terminal-events-{terminal_id}"))
            .spawn(move || match session.attach(terminal_id) {
                Ok((response, stream)) => {
                    if !attachment.is_active() {
                        return;
                    }
                    let mut emulator = TerminalEmulator::with_theme(
                        terminal_id,
                        response.size,
                        scrollback_lines,
                        theme,
                    );
                    let mut tracked_size = response.size;
                    let mut replay_events = Vec::new();
                    let mut replay_bytes = 0usize;
                    let mut last_replay_snapshot = std::time::Instant::now();
                    for wire in &response.replay {
                        let Some(event) = TerminalStreamEvent::from_wire(wire, tracked_size) else {
                            continue;
                        };
                        if let TerminalStreamEvent::Resize { size, .. } = &event {
                            tracked_size = *size;
                        }
                        crate::metrics::add(
                            crate::metrics::replay_bytes_received(),
                            event.output_bytes(),
                        );
                        replay_bytes = replay_bytes.saturating_add(event.output_bytes());
                        replay_events.push(event);
                        if replay_bytes >= MAX_TERMINAL_BYTES_PER_UI_TURN
                            || replay_events.len() >= MAX_TERMINAL_MESSAGES_PER_UI_TURN
                        {
                            let publish_snapshot =
                                last_replay_snapshot.elapsed() >= TERMINAL_SNAPSHOT_MIN_INTERVAL;
                            if !publish_replay_progress(
                                &events_tx,
                                terminal_id,
                                &attachment,
                                &mut emulator,
                                &mut replay_events,
                                publish_snapshot,
                            ) {
                                return;
                            }
                            if publish_snapshot {
                                last_replay_snapshot = std::time::Instant::now();
                            }
                        }
                        if replay_events.is_empty() {
                            replay_bytes = 0;
                        }
                    }
                    if !publish_replay_progress(
                        &events_tx,
                        terminal_id,
                        &attachment,
                        &mut emulator,
                        &mut replay_events,
                        true,
                    ) {
                        return;
                    }
                    emulator.start_live();
                    if !attachment.is_active() {
                        return;
                    }
                    if events_tx
                        .send(TerminalEventMsg::Attached {
                            terminal_id,
                            attachment: attachment.clone(),
                            snapshot: Arc::new(emulator.snapshot(None)),
                        })
                        .is_err()
                    {
                        return;
                    }
                    let mut stream = stream;
                    let mut pending_event = None;
                    let mut last_live_snapshot = std::time::Instant::now();
                    while attachment.is_active() {
                        let commands_changed =
                            apply_emulator_commands(&emulator_commands, &mut emulator);
                        let received = match pending_event.take() {
                            Some(event) => Ok(event),
                            None => stream.recv_timeout(std::time::Duration::from_millis(8)),
                        };
                        let event = match received {
                            Ok(event) => event,
                            Err(std::sync::mpsc::RecvTimeoutError::Timeout) => {
                                if commands_changed {
                                    if !publish_live_snapshot(
                                        &events_tx,
                                        terminal_id,
                                        &attachment,
                                        &mut emulator,
                                    ) {
                                        return;
                                    }
                                    last_live_snapshot = std::time::Instant::now();
                                }
                                continue;
                            }
                            Err(std::sync::mpsc::RecvTimeoutError::Disconnected) => break,
                        };
                        if !attachment.is_active() {
                            break;
                        }
                        crate::metrics::add(
                            crate::metrics::terminal_bytes_received(),
                            event.output_bytes(),
                        );
                        let effects = emulator.apply(&event);
                        if effects.iter().any(|effect| {
                            matches!(effect, crate::terminal::EmulatorEffect::SequenceGap { .. })
                        }) {
                            let _ = events_tx.send(TerminalEventMsg::Restart {
                                terminal_id,
                                attachment: attachment.clone(),
                            });
                            return;
                        }
                        let stream_backlogged = match stream.try_recv() {
                            Ok(event) => {
                                pending_event = Some(event);
                                true
                            }
                            Err(std::sync::mpsc::TryRecvError::Empty) => false,
                            Err(std::sync::mpsc::TryRecvError::Disconnected) => false,
                        };
                        let publish_snapshot = commands_changed
                            || !stream_backlogged
                            || last_live_snapshot.elapsed() >= TERMINAL_SNAPSHOT_MIN_INTERVAL;
                        if publish_snapshot {
                            if !publish_live_snapshot(
                                &events_tx,
                                terminal_id,
                                &attachment,
                                &mut emulator,
                            ) {
                                return;
                            }
                            last_live_snapshot = std::time::Instant::now();
                        }
                    }
                    if attachment.is_active() {
                        let _ = events_tx.send(TerminalEventMsg::Detached {
                            terminal_id,
                            attachment,
                        });
                    }
                }
                Err(error) => {
                    tracing::warn!(
                        target: "water::workspace",
                        ?error,
                        %terminal_id,
                        "terminal attach failed"
                    );
                    if attachment.is_active() {
                        let _ = events_tx.send(TerminalEventMsg::Detached {
                            terminal_id,
                            attachment,
                        });
                    }
                }
            });
        if spawn_result.is_err()
            && let Some(terminal) = self
                .state
                .connections
                .borrow_mut()
                .iter_mut()
                .find(|connection| connection.projection.id == connection_id)
                .and_then(|connection| connection.terminal.as_mut())
        {
            terminal.pending_attachments.remove(&terminal_id);
            terminal.emulator_commands.remove(&terminal_id);
            if let Some(attachment) = terminal.attachments.remove(&terminal_id) {
                attachment.cancel();
            }
        }
    }

    /// Returns the newest immutable snapshot published by the emulator worker.
    pub(crate) fn terminal_snapshot(
        &self,
        connection_id: ConnectionId,
        terminal_id: TerminalId,
        _previous: Option<&crate::terminal::TerminalSnapshot>,
    ) -> Option<std::sync::Arc<crate::terminal::TerminalSnapshot>> {
        let connections = self.state.connections.borrow();
        let connection = connections
            .iter()
            .find(|connection| connection.projection.id == connection_id)?;
        let terminal = connection.terminal.as_ref()?;
        terminal.snapshots.get(&terminal_id).cloned()
    }

    pub(crate) fn terminal_scroll_by(
        &self,
        connection_id: ConnectionId,
        terminal_id: TerminalId,
        delta: i64,
    ) -> bool {
        let connections = self.state.connections.borrow();
        let Some(connection) = connections
            .iter()
            .find(|connection| connection.projection.id == connection_id)
        else {
            return false;
        };
        let Some(terminal) = connection.terminal.as_ref() else {
            return false;
        };
        terminal
            .emulator_commands
            .get(&terminal_id)
            .is_some_and(|commands| {
                commands
                    .try_send(TerminalEmulatorCommand::ScrollBy(delta))
                    .is_ok()
            })
    }

    pub(crate) fn terminal_scroll_to(
        &self,
        connection_id: ConnectionId,
        terminal_id: TerminalId,
        target: i64,
    ) -> bool {
        let connections = self.state.connections.borrow();
        let Some(connection) = connections
            .iter()
            .find(|connection| connection.projection.id == connection_id)
        else {
            return false;
        };
        let Some(terminal) = connection.terminal.as_ref() else {
            return false;
        };
        terminal
            .emulator_commands
            .get(&terminal_id)
            .is_some_and(|commands| {
                commands
                    .try_send(TerminalEmulatorCommand::ScrollTo(target))
                    .is_ok()
            })
    }

    fn spawn_ui_control_listener(&self, cx: &mut App, receiver: UiControlReceiver) -> Task<()> {
        let receiver = Arc::new(receiver);
        cx.spawn(async move |cx| {
            loop {
                let receiver_for_worker = receiver.clone();
                let request = cx
                    .background_executor()
                    .spawn(async move { receiver_for_worker.recv() })
                    .await;
                let Some(request) = request else {
                    break;
                };

                match request {
                    UiControlRequest::Keystroke { keystroke, reply } => {
                        let result = cx.update(|cx| {
                            dispatch_controlled_keystroke(&keystroke, cx).map(|handled| {
                                UiKeystrokeResult {
                                    keystroke,
                                    handled,
                                    window_count: cx.windows().len(),
                                }
                            })
                        });
                        let _ = reply.send(result);
                    }
                    UiControlRequest::Snapshot { reply } => {
                        let result = cx.update(|cx| {
                            Ok(UiSnapshot {
                                window_count: cx.windows().len(),
                                has_active_window: cx.active_window().is_some(),
                            })
                        });
                        let _ = reply.send(result);
                    }
                    UiControlRequest::Wheel {
                        position,
                        delta,
                        reply,
                    } => {
                        let result = cx.update(|cx| dispatch_controlled_wheel(position, delta, cx));
                        let _ = reply.send(result);
                    }
                    #[cfg(feature = "runtime-screenshot")]
                    UiControlRequest::Screenshot { path, reply } => {
                        let result = cx.update(capture_active_window);
                        let result = match result {
                            Ok(image) => {
                                cx.background_executor()
                                    .spawn(async move { save_screenshot(image, path) })
                                    .await
                            }
                            Err(error) => Err(error),
                        };
                        let _ = reply.send(result);
                    }
                    #[cfg(not(feature = "runtime-screenshot"))]
                    UiControlRequest::Screenshot { path, reply } => {
                        let _ = path;
                        let _ = reply.send(Err(
                            "runtime screenshot support is disabled at compile time".to_owned(),
                        ));
                    }
                }
            }
        })
    }
}

#[cfg(feature = "runtime-screenshot")]
fn capture_active_window(cx: &mut App) -> Result<image::RgbaImage, String> {
    let window = cx
        .active_window()
        .or_else(|| {
            cx.window_stack()
                .and_then(|windows| windows.into_iter().next())
        })
        .or_else(|| cx.windows().into_iter().last())
        .ok_or_else(|| "Water has no open window".to_owned())?;
    window
        .update(cx, |_, window, _cx| {
            window
                .render_to_image()
                .map_err(|error| format!("could not capture Water window: {error}"))
        })
        .map_err(|error| format!("could not access Water window: {error}"))?
}

#[cfg(feature = "runtime-screenshot")]
fn save_screenshot(image: image::RgbaImage, path: PathBuf) -> Result<UiScreenshot, String> {
    let path = if path.extension().is_some() {
        path
    } else {
        path.with_extension("png")
    };
    if let Some(parent) = path.parent()
        && !parent.as_os_str().is_empty()
    {
        std::fs::create_dir_all(parent)
            .map_err(|error| format!("could not create screenshot directory: {error}"))?;
    }
    let width = image.width();
    let height = image.height();
    image
        .save_with_format(&path, image::ImageFormat::Png)
        .map_err(|error| format!("could not save screenshot {}: {error}", path.display()))?;
    Ok(UiScreenshot {
        path: path.display().to_string(),
        width,
        height,
    })
}

/// Hide the native window buttons on every window owned by this process.
///
/// Water draws its own integrated window controls, but the transparent
/// titlebar required for the AppKit resize style mask also creates native
/// traffic-light buttons. gpui's only knob for those buttons is a position
/// (it has no hide API), and negative positions are not a supported way to
/// make them vanish, so they are hidden directly through the ObjC runtime.
/// The hidden state persists across later layout passes because the button
/// views themselves stay hidden; only the system fullscreen chrome can
/// still draw its own close control.
#[cfg(target_os = "macos")]
fn hide_native_window_buttons() {
    use std::ffi::{c_char, c_void};

    // NSWindowButton raw values: CloseButton, MiniaturizeButton, ZoomButton.
    const STANDARD_BUTTONS: [usize; 3] = [1, 2, 3];

    unsafe extern "C" {
        fn objc_getClass(name: *const c_char) -> *mut c_void;
        fn sel_registerName(name: *const c_char) -> *mut c_void;
        fn objc_msgSend();
    }
    type Get = unsafe extern "C" fn(*mut c_void, *mut c_void) -> *mut c_void;
    type GetIndexed = unsafe extern "C" fn(*mut c_void, *mut c_void, usize) -> *mut c_void;
    type Count = unsafe extern "C" fn(*mut c_void, *mut c_void) -> usize;
    type SetHidden = unsafe extern "C" fn(*mut c_void, *mut c_void, i8);

    fn sel(name: &str) -> *mut c_void {
        // `sel_registerName` copies the name, so the temporary CString may
        // drop immediately after registration.
        let name = std::ffi::CString::new(name).expect("selectors never contain NUL");
        unsafe { sel_registerName(name.as_ptr()) }
    }

    // SAFETY: all messages below are scalar-argumented ObjC sends on the
    // main thread (window creation happens on the GPUI application
    // thread). `objc_msgSend` is cast to the concrete signatures AppKit
    // declares; a missing NSApplication class or a nil receiver is handled
    // by the null checks, and sending messages to nil is an ObjC no-op.
    unsafe {
        let app_class = objc_getClass(c"NSApplication".as_ptr());
        if app_class.is_null() {
            return;
        }
        let shared: Get = std::mem::transmute(objc_msgSend as *const c_void);
        let app = shared(app_class, sel("sharedApplication"));
        if app.is_null() {
            return;
        }
        let windows = shared(app, sel("windows"));
        if windows.is_null() {
            return;
        }
        let count: Count = std::mem::transmute(objc_msgSend as *const c_void);
        let count = count(windows, sel("count"));
        let object_at: GetIndexed = std::mem::transmute(objc_msgSend as *const c_void);
        let button_at: GetIndexed = std::mem::transmute(objc_msgSend as *const c_void);
        let set_hidden: SetHidden = std::mem::transmute(objc_msgSend as *const c_void);
        let button_sel = sel("standardWindowButton:");
        let hidden_sel = sel("setHidden:");
        for index in 0..count {
            let window = object_at(windows, sel("objectAtIndex:"), index);
            if window.is_null() {
                continue;
            }
            for button in STANDARD_BUTTONS {
                let view = button_at(window, button_sel, button);
                if !view.is_null() {
                    set_hidden(view, hidden_sel, 1);
                }
            }
        }
    }
}

#[cfg(not(target_os = "macos"))]
fn hide_native_window_buttons() {}

fn water_window_options(
    bounds: Bounds<gpui::Pixels>,
    min_size: Size<gpui::Pixels>,
) -> WindowOptions {
    WindowOptions {
        window_bounds: Some(WindowBounds::Windowed(bounds)),
        // Water renders the complete titlebar, including window controls,
        // inside its views, so the native titlebar must stay invisible.
        // gpui only grants the resizable/closable/miniaturizable AppKit
        // style masks through an explicit (transparent) titlebar; a bare
        // `titlebar: None` window cannot be resized at all. The native
        // traffic lights are hidden through AppKit right after the window
        // is created (`hide_native_window_buttons`); gpui itself only
        // supports *repositioning* them, and negative off-screen
        // positions are an undefined-geometry hack, so nothing is parked
        // here. (System fullscreen chrome may still surface a close
        // control on top of the window.)
        titlebar: Some(TitlebarOptions {
            title: None,
            appears_transparent: true,
            traffic_light_position: None,
        }),
        window_min_size: Some(min_size),
        is_resizable: true,
        app_owns_titlebar_drag: true,
        window_decorations: Some(WindowDecorations::Client),
        ..Default::default()
    }
}

fn settings_window_options(
    bounds: Bounds<gpui::Pixels>,
    min_size: Size<gpui::Pixels>,
) -> WindowOptions {
    water_window_options(bounds, min_size)
}

fn dispatch_controlled_keystroke(source: &str, cx: &mut App) -> Result<bool, String> {
    let keystroke = Keystroke::parse(source).map_err(|error| error.to_string())?;
    let window = cx
        .active_window()
        .or_else(|| {
            cx.window_stack()
                .and_then(|windows| windows.into_iter().next())
        })
        .or_else(|| cx.windows().into_iter().last())
        .ok_or_else(|| "Water has no open window".to_owned())?;
    window
        .update(cx, |_, window, cx| window.dispatch_keystroke(keystroke, cx))
        .map_err(|error| error.to_string())
}

fn dispatch_controlled_wheel(
    position: (f32, f32),
    delta: (f32, f32),
    cx: &mut App,
) -> Result<UiWheelResult, String> {
    let window = cx
        .active_window()
        .or_else(|| {
            cx.window_stack()
                .and_then(|windows| windows.into_iter().next())
        })
        .or_else(|| cx.windows().into_iter().last())
        .ok_or_else(|| "Water has no open window".to_owned())?;
    let event = ScrollWheelEvent {
        position: point(px(position.0), px(position.1)),
        delta: ScrollDelta::Lines(point(delta.0, delta.1)),
        modifiers: Modifiers::default(),
        touch_phase: TouchPhase::default(),
    };
    let DispatchEventResult {
        propagate,
        default_prevented,
    } = window
        .update(cx, |_, window, cx| {
            window.dispatch_event(PlatformInput::ScrollWheel(event), cx)
        })
        .map_err(|error| error.to_string())?;
    Ok(UiWheelResult {
        position,
        delta,
        propagate,
        default_prevented,
    })
}

#[cfg(test)]
pub(crate) fn window_key_bindings() -> Vec<KeyBinding> {
    configured_window_key_bindings(&AppConfig::default())
}

pub(crate) fn configured_window_key_bindings(config: &AppConfig) -> Vec<KeyBinding> {
    let shortcuts = &config.shortcuts;
    let mut bindings = vec![
        safe_key_binding(&shortcuts.open_settings, "cmd-,", OpenSettings),
        safe_key_binding(&shortcuts.new_window, "cmd-n", NewWindow),
        safe_key_binding(&shortcuts.hide_window, "cmd-w", HideWindow),
        safe_key_binding(&shortcuts.minimize_window, "cmd-m", MinimizeWindow),
        safe_key_binding(&shortcuts.ignore_quit, "cmd-q", IgnoreQuit),
        safe_key_binding(&shortcuts.new_terminal_tab, "cmd-t", NewTerminalTab),
        safe_key_binding(&shortcuts.new_workspace, "cmd-shift-n", NewWorkspace),
        safe_key_binding(&shortcuts.toggle_sidebar, "cmd-e", ToggleSidebar),
        safe_key_binding(&shortcuts.rename_workspace, "cmd-shift-e", RenameWorkspace),
        safe_key_binding(&shortcuts.rename_tab, "cmd-shift-t", RenameTab),
        safe_key_binding(&shortcuts.next_tab, "cmd-]", NextTab),
        safe_key_binding(&shortcuts.previous_tab, "cmd-[", PreviousTab),
        safe_key_binding(&shortcuts.next_workspace, "ctrl-tab", NextWorkspace),
        safe_key_binding(
            &shortcuts.previous_workspace,
            "ctrl-shift-tab",
            PreviousWorkspace,
        ),
        safe_key_binding(&shortcuts.split_right, "cmd-\\", SplitRight),
        safe_key_binding(&shortcuts.split_down, "cmd--", SplitDown),
    ];

    bindings.push(safe_key_binding(
        &switch_tab_binding(&shortcuts.switch_tab, 0),
        "cmd-1",
        ActivateTab1,
    ));
    bindings.push(safe_key_binding(
        &switch_tab_binding(&shortcuts.switch_tab, 1),
        "cmd-2",
        ActivateTab2,
    ));
    bindings.push(safe_key_binding(
        &switch_tab_binding(&shortcuts.switch_tab, 2),
        "cmd-3",
        ActivateTab3,
    ));
    bindings.push(safe_key_binding(
        &switch_tab_binding(&shortcuts.switch_tab, 3),
        "cmd-4",
        ActivateTab4,
    ));
    bindings.push(safe_key_binding(
        &switch_tab_binding(&shortcuts.switch_tab, 4),
        "cmd-5",
        ActivateTab5,
    ));
    bindings.push(safe_key_binding(
        &switch_tab_binding(&shortcuts.switch_tab, 5),
        "cmd-6",
        ActivateTab6,
    ));
    bindings.push(safe_key_binding(
        &switch_tab_binding(&shortcuts.switch_tab, 6),
        "cmd-7",
        ActivateTab7,
    ));
    bindings.push(safe_key_binding(
        &switch_tab_binding(&shortcuts.switch_tab, 7),
        "cmd-8",
        ActivateTab8,
    ));
    bindings.push(safe_key_binding(
        &switch_tab_binding(&shortcuts.switch_tab, 8),
        "cmd-9",
        ActivateTab9,
    ));
    bindings.push(safe_key_binding(
        &switch_tab_binding(&shortcuts.switch_tab, 9),
        "cmd-0",
        ActivateTab10,
    ));
    bindings
}

fn safe_key_binding<A: gpui::Action>(source: &str, fallback: &str, action: A) -> KeyBinding {
    let source = if shortcut_is_valid(source) {
        source.to_owned()
    } else {
        fallback.to_owned()
    };
    KeyBinding::new(&source, action, None)
}

fn shortcut_is_valid(source: &str) -> bool {
    let source = source.trim();
    !source.is_empty()
        && source
            .split_whitespace()
            .all(|keystroke| Keystroke::parse(keystroke).is_ok())
}

pub(crate) fn shortcut_matches(source: &str, actual: &Keystroke) -> bool {
    let Some(source) = source.split_whitespace().next() else {
        return false;
    };
    let Ok(expected) = Keystroke::parse(source) else {
        return false;
    };
    expected.modifiers == actual.modifiers && expected.key == actual.key
}

pub(crate) fn shortcut_matches_or_default(
    source: &str,
    fallback: &str,
    actual: &Keystroke,
) -> bool {
    if shortcut_is_valid(source) {
        shortcut_matches(source, actual)
    } else {
        shortcut_matches(fallback, actual)
    }
}

fn application_menus() -> Vec<Menu> {
    vec![
        Menu::new("Water").items([
            MenuItem::action("Settings…", OpenSettings),
            MenuItem::separator(),
            MenuItem::os_submenu("Services", SystemMenuType::Services),
            MenuItem::separator(),
            // No key equivalent is registered for QuitApplication on
            // purpose: cmd-q stays swallowed by the ignore-quit shortcut,
            // so quitting is a deliberate menu click.
            MenuItem::action("Quit Water", QuitApplication),
            MenuItem::action("Quit GUI and Server", QuitApplicationAndServer),
        ]),
        Menu::new("File").items([
            MenuItem::action("New Window", NewWindow),
            MenuItem::action("New Workspace", NewWorkspace),
            MenuItem::action("New Terminal Tab", NewTerminalTab),
            MenuItem::separator(),
            MenuItem::action("Rename Workspace", RenameWorkspace),
            MenuItem::action("Rename Tab", RenameTab),
        ]),
        Menu::new("View").items([
            MenuItem::action("Toggle Sidebar", ToggleSidebar),
            MenuItem::separator(),
            MenuItem::action("Next Tab", NextTab),
            MenuItem::action("Previous Tab", PreviousTab),
            MenuItem::action("Next Workspace", NextWorkspace),
            MenuItem::action("Previous Workspace", PreviousWorkspace),
            MenuItem::separator(),
            MenuItem::action("Split Right", SplitRight),
            MenuItem::action("Split Down", SplitDown),
        ]),
        Menu::new("Window").items([
            MenuItem::action("Hide Window", HideWindow),
            MenuItem::action("Minimize", MinimizeWindow),
        ]),
    ]
}

#[cfg(test)]
mod tests {
    use super::*;

    fn terminal_snapshot_message(seq: u64) -> TerminalEventMsg {
        terminal_snapshot_message_for(seq, Arc::new(TerminalAttachmentState::new()))
    }

    fn terminal_snapshot_message_for(
        seq: u64,
        attachment: Arc<TerminalAttachmentState>,
    ) -> TerminalEventMsg {
        let terminal_id = TerminalId::new(1);
        let size = crate::terminal::TerminalSize::new(80, 24);
        let mut emulator = TerminalEmulator::new(terminal_id, size, 100);
        emulator.apply(&TerminalStreamEvent::Output {
            seq,
            size,
            bytes: Arc::from(format!("snapshot {seq}\r\n").into_bytes()),
        });
        TerminalEventMsg::LiveSnapshot {
            terminal_id,
            attachment,
            snapshot: Arc::new(emulator.snapshot(None)),
            pty_writes: Vec::new(),
        }
    }

    fn snapshot_sequence(message: &TerminalEventMsg) -> u64 {
        match message {
            TerminalEventMsg::Attached { snapshot, .. }
            | TerminalEventMsg::LiveSnapshot { snapshot, .. }
            | TerminalEventMsg::ReplayProgress { snapshot, .. } => snapshot.revision,
            TerminalEventMsg::Restart { .. } | TerminalEventMsg::Detached { .. } => {
                panic!("expected terminal snapshot")
            }
        }
    }

    #[test]
    fn terminal_snapshot_batches_preserve_order_and_pace_frames() {
        let (sender, receiver) = std::sync::mpsc::sync_channel(4);
        for seq in 1..=3 {
            sender.send(terminal_snapshot_message(seq)).unwrap();
        }

        let (first, pending) = recv_terminal_event_batch(&receiver, None).unwrap();
        assert_eq!(first.len(), 1);
        assert_eq!(snapshot_sequence(&first[0]), 1);

        let (second, pending) = recv_terminal_event_batch(&receiver, pending).unwrap();
        assert_eq!(second.len(), 1);
        assert_eq!(snapshot_sequence(&second[0]), 2);

        let (third, pending) = recv_terminal_event_batch(&receiver, pending).unwrap();
        assert_eq!(third.len(), 1);
        assert_eq!(snapshot_sequence(&third[0]), 3);
        assert!(pending.is_none());
    }

    #[test]
    fn cancelled_attachment_events_do_not_consume_the_ui_byte_budget() {
        let cancelled = Arc::new(TerminalAttachmentState::new());
        cancelled.cancel();
        let active = Arc::new(TerminalAttachmentState::new());
        let (sender, receiver) = std::sync::mpsc::sync_channel(4);
        sender
            .send(terminal_snapshot_message_for(1, cancelled.clone()))
            .unwrap();
        sender
            .send(terminal_snapshot_message_for(2, cancelled))
            .unwrap();
        sender
            .send(terminal_snapshot_message_for(3, active.clone()))
            .unwrap();
        sender
            .send(terminal_snapshot_message_for(4, active))
            .unwrap();

        let (batch, pending) = recv_terminal_event_batch(&receiver, None).unwrap();
        assert_eq!(
            batch.iter().map(snapshot_sequence).collect::<Vec<_>>(),
            [1, 2, 3]
        );
        assert_eq!(pending.as_ref().map(snapshot_sequence), Some(4));
    }

    #[test]
    fn replay_progress_is_published_and_paced_before_emulator_handoff() {
        let terminal_id = TerminalId::new(5);
        let size = crate::terminal::TerminalSize::new(80, 24);
        let attachment = Arc::new(TerminalAttachmentState::new());
        let (sender, receiver) = std::sync::mpsc::sync_channel(2);
        let mut emulator = TerminalEmulator::new(terminal_id, size, 100);
        let mut events = vec![TerminalStreamEvent::Output {
            seq: 1,
            size,
            bytes: Arc::from(b"first replay row\r\n".as_slice()),
        }];
        assert!(publish_replay_progress(
            &sender,
            terminal_id,
            &attachment,
            &mut emulator,
            &mut events,
            true,
        ));
        events.push(TerminalStreamEvent::Output {
            seq: 2,
            size,
            bytes: Arc::from(b"second replay row\r\n".as_slice()),
        });
        assert!(publish_replay_progress(
            &sender,
            terminal_id,
            &attachment,
            &mut emulator,
            &mut events,
            true,
        ));

        let (batch, pending) = recv_terminal_event_batch(&receiver, None).unwrap();
        assert_eq!(batch.len(), 1);
        let TerminalEventMsg::ReplayProgress { snapshot, .. } = &batch[0] else {
            panic!("expected replay progress")
        };
        assert!(snapshot.visible_text().contains("first replay row"));
        assert!(pending.is_some(), "the next progress frame must be paced");
    }

    #[test]
    fn replay_can_advance_without_publishing_an_intermediate_snapshot() {
        let terminal_id = TerminalId::new(6);
        let size = crate::terminal::TerminalSize::new(80, 24);
        let attachment = Arc::new(TerminalAttachmentState::new());
        let (sender, receiver) = std::sync::mpsc::sync_channel(1);
        let mut emulator = TerminalEmulator::new(terminal_id, size, 100);
        let mut events = vec![TerminalStreamEvent::Output {
            seq: 1,
            size,
            bytes: Arc::from(b"coalesced replay row\r\n".as_slice()),
        }];

        assert!(publish_replay_progress(
            &sender,
            terminal_id,
            &attachment,
            &mut emulator,
            &mut events,
            false,
        ));
        assert!(events.is_empty());
        assert_eq!(emulator.last_seq(), 1);
        assert!(matches!(
            receiver.try_recv(),
            Err(std::sync::mpsc::TryRecvError::Empty)
        ));
    }

    #[test]
    fn emulator_scroll_commands_are_applied_on_the_worker_owned_emulator() {
        let terminal_id = TerminalId::new(9);
        let size = crate::terminal::TerminalSize::new(80, 24);
        let mut emulator = TerminalEmulator::new(terminal_id, size, 100);
        emulator.apply(&TerminalStreamEvent::Output {
            seq: 1,
            size,
            bytes: Arc::from(vec![b'\n'; 40]),
        });
        let (sender, receiver) = std::sync::mpsc::sync_channel(1);
        sender.send(TerminalEmulatorCommand::ScrollBy(1)).unwrap();
        assert!(apply_emulator_commands(&receiver, &mut emulator));
        assert_eq!(emulator.viewport_position(), 1);
    }

    #[test]
    fn custom_titlebar_window_options_keep_the_window_freely_resizable() {
        let min_size = size(px(400.), px(260.));
        let options = water_window_options(Bounds::default(), min_size);
        assert!(options.is_resizable, "the window must be freely resizable");
        assert!(options.app_owns_titlebar_drag);
        assert_eq!(options.window_decorations, Some(WindowDecorations::Client));
        assert_eq!(options.window_min_size, Some(min_size));
        let titlebar = options
            .titlebar
            .expect("a transparent titlebar keeps the AppKit resize style mask");
        assert!(titlebar.appears_transparent);
        assert!(titlebar.title.is_none());
        assert!(
            titlebar.traffic_light_position.is_none(),
            "native buttons are hidden through AppKit after creation; gpui \
             positioning must not be abused as an off-screen hack"
        );
    }

    #[test]
    fn menu_bar_exposes_quit_without_a_key_binding() {
        let menus = application_menus();
        assert_eq!(
            menus
                .iter()
                .map(|menu| menu.name.as_ref())
                .collect::<Vec<_>>(),
            ["Water", "File", "View", "Window"]
        );
        assert!(menus[0].items.iter().any(|item| matches!(
            item,
            MenuItem::Action { name, .. } if name == "Quit Water"
        )));
        assert!(menus[0].items.iter().any(|item| matches!(
            item,
            MenuItem::Action { name, .. } if name == "Quit GUI and Server"
        )));
        // gpui renders menu key equivalents from keymap bindings for the
        // action; QuitApplication is deliberately never bound, so the item
        // shows no shortcut and cmd-q remains swallowed by IgnoreQuit.
        assert!(
            configured_window_key_bindings(&AppConfig::default())
                .iter()
                .all(|binding| {
                    binding.action().name()
                        != <QuitApplication as gpui::Action>::name(&QuitApplication)
                        && binding.action().name()
                            != <QuitApplicationAndServer as gpui::Action>::name(
                                &QuitApplicationAndServer,
                            )
                })
        );
    }

    #[test]
    fn menu_bar_surfaces_workspace_and_sidebar_actions() {
        let menus = application_menus();
        let labels = menus
            .iter()
            .flat_map(|menu| menu.items.iter())
            .filter_map(|item| match item {
                MenuItem::Action { name, .. } => Some(name.as_ref()),
                MenuItem::Separator | MenuItem::Submenu(_) | MenuItem::SystemMenu(_) => None,
            })
            .collect::<Vec<_>>();
        assert!(labels.contains(&"Settings…"));
        assert!(labels.contains(&"New Workspace"));
        assert!(labels.contains(&"Rename Workspace"));
        assert!(labels.contains(&"Rename Tab"));
        assert!(labels.contains(&"Toggle Sidebar"));
    }

    #[gpui::test]
    fn kill_remote_connection_defers_view_fanout_until_callback_returns(
        cx: &mut gpui::TestAppContext,
    ) {
        let mut host = crate::app::ModelHost::start();
        let client: Arc<dyn CommandTransport> = Arc::new(host.client());
        let snapshot = host.client().state_dump().unwrap();
        let application =
            WaterApplication::new(client.clone(), snapshot.clone(), AppConfig::default());
        let remote_id = ConnectionId::new(2);
        application
            .state
            .connections
            .borrow_mut()
            .push(ManagedConnection {
                projection: WorkspaceConnection {
                    id: remote_id,
                    title: "test-remote".to_owned(),
                    kind: WorkspaceConnectionKind::Remote,
                    client,
                    snapshot,
                },
                _tunnel: None,
                local_socket: None,
                last_snapshot_apply: std::time::Instant::now()
                    - WaterApplication::SNAPSHOT_MIN_INTERVAL,
                terminal: None,
            });

        let connections = application.connection_projections();
        let application_for_view = application.clone();
        let (view, cx) = cx.add_window_view(move |_, cx| {
            WorkspaceView::new_with_connections(
                Some(application_for_view),
                connections,
                ConnectionId::new(1),
                cx.focus_handle(),
                AppConfig::default(),
            )
        });
        application.state.views.borrow_mut().push(view.downgrade());

        // This is the same nesting as the context-menu mouse callback. Before
        // removal was deferred, kill_connection synchronously updated `view`
        // again here and GPUI aborted on the re-entrant mutable entity update.
        view.update_in(cx, |_, _, cx| {
            application.kill_connection(remote_id, cx);
        });
        cx.run_until_parked();

        assert_eq!(
            application
                .state
                .connections
                .borrow()
                .iter()
                .map(|connection| connection.projection.id)
                .collect::<Vec<_>>(),
            vec![ConnectionId::new(1)]
        );
        assert_eq!(application.state.views.borrow().len(), 1);
        host.shutdown();
    }

    #[gpui::test]
    fn settings_shortcut_opens_one_standalone_settings_window(cx: &mut gpui::TestAppContext) {
        let mut host = crate::app::ModelHost::start();
        let client = std::sync::Arc::new(host.client());
        let snapshot = client.state_dump().unwrap();
        let application = WaterApplication::new(client.clone(), snapshot, AppConfig::default());

        cx.update(|cx| {
            cx.bind_keys(configured_window_key_bindings(&AppConfig::default()));
            let application_for_action = application.clone();
            cx.on_action(move |_: &OpenSettings, cx| application_for_action.open_settings(cx));
            application.open_window(cx);
        });
        let workspace_window = cx.read(|cx| cx.windows()[0]);
        cx.simulate_keystrokes(workspace_window, "cmd-,");
        cx.run_until_parked();
        assert_eq!(cx.read(|cx| cx.windows().len()), 2);
        assert!(cx.read(|cx| {
            cx.windows()
                .iter()
                .any(|window| window.root_entity_type_name().contains("SettingsView"))
        }));

        let settings_window = cx.read(|cx| {
            cx.windows()
                .into_iter()
                .find(|window| window.root_entity_type_name().contains("SettingsView"))
                .expect("settings window exists")
        });
        cx.simulate_keystrokes(settings_window, "cmd-,");
        cx.run_until_parked();
        assert_eq!(cx.read(|cx| cx.windows().len()), 2);
        assert_eq!(
            cx.read(|cx| {
                cx.windows()
                    .iter()
                    .filter(|window| window.root_entity_type_name().contains("SettingsView"))
                    .count()
            }),
            1
        );

        cx.update(|cx| {
            workspace_window
                .update(cx, |_, window, _| window.activate_window())
                .unwrap();
        });
        cx.run_until_parked();
        cx.simulate_keystrokes(workspace_window, "cmd-,");
        cx.run_until_parked();
        assert_eq!(cx.read(|cx| cx.windows().len()), 2);

        settings_window
            .update(cx, |_, window, _| window.remove_window())
            .unwrap();
        cx.run_until_parked();
        assert_eq!(cx.read(|cx| cx.windows().len()), 1);

        cx.simulate_keystrokes(workspace_window, "cmd-,");
        cx.run_until_parked();
        assert_eq!(cx.read(|cx| cx.windows().len()), 2);
        assert_eq!(
            cx.read(|cx| {
                cx.windows()
                    .iter()
                    .filter(|window| window.root_entity_type_name().contains("SettingsView"))
                    .count()
            }),
            1
        );
        host.shutdown();
    }

    #[gpui::test]
    fn reopen_restores_a_workspace_window_when_settings_is_still_open(
        cx: &mut gpui::TestAppContext,
    ) {
        let mut host = crate::app::ModelHost::start();
        let client = std::sync::Arc::new(host.client());
        let snapshot = client.state_dump().unwrap();
        let application = WaterApplication::new(client.clone(), snapshot, AppConfig::default());

        cx.update(|cx| {
            application.open_window(cx);
            application.open_settings(cx);
        });
        assert_eq!(cx.read(|cx| cx.windows().len()), 2);
        let workspace_window = cx.read(|cx| {
            cx.windows()
                .into_iter()
                .find(|window| window.downcast::<WorkspaceView>().is_some())
                .expect("workspace window exists")
        });
        workspace_window
            .update(cx, |_, window, _| window.remove_window())
            .unwrap();
        cx.run_until_parked();
        assert_eq!(cx.read(|cx| cx.windows().len()), 1);

        cx.update(|cx| application.reopen(cx));
        cx.run_until_parked();
        assert_eq!(cx.read(|cx| cx.windows().len()), 2);
        assert_eq!(
            cx.read(|cx| {
                cx.windows()
                    .iter()
                    .filter(|window| window.downcast::<WorkspaceView>().is_some())
                    .count()
            }),
            1
        );
        assert_eq!(
            cx.read(|cx| {
                cx.windows()
                    .iter()
                    .filter(|window| window.root_entity_type_name().contains("SettingsView"))
                    .count()
            }),
            1
        );
        host.shutdown();
    }

    #[gpui::test]
    fn application_shortcuts_route_through_the_focused_window(cx: &mut gpui::TestAppContext) {
        let mut host = crate::app::ModelHost::start();
        let client = std::sync::Arc::new(host.client());
        let snapshot = client.state_dump().unwrap();
        let application = WaterApplication::new(client.clone(), snapshot, AppConfig::default());

        cx.update(|cx| {
            cx.bind_keys(window_key_bindings());
            let application_for_action = application.clone();
            cx.on_action(move |_: &NewWindow, cx| application_for_action.open_window(cx));
            application.open_window(cx);
        });
        let first_window = cx.read(|cx| cx.windows()[0]);
        cx.update(|cx| {
            first_window
                .update(cx, |_, window, _| window.activate_window())
                .unwrap();
        });
        cx.run_until_parked();

        cx.simulate_keystrokes(first_window, "cmd-n");
        assert_eq!(cx.read(|cx| cx.windows().len()), 2);

        let second_window = cx.read(|cx| cx.windows()[1]);
        cx.update(|cx| {
            second_window
                .update(cx, |_, window, _| window.activate_window())
                .unwrap();
        });
        cx.run_until_parked();
        cx.simulate_keystrokes(second_window, "cmd-w");
        cx.run_until_parked();
        assert_eq!(cx.read(|cx| cx.windows().len()), 1);

        let remaining_window = cx.read(|cx| cx.windows()[0]);
        cx.simulate_keystrokes(remaining_window, "cmd-q");
        cx.run_until_parked();
        assert_eq!(cx.read(|cx| cx.windows().len()), 1);

        host.shutdown();
    }
}

/// The configured size of one terminal inside a projected state dump.
fn terminal_size_in_snapshot(
    snapshot: &ModelSnapshot,
    terminal_id: TerminalId,
) -> Option<crate::terminal::TerminalSize> {
    for workspace in &snapshot.workspaces {
        for tab in &workspace.tabs {
            let mut stack = std::vec::Vec::with_capacity(8);
            stack.push(&tab.tree);
            while let Some(tree) = stack.pop() {
                match tree {
                    crate::app::model::PaneTreeDump::Leaf { terminal, .. } => {
                        if let Some(terminal) = terminal
                            && terminal.summary.terminal_id == terminal_id
                        {
                            return Some(terminal.summary.size);
                        }
                    }
                    crate::app::model::PaneTreeDump::Split { first, second, .. } => {
                        stack.push(first);
                        stack.push(second);
                    }
                }
            }
        }
    }
    None
}

/// All terminal identities projected by a control-plane snapshot. Stream
/// attachment is eager so hidden terminals keep their local emulator current.
fn terminal_ids_in_snapshot(snapshot: &ModelSnapshot) -> Vec<TerminalId> {
    let mut terminal_ids = Vec::new();
    for workspace in &snapshot.workspaces {
        for tab in &workspace.tabs {
            let mut stack = vec![&tab.tree];
            while let Some(tree) = stack.pop() {
                match tree {
                    crate::app::model::PaneTreeDump::Leaf { terminal, .. } => {
                        if let Some(terminal) = terminal {
                            terminal_ids.push(terminal.summary.terminal_id);
                        }
                    }
                    crate::app::model::PaneTreeDump::Split { first, second, .. } => {
                        stack.push(first);
                        stack.push(second);
                    }
                }
            }
        }
    }
    terminal_ids.sort_unstable();
    terminal_ids.dedup();
    terminal_ids
}
