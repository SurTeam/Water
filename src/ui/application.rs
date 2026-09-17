use std::cell::{Cell, RefCell};
use std::path::PathBuf;
use std::rc::Rc;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex};

use gpui::{
    App, AppContext, Bounds, DispatchEventResult, Focusable, KeyBinding, Keystroke, Menu, MenuItem,
    Modifiers, PlatformInput, QuitMode, ScrollDelta, ScrollWheelEvent, Size, SystemMenuType,
    SystemNotification, Task, TitlebarOptions, TouchPhase, WeakEntity, WindowBounds,
    WindowDecorations, WindowHandle, WindowOptions, actions, point, px, size,
};

use crate::agent::AgentKind;
#[cfg(test)]
use crate::app::model::AgentDump;
use crate::app::{CommandTransport, ModelSnapshot};
use crate::config::{AppConfig, switch_tab_binding};
use crate::control::{
    ConnectionInfo, ConnectionListResponse, ControlClient, RemoteCommandClient, WaterSession,
    connect_water_session, spawn_state_polling_fallback,
};
use crate::ids::{ConnectionId, TerminalId};
use crate::remote::SshTunnel;
use crate::terminal::{
    MAX_OUTPUT_EVENT_BYTES, TerminalEmulator, TerminalSnapshot, TerminalStreamEvent, TerminalTheme,
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
        ConnectRemote,
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
    next_notification_id: Cell<u64>,
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
    emulator_commands:
        std::collections::BTreeMap<TerminalId, std::sync::mpsc::Sender<TerminalEmulatorCommand>>,
    snapshots: std::collections::BTreeMap<TerminalId, Arc<crate::terminal::TerminalSnapshot>>,
    pending_attachments: std::collections::BTreeSet<TerminalId>,
    attachments: std::collections::BTreeMap<TerminalId, Arc<TerminalAttachmentState>>,
    cell_sizes: std::collections::BTreeMap<TerminalId, (u16, u16)>,
    desired_scrollback: std::collections::BTreeMap<TerminalId, (usize, bool)>,
    event_sink: TerminalEventSink,
    scrollback_lines: usize,
    inactive_scrollback_lines: usize,
    max_total_scrollback_bytes: usize,
    theme: TerminalTheme,
}

enum TerminalEmulatorCommand {
    ScrollBy(i64),
    ScrollTo(i64),
    SetPinned(bool),
    SetScrollbackLines(usize),
    SetScrollbackProtected(bool),
    SetCellSize { cell_width: u16, cell_height: u16 },
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

/// Large terminal grids are owned by attachment threads. Once one exits, ask
/// the platform allocator to return releasable pages (a no-op on platforms
/// without the macOS API) so Activity Monitor reflects the dropped grid sooner.
struct TerminalAttachmentCleanup;

impl Drop for TerminalAttachmentCleanup {
    fn drop(&mut self) {
        crate::terminal::release_allocator_pressure();
    }
}

enum TerminalEventMsg {
    Attached {
        terminal_id: TerminalId,
        attachment: Arc<TerminalAttachmentState>,
        snapshot: Arc<crate::terminal::TerminalSnapshot>,
    },
    SnapshotReady {
        terminal_id: TerminalId,
        attachment: Arc<TerminalAttachmentState>,
    },
    PtyWrites {
        terminal_id: TerminalId,
        attachment: Arc<TerminalAttachmentState>,
        writes: Vec<Vec<u8>>,
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
/// Keep the control/wakeup mailbox small. Full render snapshots live in a
/// per-terminal latest-value slot, so a slow UI cannot accumulate historical
/// frames and a full mailbox cannot discard the final state.
const TERMINAL_EVENT_QUEUE_CAPACITY: usize = 64;

impl TerminalEventMsg {
    fn terminal_id(&self) -> TerminalId {
        match self {
            Self::Attached { terminal_id, .. }
            | Self::SnapshotReady { terminal_id, .. }
            | Self::PtyWrites { terminal_id, .. }
            | Self::Restart { terminal_id, .. }
            | Self::Detached { terminal_id, .. } => *terminal_id,
        }
    }

    fn attachment(&self) -> &TerminalAttachmentState {
        match self {
            Self::Attached { attachment, .. }
            | Self::SnapshotReady { attachment, .. }
            | Self::PtyWrites { attachment, .. }
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
            Self::SnapshotReady { .. } => MAX_TERMINAL_BYTES_PER_UI_TURN,
            Self::Attached { .. }
            | Self::PtyWrites { .. }
            | Self::Restart { .. }
            | Self::Detached { .. } => 0,
        }
    }
}

struct PendingTerminalSnapshot {
    attachment: Arc<TerminalAttachmentState>,
    snapshot: Arc<crate::terminal::TerminalSnapshot>,
}

struct TerminalSnapshotStore {
    pending: std::collections::BTreeMap<TerminalId, PendingTerminalSnapshot>,
    notified: std::collections::BTreeSet<TerminalId>,
}

/// Delivers lifecycle/control messages reliably while keeping only the latest
/// render snapshot per terminal. Snapshot notifications are tiny; the actual
/// immutable snapshot lives in this coalescing slot instead of piling up in a
/// FIFO when GPUI is busy.
#[derive(Clone)]
struct TerminalEventSink {
    events_tx: std::sync::mpsc::SyncSender<TerminalEventMsg>,
    snapshots: Arc<Mutex<TerminalSnapshotStore>>,
}

impl TerminalEventSink {
    fn new(events_tx: std::sync::mpsc::SyncSender<TerminalEventMsg>) -> Self {
        Self {
            events_tx,
            snapshots: Arc::new(Mutex::new(TerminalSnapshotStore {
                pending: std::collections::BTreeMap::new(),
                notified: std::collections::BTreeSet::new(),
            })),
        }
    }

    fn publish_control(
        &self,
        attachment: &Arc<TerminalAttachmentState>,
        mut message: TerminalEventMsg,
    ) -> bool {
        if !attachment.is_active() {
            return false;
        }
        loop {
            match self.events_tx.try_send(message) {
                Ok(()) => return true,
                Err(std::sync::mpsc::TrySendError::Disconnected(_)) => return false,
                Err(std::sync::mpsc::TrySendError::Full(next)) => {
                    if !attachment.is_active() {
                        return false;
                    }
                    message = next;
                    std::thread::park_timeout(std::time::Duration::from_millis(1));
                }
            }
        }
    }

    fn publish_snapshot(
        &self,
        terminal_id: TerminalId,
        attachment: &Arc<TerminalAttachmentState>,
        snapshot: Arc<crate::terminal::TerminalSnapshot>,
    ) -> bool {
        if !attachment.is_active() {
            return false;
        }
        let should_notify = {
            let mut store = self.snapshots.lock().expect("terminal snapshots poisoned");
            store.pending.insert(
                terminal_id,
                PendingTerminalSnapshot {
                    attachment: attachment.clone(),
                    snapshot,
                },
            );
            store.notified.insert(terminal_id)
        };
        if !should_notify {
            return true;
        }
        if self.publish_control(
            attachment,
            TerminalEventMsg::SnapshotReady {
                terminal_id,
                attachment: attachment.clone(),
            },
        ) {
            true
        } else {
            self.remove_if(terminal_id, attachment);
            false
        }
    }

    fn take_snapshot(
        &self,
        terminal_id: TerminalId,
        attachment: &Arc<TerminalAttachmentState>,
    ) -> Option<Arc<crate::terminal::TerminalSnapshot>> {
        let mut store = self.snapshots.lock().expect("terminal snapshots poisoned");
        let matches = store
            .pending
            .get(&terminal_id)
            .is_some_and(|pending| Arc::ptr_eq(&pending.attachment, attachment));
        if !matches {
            return None;
        }
        let pending = store
            .pending
            .remove(&terminal_id)
            .expect("snapshot disappeared");
        store.notified.remove(&terminal_id);
        Some(pending.snapshot)
    }

    fn remove(&self, terminal_id: TerminalId) {
        let mut store = self.snapshots.lock().expect("terminal snapshots poisoned");
        store.pending.remove(&terminal_id);
        store.notified.remove(&terminal_id);
    }

    fn remove_if(&self, terminal_id: TerminalId, attachment: &Arc<TerminalAttachmentState>) {
        let mut store = self.snapshots.lock().expect("terminal snapshots poisoned");
        if store
            .pending
            .get(&terminal_id)
            .is_some_and(|pending| Arc::ptr_eq(&pending.attachment, attachment))
        {
            store.pending.remove(&terminal_id);
            store.notified.remove(&terminal_id);
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
    sender: &TerminalEventSink,
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
    if !attachment.is_active() {
        return false;
    }
    if !publish_snapshot {
        return true;
    }
    sender.publish_snapshot(terminal_id, attachment, Arc::new(emulator.snapshot(None)))
}

/// Publishes a lifecycle/control event without silently dropping it. A slow
/// GPUI turn can backpressure this attachment thread, but PTY bytes and state
/// transitions remain ordered and intact.
fn try_publish_terminal_event(
    sender: &TerminalEventSink,
    attachment: &Arc<TerminalAttachmentState>,
    message: TerminalEventMsg,
) -> bool {
    sender.publish_control(attachment, message)
}

/// Enables verbose per-terminal attach/live diagnostics behind an env gate.
/// Off by default so normal logging stays quiet; set WATER_DEBUG_TERMINAL=1.
fn terminal_debug_enabled() -> bool {
    static ENABLED: std::sync::OnceLock<bool> = std::sync::OnceLock::new();
    *ENABLED.get_or_init(|| std::env::var_os("WATER_DEBUG_TERMINAL").is_some())
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
            TerminalEmulatorCommand::SetPinned(pinned) => emulator.set_viewport_pinned(pinned),
            TerminalEmulatorCommand::SetScrollbackLines(lines) => {
                emulator.set_scrollback_lines(lines)
            }
            TerminalEmulatorCommand::SetScrollbackProtected(protected) => {
                emulator.set_scrollback_protected(protected)
            }
            TerminalEmulatorCommand::SetCellSize {
                cell_width,
                cell_height,
            } => emulator.set_cell_size(cell_width, cell_height),
        }
        changed = true;
    }
    changed
}

fn terminal_scrollback_protected(snapshot: &ModelSnapshot, terminal_id: TerminalId) -> bool {
    snapshot.agents.iter().any(|agent| {
        agent.terminal_id == terminal_id
            && agent.kind == AgentKind::Pi
            && matches!(agent.status, crate::surface::TerminalStatus::Running)
    })
}

fn publish_live_snapshot(
    sender: &TerminalEventSink,
    terminal_id: TerminalId,
    attachment: &Arc<TerminalAttachmentState>,
    emulator: &mut TerminalEmulator,
    previous: Option<&crate::terminal::TerminalSnapshot>,
) -> bool {
    if !attachment.is_active() {
        return false;
    }
    let pty_writes = emulator.pty_writes();
    if !pty_writes.is_empty()
        && !try_publish_terminal_event(
            sender,
            attachment,
            TerminalEventMsg::PtyWrites {
                terminal_id,
                attachment: attachment.clone(),
                writes: pty_writes,
            },
        )
    {
        return false;
    }
    sender.publish_snapshot(
        terminal_id,
        attachment,
        Arc::new(emulator.snapshot(previous)),
    )
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
                next_notification_id: Cell::new(0),
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

    pub fn set_local_socket(&self, local_socket: PathBuf) {
        let mut connections = self.state.connections.borrow_mut();
        if let Some(connection) = connections.first_mut() {
            connection.local_socket = Some(local_socket);
        }
    }

    fn connection_projections(&self) -> Vec<WorkspaceConnection> {
        self.state
            .connections
            .borrow()
            .iter()
            .map(|connection| connection.projection.clone())
            .collect()
    }

    fn connection_list(&self) -> ConnectionListResponse {
        let connections = self
            .state
            .connections
            .borrow()
            .iter()
            .map(|connection| {
                let remote = connection._tunnel.as_ref();
                ConnectionInfo {
                    id: connection.projection.id,
                    name: connection.projection.title.clone(),
                    kind: match connection.projection.kind {
                        WorkspaceConnectionKind::Local => "local".to_owned(),
                        WorkspaceConnectionKind::Remote => "remote".to_owned(),
                    },
                    socket_path: connection
                        .local_socket
                        .as_ref()
                        .map(|path| path.display().to_string()),
                    remote_socket_path: remote
                        .map(|tunnel| tunnel.remote_socket().display().to_string()),
                    destination: remote.map(|tunnel| tunnel.destination().to_owned()),
                }
            })
            .collect();
        ConnectionListResponse { connections }
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

    pub(crate) fn sync_hyperlink_settings(&self, cx: &mut App) {
        if let Some(window) = *self.state.settings_window.borrow() {
            let enabled = self.config().terminal.remote_hyperlink_auto_download;
            cx.defer(move |cx| {
                let _ = window.update(cx, |settings, _, cx| {
                    settings.sync_hyperlink_preference(enabled, cx)
                });
            });
        }
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
        let (app_identifier, app_name) = if crate::BUILD_VARIANT == "release" {
            ("dev.water.terminal", "Water")
        } else {
            ("dev.water.terminal.dev", "Water Dev")
        };
        cx.set_app_identity(app_identifier, app_name);
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

    /// Turns the model's agent activity edge into a platform notification.
    ///
    /// The model/PTY threads only publish state. GPUI owns the actual system
    /// notification call and runs this method on the application thread.
    fn notify_agent_transitions(
        &self,
        previous: &ModelSnapshot,
        current: &ModelSnapshot,
        cx: &mut App,
    ) {
        for before in &previous.agents {
            let after = current
                .agents
                .iter()
                .find(|agent| agent.terminal_id == before.terminal_id);
            let became_idle = after.is_some_and(|agent| {
                agent.kind == before.kind
                    && before.active
                    && !agent.active
                    && matches!(agent.status, crate::surface::TerminalStatus::Running)
            });
            let exited = matches!(before.status, crate::surface::TerminalStatus::Running)
                && after.is_none_or(|agent| {
                    matches!(agent.status, crate::surface::TerminalStatus::Exited { .. })
                });
            if !became_idle && !exited {
                continue;
            }

            let notification_id = self.state.next_notification_id.get().wrapping_add(1).max(1);
            self.state.next_notification_id.set(notification_id);
            let detail = if before.cwd.is_empty() {
                String::new()
            } else {
                format!(" · {}", before.cwd)
            };
            let body = if exited {
                format!("{} exited{}", before.display_label(), detail)
            } else {
                format!("{} is waiting for input{}", before.display_label(), detail)
            };
            cx.show_system_notification(SystemNotification {
                tag: format!("water-agent-{}-{}", before.terminal_id, notification_id).into(),
                title: "Agent finished".into(),
                body: body.into(),
                actions: Vec::new(),
            });
        }
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
                let (installed, previous_snapshot) = {
                    let mut connections = state.connections.borrow_mut();
                    let Some(connection) = connections
                        .iter_mut()
                        .find(|connection| connection.projection.id == connection_id)
                    else {
                        break;
                    };
                    let previous_snapshot = connection.projection.snapshot.clone();
                    if snapshot.state_revision <= connection.projection.snapshot.state_revision {
                        (false, previous_snapshot)
                    } else {
                        connection.last_snapshot_apply = std::time::Instant::now();
                        connection.projection.snapshot = snapshot.clone();
                        (true, previous_snapshot)
                    }
                };
                if !installed {
                    continue;
                }
                cx.update(|cx| {
                    application.notify_agent_transitions(&previous_snapshot, &snapshot, cx);
                });
                application.sync_terminal_scrollback_protection(connection_id, &snapshot);
                let current_ids = terminal_ids_in_snapshot(&snapshot);
                for terminal_id in &current_ids {
                    application.ensure_terminal_attached(connection_id, *terminal_id);
                }
                for terminal_id in terminal_ids_in_snapshot(&previous_snapshot)
                    .iter()
                    .copied()
                    .filter(|terminal_id| !current_ids.contains(terminal_id))
                {
                    cx.update(|cx| application.detach_terminal(cx, connection_id, terminal_id));
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
        let (events_tx, events_rx) = std::sync::mpsc::sync_channel(TERMINAL_EVENT_QUEUE_CAPACITY);
        let event_sink = TerminalEventSink::new(events_tx);
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
            cell_sizes: std::collections::BTreeMap::new(),
            desired_scrollback: std::collections::BTreeMap::new(),
            event_sink,
            scrollback_lines: config.terminal.scrollback_lines,
            inactive_scrollback_lines: config.terminal.inactive_scrollback_lines,
            max_total_scrollback_bytes: config.terminal.max_total_scrollback_bytes,
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
                let mut detached_attachment = None;
                for message in messages {
                    match message {
                        TerminalEventMsg::Attached { snapshot, .. } => {
                            terminal.pending_attachments.remove(&terminal_id);
                            // The live worker can publish a newer snapshot after replay
                            // wakes the UI but before this batch is consumed. Keep that
                            // snapshot instead of replacing it with the attach-time state.
                            Self::merge_terminal_snapshot(&mut latest_snapshot, snapshot, true);
                        }
                        TerminalEventMsg::SnapshotReady { attachment, .. } => {
                            if let Some(snapshot) =
                                terminal.event_sink.take_snapshot(terminal_id, &attachment)
                            {
                                Self::merge_terminal_snapshot(
                                    &mut latest_snapshot,
                                    snapshot,
                                    false,
                                );
                            }
                        }
                        TerminalEventMsg::PtyWrites { writes, .. } => {
                            pty_writes.extend(writes.into_iter().map(|bytes| (terminal_id, bytes)));
                        }
                        TerminalEventMsg::Restart { .. } => resync.push(terminal_id),
                        TerminalEventMsg::Detached { attachment, .. } => {
                            detached = true;
                            detached_attachment = Some(attachment);
                        }
                    }
                }
                if let Some(snapshot) = latest_snapshot {
                    terminal.snapshots.insert(terminal_id, snapshot);
                    changed.insert(terminal_id);
                }
                if detached {
                    if detached_attachment.is_some() {
                        terminal.event_sink.remove(terminal_id);
                    }
                    terminal.pending_attachments.remove(&terminal_id);
                    terminal.attachments.remove(&terminal_id);
                    terminal.emulator_commands.remove(&terminal_id);
                    terminal.cell_sizes.remove(&terminal_id);
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
                    workspace.apply_terminal_events_for_connection(connection_id, &changed, cx)
                })
                .is_ok()
            {
                live_views.push(view);
            }
        }
        self.state.views.replace(live_views);
    }

    fn merge_terminal_snapshot(
        latest_snapshot: &mut Option<Arc<TerminalSnapshot>>,
        snapshot: Arc<TerminalSnapshot>,
        from_attach: bool,
    ) {
        if !from_attach || latest_snapshot.is_none() {
            *latest_snapshot = Some(snapshot);
        }
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
            terminal.event_sink.remove(terminal_id);
            terminal.pending_attachments.remove(&terminal_id);
            terminal.emulator_commands.remove(&terminal_id);
            terminal.cell_sizes.remove(&terminal_id);
            terminal.desired_scrollback.remove(&terminal_id);
            terminal.session.clone()
        };
        session.detach(terminal_id);
        self.ensure_terminal_attached(connection_id, terminal_id);
    }

    /// Attaches every terminal projected by an authoritative model snapshot.
    ///
    /// Command RPCs return a fresh `state_dump()` before the asynchronous
    /// `push.snapshot` stream necessarily reaches `WaterApplication`. Use the
    /// command snapshot directly so newly-created remote panes can attach their
    /// raw PTY stream even when the application-level projection is briefly stale.
    pub(crate) fn ensure_terminals_attached_from_snapshot(
        &self,
        connection_id: ConnectionId,
        snapshot: &ModelSnapshot,
    ) {
        for terminal_id in terminal_ids_in_snapshot(snapshot) {
            self.ensure_terminal_attached_with_snapshot(connection_id, terminal_id, snapshot);
        }
    }

    /// Attaches the raw stream and starts its worker-owned emulator. Replay
    /// and live parsing both publish immutable snapshots; GPUI never mutates
    /// or waits on the emulator.
    pub fn ensure_terminal_attached(&self, connection_id: ConnectionId, terminal_id: TerminalId) {
        let snapshot = self
            .state
            .connections
            .borrow()
            .iter()
            .find(|connection| connection.projection.id == connection_id)
            .map(|connection| connection.projection.snapshot.clone());
        let Some(snapshot) = snapshot else {
            return;
        };
        self.ensure_terminal_attached_with_snapshot(connection_id, terminal_id, &snapshot);
    }

    fn ensure_terminal_attached_with_snapshot(
        &self,
        connection_id: ConnectionId,
        terminal_id: TerminalId,
        snapshot: &ModelSnapshot,
    ) {
        if terminal_size_in_snapshot(snapshot, terminal_id).is_none() {
            return;
        }
        let (
            session,
            event_sink,
            scrollback_lines,
            theme,
            scrollback_protected,
            attachment,
            emulator_commands,
        ) = {
            let mut connections = self.state.connections.borrow_mut();
            let Some(connection) = connections
                .iter_mut()
                .find(|connection| connection.projection.id == connection_id)
            else {
                return;
            };
            let scrollback_protected = terminal_scrollback_protected(snapshot, terminal_id);
            let Some(terminal) = connection.terminal.as_mut() else {
                return;
            };
            if terminal.emulator_commands.contains_key(&terminal_id)
                || !terminal.pending_attachments.insert(terminal_id)
            {
                return;
            }
            let attachment = Arc::new(TerminalAttachmentState::new());
            let (emulator_commands_tx, emulator_commands) = std::sync::mpsc::channel();
            let scrollback_lines = terminal_scrollback_lines(
                snapshot,
                terminal_id,
                terminal.scrollback_lines,
                terminal.inactive_scrollback_lines,
                terminal.max_total_scrollback_bytes,
            );
            terminal
                .desired_scrollback
                .insert(terminal_id, (scrollback_lines, scrollback_protected));
            terminal.attachments.insert(terminal_id, attachment.clone());
            terminal
                .emulator_commands
                .insert(terminal_id, emulator_commands_tx);
            (
                terminal.session.clone(),
                terminal.event_sink.clone(),
                scrollback_lines,
                terminal.theme,
                scrollback_protected,
                attachment,
                emulator_commands,
            )
        };
        let spawn_result = std::thread::Builder::new()
            .name(format!("water-terminal-events-{terminal_id}"))
            .spawn(move || match session.attach(terminal_id) {
                Ok((response, stream)) => {
                    let _allocator_cleanup = TerminalAttachmentCleanup;
                    if !attachment.is_active() {
                        return;
                    }
                    let mut emulator = TerminalEmulator::with_theme_and_scrollback_protection(
                        terminal_id,
                        response.size,
                        scrollback_lines,
                        theme,
                        scrollback_protected,
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
                                &event_sink,
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
                        &event_sink,
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
                    if !try_publish_terminal_event(
                        &event_sink,
                        &attachment,
                        TerminalEventMsg::Attached {
                            terminal_id,
                            attachment: attachment.clone(),
                            snapshot: Arc::new(emulator.snapshot(None)),
                        },
                    ) {
                        return;
                    }
                    if terminal_debug_enabled() {
                        tracing::warn!(
                            target: "water::terminal-debug",
                            ?terminal_id, ?connection_id,
                            replay_applied = emulator.last_seq(),
                            replay_rows = response.replay.len(),
                            "attach: replay processed, entering live loop"
                        );
                    }
                    let mut stream = stream;
                    let mut pending_event = None;
                    let mut last_live_snapshot = std::time::Instant::now();
                    let mut last_snapshot: Option<crate::terminal::TerminalSnapshot> =
                        Some(emulator.snapshot(None));
                    let mut live_events = 0u64;
                    let mut last_live_log = std::time::Instant::now();
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
                                    let prev = last_snapshot.as_ref();
                                    if !publish_live_snapshot(
                                        &event_sink,
                                        terminal_id,
                                        &attachment,
                                        &mut emulator,
                                        prev,
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
                        if terminal_debug_enabled() {
                            live_events += 1;
                            let bytes = event.output_bytes();
                            if bytes > 0
                                && (live_events <= 5 || last_live_log.elapsed().as_secs() >= 1)
                            {
                                tracing::warn!(
                                    target: "water::terminal-debug",
                                    ?terminal_id, ?connection_id,
                                    live_events, seq = event.seq(), bytes,
                                    "attach: live event received"
                                );
                                last_live_log = std::time::Instant::now();
                            }
                        }
                        let effects = emulator.apply(&event);
                        if effects.iter().any(|effect| {
                            matches!(effect, crate::terminal::EmulatorEffect::SequenceGap { .. })
                        }) {
                            let _ = try_publish_terminal_event(
                                &event_sink,
                                &attachment,
                                TerminalEventMsg::Restart {
                                    terminal_id,
                                    attachment: attachment.clone(),
                                },
                            );
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
                            let prev = last_snapshot.as_ref();
                            if !publish_live_snapshot(
                                &event_sink,
                                terminal_id,
                                &attachment,
                                &mut emulator,
                                prev,
                            ) {
                                return;
                            }
                            last_snapshot = Some(emulator.snapshot(prev));
                            last_live_snapshot = std::time::Instant::now();
                        }
                    }
                    if attachment.is_active() {
                        let _ = try_publish_terminal_event(
                            &event_sink,
                            &attachment,
                            TerminalEventMsg::Detached {
                                terminal_id,
                                attachment: attachment.clone(),
                            },
                        );
                    }
                }
                Err(error) => {
                    let _allocator_cleanup = TerminalAttachmentCleanup;
                    tracing::warn!(
                        target: "water::workspace",
                        ?error,
                        %terminal_id,
                        "terminal attach failed"
                    );
                    if attachment.is_active() {
                        let _ = try_publish_terminal_event(
                            &event_sink,
                            &attachment,
                            TerminalEventMsg::Detached {
                                terminal_id,
                                attachment: attachment.clone(),
                            },
                        );
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
            terminal.cell_sizes.remove(&terminal_id);
            terminal.desired_scrollback.remove(&terminal_id);
            terminal.event_sink.remove(terminal_id);
            if let Some(attachment) = terminal.attachments.remove(&terminal_id) {
                attachment.cancel();
            }
        }
    }

    /// Stops the local emulator worker for a terminal that left the model
    /// (closed tab/pane/window) and detaches its live stream.
    ///
    /// Without this the attach thread parks on `recv_timeout` forever (its
    /// `SyncSender` stays alive in `WaterSession::terminal_channels`) and its
    /// Alacritty grid + scrollback stay allocated for the life of the GUI
    /// process, while the server keeps pumping a queue nobody drains.
    fn detach_terminal(
        &self,
        cx: &mut gpui::App,
        connection_id: ConnectionId,
        terminal_id: TerminalId,
    ) {
        let session = {
            let mut connections = self.state.connections.borrow_mut();
            let Some(terminal) = connections
                .iter_mut()
                .find(|connection| connection.projection.id == connection_id)
                .and_then(|connection| connection.terminal.as_mut())
            else {
                return;
            };
            let had_attachment = terminal.attachments.remove(&terminal_id);
            if had_attachment.is_none() && !terminal.pending_attachments.contains(&terminal_id) {
                return;
            }
            if let Some(attachment) = had_attachment.as_ref() {
                // Cancel before dropping the local command/channel handles so
                // a parser that is between recv timeouts cannot publish more
                // snapshots while the tab's state is being removed.
                attachment.cancel();
            }
            terminal.event_sink.remove(terminal_id);
            terminal.pending_attachments.remove(&terminal_id);
            terminal.emulator_commands.remove(&terminal_id);
            terminal.cell_sizes.remove(&terminal_id);
            terminal.desired_scrollback.remove(&terminal_id);
            terminal.snapshots.remove(&terminal_id);
            terminal.session.clone()
        };
        session.detach(terminal_id);
        for view in self.state.views.borrow().iter().cloned() {
            // Dropped views free their maps with the entity.
            let _ = view.update(cx, |workspace, _| {
                workspace.forget_closed_terminal(terminal_id)
            });
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

    /// Updates the local emulator's physical cell size. The cache keeps the
    /// observer idempotent while the attachment worker receives the command
    /// on its lossless local command channel.
    pub(crate) fn terminal_set_cell_size(
        &self,
        connection_id: ConnectionId,
        terminal_id: TerminalId,
        cell_width: u16,
        cell_height: u16,
    ) -> bool {
        let mut connections = self.state.connections.borrow_mut();
        let Some(connection) = connections
            .iter_mut()
            .find(|connection| connection.projection.id == connection_id)
        else {
            return false;
        };
        let Some(terminal) = connection.terminal.as_mut() else {
            return false;
        };
        if terminal.cell_sizes.get(&terminal_id) == Some(&(cell_width, cell_height)) {
            return true;
        }
        let Some(commands) = terminal.emulator_commands.get(&terminal_id) else {
            return false;
        };
        if commands
            .send(TerminalEmulatorCommand::SetCellSize {
                cell_width,
                cell_height,
            })
            .is_err()
        {
            return false;
        }
        terminal
            .cell_sizes
            .insert(terminal_id, (cell_width, cell_height));
        true
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
                    .send(TerminalEmulatorCommand::ScrollBy(delta))
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
                    .send(TerminalEmulatorCommand::ScrollTo(target))
                    .is_ok()
            })
    }

    pub(crate) fn terminal_set_viewport_pinned(
        &self,
        connection_id: ConnectionId,
        terminal_id: TerminalId,
        pinned: bool,
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
                    .send(TerminalEmulatorCommand::SetPinned(pinned))
                    .is_ok()
            })
    }

    pub(crate) fn sync_terminal_scrollback_protection(
        &self,
        connection_id: ConnectionId,
        snapshot: &ModelSnapshot,
    ) {
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
        let command_channels: Vec<_> = terminal
            .emulator_commands
            .iter()
            .map(|(&terminal_id, commands)| (terminal_id, commands.clone()))
            .collect();
        for (terminal_id, commands) in command_channels {
            let scrollback_lines = terminal_scrollback_lines(
                snapshot,
                terminal_id,
                terminal.scrollback_lines,
                terminal.inactive_scrollback_lines,
                terminal.max_total_scrollback_bytes,
            );
            let protected = terminal_scrollback_protected(snapshot, terminal_id);
            let desired = (scrollback_lines, protected);
            if terminal.desired_scrollback.get(&terminal_id) == Some(&desired) {
                continue;
            }
            if commands
                .send(TerminalEmulatorCommand::SetScrollbackLines(
                    scrollback_lines,
                ))
                .is_ok()
                && commands
                    .send(TerminalEmulatorCommand::SetScrollbackProtected(protected))
                    .is_ok()
            {
                terminal.desired_scrollback.insert(terminal_id, desired);
            }
        }
    }

    fn spawn_ui_control_listener(&self, cx: &mut App, receiver: UiControlReceiver) -> Task<()> {
        let receiver = Arc::new(receiver);
        let application = self.clone();
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
                    UiControlRequest::Connections { reply } => {
                        let result = cx.update(|_| Ok(application.connection_list()));
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
                    UiControlRequest::Click {
                        position,
                        click_count,
                        reply,
                    } => {
                        let result =
                            cx.update(|cx| dispatch_controlled_click(position, click_count, cx));
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

fn water_window_options(
    bounds: Bounds<gpui::Pixels>,
    min_size: Size<gpui::Pixels>,
) -> WindowOptions {
    WindowOptions {
        window_bounds: Some(WindowBounds::Windowed(bounds)),
        // Keep AppKit's titlebar transparent so Water can draw its tab strip
        // and drag surface, while leaving the native traffic-light buttons
        // visible in their system-managed position. A bare `titlebar: None`
        // window cannot be resized on macOS because it does not receive the
        // required window style mask.
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

fn dispatch_controlled_click(
    position: (f32, f32),
    click_count: usize,
    cx: &mut App,
) -> Result<UiSnapshot, String> {
    if !position.0.is_finite() || !position.1.is_finite() {
        return Err("click coordinates must be finite".into());
    }
    let window = cx
        .active_window()
        .or_else(|| {
            cx.window_stack()
                .and_then(|windows| windows.into_iter().next())
        })
        .or_else(|| cx.windows().into_iter().last())
        .ok_or("Water has no open window")?;
    window
        .update(cx, |_, window, cx| {
            let point = point(px(position.0), px(position.1));
            window.dispatch_event(
                PlatformInput::MouseDown(gpui::MouseDownEvent {
                    button: gpui::MouseButton::Left,
                    position: point,
                    click_count: click_count.max(1),
                    ..Default::default()
                }),
                cx,
            );
            window.dispatch_event(
                PlatformInput::MouseUp(gpui::MouseUpEvent {
                    button: gpui::MouseButton::Left,
                    position: point,
                    click_count: click_count.max(1),
                    ..Default::default()
                }),
                cx,
            );
        })
        .map_err(|error| error.to_string())?;
    Ok(UiSnapshot {
        window_count: cx.windows().len(),
        has_active_window: cx.active_window().is_some(),
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
        safe_key_binding(&shortcuts.connect_remote, "cmd-shift-k", ConnectRemote),
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

    #[test]
    fn terminal_control_events_are_lossless_when_mailbox_is_full() {
        let (sender, receiver) = std::sync::mpsc::sync_channel(1);
        let sink = TerminalEventSink::new(sender);
        let attachment = Arc::new(TerminalAttachmentState::new());
        assert!(sink.publish_control(
            &attachment,
            TerminalEventMsg::Restart {
                terminal_id: TerminalId::new(1),
                attachment: attachment.clone(),
            },
        ));

        let blocked_sink = sink.clone();
        let blocked_attachment = attachment.clone();
        let second = std::thread::spawn(move || {
            blocked_sink.publish_control(
                &blocked_attachment,
                TerminalEventMsg::Restart {
                    terminal_id: TerminalId::new(2),
                    attachment: blocked_attachment.clone(),
                },
            )
        });

        assert!(matches!(
            receiver.recv().unwrap(),
            TerminalEventMsg::Restart { terminal_id, .. } if terminal_id == TerminalId::new(1)
        ));
        assert!(second.join().unwrap());
        assert!(matches!(
            receiver.recv().unwrap(),
            TerminalEventMsg::Restart { terminal_id, .. } if terminal_id == TerminalId::new(2)
        ));
    }

    #[gpui::test]
    fn agent_activity_transition_posts_a_water_system_notification(cx: &mut gpui::TestAppContext) {
        let mut host = crate::app::ModelHost::start();
        let client: Arc<dyn CommandTransport> = Arc::new(host.client());
        let initial = host.client().state_dump().unwrap();
        let application = WaterApplication::new(client, initial.clone(), AppConfig::default());

        let terminal_id = TerminalId::new(900);
        let mut previous = initial;
        previous.state_revision = 1;
        previous.agents.push(AgentDump {
            kind: AgentKind::Codex,
            label: AgentKind::Codex.label().to_owned(),
            custom_label: Some("Build agent".to_owned()),
            active: true,
            workspace_id: crate::ids::WorkspaceId::new(1),
            tab_id: crate::ids::TabId::new(2),
            pane_id: crate::ids::PaneId::new(3),
            terminal_id,
            cwd: "/tmp/project".to_owned(),
            status: crate::surface::TerminalStatus::Running,
        });
        let mut current = previous.clone();
        current.state_revision = 2;
        current.agents[0].active = false;

        cx.update(|cx| {
            cx.set_app_identity("dev.water.terminal.dev", "Water Dev");
            application.notify_agent_transitions(&previous, &current, cx);
        });
        let notifications = cx.shown_system_notifications();
        assert_eq!(notifications.len(), 1);
        assert_eq!(notifications[0].title.as_ref(), "Agent finished");
        assert!(notifications[0].body.contains("Build agent"));
        assert!(notifications[0].body.contains("/tmp/project"));

        host.shutdown();
    }

    #[test]
    fn cancelled_snapshot_notifications_do_not_consume_the_ui_byte_budget() {
        let cancelled = Arc::new(TerminalAttachmentState::new());
        cancelled.cancel();
        let active = Arc::new(TerminalAttachmentState::new());
        let (sender, receiver) = std::sync::mpsc::sync_channel(4);
        sender
            .send(TerminalEventMsg::SnapshotReady {
                terminal_id: TerminalId::new(1),
                attachment: cancelled,
            })
            .unwrap();
        sender
            .send(TerminalEventMsg::SnapshotReady {
                terminal_id: TerminalId::new(2),
                attachment: active.clone(),
            })
            .unwrap();
        sender
            .send(TerminalEventMsg::SnapshotReady {
                terminal_id: TerminalId::new(3),
                attachment: active.clone(),
            })
            .unwrap();

        let (batch, pending) = recv_terminal_event_batch(&receiver, None).unwrap();
        assert_eq!(batch.len(), 2);
        assert!(!batch[0].is_active());
        assert!(batch[1].is_active());
        assert_eq!(batch[0].ui_cost_bytes(), 0);
        assert_eq!(batch[1].ui_cost_bytes(), MAX_TERMINAL_BYTES_PER_UI_TURN);
        assert_eq!(
            pending.as_ref().map(TerminalEventMsg::terminal_id),
            Some(TerminalId::new(3))
        );
    }

    #[test]
    fn replay_progress_is_published_and_paced_before_emulator_handoff() {
        let terminal_id = TerminalId::new(5);
        let size = crate::terminal::TerminalSize::new(80, 24);
        let attachment = Arc::new(TerminalAttachmentState::new());
        let (sender, receiver) = std::sync::mpsc::sync_channel(2);
        let sink = TerminalEventSink::new(sender);
        let mut emulator = TerminalEmulator::new(terminal_id, size, 100);
        let mut events = vec![TerminalStreamEvent::Output {
            seq: 1,
            size,
            bytes: Arc::from(b"first replay row\r\n".as_slice()),
        }];
        assert!(publish_replay_progress(
            &sink,
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
            &sink,
            terminal_id,
            &attachment,
            &mut emulator,
            &mut events,
            true,
        ));

        let (batch, pending) = recv_terminal_event_batch(&receiver, None).unwrap();
        assert_eq!(batch.len(), 1);
        let TerminalEventMsg::SnapshotReady {
            terminal_id,
            attachment,
        } = &batch[0]
        else {
            panic!("expected snapshot notification")
        };
        let snapshot = sink
            .take_snapshot(*terminal_id, attachment)
            .expect("latest replay snapshot");
        assert!(snapshot.visible_text().contains("second replay row"));
        assert!(
            pending.is_none(),
            "intermediate replay frames are coalesced"
        );
    }

    #[test]
    fn replay_can_advance_without_publishing_an_intermediate_snapshot() {
        let terminal_id = TerminalId::new(6);
        let size = crate::terminal::TerminalSize::new(80, 24);
        let attachment = Arc::new(TerminalAttachmentState::new());
        let (sender, receiver) = std::sync::mpsc::sync_channel(1);
        let sink = TerminalEventSink::new(sender);
        let mut emulator = TerminalEmulator::new(terminal_id, size, 100);
        let mut events = vec![TerminalStreamEvent::Output {
            seq: 1,
            size,
            bytes: Arc::from(b"coalesced replay row\r\n".as_slice()),
        }];

        assert!(publish_replay_progress(
            &sink,
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
    fn latest_snapshot_slot_preserves_the_final_state_when_ui_is_slow() {
        let terminal_id = TerminalId::new(7);
        let size = crate::terminal::TerminalSize::new(80, 24);
        let attachment = Arc::new(TerminalAttachmentState::new());
        let (sender, receiver) = std::sync::mpsc::sync_channel(1);
        let sink = TerminalEventSink::new(sender);
        let mut emulator = TerminalEmulator::new(terminal_id, size, 100);

        for seq in 1..=10_000 {
            emulator.apply(&TerminalStreamEvent::Output {
                seq,
                size,
                bytes: Arc::from(format!("seq {seq}\r\n").into_bytes()),
            });
            assert!(sink.publish_snapshot(
                terminal_id,
                &attachment,
                Arc::new(emulator.snapshot(None)),
            ));
        }

        let TerminalEventMsg::SnapshotReady {
            terminal_id: ready_id,
            attachment: ready_attachment,
        } = receiver.recv().expect("snapshot wakeup")
        else {
            panic!("expected snapshot wakeup")
        };
        let snapshot = sink
            .take_snapshot(ready_id, &ready_attachment)
            .expect("latest snapshot slot");
        assert!(snapshot.visible_text().contains("seq 10000"));
        assert!(receiver.try_recv().is_err());
    }

    #[test]
    fn attach_snapshot_does_not_overwrite_live_snapshot_queued_before_ui_batch() {
        let terminal_id = TerminalId::new(8);
        let size = crate::terminal::TerminalSize::new(80, 24);
        let mut emulator = TerminalEmulator::new(terminal_id, size, 100);
        let attached = Arc::new(emulator.snapshot(None));

        emulator.start_live();
        emulator.apply(&TerminalStreamEvent::Output {
            seq: 1,
            size,
            bytes: Arc::from(b"shell prompt\r\n".as_slice()),
        });
        let live = Arc::new(emulator.snapshot(None));

        let mut latest = Some(live.clone());
        WaterApplication::merge_terminal_snapshot(&mut latest, attached.clone(), true);
        assert!(Arc::ptr_eq(latest.as_ref().unwrap(), &live));

        let mut fallback = None;
        WaterApplication::merge_terminal_snapshot(&mut fallback, attached, true);
        assert!(fallback.is_some());

        WaterApplication::merge_terminal_snapshot(&mut fallback, live.clone(), false);
        assert!(Arc::ptr_eq(fallback.as_ref().unwrap(), &live));
    }

    #[test]
    fn scrollback_budget_scaling_preserves_line_units_and_bounds() {
        let row_80 = crate::terminal::scrollback_row_bytes(80);
        let row_512 = crate::terminal::scrollback_row_bytes(512);
        let total_bytes = 2_000usize
            .saturating_mul(row_80)
            .saturating_add(500usize.saturating_mul(row_512));
        let budget = 64 * 1024;
        let cases = [(2_000, 2_000usize * row_80), (500, 500usize * row_512)];

        for (requested, selected_bytes) in cases {
            let lines = scale_scrollback_lines(requested, selected_bytes, total_bytes, budget);
            assert!((1..=requested).contains(&lines));
            assert!(lines <= crate::terminal::MAX_SCROLLBACK_LINES);
            assert!(lines.saturating_mul(selected_bytes / requested.max(1)) <= budget + row_512);
        }

        assert_eq!(scale_scrollback_lines(0, 0, 0, 0), 1);
        assert_eq!(
            scale_scrollback_lines(crate::terminal::MAX_SCROLLBACK_LINES * 2, 1, 1, usize::MAX,),
            crate::terminal::MAX_SCROLLBACK_LINES
        );
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
    fn native_titlebar_window_options_keep_the_window_freely_resizable() {
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
            "native buttons should keep AppKit's system-managed position"
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

/// Computes the per-terminal history limit from focus state and the aggregate
/// byte budget. Rows are weighted by their actual cell width so a wide terminal
/// cannot silently consume the whole GUI history budget.
fn terminal_scrollback_lines(
    snapshot: &ModelSnapshot,
    terminal_id: TerminalId,
    focused_lines: usize,
    inactive_lines: usize,
    max_total_bytes: usize,
) -> usize {
    let max_lines = crate::terminal::MAX_SCROLLBACK_LINES;
    let terminal_ids = terminal_ids_in_snapshot(snapshot);
    let mut total_bytes = 0usize;
    let mut selected_bytes = 0usize;
    let mut selected_lines = inactive_lines.max(1);
    for id in terminal_ids {
        let Some(size) = terminal_size_in_snapshot(snapshot, id) else {
            continue;
        };
        let lines = if terminal_is_focused(snapshot, id) {
            focused_lines.max(1)
        } else {
            inactive_lines.max(1)
        }
        .clamp(1, max_lines);
        let row_bytes = crate::terminal::scrollback_row_bytes(size.columns.max(1));
        let bytes = lines.saturating_mul(row_bytes);
        total_bytes = total_bytes.saturating_add(bytes);
        if id == terminal_id {
            selected_bytes = bytes;
            selected_lines = lines;
        }
    }
    if selected_bytes == 0 || total_bytes <= max_total_bytes.max(1) {
        return selected_lines;
    }
    scale_scrollback_lines(selected_lines, selected_bytes, total_bytes, max_total_bytes)
}

fn scale_scrollback_lines(
    selected_lines: usize,
    selected_bytes: usize,
    total_bytes: usize,
    max_total_bytes: usize,
) -> usize {
    let upper = selected_lines.clamp(1, crate::terminal::MAX_SCROLLBACK_LINES);
    if selected_bytes == 0 || total_bytes <= max_total_bytes.max(1) {
        return upper;
    }
    // selected_bytes/total_bytes is the terminal's share of the budget. It
    // must scale the requested *line count*, not be returned as a byte count.
    let scaled = upper
        .saturating_mul(max_total_bytes.max(1))
        .checked_div(total_bytes.max(1))
        .unwrap_or(1);
    scaled.clamp(1, upper)
}

fn terminal_is_focused(snapshot: &ModelSnapshot, terminal_id: TerminalId) -> bool {
    let Some(workspace_id) = snapshot.active_workspace else {
        return false;
    };
    let Some(workspace) = snapshot
        .workspaces
        .iter()
        .find(|workspace| workspace.id == workspace_id)
    else {
        return false;
    };
    let Some(tab_id) = workspace.active_tab else {
        return false;
    };
    let Some(tab) = workspace.tabs.iter().find(|tab| tab.id == tab_id) else {
        return false;
    };
    terminal_id_for_pane(&tab.tree, tab.active_pane) == Some(terminal_id)
}

fn terminal_id_for_pane(
    tree: &crate::app::model::PaneTreeDump,
    pane_id: crate::ids::PaneId,
) -> Option<TerminalId> {
    match tree {
        crate::app::model::PaneTreeDump::Leaf {
            pane_id: leaf_id,
            terminal,
            ..
        } if *leaf_id == pane_id => terminal
            .as_ref()
            .map(|terminal| terminal.summary.terminal_id),
        crate::app::model::PaneTreeDump::Split { first, second, .. } => {
            terminal_id_for_pane(first, pane_id).or_else(|| terminal_id_for_pane(second, pane_id))
        }
        _ => None,
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
