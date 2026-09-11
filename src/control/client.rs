use std::path::PathBuf;
use std::sync::Arc;
use std::sync::atomic::{AtomicU64, Ordering};
use std::time::Duration;

use serde::Deserialize;
use serde::de::DeserializeOwned;
use thiserror::Error;

use crate::app::model::ModelSnapshot;
use crate::app::model::{MemoryStats, StateDump};
use crate::command::{AppCommand, DispatchError, OperationSnapshot, TerminalCommand};
use crate::event::AppEvent;
use crate::ids::{OperationId, TerminalId};
use crate::terminal::{
    DEFAULT_SCROLLBACK_LINES, TerminalReplay, TerminalSnapshot, TerminalStreamEvent,
    WireTerminalEvent, snapshot_from_replay,
};
use crate::ui::{UiControlClient, UiKeystrokeResult, UiScreenshot, UiSnapshot, UiWheelResult};

use super::protocol::{
    PROTOCOL_VERSION, PUSH_SNAPSHOT_METHOD, PUSH_TERMINAL_METHOD, PUSH_UI_METHOD, RpcError,
    RpcMethod, RpcRequest, RpcResponse, ServerInfoResponse, SessionOpenResponse, SessionWireFrame,
    TerminalAttachResponse, TerminalPush, WireMessage, read_frame, read_session_frame, write_frame,
};

/// At the shared 64 KiB event limit this bounds decoded client backlog to
/// roughly 4 MiB while leaving enough runway for ordinary scheduler jitter.
const TERMINAL_CLIENT_QUEUE_EVENTS: usize = 64;

#[derive(Debug, Error)]
pub enum ControlClientError {
    #[error("control socket I/O failed: {0}")]
    Io(#[from] std::io::Error),
    #[error("control protocol failed: {0}")]
    Protocol(#[from] serde_json::Error),
    #[error("remote error {code}: {message}")]
    Remote { code: String, message: String },
    #[error("model channel failed: {0}")]
    Dispatch(#[from] crate::command::DispatchError),
    #[error("control socket is not supported on this platform")]
    Unsupported,
}

#[cfg(unix)]
#[derive(Clone, Debug)]
pub struct ControlClient {
    socket_path: PathBuf,
    next_request_id: Arc<AtomicU64>,
}

#[cfg(unix)]
impl ControlClient {
    pub fn new(socket_path: impl Into<PathBuf>) -> Self {
        Self {
            socket_path: socket_path.into(),
            next_request_id: Arc::new(AtomicU64::new(1)),
        }
    }

    pub fn socket_path(&self) -> &std::path::Path {
        &self.socket_path
    }

    pub fn dispatch(&self, command: AppCommand) -> Result<OperationId, ControlClientError> {
        #[derive(serde::Deserialize)]
        struct Response {
            operation_id: OperationId,
        }
        let response: Response = self.call(RpcMethod::CommandDispatch { command })?;
        Ok(response.operation_id)
    }

    pub fn get_operation(
        &self,
        operation_id: OperationId,
    ) -> Result<Option<OperationSnapshot>, ControlClientError> {
        match self.call::<OperationSnapshot>(RpcMethod::OperationGet { operation_id }) {
            Ok(operation) => Ok(Some(operation)),
            Err(ControlClientError::Remote { code, .. }) if code == "OPERATION_NOT_FOUND" => {
                Ok(None)
            }
            Err(error) => Err(error),
        }
    }

    pub fn wait_operation(
        &self,
        operation_id: OperationId,
    ) -> Result<OperationSnapshot, ControlClientError> {
        self.call(RpcMethod::OperationWait { operation_id })
    }

    pub fn state_dump(&self) -> Result<StateDump, ControlClientError> {
        self.call(RpcMethod::StateDump)
    }

    pub fn events_since(&self, sequence: u64) -> Result<Vec<AppEvent>, ControlClientError> {
        self.call(RpcMethod::EventList {
            after_sequence: Some(sequence),
        })
    }

    pub fn memory_stats(&self) -> Result<MemoryStats, ControlClientError> {
        self.call(RpcMethod::DebugMemory)
    }

    pub fn metrics(&self) -> Result<serde_json::Value, ControlClientError> {
        self.call(RpcMethod::DebugMetrics)
    }

    pub fn terminal_contains(
        &self,
        terminal_id: TerminalId,
        text: impl Into<String>,
        timeout: Duration,
    ) -> Result<TerminalSnapshot, ControlClientError> {
        let replay: TerminalReplay = self.call(RpcMethod::TerminalContains {
            terminal_id,
            text: text.into(),
            timeout_ms: timeout.as_millis() as u64,
        })?;
        Ok(snapshot_from_replay(&replay, DEFAULT_SCROLLBACK_LINES))
    }

    pub fn wait_terminal_exit(
        &self,
        terminal_id: TerminalId,
        timeout: Duration,
    ) -> Result<TerminalSnapshot, ControlClientError> {
        let replay: TerminalReplay = self.call(RpcMethod::TerminalWaitExit {
            terminal_id,
            timeout_ms: timeout.as_millis() as u64,
        })?;
        Ok(snapshot_from_replay(&replay, DEFAULT_SCROLLBACK_LINES))
    }

    /// Bounded raw replay history; the caller replays the events through a
    /// temporary local terminal to inspect the screen.
    pub fn terminal_replay(
        &self,
        terminal_id: TerminalId,
    ) -> Result<TerminalReplay, ControlClientError> {
        self.call(RpcMethod::TerminalSnapshot { terminal_id })
    }

    /// Compatibility inspection API: raw history crosses the socket and is
    /// rendered in this calling process, never on the server.
    pub fn terminal_snapshot(
        &self,
        terminal_id: TerminalId,
    ) -> Result<TerminalSnapshot, ControlClientError> {
        let replay = self.terminal_replay(terminal_id)?;
        Ok(snapshot_from_replay(&replay, DEFAULT_SCROLLBACK_LINES))
    }

    pub fn ui_keystroke(
        &self,
        keystroke: impl Into<String>,
    ) -> Result<UiKeystrokeResult, ControlClientError> {
        self.call(RpcMethod::UiKeystroke {
            keystroke: keystroke.into(),
        })
    }

    pub fn ui_snapshot(&self) -> Result<UiSnapshot, ControlClientError> {
        self.call(RpcMethod::UiSnapshot)
    }

    pub fn ui_screenshot(
        &self,
        path: impl Into<PathBuf>,
    ) -> Result<UiScreenshot, ControlClientError> {
        self.call(RpcMethod::UiScreenshot {
            path: path.into().display().to_string(),
        })
    }

    pub fn ui_wheel(
        &self,
        x: f32,
        y: f32,
        dx: f32,
        dy: f32,
    ) -> Result<UiWheelResult, ControlClientError> {
        self.call(RpcMethod::UiWheel { x, y, dx, dy })
    }

    pub fn ping(&self) -> Result<(), ControlClientError> {
        let response: PingResponse = self.call(RpcMethod::Ping)?;
        if response.protocol_version != PROTOCOL_VERSION {
            return Err(ControlClientError::Remote {
                code: "PROTOCOL_VERSION_UNSUPPORTED".to_owned(),
                message: format!(
                    "server returned protocol version {}, expected {}",
                    response.protocol_version, PROTOCOL_VERSION
                ),
            });
        }
        Ok(())
    }

    pub fn server_info(&self) -> Result<ServerInfoResponse, ControlClientError> {
        self.call(RpcMethod::ServerInfo)
    }

    pub fn server_shutdown(&self) -> Result<bool, ControlClientError> {
        #[derive(serde::Deserialize)]
        struct Ack {
            #[serde(default)]
            ack: bool,
        }
        Ok(self.call::<Ack>(RpcMethod::ServerShutdown)?.ack)
    }

    pub fn session_open(&self) -> Result<SessionOpenResponse, ControlClientError> {
        self.call(RpcMethod::SessionOpen {
            role: "gui".to_owned(),
            compact_snapshots: true,
        })
    }

    fn call<T: DeserializeOwned>(&self, method: RpcMethod) -> Result<T, ControlClientError> {
        let request_id = self.next_request_id.fetch_add(1, Ordering::Relaxed);
        let request = RpcRequest {
            build_variant: crate::BUILD_VARIANT.to_owned(),
            protocol_version: PROTOCOL_VERSION,
            request_id,
            method,
        };
        let mut stream = std::os::unix::net::UnixStream::connect(&self.socket_path)?;
        write_frame(&mut stream, &request)?;
        let response: RpcResponse = read_frame(&mut stream)?;
        if let Some(error) = response.error {
            return Err(ControlClientError::Remote {
                code: error.code,
                message: error.message,
            });
        }
        let value = response.result.ok_or_else(|| ControlClientError::Remote {
            code: "EMPTY_RESPONSE".to_owned(),
            message: "control response did not contain a result".to_owned(),
        })?;
        Ok(serde_json::from_value(value)?)
    }
}

#[cfg(unix)]
#[derive(serde::Deserialize)]
struct PingResponse {
    protocol_version: u32,
}

/// Socket-backed [`CommandTransport`] for the GUI client.
///
/// `enqueue` (high-frequency terminal input) rides a persistent background
/// writer so the GPUI main thread never blocks on the model; every other
/// method is a one-shot request/response on its own connection, exactly like
/// `water ctl`. The server handles connections concurrently, so an in-flight
/// `operation.wait` never blocks input.
#[cfg(unix)]
#[derive(Clone, Debug)]
pub struct RemoteCommandClient {
    inner: ControlClient,
    enqueue_tx: std::sync::mpsc::Sender<AppCommand>,
}

#[cfg(unix)]
impl RemoteCommandClient {
    pub fn connect(socket_path: impl Into<PathBuf>) -> Result<Self, ControlClientError> {
        let socket_path = socket_path.into();
        // Verify the server answers before the GUI commits to it.
        let inner = ControlClient::new(socket_path.clone());
        inner.ping()?;
        let stream = std::os::unix::net::UnixStream::connect(&socket_path)?;
        let (enqueue_tx, enqueue_rx) = std::sync::mpsc::channel::<AppCommand>();
        let next_id = std::sync::Arc::new(std::sync::atomic::AtomicU64::new(10_000_000));
        std::thread::Builder::new()
            .name("water-cmd-queue".to_owned())
            .spawn(move || {
                let mut stream = stream;
                let mut deferred = None;
                while let Some(mut command) = deferred.take().or_else(|| enqueue_rx.recv().ok()) {
                    coalesce_queued_viewport_commands(&mut command, &enqueue_rx, &mut deferred);
                    let request_id = next_id.fetch_add(1, std::sync::atomic::Ordering::Relaxed);
                    let request = RpcRequest {
                        build_variant: crate::BUILD_VARIANT.to_owned(),
                        protocol_version: PROTOCOL_VERSION,
                        request_id,
                        method: RpcMethod::CommandDispatch { command },
                    };
                    // One round trip per command on a persistent connection;
                    // a slow model thread (for example a terminal wait) only
                    // delays queued input, it never blocks the GPUI thread.
                    if write_frame(&mut stream, &request).is_err() {
                        break;
                    }
                    let _response: RpcResponse = match read_frame(&mut stream) {
                        Ok(response) => response,
                        Err(_) => break,
                    };
                }
            })
            .ok();
        Ok(Self { inner, enqueue_tx })
    }
}

#[cfg(unix)]
fn viewport_command_key(
    command: &AppCommand,
) -> Option<(Option<TerminalId>, Option<crate::ids::PaneId>)> {
    let AppCommand::Terminal(TerminalCommand::SetViewportPosition {
        terminal_id,
        pane_id,
        ..
    }) = command
    else {
        return None;
    };
    Some((*terminal_id, *pane_id))
}

#[cfg(unix)]
fn same_viewport_command_stream(left: &AppCommand, right: &AppCommand) -> bool {
    viewport_command_key(left)
        .zip(viewport_command_key(right))
        .is_some_and(|(left, right)| left == right)
}

#[cfg(unix)]
fn coalesce_queued_viewport_commands(
    command: &mut AppCommand,
    receiver: &std::sync::mpsc::Receiver<AppCommand>,
    deferred: &mut Option<AppCommand>,
) {
    if viewport_command_key(command).is_none() {
        return;
    }
    while let Ok(next) = receiver.try_recv() {
        if same_viewport_command_stream(command, &next) {
            *command = next;
        } else {
            *deferred = Some(next);
            break;
        }
    }
}

#[cfg(unix)]
impl crate::app::CommandTransport for RemoteCommandClient {
    fn dispatch(&self, command: AppCommand) -> Result<OperationId, DispatchError> {
        self.inner.dispatch(command).map_err(into_dispatch_error)
    }

    fn enqueue(&self, command: AppCommand) -> Result<(), DispatchError> {
        self.enqueue_tx
            .send(command)
            .map_err(|_| DispatchError::ChannelClosed)
    }

    fn get_operation(&self, operation_id: OperationId) -> Option<OperationSnapshot> {
        self.inner.get_operation(operation_id).ok().flatten()
    }

    fn wait_operation(
        &self,
        operation_id: OperationId,
    ) -> Result<OperationSnapshot, DispatchError> {
        self.inner
            .wait_operation(operation_id)
            .map_err(into_dispatch_error)
    }

    fn state_dump(&self) -> Result<crate::app::model::ModelSnapshot, DispatchError> {
        self.inner.state_dump().map_err(into_dispatch_error)
    }

    fn memory_stats(&self) -> Result<crate::app::model::MemoryStats, DispatchError> {
        self.inner.memory_stats().map_err(into_dispatch_error)
    }

    fn events_since(&self, sequence: u64) -> Result<Vec<AppEvent>, DispatchError> {
        self.inner
            .events_since(sequence)
            .map_err(into_dispatch_error)
    }

    fn terminal_contains(
        &self,
        terminal_id: TerminalId,
        text: String,
        timeout: std::time::Duration,
    ) -> Result<TerminalSnapshot, DispatchError> {
        self.inner
            .terminal_contains(terminal_id, text, timeout)
            .map_err(into_dispatch_error)
    }

    fn wait_terminal_exit(
        &self,
        terminal_id: TerminalId,
        timeout: std::time::Duration,
    ) -> Result<TerminalSnapshot, DispatchError> {
        self.inner
            .wait_terminal_exit(terminal_id, timeout)
            .map_err(into_dispatch_error)
    }

    fn terminal_replay(&self, terminal_id: TerminalId) -> Result<TerminalReplay, DispatchError> {
        self.inner
            .terminal_replay(terminal_id)
            .map_err(into_dispatch_error)
    }
}

#[cfg(unix)]
fn into_dispatch_error(error: ControlClientError) -> DispatchError {
    use crate::command::{CommandError, DispatchError};
    match error {
        ControlClientError::Remote { code, message } => {
            DispatchError::Command(CommandError::new(code, message))
        }
        other => DispatchError::Command(CommandError::new("CONTROL_FAILED", other.to_string())),
    }
}

/// A long-lived GUI session on the control socket.
///
/// The server pushes revisioned `ModelSnapshot` frames (coalesced to the
/// latest per flush) and forwards UI automation requests from other clients
/// (`water ctl ui.*`). The session owns two background threads: a reader that
/// decodes push frames and feeds the snapshot channel / UI control channel /
/// terminal stream channels, and a writer that serializes reply frames.
///
/// Terminal attach: [`WaterSession::attach`] requests the ordered raw
/// replay history on the session connection; the server answers with the
/// replay and then streams the live tail as `push.terminal` frames, which
/// the reader routes into the per-terminal channel returned to the caller.
#[cfg(unix)]
pub struct WaterSession {
    next_request_id: std::sync::Arc<std::sync::atomic::AtomicU64>,
    attach_tx: std::sync::mpsc::Sender<AttachRequest>,
    /// Per-terminal live channels; shared with the session reader thread.
    /// A missing entry means the GUI is not consuming that terminal.
    terminal_channels: std::sync::Arc<
        std::sync::Mutex<
            std::collections::HashMap<
                TerminalId,
                std::sync::mpsc::SyncSender<QueuedLiveTerminalEvent>,
            >,
        >,
    >,
    snapshot_rx: crate::app::SnapshotStream,
}

struct AttachRequest {
    request_id: u64,
    terminal_id: TerminalId,
    reply: std::sync::mpsc::Sender<AttachReply>,
}

enum AttachReply {
    Response(TerminalAttachResponse),
    Failed(ControlClientError),
}

impl std::fmt::Debug for WaterSession {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("WaterSession").finish_non_exhaustive()
    }
}

/// Live terminal event stream: events buffered while the attach reply was in
/// flight (prefix) followed by the live channel. The split exists so the
/// session reader can deliver events with strict (blocking) backpressure
/// even before the attach call returns, without losing anything.
#[cfg(unix)]
pub struct TerminalEventStream {
    prefix: std::collections::VecDeque<QueuedLiveTerminalEvent>,
    rx: std::sync::mpsc::Receiver<QueuedLiveTerminalEvent>,
    tracked_size: crate::terminal::TerminalSize,
}

enum LiveTerminalEvent {
    Raw(TerminalStreamEvent),
    Wire(WireTerminalEvent),
}

struct QueuedLiveTerminalEvent {
    event: Option<LiveTerminalEvent>,
    bytes: usize,
}

impl QueuedLiveTerminalEvent {
    fn new(event: LiveTerminalEvent) -> Self {
        let bytes = match &event {
            LiveTerminalEvent::Raw(event) => event.output_bytes(),
            LiveTerminalEvent::Wire(WireTerminalEvent::Output { bytes, .. }) => bytes.len(),
            LiveTerminalEvent::Wire(_) => 0,
        };
        crate::metrics::terminal_client_queue_add(bytes);
        Self {
            event: Some(event),
            bytes,
        }
    }

    fn into_inner(mut self) -> LiveTerminalEvent {
        self.event.take().unwrap()
    }
}

impl Drop for QueuedLiveTerminalEvent {
    fn drop(&mut self) {
        crate::metrics::terminal_client_queue_remove(self.bytes);
    }
}

#[cfg(unix)]
impl TerminalEventStream {
    /// Blocks until the next ordered event; `Err` when the session ended or
    /// the terminal was detached. Callers treat `Err` as a resync signal
    /// (re-attach: the server replay ring still holds the history).
    pub fn recv(&mut self) -> Result<TerminalStreamEvent, std::sync::mpsc::RecvError> {
        loop {
            let event = match self.prefix.pop_front() {
                Some(event) => event,
                None => self.rx.recv()?,
            };
            if let Some(event) = self.decode(event) {
                return Ok(event);
            }
        }
    }

    /// Non-blocking drain used to batch all terminal events already decoded
    /// from one socket burst before advancing the local emulator.
    pub fn try_recv(&mut self) -> Result<TerminalStreamEvent, std::sync::mpsc::TryRecvError> {
        loop {
            let event = match self.prefix.pop_front() {
                Some(event) => event,
                None => self.rx.try_recv()?,
            };
            if let Some(event) = self.decode(event) {
                return Ok(event);
            }
        }
    }

    pub fn recv_timeout(
        &mut self,
        timeout: std::time::Duration,
    ) -> Result<TerminalStreamEvent, std::sync::mpsc::RecvTimeoutError> {
        let deadline = std::time::Instant::now() + timeout;
        loop {
            let event = match self.prefix.pop_front() {
                Some(event) => event,
                None => self
                    .rx
                    .recv_timeout(deadline.saturating_duration_since(std::time::Instant::now()))?,
            };
            if let Some(event) = self.decode(event) {
                return Ok(event);
            }
        }
    }

    fn decode(&mut self, event: QueuedLiveTerminalEvent) -> Option<TerminalStreamEvent> {
        let event = match event.into_inner() {
            LiveTerminalEvent::Raw(event) => event,
            LiveTerminalEvent::Wire(event) => {
                TerminalStreamEvent::from_wire(&event, self.tracked_size)?
            }
        };
        if let TerminalStreamEvent::Resize { size, .. } | TerminalStreamEvent::Output { size, .. } =
            &event
        {
            self.tracked_size = *size;
        }
        Some(event)
    }
}

impl std::fmt::Debug for TerminalEventStream {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("TerminalEventStream")
            .field("prefix", &self.prefix.len())
            .finish_non_exhaustive()
    }
}

#[cfg(unix)]
impl WaterSession {
    pub fn snapshot_stream(&self) -> &crate::app::SnapshotStream {
        &self.snapshot_rx
    }

    /// Detaches a terminal's live stream.
    pub fn detach(&self, terminal_id: TerminalId) {
        self.terminal_channels
            .lock()
            .expect("terminal channels poisoned")
            .remove(&terminal_id);
    }

    /// Attaches to a terminal's raw stream. Returns the ordered historical
    /// replay (oldest first, with its start geometry) plus the live event
    /// stream. Live events whose `seq` is at or below `last_seq` duplicate
    /// the replay tail and must be skipped. Attaching the same terminal
    /// again replaces the previous stream (resync).
    pub fn attach(
        &self,
        terminal_id: TerminalId,
    ) -> Result<(TerminalAttachResponse, TerminalEventStream), ControlClientError> {
        // Register the live channel before requesting the replay: the server
        // registers its subscriber before taking the ring snapshot, so every
        // event from this point on is either in the replay (deduplicated by
        // sequence) or buffered here while the reply is in flight.
        let (events_tx, events_rx) =
            std::sync::mpsc::sync_channel::<QueuedLiveTerminalEvent>(TERMINAL_CLIENT_QUEUE_EVENTS);
        self.terminal_channels
            .lock()
            .expect("terminal channels poisoned")
            .insert(terminal_id, events_tx);

        let request_id = self
            .next_request_id
            .fetch_add(1, std::sync::atomic::Ordering::Relaxed);
        let (reply_tx, reply_rx) = std::sync::mpsc::channel();
        self.attach_tx
            .send(AttachRequest {
                request_id,
                terminal_id,
                reply: reply_tx,
            })
            .map_err(|_| {
                ControlClientError::Protocol(serde_json::Error::io(std::io::Error::new(
                    std::io::ErrorKind::BrokenPipe,
                    "session closed",
                )))
            })?;
        // Drain events arriving while the reply is in flight into the
        // prefix buffer so the reader never blocks on an unconsumed channel
        // (which would deadlock the reply itself).
        let mut prefix = std::collections::VecDeque::new();
        let deadline = std::time::Instant::now() + std::time::Duration::from_secs(5);
        let response = loop {
            match reply_rx.try_recv() {
                Ok(reply) => break reply,
                Err(std::sync::mpsc::TryRecvError::Empty) => match events_rx.try_recv() {
                    Ok(event) => prefix.push_back(event),
                    Err(std::sync::mpsc::TryRecvError::Empty) => {
                        if std::time::Instant::now() >= deadline {
                            self.terminal_channels
                                .lock()
                                .expect("terminal channels poisoned")
                                .remove(&terminal_id);
                            return Err(ControlClientError::Remote {
                                code: "ATTACH_TIMEOUT".to_owned(),
                                message: "terminal attach timed out".to_owned(),
                            });
                        }
                        std::thread::sleep(std::time::Duration::from_micros(200));
                    }
                    Err(std::sync::mpsc::TryRecvError::Disconnected) => {}
                },
                Err(std::sync::mpsc::TryRecvError::Disconnected) => {
                    self.terminal_channels
                        .lock()
                        .expect("terminal channels poisoned")
                        .remove(&terminal_id);
                    return Err(ControlClientError::Remote {
                        code: "ATTACH_FAILED".to_owned(),
                        message: "session closed during attach".to_owned(),
                    });
                }
            }
        };
        match response {
            AttachReply::Response(response) => {
                let tracked_size = response.size;
                Ok((
                    response,
                    TerminalEventStream {
                        prefix,
                        rx: events_rx,
                        tracked_size,
                    },
                ))
            }
            AttachReply::Failed(error) => {
                self.terminal_channels
                    .lock()
                    .expect("terminal channels poisoned")
                    .remove(&terminal_id);
                Err(error)
            }
        }
    }
}

#[cfg(unix)]
pub fn connect_water_session(
    socket_path: impl Into<PathBuf>,
    ui_client: UiControlClient,
) -> Result<WaterSession, ControlClientError> {
    let mut stream = std::os::unix::net::UnixStream::connect(socket_path.into())?;
    let request = RpcRequest {
        build_variant: crate::BUILD_VARIANT.to_owned(),
        protocol_version: PROTOCOL_VERSION,
        request_id: 0,
        method: RpcMethod::SessionOpen {
            role: "gui".to_owned(),
            compact_snapshots: true,
        },
    };
    write_frame(&mut stream, &request)?;
    let response: RpcResponse = read_frame(&mut stream)?;
    if !response.ok {
        let Some(error) = response.error else {
            return Err(ControlClientError::Remote {
                code: "EMPTY_RESPONSE".to_owned(),
                message: "session.open failed without an error".to_owned(),
            });
        };
        return Err(ControlClientError::Remote {
            code: error.code,
            message: error.message,
        });
    }
    let Some(result) = response.result else {
        return Err(ControlClientError::Remote {
            code: "EMPTY_RESPONSE".to_owned(),
            message: "session.open returned no info".to_owned(),
        });
    };
    let _info: SessionOpenResponse = serde_json::from_value(result)?;

    let (snapshot_tx, snapshot_rx) = crate::app::runtime::snapshot_stream_channel();
    let (write_tx, write_rx) = std::sync::mpsc::channel::<WireMessage>();
    let (attach_tx, attach_rx) = std::sync::mpsc::channel::<AttachRequest>();
    let mut writer_stream = stream.try_clone()?;
    std::thread::Builder::new()
        .name("water-session-writer".to_owned())
        .spawn(move || {
            for message in write_rx {
                if write_frame(&mut writer_stream, &message).is_err() {
                    break;
                }
            }
        })
        .ok();
    let terminal_channels =
        std::sync::Arc::new(std::sync::Mutex::new(std::collections::HashMap::new()));
    let reader_channels = terminal_channels.clone();
    std::thread::Builder::new()
        .name("water-session-reader".to_owned())
        .spawn(move || {
            session_reader_loop(
                stream,
                snapshot_tx,
                write_tx,
                ui_client,
                attach_rx,
                reader_channels,
            )
        })
        .ok();
    Ok(WaterSession {
        next_request_id: std::sync::Arc::new(std::sync::atomic::AtomicU64::new(1)),
        attach_tx,
        terminal_channels,
        snapshot_rx,
    })
}

/// Fallback for servers that predate `session.open` (the monolithic build):
/// poll `state.dump` on a fixed cadence.
pub fn spawn_state_polling_fallback(
    client: std::sync::Arc<dyn crate::app::CommandTransport>,
) -> crate::app::SnapshotStream {
    let (tx, rx) = crate::app::runtime::snapshot_stream_channel();
    std::thread::Builder::new()
        .name("water-state-poll".to_owned())
        .spawn(move || {
            let mut last_revision = 0_u64;
            while let Ok(state) = client.state_dump() {
                if state.state_revision > last_revision {
                    last_revision = state.state_revision;
                    if tx.send(state).is_err() {
                        break;
                    }
                }
                std::thread::sleep(std::time::Duration::from_millis(33));
            }
        })
        .ok();
    rx
}

#[cfg(unix)]
fn session_reader_loop(
    mut stream: std::os::unix::net::UnixStream,
    snapshot_tx: crate::app::runtime::SnapshotStreamSender,
    write_tx: std::sync::mpsc::Sender<WireMessage>,
    ui_client: UiControlClient,
    attach_rx: std::sync::mpsc::Receiver<AttachRequest>,
    terminal_channels: std::sync::Arc<
        std::sync::Mutex<
            std::collections::HashMap<
                TerminalId,
                std::sync::mpsc::SyncSender<QueuedLiveTerminalEvent>,
            >,
        >,
    >,
) {
    #[derive(Deserialize)]
    struct SessionFrame {
        #[serde(default)]
        request_id: u64,
        #[serde(default)]
        method: Option<String>,
        #[serde(default)]
        ok: Option<bool>,
        #[serde(default)]
        params: Option<Box<serde_json::value::RawValue>>,
        #[serde(default)]
        result: Option<Box<serde_json::value::RawValue>>,
        #[serde(default)]
        error: Option<RpcError>,
    }

    // Pending attach replies, keyed by the request id we sent.
    let mut pending_attach: std::collections::HashMap<u64, std::sync::mpsc::Sender<AttachReply>> =
        std::collections::HashMap::new();
    // The 50ms read timeout lets attach requests be forwarded between frames
    // without a second thread; terminal bursts keep the timeout hot anyway.
    let _ = stream.set_read_timeout(Some(std::time::Duration::from_millis(50)));

    loop {
        // Forward attach requests onto the session connection.
        while let Ok(request) = attach_rx.try_recv() {
            pending_attach.insert(request.request_id, request.reply);
            let frame = WireMessage {
                build_variant: crate::BUILD_VARIANT.to_owned(),
                protocol_version: PROTOCOL_VERSION,
                request_id: request.request_id,
                method: Some("terminal.attach".to_owned()),
                params: Some(serde_json::json!({
                    "terminal_id": request.terminal_id,
                })),
                ok: None,
                result: None,
                error: None,
            };
            if write_tx.send(frame).is_err() {
                return;
            }
        }

        let frame = match read_session_frame(&mut stream) {
            Ok(frame) => frame,
            Err(error) if error.kind() == std::io::ErrorKind::WouldBlock => continue,
            Err(error) if error.kind() == std::io::ErrorKind::UnexpectedEof => break,
            Err(error) => {
                tracing::warn!(
                    target: "water::automation",
                    ?error,
                    "water session stream ended"
                );
                break;
            }
        };
        let message: SessionFrame = match frame {
            SessionWireFrame::Terminal { terminal_id, event } => {
                let sender = terminal_channels
                    .lock()
                    .expect("terminal channels poisoned")
                    .get(&terminal_id)
                    .cloned();
                if let Some(sender) = sender {
                    let _ =
                        sender.send(QueuedLiveTerminalEvent::new(LiveTerminalEvent::Raw(event)));
                }
                continue;
            }
            SessionWireFrame::Json(payload) => match serde_json::from_slice(&payload) {
                Ok(message) => message,
                Err(error) => {
                    tracing::warn!(
                        target: "water::automation",
                        ?error,
                        "invalid JSON frame on water session"
                    );
                    break;
                }
            },
        };

        // Attach responses and stray RPC replies: route to pending replies.
        if let Some(reply_tx) = pending_attach.remove(&message.request_id) {
            let reply = match (message.ok, &message.result, message.error) {
                (Some(true), Some(result), _) => {
                    match serde_json::from_str::<TerminalAttachResponse>(result.get()) {
                        Ok(response) => AttachReply::Response(response),
                        Err(error) => AttachReply::Failed(ControlClientError::Protocol(error)),
                    }
                }
                (_, _, Some(error)) => AttachReply::Failed(ControlClientError::Remote {
                    code: error.code,
                    message: error.message,
                }),
                _ => AttachReply::Failed(ControlClientError::Remote {
                    code: "ATTACH_FAILED".to_owned(),
                    message: "malformed attach reply".to_owned(),
                }),
            };
            let _ = reply_tx.send(reply);
            continue;
        }

        match message.method.as_deref() {
            Some(PUSH_TERMINAL_METHOD) => {
                let Some(params) = message.params.as_deref() else {
                    continue;
                };
                let Ok(push) = serde_json::from_str::<TerminalPush>(params.get()) else {
                    continue;
                };
                // Blocking send: a slow GUI backpressures the PTY through the
                // whole chain (socket -> server session writer -> worker
                // fanout) instead of dropping output. A terminal without a
                // registered consumer (not attached / detached) is skipped;
                // the server replay ring remains the resync source.
                let sender = terminal_channels
                    .lock()
                    .expect("terminal channels poisoned")
                    .get(&push.terminal_id)
                    .cloned();
                let Some(sender) = sender else {
                    continue;
                };
                let _ = sender.send(QueuedLiveTerminalEvent::new(LiveTerminalEvent::Wire(
                    push.event,
                )));
            }
            Some(PUSH_SNAPSHOT_METHOD) => {
                let Some(params) = message.params.as_deref() else {
                    continue;
                };
                let Ok(state) = serde_json::from_str::<ModelSnapshot>(params.get()) else {
                    continue;
                };
                if snapshot_tx.send(state).is_err() {
                    break;
                }
            }
            Some(PUSH_UI_METHOD) => {
                let Some(params) = message.params.as_deref() else {
                    continue;
                };
                let Ok(params) = serde_json::from_str::<serde_json::Value>(params.get()) else {
                    continue;
                };
                let method = params
                    .get("method")
                    .and_then(serde_json::Value::as_str)
                    .unwrap_or("");
                let inner = params.get("params").cloned().unwrap_or_default();
                let result: Result<serde_json::Value, RpcError> = match method {
                    "ui.keystroke" => ui_client
                        .dispatch_keystroke(
                            inner
                                .get("keystroke")
                                .and_then(serde_json::Value::as_str)
                                .unwrap_or("")
                                .to_owned(),
                        )
                        .map_err(string_error)
                        .and_then(serialize_value),
                    "ui.snapshot" => ui_client
                        .snapshot()
                        .map_err(string_error)
                        .and_then(serialize_value),
                    "ui.screenshot" => ui_client
                        .screenshot(
                            inner
                                .get("path")
                                .and_then(serde_json::Value::as_str)
                                .unwrap_or("")
                                .to_owned(),
                        )
                        .map_err(string_error)
                        .and_then(serialize_value),
                    "ui.wheel" => {
                        let point = |key: &str| inner.get(key).and_then(serde_json::Value::as_f64);
                        ui_client
                            .wheel(
                                (
                                    point("x").unwrap_or(0.) as f32,
                                    point("y").unwrap_or(0.) as f32,
                                ),
                                (
                                    point("dx").unwrap_or(0.) as f32,
                                    point("dy").unwrap_or(0.) as f32,
                                ),
                            )
                            .map_err(string_error)
                            .and_then(serialize_value)
                    }
                    other => Err(RpcError::new(
                        "UNKNOWN_UI_METHOD",
                        format!("unknown UI method {other}"),
                    )),
                };
                let reply = WireMessage::reply(message.request_id, result);
                if write_tx.send(reply).is_err() {
                    break;
                }
            }
            // Handshake responses and stray frames are not expected on the
            // session stream; ignore them.
            _ => {}
        }
    }
    tracing::info!(target: "water::automation", "water session closed");
}

fn string_error(error: String) -> RpcError {
    RpcError::new("UI_AUTOMATION_FAILED", error)
}

fn serialize_value<T: serde::Serialize>(value: T) -> Result<serde_json::Value, RpcError> {
    serde_json::to_value(value)
        .map_err(|error| RpcError::new("SERIALIZATION_FAILED", error.to_string()))
}

#[cfg(all(test, unix))]
mod tests {
    use super::*;
    use crate::ids::PaneId;

    fn viewport(target: i64) -> AppCommand {
        AppCommand::Terminal(TerminalCommand::SetViewportPosition {
            terminal_id: Some(TerminalId::new(3)),
            pane_id: Some(PaneId::new(5)),
            target,
        })
    }

    #[test]
    fn queued_viewport_requests_are_latest_wins_without_crossing_other_commands() {
        let (tx, rx) = std::sync::mpsc::channel();
        tx.send(viewport(4)).unwrap();
        tx.send(viewport(7)).unwrap();
        tx.send(AppCommand::Terminal(TerminalCommand::SendText {
            terminal_id: Some(TerminalId::new(3)),
            pane_id: Some(PaneId::new(5)),
            text: "x".to_owned(),
        }))
        .unwrap();
        tx.send(viewport(9)).unwrap();

        let mut command = viewport(1);
        let mut deferred = None;
        coalesce_queued_viewport_commands(&mut command, &rx, &mut deferred);

        assert_eq!(command, viewport(7));
        assert!(matches!(
            deferred,
            Some(AppCommand::Terminal(TerminalCommand::SendText { .. }))
        ));
        assert_eq!(rx.try_recv().unwrap(), viewport(9));
    }
}

#[cfg(not(unix))]
#[derive(Clone, Debug)]
pub struct ControlClient {
    socket_path: PathBuf,
}

#[cfg(not(unix))]
impl ControlClient {
    pub fn new(socket_path: impl Into<PathBuf>) -> Self {
        Self {
            socket_path: socket_path.into(),
        }
    }

    pub fn socket_path(&self) -> &std::path::Path {
        &self.socket_path
    }

    pub fn dispatch(&self, _command: AppCommand) -> Result<OperationId, ControlClientError> {
        Err(ControlClientError::Unsupported)
    }

    pub fn get_operation(
        &self,
        _operation_id: OperationId,
    ) -> Result<Option<OperationSnapshot>, ControlClientError> {
        Err(ControlClientError::Unsupported)
    }

    pub fn wait_operation(
        &self,
        _operation_id: OperationId,
    ) -> Result<OperationSnapshot, ControlClientError> {
        Err(ControlClientError::Unsupported)
    }

    pub fn state_dump(&self) -> Result<StateDump, ControlClientError> {
        Err(ControlClientError::Unsupported)
    }

    pub fn events_since(&self, _sequence: u64) -> Result<Vec<AppEvent>, ControlClientError> {
        Err(ControlClientError::Unsupported)
    }

    pub fn memory_stats(&self) -> Result<MemoryStats, ControlClientError> {
        Err(ControlClientError::Unsupported)
    }

    pub fn metrics(&self) -> Result<serde_json::Value, ControlClientError> {
        Err(ControlClientError::Unsupported)
    }

    pub fn terminal_contains(
        &self,
        _terminal_id: TerminalId,
        _text: impl Into<String>,
        _timeout: Duration,
    ) -> Result<TerminalSnapshot, ControlClientError> {
        Err(ControlClientError::Unsupported)
    }

    pub fn wait_terminal_exit(
        &self,
        _terminal_id: TerminalId,
        _timeout: Duration,
    ) -> Result<TerminalSnapshot, ControlClientError> {
        Err(ControlClientError::Unsupported)
    }

    pub fn terminal_snapshot(
        &self,
        _terminal_id: TerminalId,
    ) -> Result<TerminalSnapshot, ControlClientError> {
        Err(ControlClientError::Unsupported)
    }

    pub fn terminal_replay(
        &self,
        _terminal_id: TerminalId,
    ) -> Result<TerminalReplay, ControlClientError> {
        Err(ControlClientError::Unsupported)
    }

    pub fn ui_keystroke(
        &self,
        _keystroke: impl Into<String>,
    ) -> Result<UiKeystrokeResult, ControlClientError> {
        Err(ControlClientError::Unsupported)
    }

    pub fn ui_snapshot(&self) -> Result<UiSnapshot, ControlClientError> {
        Err(ControlClientError::Unsupported)
    }

    pub fn ui_screenshot(
        &self,
        _path: impl Into<PathBuf>,
    ) -> Result<UiScreenshot, ControlClientError> {
        Err(ControlClientError::Unsupported)
    }

    pub fn ui_wheel(
        &self,
        _x: f32,
        _y: f32,
        _dx: f32,
        _dy: f32,
    ) -> Result<UiWheelResult, ControlClientError> {
        Err(ControlClientError::Unsupported)
    }

    pub fn ping(&self) -> Result<(), ControlClientError> {
        Err(ControlClientError::Unsupported)
    }
}

#[cfg(not(unix))]
impl ControlClient {
    pub fn server_info(&self) -> Result<ServerInfoResponse, ControlClientError> {
        Err(ControlClientError::Unsupported)
    }

    pub fn server_shutdown(&self) -> Result<bool, ControlClientError> {
        Err(ControlClientError::Unsupported)
    }

    pub fn session_open(&self) -> Result<SessionOpenResponse, ControlClientError> {
        Err(ControlClientError::Unsupported)
    }
}

#[cfg(not(unix))]
#[derive(Clone, Debug)]
pub struct RemoteCommandClient;

#[cfg(not(unix))]
impl RemoteCommandClient {
    pub fn connect(_socket_path: impl Into<PathBuf>) -> Result<Self, ControlClientError> {
        Err(ControlClientError::Unsupported)
    }
}

#[cfg(not(unix))]
impl crate::app::CommandTransport for RemoteCommandClient {
    fn dispatch(&self, _command: AppCommand) -> Result<OperationId, DispatchError> {
        Err(DispatchError::ChannelClosed)
    }
    fn enqueue(&self, _command: AppCommand) -> Result<(), DispatchError> {
        Err(DispatchError::ChannelClosed)
    }
    fn get_operation(&self, _operation_id: OperationId) -> Option<OperationSnapshot> {
        None
    }
    fn wait_operation(
        &self,
        _operation_id: OperationId,
    ) -> Result<OperationSnapshot, DispatchError> {
        Err(DispatchError::ChannelClosed)
    }
    fn state_dump(&self) -> Result<ModelSnapshot, DispatchError> {
        Err(DispatchError::ChannelClosed)
    }
    fn memory_stats(&self) -> Result<crate::app::model::MemoryStats, DispatchError> {
        Err(DispatchError::ChannelClosed)
    }
    fn events_since(&self, _sequence: u64) -> Result<Vec<AppEvent>, DispatchError> {
        Err(DispatchError::ChannelClosed)
    }
    fn terminal_contains(
        &self,
        _terminal_id: TerminalId,
        _text: String,
        _timeout: std::time::Duration,
    ) -> Result<TerminalSnapshot, DispatchError> {
        Err(DispatchError::ChannelClosed)
    }
    fn wait_terminal_exit(
        &self,
        _terminal_id: TerminalId,
        _timeout: std::time::Duration,
    ) -> Result<TerminalSnapshot, DispatchError> {
        Err(DispatchError::ChannelClosed)
    }
    fn terminal_replay(&self, _terminal_id: TerminalId) -> Result<TerminalReplay, DispatchError> {
        Err(DispatchError::ChannelClosed)
    }
}

#[cfg(not(unix))]
pub fn connect_water_session(
    _socket_path: impl Into<PathBuf>,
    _ui_client: crate::ui::UiControlClient,
) -> Result<crate::app::SnapshotStream, ControlClientError> {
    Err(ControlClientError::Unsupported)
}
