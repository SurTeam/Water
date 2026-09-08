use std::path::PathBuf;
use std::sync::Arc;
use std::sync::atomic::{AtomicU64, Ordering};
use std::time::Duration;

use serde::de::DeserializeOwned;
use thiserror::Error;

use crate::app::model::ModelSnapshot;
use crate::app::model::{MemoryStats, StateDump};
use crate::command::{AppCommand, DispatchError, OperationSnapshot};
use crate::event::AppEvent;
use crate::ids::{OperationId, TerminalId};
use crate::terminal::TerminalSnapshot;
use crate::ui::{UiControlClient, UiKeystrokeResult, UiScreenshot, UiSnapshot, UiWheelResult};

use super::protocol::{
    PROTOCOL_VERSION, PUSH_SNAPSHOT_METHOD, PUSH_UI_METHOD, RpcError, RpcMethod, RpcRequest,
    RpcResponse, ServerInfoResponse, SessionOpenResponse, WireMessage, read_frame, write_frame,
};

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

    pub fn terminal_contains(
        &self,
        terminal_id: TerminalId,
        text: impl Into<String>,
        timeout: Duration,
    ) -> Result<TerminalSnapshot, ControlClientError> {
        self.call(RpcMethod::TerminalContains {
            terminal_id,
            text: text.into(),
            timeout_ms: timeout.as_millis() as u64,
        })
    }

    pub fn wait_terminal_exit(
        &self,
        terminal_id: TerminalId,
        timeout: Duration,
    ) -> Result<TerminalSnapshot, ControlClientError> {
        self.call(RpcMethod::TerminalWaitExit {
            terminal_id,
            timeout_ms: timeout.as_millis() as u64,
        })
    }

    pub fn terminal_snapshot(
        &self,
        terminal_id: TerminalId,
    ) -> Result<TerminalSnapshot, ControlClientError> {
        self.call(RpcMethod::TerminalSnapshot { terminal_id })
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
        })
    }

    fn call<T: DeserializeOwned>(&self, method: RpcMethod) -> Result<T, ControlClientError> {
        let request_id = self.next_request_id.fetch_add(1, Ordering::Relaxed);
        let request = RpcRequest {
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
/// `waterctl`. The server handles connections concurrently, so an in-flight
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
                for command in enqueue_rx {
                    let request_id = next_id.fetch_add(1, std::sync::atomic::Ordering::Relaxed);
                    let request = RpcRequest {
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

    fn terminal_snapshot(
        &self,
        terminal_id: TerminalId,
    ) -> Result<TerminalSnapshot, DispatchError> {
        self.inner
            .terminal_snapshot(terminal_id)
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
/// (`waterctl ui.*`). The session owns two background threads: a reader that
/// decodes push frames and feeds the snapshot channel / UI control channel,
/// and a writer that serializes reply frames. The returned receiver is the
/// GUI snapshot stream; it ends when the server closes the session.
#[cfg(unix)]
pub fn connect_water_session(
    socket_path: impl Into<PathBuf>,
    ui_client: UiControlClient,
) -> Result<crate::app::SnapshotStream, ControlClientError> {
    let mut stream = std::os::unix::net::UnixStream::connect(socket_path.into())?;
    let request = RpcRequest {
        protocol_version: PROTOCOL_VERSION,
        request_id: 0,
        method: RpcMethod::SessionOpen {
            role: "gui".to_owned(),
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

    let (snapshot_tx, snapshot_rx) = std::sync::mpsc::channel();
    let (write_tx, write_rx) = std::sync::mpsc::channel::<WireMessage>();
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
    std::thread::Builder::new()
        .name("water-session-reader".to_owned())
        .spawn(move || session_reader_loop(stream, snapshot_tx, write_tx, ui_client))
        .ok();
    Ok(crate::app::SnapshotStream::new(snapshot_rx))
}

/// Fallback for servers that predate `session.open` (the monolithic build):
/// poll `state.dump` on a fixed cadence.
pub fn spawn_state_polling_fallback(
    client: std::sync::Arc<dyn crate::app::CommandTransport>,
) -> crate::app::SnapshotStream {
    let (tx, rx) = std::sync::mpsc::channel();
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
    crate::app::SnapshotStream::new(rx)
}

#[cfg(unix)]
fn session_reader_loop(
    mut stream: std::os::unix::net::UnixStream,
    snapshot_tx: std::sync::mpsc::Sender<ModelSnapshot>,
    write_tx: std::sync::mpsc::Sender<WireMessage>,
    ui_client: UiControlClient,
) {
    loop {
        let value: serde_json::Value = match read_frame(&mut stream) {
            Ok(value) => value,
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
        let message: WireMessage = match serde_json::from_value(value) {
            Ok(message) => message,
            Err(error) => {
                tracing::warn!(
                    target: "water::automation",
                    ?error,
                    "malformed water session frame"
                );
                continue;
            }
        };
        match message.method.as_deref() {
            Some(PUSH_SNAPSHOT_METHOD) => {
                let Some(params) = &message.params else {
                    continue;
                };
                let Ok(state) = serde_json::from_value::<ModelSnapshot>(params.clone()) else {
                    continue;
                };
                if snapshot_tx.send(state).is_err() {
                    break;
                }
            }
            Some(PUSH_UI_METHOD) => {
                let Some(params) = &message.params else {
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
    fn terminal_snapshot(
        &self,
        _terminal_id: TerminalId,
    ) -> Result<TerminalSnapshot, DispatchError> {
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
