use std::collections::{BTreeMap, HashMap};
use std::io;
use std::path::PathBuf;
use std::sync::{
    Arc,
    atomic::{AtomicBool, AtomicU64, Ordering},
    mpsc::{self, Receiver, RecvTimeoutError, Sender, TrySendError},
};
use std::time::Duration;

use serde_json::Value;

use crate::app::{CommandClient, ModelSnapshot, ModelSnapshotReceiver};
use crate::ui::UiControlClient;

use super::protocol::{
    PROTOCOL_VERSION, RpcError, RpcMethod, RpcRequest, RpcResponse, ServerInfoResponse,
    SessionOpenResponse, TerminalAttachResponse, WireMessage, read_frame, write_frame,
    write_snapshot_frame, write_terminal_frame,
};

/// How long the session writer waits for the first pending push before
/// idling. During a sustained output burst this bounds the snapshot push
/// cadence to roughly 60 fps while a quiet terminal still flushes promptly.
const SESSION_FLUSH_INTERVAL: Duration = Duration::from_millis(16);
/// How long a forwarded UI automation request waits for the GUI to answer.
const UI_FORWARD_TIMEOUT: Duration = Duration::from_secs(15);

pub struct ControlServer;

pub struct ControlServerHandle {
    stop: Arc<AtomicBool>,
    socket_path: PathBuf,
    join_handle: Option<JoinHandle<()>>,
}

impl ControlServerHandle {
    pub fn shutdown(&mut self) {
        if let Some(join_handle) = self.join_handle.take() {
            self.stop.store(true, Ordering::Release);
            #[cfg(unix)]
            {
                // Wake the blocking accept so the server can observe `stop` without
                // a polling timer in the application or socket thread.
                let _ = std::os::unix::net::UnixStream::connect(&self.socket_path);
            }
            let _ = join_handle.join();
            let _ = std::fs::remove_file(&self.socket_path);
        }
    }
}

impl Drop for ControlServerHandle {
    fn drop(&mut self) {
        self.shutdown();
    }
}

type JoinHandle<T> = std::thread::JoinHandle<T>;

/// Shared server state, one instance per control socket.
struct ServerState {
    client: CommandClient,
    /// In-process UI control delivery (embedded server). `None` on a
    /// detached server, where UI automation is forwarded to a connected GUI
    /// session through `push.ui`.
    ui_client: Option<UiControlClient>,
    socket_path: PathBuf,
    shutdown_tx: Option<Sender<()>>,
    sessions: std::sync::Mutex<BTreeMap<u64, Arc<Session>>>,
    next_session_id: AtomicU64,
    next_ui_id: AtomicU64,
    /// Replies to forwarded `push.ui` requests, keyed by the forwarded
    /// request id, until the GUI answers or the timeout fires.
    pending_ui_replies: std::sync::Mutex<HashMap<u64, Sender<WireMessage>>>,
}

impl ServerState {
    fn ui_session_count(&self) -> u32 {
        self.sessions.lock().expect("sessions poisoned").len() as u32
    }
}

/// One attached GUI connection: a push channel into the session writer plus
/// registration in the server state.
struct Session {
    id: u64,
    writer_tx: std::sync::mpsc::SyncSender<SessionWriterItem>,
    closed: AtomicBool,
    terminal_pumps: std::sync::Mutex<HashMap<crate::ids::TerminalId, Arc<AtomicBool>>>,
}

impl Session {
    fn new(id: u64, writer_tx: std::sync::mpsc::SyncSender<SessionWriterItem>) -> Self {
        Self {
            id,
            writer_tx,
            closed: AtomicBool::new(false),
            terminal_pumps: std::sync::Mutex::new(HashMap::new()),
        }
    }

    /// Queues a control/message push without silently dropping it. Terminal
    /// pumps use the interruptible variant below; ordinary control frames
    /// must remain ordered even when the writer queue is temporarily full.
    fn push(&self, item: SessionWriterItem) -> bool {
        self.push_blocking(item, None)
    }

    /// Model snapshots are latest-wins and may be skipped while the writer is
    /// busy; the next model revision remains authoritative.
    fn push_snapshot(&self, snapshot: ModelSnapshot) -> bool {
        self.is_open()
            && self
                .writer_tx
                .try_send(SessionWriterItem::Snapshot(snapshot))
                .is_ok()
    }

    fn is_open(&self) -> bool {
        !self.closed.load(Ordering::Acquire)
    }

    /// Queues an ordered frame while remaining interruptible by either the
    /// session closing or its terminal attachment being detached. A plain
    /// `SyncSender::send` can otherwise keep a pump (and all raw event Arcs it
    /// owns) alive forever behind a slow socket.
    fn push_blocking(&self, mut item: SessionWriterItem, cancel: Option<&AtomicBool>) -> bool {
        loop {
            if !self.is_open() || cancel.is_some_and(|cancel| cancel.load(Ordering::Acquire)) {
                return false;
            }
            match self.writer_tx.try_send(item) {
                Ok(()) => return true,
                Err(TrySendError::Disconnected(_)) => return false,
                Err(TrySendError::Full(next)) => {
                    item = next;
                    std::thread::park_timeout(Duration::from_millis(1));
                }
            }
        }
    }

    fn register_terminal_pump(&self, terminal_id: crate::ids::TerminalId) -> Arc<AtomicBool> {
        let cancel = Arc::new(AtomicBool::new(false));
        let mut pumps = self.terminal_pumps.lock().expect("terminal pumps poisoned");
        if let Some(previous) = pumps.insert(terminal_id, cancel.clone()) {
            previous.store(true, Ordering::Release);
        }
        cancel
    }

    fn detach_terminal(&self, terminal_id: crate::ids::TerminalId) {
        if let Some(cancel) = self
            .terminal_pumps
            .lock()
            .expect("terminal pumps poisoned")
            .remove(&terminal_id)
        {
            cancel.store(true, Ordering::Release);
        }
    }

    fn finish_terminal_pump(&self, terminal_id: crate::ids::TerminalId, cancel: &Arc<AtomicBool>) {
        let mut pumps = self.terminal_pumps.lock().expect("terminal pumps poisoned");
        if pumps
            .get(&terminal_id)
            .is_some_and(|current| Arc::ptr_eq(current, cancel))
        {
            pumps.remove(&terminal_id);
        }
    }

    fn close(&self) {
        self.closed.store(true, Ordering::Release);
        let pumps =
            std::mem::take(&mut *self.terminal_pumps.lock().expect("terminal pumps poisoned"));
        for cancel in pumps.into_values() {
            cancel.store(true, Ordering::Release);
        }
    }
}

enum SessionWriterItem {
    /// Revisioned model state. Multiple pending snapshots coalesce to the
    /// latest; only the newest grid ever reaches the wire.
    Snapshot(ModelSnapshot),
    /// Any other frame (session response, forwarded `push.ui`, ...).
    Message(WireMessage),
    /// Ordered live terminal data-plane frame. Kept raw until the writer so
    /// the hot path never expands bytes through Base64/JSON.
    Terminal(QueuedTerminalEvent),
}

struct QueuedTerminalEvent {
    terminal_id: crate::ids::TerminalId,
    event: crate::terminal::TerminalStreamEvent,
    bytes: usize,
}

impl QueuedTerminalEvent {
    fn new(
        terminal_id: crate::ids::TerminalId,
        event: crate::terminal::TerminalStreamEvent,
    ) -> Self {
        let bytes = event.output_bytes();
        crate::metrics::terminal_server_queue_add(bytes);
        Self {
            terminal_id,
            event,
            bytes,
        }
    }
}

impl Drop for QueuedTerminalEvent {
    fn drop(&mut self) {
        crate::metrics::terminal_server_queue_remove(self.bytes);
    }
}

#[cfg(unix)]
impl ControlServer {
    /// Starts the control socket. `ui_client` delivers UI automation
    /// requests in-process (embedded server); otherwise they are forwarded
    /// to a connected GUI session. `snapshot_rx` drives the `push.snapshot`
    /// stream for GUI sessions when present.
    pub fn start(
        socket_path: PathBuf,
        client: CommandClient,
        ui_client: Option<UiControlClient>,
        snapshot_rx: Option<ModelSnapshotReceiver>,
    ) -> io::Result<(ControlServerHandle, Receiver<()>)> {
        if socket_path.exists() {
            use std::os::unix::fs::FileTypeExt;
            if !std::fs::symlink_metadata(&socket_path)?
                .file_type()
                .is_socket()
            {
                return Err(io::Error::new(
                    io::ErrorKind::AddrInUse,
                    "socket path is not a Unix socket",
                ));
            }
            match std::os::unix::net::UnixStream::connect(&socket_path) {
                Ok(_) => {
                    return Err(io::Error::new(
                        io::ErrorKind::AddrInUse,
                        "a server already owns this socket",
                    ));
                }
                Err(error) if error.kind() == io::ErrorKind::ConnectionRefused => {
                    std::fs::remove_file(&socket_path)?;
                }
                Err(error) => return Err(error),
            }
        }
        if let Some(parent) = socket_path.parent()
            && !parent.as_os_str().is_empty()
        {
            std::fs::create_dir_all(parent)?;
        }
        let listener = std::os::unix::net::UnixListener::bind(&socket_path)?;
        let stop = Arc::new(AtomicBool::new(false));
        let server_stop = stop.clone();
        let server_path = socket_path.clone();
        let (shutdown_tx, shutdown_rx) = mpsc::channel();
        let state = Arc::new(ServerState {
            client,
            ui_client,
            socket_path: server_path.clone(),
            shutdown_tx: Some(shutdown_tx),
            sessions: std::sync::Mutex::new(BTreeMap::new()),
            next_session_id: AtomicU64::new(1),
            next_ui_id: AtomicU64::new(1),
            pending_ui_replies: std::sync::Mutex::new(HashMap::new()),
        });
        let stop_for_broadcaster = stop.clone();
        let state_for_broadcaster = state.clone();
        let join_handle = std::thread::Builder::new()
            .name("water-control".to_owned())
            .spawn(move || {
                tracing::info!(
                    target: "water::automation",
                    socket = %server_path.display(),
                    "control socket listening"
                );
                let state = state;
                // One handler thread per connection: a long-lived GUI session
                // must never starve short ctl RPCs (the legacy serial
                // accept loop blocked every other client for the lifetime of
                // a single connection).
                while !server_stop.load(Ordering::Acquire) {
                    match listener.accept() {
                        Ok((stream, _)) => {
                            if server_stop.load(Ordering::Acquire) {
                                break;
                            }
                            let state = state.clone();
                            std::thread::Builder::new()
                                .name("water-control-conn".to_owned())
                                .spawn(move || handle_connection(stream, state))
                                .ok();
                        }
                        Err(error) => {
                            if server_stop.load(Ordering::Acquire) {
                                break;
                            }
                            tracing::warn!(
                                target: "water::automation",
                                ?error,
                                "control socket accept failed"
                            );
                            break;
                        }
                    }
                }
                let _ = std::fs::remove_file(&server_path);
            })?;

        // Snapshot broadcaster: the model thread publishes at most one
        // pending revision, so each recv here is already the latest state at
        // that moment. The per-session writer coalesces bursts further.
        if let Some(snapshot_rx) = snapshot_rx {
            let state = state_for_broadcaster;
            let stop = stop_for_broadcaster;
            std::thread::Builder::new()
                .name("water-snapshot-broadcaster".to_owned())
                .spawn(move || {
                    while !stop.load(Ordering::Acquire) {
                        match snapshot_rx.recv() {
                            Ok(snapshot) => {
                                for session in
                                    state.sessions.lock().expect("sessions poisoned").values()
                                {
                                    if !session.push_snapshot(snapshot.clone()) {
                                        break;
                                    }
                                }
                            }
                            Err(_) => break,
                        }
                    }
                })
                .ok();
        }

        Ok((
            ControlServerHandle {
                stop,
                socket_path,
                join_handle: Some(join_handle),
            },
            shutdown_rx,
        ))
    }
}

#[cfg(not(unix))]
impl ControlServer {
    pub fn start(
        _socket_path: PathBuf,
        _client: CommandClient,
        _ui_client: Option<UiControlClient>,
        _snapshot_rx: Option<ModelSnapshotReceiver>,
    ) -> io::Result<(ControlServerHandle, Receiver<()>)> {
        let (_shutdown_tx, shutdown_rx) = mpsc::channel();
        let _ = shutdown_rx;
        Err(io::Error::new(
            io::ErrorKind::Unsupported,
            "Unix domain sockets are not available on this platform yet",
        ))
    }
}

#[cfg(unix)]
fn handle_connection(stream: std::os::unix::net::UnixStream, state: Arc<ServerState>) {
    let mut stream = stream;
    let mut session: Option<Arc<Session>> = None;
    loop {
        let value: Value = match read_frame(&mut stream) {
            Ok(value) => value,
            Err(error) if error.kind() == io::ErrorKind::UnexpectedEof => break,
            Err(error) => {
                let response =
                    RpcResponse::failure(0, RpcError::new("INVALID_REQUEST", error.to_string()));
                if write_frame(&mut stream, &response).is_err() {
                    break;
                }
                break;
            }
        };
        let message: WireMessage = match serde_json::from_value(value) {
            Ok(message) => message,
            Err(error) => {
                let response =
                    RpcResponse::failure(0, RpcError::new("INVALID_REQUEST", error.to_string()));
                if write_frame(&mut stream, &response).is_err() {
                    break;
                }
                break;
            }
        };

        if message.is_reply() {
            // Reply to a forwarded `push.ui` request from a GUI session.
            if let Some(reply_tx) = state
                .pending_ui_replies
                .lock()
                .expect("pending ui replies poisoned")
                .remove(&message.request_id)
            {
                let _ = reply_tx.send(message);
            }
            continue;
        }

        if !message.is_request() {
            continue;
        }

        let request: RpcRequest = match serde_json::to_value(&message)
            .ok()
            .and_then(|value| serde_json::from_value(value).ok())
        {
            Some(request) => request,
            None => {
                let response = RpcResponse::failure(
                    message.request_id,
                    RpcError::new("INVALID_REQUEST", "could not parse request frame"),
                );
                send_response(&mut stream, session.as_ref(), &response);
                continue;
            }
        };
        let (response, pending_session, pending_terminal) =
            handle_request(request, &state, session.as_ref());
        if let Some(pending) = pending_session {
            let writer_stream = match stream.try_clone() {
                Ok(writer_stream) => writer_stream,
                Err(error) => {
                    let failure = RpcResponse::failure(
                        response.request_id,
                        RpcError::new("SESSION_FAILED", error.to_string()),
                    );
                    let _ = write_frame(&mut stream, &failure);
                    break;
                }
            };
            std::thread::Builder::new()
                .name("water-session-writer".to_owned())
                .spawn(move || {
                    session_writer(writer_stream, pending.writer_rx, pending.compact_snapshots)
                })
                .ok();
            session = Some(pending.session);
            // `open_session` already queued the response frame on the
            // session writer ahead of the initial snapshot; sending it again
            // here would duplicate the handshake.
            continue;
        }
        send_response(&mut stream, session.as_ref(), &response);
        if let Some(pending) = pending_terminal {
            spawn_terminal_pump(
                pending.session,
                pending.terminal_id,
                pending.cancel,
                pending.events,
            );
        }
    }
    // Connection closed: drop the session (writer channel closes, pushes are
    // dropped) and unblock any pending UI forward.
    if let Some(session) = session {
        session.close();
        state
            .sessions
            .lock()
            .expect("sessions poisoned")
            .remove(&session.id);
    }
}

/// Writes a response directly (before a session exists) or through the
/// session writer (after `session.open`, when all writes must be serialized
/// with pushes).
#[cfg(unix)]
fn send_response(
    stream: &mut std::os::unix::net::UnixStream,
    session: Option<&Arc<Session>>,
    response: &RpcResponse,
) {
    match session {
        Some(session) => {
            // Replies share the ordered writer with terminal pushes. Blocking
            // avoids both loss and concurrent direct writes that could splice
            // two length-prefixed frames together.
            let _ = session.push_blocking(
                SessionWriterItem::Message(WireMessage::from_response(response)),
                None,
            );
        }
        None => {
            let _ = write_frame(stream, response);
        }
    }
}

#[cfg(unix)]
fn handle_request(
    request: RpcRequest,
    state: &ServerState,
    session: Option<&Arc<Session>>,
) -> (
    RpcResponse,
    Option<PendingSession>,
    Option<PendingTerminalPump>,
) {
    if request.protocol_version != PROTOCOL_VERSION || request.build_variant != crate::BUILD_VARIANT
    {
        return (
            RpcResponse::failure(
                request.request_id,
                RpcError::new(
                    "INCOMPATIBLE_SERVER",
                    "protocol version and dev/release variant must match",
                ),
            ),
            None,
            None,
        );
    }
    if let RpcMethod::SessionOpen {
        compact_snapshots, ..
    } = request.method
    {
        let (response, pending) = open_session(request.request_id, state, compact_snapshots);
        return (response, pending, None);
    }
    let mut pending_terminal = None;
    let response = handle_regular(request, state, session, &mut pending_terminal);
    (response, None, pending_terminal)
}

#[cfg(unix)]
struct PendingTerminalPump {
    session: Arc<Session>,
    terminal_id: crate::ids::TerminalId,
    events: std::sync::mpsc::Receiver<crate::terminal::TerminalStreamEvent>,
    cancel: Arc<AtomicBool>,
}

#[cfg(unix)]
fn handle_regular(
    request: RpcRequest,
    state: &ServerState,
    session: Option<&Arc<Session>>,
    pending_terminal: &mut Option<PendingTerminalPump>,
) -> RpcResponse {
    if request.protocol_version != PROTOCOL_VERSION {
        return RpcResponse::failure(
            request.request_id,
            RpcError::new(
                "PROTOCOL_VERSION_UNSUPPORTED",
                format!(
                    "requested protocol version {}, supported {}",
                    request.protocol_version, PROTOCOL_VERSION
                ),
            ),
        );
    }

    match request.method {
        RpcMethod::CommandDispatch { command } => match state.client.dispatch(command) {
            Ok(operation_id) => {
                RpcResponse::success(request.request_id, &OperationIdResponse { operation_id })
            }
            Err(error) => RpcResponse::failure(request.request_id, dispatch_error(error)),
        },
        RpcMethod::OperationGet { operation_id } => {
            match state.client.get_operation(operation_id) {
                Some(operation) => RpcResponse::success(request.request_id, &operation),
                None => RpcResponse::failure(
                    request.request_id,
                    RpcError::new(
                        "OPERATION_NOT_FOUND",
                        format!("operation {operation_id} does not exist"),
                    ),
                ),
            }
        }
        RpcMethod::OperationWait { operation_id } => {
            match state.client.wait_operation(operation_id) {
                Ok(operation) => RpcResponse::success(request.request_id, &operation),
                Err(error) => RpcResponse::failure(request.request_id, dispatch_error(error)),
            }
        }
        RpcMethod::StateDump => match state.client.state_dump() {
            Ok(state) => RpcResponse::success(request.request_id, &state),
            Err(error) => RpcResponse::failure(request.request_id, dispatch_error(error)),
        },
        RpcMethod::EventList { after_sequence } => {
            match state.client.events_since(after_sequence.unwrap_or(0)) {
                Ok(events) => RpcResponse::success(request.request_id, &events),
                Err(error) => RpcResponse::failure(request.request_id, dispatch_error(error)),
            }
        }
        RpcMethod::DebugMemory => match state.client.memory_stats() {
            Ok(memory) => RpcResponse::success(request.request_id, &memory),
            Err(error) => RpcResponse::failure(request.request_id, dispatch_error(error)),
        },
        RpcMethod::DebugMetrics => {
            RpcResponse::success(request.request_id, &crate::metrics::snapshot())
        }
        RpcMethod::TerminalContains {
            terminal_id,
            text,
            timeout_ms,
        } => match state.client.terminal_contains_replay(
            terminal_id,
            text,
            std::time::Duration::from_millis(timeout_ms),
        ) {
            Ok(snapshot) => RpcResponse::success(request.request_id, &snapshot),
            Err(error) => RpcResponse::failure(request.request_id, dispatch_error(error)),
        },
        RpcMethod::TerminalWaitExit {
            terminal_id,
            timeout_ms,
        } => match state
            .client
            .wait_terminal_exit_replay(terminal_id, std::time::Duration::from_millis(timeout_ms))
        {
            Ok(snapshot) => RpcResponse::success(request.request_id, &snapshot),
            Err(error) => RpcResponse::failure(request.request_id, dispatch_error(error)),
        },
        RpcMethod::TerminalSnapshot { terminal_id } => {
            match state.client.terminal_replay(terminal_id) {
                Ok(replay) => RpcResponse::success(request.request_id, &replay),
                Err(error) => RpcResponse::failure(request.request_id, dispatch_error(error)),
            }
        }
        RpcMethod::TerminalAttach { terminal_id } => {
            match state.client.terminal_attach(terminal_id) {
                Ok(attachment) => {
                    let response = RpcResponse::success(
                        request.request_id,
                        &TerminalAttachResponse {
                            terminal_id,
                            first_seq: attachment.replay.first().map(|event| event.seq()),
                            last_seq: attachment.last_seq,
                            size: attachment
                                .replay
                                .iter()
                                .find_map(|event| event.size())
                                .unwrap_or_default(),
                            replay: attachment
                                .replay
                                .into_iter()
                                .map(|event| event.to_wire())
                                .collect(),
                        },
                    );
                    // Live tail: one pump thread per attachment feeds the
                    // session writer in strict order. Without a session
                    // there is no push channel; the replay alone answers
                    // one-shot callers.
                    if let Some(session) = session {
                        let cancel = session.register_terminal_pump(terminal_id);
                        *pending_terminal = Some(PendingTerminalPump {
                            session: session.clone(),
                            terminal_id,
                            events: attachment.events,
                            cancel,
                        });
                    }
                    response
                }
                Err(error) => RpcResponse::failure(request.request_id, dispatch_error(error)),
            }
        }
        RpcMethod::TerminalDetach { terminal_id } => {
            if let Some(session) = session {
                session.detach_terminal(terminal_id);
            }
            RpcResponse::success(request.request_id, &serde_json::json!({}))
        }
        RpcMethod::UiKeystroke { keystroke } => ui_request(
            request.request_id,
            state,
            |ui_client| ui_client.dispatch_keystroke(keystroke.clone()),
            || {
                ui_method_frame(
                    "ui.keystroke",
                    &serde_json::json!({ "keystroke": keystroke }),
                )
            },
        ),
        RpcMethod::UiSnapshot => ui_request(
            request.request_id,
            state,
            |ui_client| ui_client.snapshot(),
            || ui_method_frame("ui.snapshot", &serde_json::json!({})),
        ),
        RpcMethod::UiScreenshot { path } => ui_request(
            request.request_id,
            state,
            |ui_client| ui_client.screenshot(path.clone()),
            || ui_method_frame("ui.screenshot", &serde_json::json!({ "path": path })),
        ),
        RpcMethod::UiWheel { x, y, dx, dy } => ui_request(
            request.request_id,
            state,
            |ui_client| ui_client.wheel((x, y), (dx, dy)),
            || {
                ui_method_frame(
                    "ui.wheel",
                    &serde_json::json!({ "x": x, "y": y, "dx": dx, "dy": dy }),
                )
            },
        ),
        RpcMethod::SessionOpen { .. } => unreachable!("handled by handle_request"),
        RpcMethod::ServerInfo => RpcResponse::success(
            request.request_id,
            &ServerInfoResponse {
                build_variant: crate::BUILD_VARIANT.to_owned(),
                server_pid: std::process::id(),
                protocol_version: PROTOCOL_VERSION,
                server_version: env!("CARGO_PKG_VERSION").to_owned(),
                socket_path: state.socket_path.display().to_string(),
                ui_sessions: state.ui_session_count(),
            },
        ),
        RpcMethod::ServerShutdown => {
            let accepted = state
                .shutdown_tx
                .as_ref()
                .is_some_and(|tx| tx.send(()).is_ok());
            RpcResponse::success(request.request_id, &serde_json::json!({ "ack": accepted }))
        }
        RpcMethod::Ping => RpcResponse::success(
            request.request_id,
            &PingResponse {
                protocol_version: PROTOCOL_VERSION,
            },
        ),
    }
}

/// A session whose writer thread has not been spawned yet. The connection
/// handler spawns it once `session.open` is answered, so the first frame the
/// client reads after the response is the session's own traffic.
struct PendingSession {
    session: Arc<Session>,
    writer_rx: std::sync::mpsc::Receiver<SessionWriterItem>,
    compact_snapshots: bool,
}

/// Registers a long-lived GUI session: creates the session, queues the
/// `session.open` response followed by the current state onto the session
/// writer, and returns the pending writer for the connection handler to
/// spawn.
#[cfg(unix)]
fn open_session(
    request_id: u64,
    state: &ServerState,
    compact_snapshots: bool,
) -> (RpcResponse, Option<PendingSession>) {
    let session_id = state.next_session_id.fetch_add(1, Ordering::Relaxed);
    let (writer_tx, writer_rx) = mpsc::sync_channel::<SessionWriterItem>(32);
    let session = Arc::new(Session::new(session_id, writer_tx));
    state
        .sessions
        .lock()
        .expect("sessions poisoned")
        .insert(session_id, session.clone());

    // The response frame first, then the current state, so a slow client can
    // render immediately without waiting for the next model revision.
    let response = RpcResponse::success(
        request_id,
        &SessionOpenResponse {
            server_pid: std::process::id(),
            protocol_version: PROTOCOL_VERSION,
            server_version: env!("CARGO_PKG_VERSION").to_owned(),
            socket_path: state.socket_path.display().to_string(),
        },
    );
    session.push(SessionWriterItem::Message(WireMessage::from_response(
        &response,
    )));
    if let Ok(current) = state.client.state_dump() {
        session.push(SessionWriterItem::Snapshot(current));
    }
    (
        response,
        Some(PendingSession {
            session,
            writer_rx,
            compact_snapshots,
        }),
    )
}

/// Dispatches a UI automation request: in-process when the server embedded
/// the UI (detached == false), otherwise forwarded to the first connected
/// GUI session.
#[cfg(unix)]
fn ui_method_frame(name: &str, params: &Value) -> Value {
    serde_json::json!({ "method": name, "params": params })
}

fn ui_request<T: serde::Serialize>(
    request_id: u64,
    state: &ServerState,
    direct: impl FnOnce(&UiControlClient) -> Result<T, String>,
    params: impl FnOnce() -> Value,
) -> RpcResponse {
    match &state.ui_client {
        Some(ui_client) => match direct(ui_client) {
            Ok(result) => RpcResponse::success(request_id, &result),
            Err(error) => {
                RpcResponse::failure(request_id, RpcError::new("UI_AUTOMATION_FAILED", error))
            }
        },
        None => match forward_ui_request(request_id, state, params) {
            Some(response) => response,
            None => RpcResponse::failure(
                request_id,
                RpcError::new("UI_AUTOMATION_UNAVAILABLE", "no GUI session is connected"),
            ),
        },
    }
}

/// Forwards a UI automation request to the first connected GUI session and
/// waits for its reply. Returns `None` when no session is attached.
#[cfg(unix)]
fn forward_ui_request(
    request_id: u64,
    state: &ServerState,
    params: impl FnOnce() -> Value,
) -> Option<RpcResponse> {
    let session = state
        .sessions
        .lock()
        .expect("sessions poisoned")
        .values()
        .next()
        .cloned()?;
    let ui_id = state.next_ui_id.fetch_add(1, Ordering::Relaxed);
    let (reply_tx, reply_rx) = mpsc::channel();
    state
        .pending_ui_replies
        .lock()
        .expect("pending ui replies poisoned")
        .insert(ui_id, reply_tx);
    let frame = WireMessage::push_ui(ui_id, &params());
    let push_ok = session.push(SessionWriterItem::Message(frame));
    if !push_ok {
        state
            .pending_ui_replies
            .lock()
            .expect("pending ui replies poisoned")
            .remove(&ui_id);
        return None;
    }
    let reply = match reply_rx.recv_timeout(UI_FORWARD_TIMEOUT) {
        Ok(reply) => reply,
        Err(_) => {
            state
                .pending_ui_replies
                .lock()
                .expect("pending ui replies poisoned")
                .remove(&ui_id);
            return Some(RpcResponse::failure(
                request_id,
                RpcError::new(
                    "UI_AUTOMATION_TIMEOUT",
                    "timed out waiting for the GUI to answer",
                ),
            ));
        }
    };
    Some(match (reply.ok, reply.result, reply.error) {
        (Some(true), Some(result), _) => RpcResponse {
            protocol_version: PROTOCOL_VERSION,
            request_id,
            ok: true,
            result: Some(result),
            error: None,
        },
        (Some(false), _, error) => RpcResponse::failure(
            request_id,
            error.unwrap_or_else(|| RpcError::new("UI_AUTOMATION_FAILED", "no error detail")),
        ),
        _ => RpcResponse::failure(
            request_id,
            RpcError::new("UI_AUTOMATION_FAILED", "malformed GUI reply"),
        ),
    })
}

/// Pumps one live terminal attachment into the session writer until the
/// worker channel disconnects (terminal exited/removed) or the session
/// writer stops. Ordering is strict: one `push.terminal` frame per event, in
/// sequence order, never coalesced.
#[cfg(unix)]
fn spawn_terminal_pump(
    session: Arc<Session>,
    terminal_id: crate::ids::TerminalId,
    cancel: Arc<AtomicBool>,
    events: Receiver<crate::terminal::TerminalStreamEvent>,
) {
    std::thread::Builder::new()
        .name("water-terminal-pump".to_owned())
        .spawn(move || {
            loop {
                if cancel.load(Ordering::Acquire) || !session.is_open() {
                    break;
                }
                let event = match events.recv_timeout(Duration::from_millis(50)) {
                    Ok(event) => event,
                    Err(RecvTimeoutError::Timeout) => continue,
                    Err(RecvTimeoutError::Disconnected) => break,
                };
                if !session.push_blocking(
                    SessionWriterItem::Terminal(QueuedTerminalEvent::new(terminal_id, event)),
                    Some(&cancel),
                ) {
                    break;
                }
            }
            session.finish_terminal_pump(terminal_id, &cancel);
        })
        .ok();
}

#[cfg(unix)]
fn session_writer(
    mut stream: std::os::unix::net::UnixStream,
    rx: Receiver<SessionWriterItem>,
    compact_snapshots: bool,
) {
    // Messages (handshakes, forwarded UI requests, ordered `push.terminal`
    // frames) are written one at a time in arrival order. Do not drain the
    // bounded channel into an unbounded VecDeque: that would make a slow
    // socket retain every PTY event despite the upstream backpressure.
    // Snapshots are latest-wins and may wait behind ordered frames.
    let mut pending_snapshot: Option<ModelSnapshot> = None;
    loop {
        match rx.recv_timeout(SESSION_FLUSH_INTERVAL) {
            Ok(SessionWriterItem::Snapshot(snapshot)) => {
                pending_snapshot = Some(snapshot);
            }
            Ok(SessionWriterItem::Message(message)) => {
                if write_frame(&mut stream, &message).is_err() {
                    return;
                }
            }
            Ok(SessionWriterItem::Terminal(queued)) => {
                if write_terminal_frame(&mut stream, queued.terminal_id, &queued.event).is_err() {
                    return;
                }
            }
            Err(RecvTimeoutError::Disconnected) => {
                if let Some(snapshot) = pending_snapshot.take() {
                    let _ = write_snapshot_frame(&mut stream, &snapshot, compact_snapshots);
                }
                return;
            }
            Err(RecvTimeoutError::Timeout) => {
                if let Some(snapshot) = pending_snapshot.take() {
                    if write_snapshot_frame(&mut stream, &snapshot, compact_snapshots).is_err() {
                        return;
                    }
                    crate::metrics::inc(crate::metrics::model_snapshot_pushes());
                }
            }
        }
    }
}

#[derive(serde::Serialize)]
struct OperationIdResponse {
    operation_id: crate::ids::OperationId,
}

#[derive(serde::Serialize)]
struct PingResponse {
    protocol_version: u32,
}

fn dispatch_error(error: crate::command::DispatchError) -> RpcError {
    match error {
        crate::command::DispatchError::Command(error) => error.into(),
        crate::command::DispatchError::ChannelClosed => {
            RpcError::new("MODEL_UNAVAILABLE", "model thread stopped")
        }
    }
}

#[cfg(test)]
mod tests {
    use super::super::protocol::PUSH_SNAPSHOT_METHOD;
    use super::*;

    #[test]
    fn wire_message_is_a_superset_of_the_legacy_frame_shapes() {
        // Legacy request frame (standalone-CLI era / GUI).
        let legacy_request: Value = serde_json::json!({
            "protocol_version": 1,
            "request_id": 7,
            "method": "terminal.contains",
            "params": { "terminal_id": 1, "text": "x", "timeout_ms": 10 }
        });
        let message: WireMessage = serde_json::from_value(legacy_request.clone()).unwrap();
        assert!(message.is_request());
        assert!(!message.is_reply());
        let request: RpcRequest =
            serde_json::from_value(serde_json::to_value(&message).unwrap()).unwrap();
        assert!(matches!(request.method, RpcMethod::TerminalContains { .. }));

        // Legacy response frame.
        let legacy_response: Value = serde_json::json!({
            "protocol_version": 1,
            "request_id": 7,
            "ok": true,
            "result": { "state_revision": 1 }
        });
        let message: WireMessage = serde_json::from_value(legacy_response).unwrap();
        assert!(message.is_reply());

        // Push frames round-trip through the same frame codec.
        let state = crate::app::StateDump {
            state_revision: 3,
            workspace: None,
            workspaces: Vec::new(),
            active_workspace: None,
            focused_pane: None,
            agents: Vec::new(),
        };
        let push = WireMessage::push_snapshot(&state);
        let round: WireMessage =
            serde_json::from_value(serde_json::to_value(&push).unwrap()).unwrap();
        assert_eq!(round.method.as_deref(), Some(PUSH_SNAPSHOT_METHOD));
        assert_eq!(round.request_id, 0);

        let ui_push = WireMessage::push_ui(
            42,
            &serde_json::json!({ "method": "ui.keystroke", "params": { "keystroke": "cmd-t" } }),
        );
        let round: WireMessage =
            serde_json::from_value(serde_json::to_value(&ui_push).unwrap()).unwrap();
        assert_eq!(round.method.as_deref(), Some("push.ui"));
        assert_eq!(round.request_id, 42);
        assert!(!round.is_request());

        let reply = WireMessage::reply(42, Ok(serde_json::json!({ "handled": true })));
        assert!(reply.is_reply());
    }

    #[test]
    fn snapshot_items_coalesce_to_the_latest_revision() {
        let mut snapshot: Option<ModelSnapshot> = None;
        for revision in [1_u64, 2, 3] {
            let state = crate::app::StateDump {
                state_revision: revision,
                workspace: None,
                workspaces: Vec::new(),
                active_workspace: None,
                focused_pane: None,
                agents: Vec::new(),
            };
            snapshot = Some(state);
        }
        assert_eq!(snapshot.as_ref().unwrap().state_revision, 3);
    }

    #[test]
    fn terminal_messages_keep_arrival_order() {
        let mut messages = Vec::new();
        for seq in 1..=4u64 {
            let event = crate::terminal::TerminalStreamEvent::Resize {
                seq,
                size: crate::terminal::TerminalSize::new(80, 24),
            };
            messages.push(QueuedTerminalEvent::new(
                crate::ids::TerminalId::new(1),
                event,
            ));
        }
        assert_eq!(messages.len(), 4);
        let seqs: Vec<u64> = messages.iter().map(|queued| queued.event.seq()).collect();
        assert_eq!(seqs, vec![1, 2, 3, 4]);
    }

    #[test]
    fn terminal_pump_can_be_cancelled_while_writer_queue_is_full() {
        let (writer_tx, _writer_rx) = mpsc::sync_channel(1);
        let session = Session::new(1, writer_tx);
        assert!(session.push(SessionWriterItem::Message(WireMessage::reply(
            1,
            Ok(serde_json::json!({})),
        ))));
        let cancel = Arc::new(AtomicBool::new(true));
        assert!(!session.push_blocking(
            SessionWriterItem::Message(WireMessage::reply(2, Ok(serde_json::json!({})))),
            Some(&cancel),
        ));
    }

    #[test]
    fn detaching_a_terminal_cancels_its_registered_pump() {
        let (writer_tx, _writer_rx) = mpsc::sync_channel(1);
        let session = Session::new(1, writer_tx);
        let cancel = session.register_terminal_pump(crate::ids::TerminalId::new(7));
        assert!(!cancel.load(Ordering::Acquire));
        session.detach_terminal(crate::ids::TerminalId::new(7));
        assert!(cancel.load(Ordering::Acquire));
    }
}
