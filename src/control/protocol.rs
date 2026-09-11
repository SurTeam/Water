use std::io::{self, Read, Write};

use serde::de::DeserializeOwned;
use serde::{Deserialize, Serialize};
use serde_json::Value;

use crate::app::model::{AgentDump, MemoryStats, StateDump, WorkspaceDump};
use crate::command::{AppCommand, CommandError, OperationSnapshot};
use crate::event::AppEvent;
use crate::ids::{OperationId, PaneId, TerminalId, WorkspaceId};
use crate::terminal::{TerminalReplay, TerminalSize, TerminalStreamEvent, WireTerminalEvent};
use crate::ui::{UiKeystrokeResult, UiScreenshot, UiSnapshot, UiWheelResult};

/// Version 4 carries full 128-bit terminal IDs in binary frames. Control messages
/// and bounded attach replay remain JSON for compatibility and debuggability.
pub const PROTOCOL_VERSION: u32 = 4;
const MAX_FRAME_BYTES: usize = 16 * 1024 * 1024;
const TERMINAL_FRAME_PREFIX: &[u8; 4] = b"\0WT4";
const TERMINAL_FRAME_OUTPUT: u8 = 1;
const TERMINAL_FRAME_RESIZE: u8 = 2;
const TERMINAL_FRAME_EXIT: u8 = 3;

/// Push frames the server sends on a GUI session connection.
///
/// `push.snapshot` carries the next revisioned model state (at most one per
/// flush, intermediates coalesced). `push.ui` forwards a UI automation
/// request (from `water ctl`) to the connected GUI; the GUI answers with a
/// plain reply frame carrying the same request id.
pub const PUSH_SNAPSHOT_METHOD: &str = "push.snapshot";
pub const PUSH_UI_METHOD: &str = "push.ui";
/// Ordered raw terminal stream event (see `TerminalStreamEvent`). These
/// frames must reach the client in sequence order; the session writer never
/// coalesces or reorders them.
pub const PUSH_TERMINAL_METHOD: &str = "push.terminal";

/// A single length-prefixed JSON frame, shared by both directions.
///
/// - client -> server request: `method` (+ optional `params`) set, `ok` unset
/// - server -> client response: `ok` set, `request_id` matches the request
/// - server -> client push: `method` is a `push.*` name, `ok` unset
/// - client -> server reply to a forwarded `push.ui`: `ok` set with the push's
///   request id
///
/// The frame is a superset of the legacy `RpcRequest`/`RpcResponse` JSON
/// shapes, so pre-split clients and servers keep working: old requests parse
/// into `RpcRequest` directly, and old responses parse into `RpcResponse`.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct WireMessage {
    #[serde(default)]
    pub build_variant: String,
    pub protocol_version: u32,
    pub request_id: u64,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub method: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub params: Option<Value>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub ok: Option<bool>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub result: Option<Value>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub error: Option<RpcError>,
}

impl WireMessage {
    pub fn push_snapshot(state: &StateDump) -> Self {
        Self {
            build_variant: crate::BUILD_VARIANT.to_owned(),
            protocol_version: PROTOCOL_VERSION,
            request_id: 0,
            method: Some(PUSH_SNAPSHOT_METHOD.to_owned()),
            params: serde_json::to_value(state).ok(),
            ok: None,
            result: None,
            error: None,
        }
    }

    /// A `push.terminal` frame carrying one ordered raw terminal event.
    pub fn push_terminal(terminal_id: TerminalId, event: &WireTerminalEvent) -> Self {
        Self {
            build_variant: crate::BUILD_VARIANT.to_owned(),
            protocol_version: PROTOCOL_VERSION,
            request_id: 0,
            method: Some(PUSH_TERMINAL_METHOD.to_owned()),
            params: Some(serde_json::json!({
                "terminal_id": terminal_id,
                "event": event,
            })),
            ok: None,
            result: None,
            error: None,
        }
    }

    /// A `push.ui` frame. `inner` is the serialized `RpcMethod` value
    /// (i.e. `{"method": "ui.keystroke", "params": {...}}`), so the GUI can
    /// dispatch the request and mirror the params in its reply.
    pub fn push_ui(request_id: u64, inner: &Value) -> Self {
        Self {
            build_variant: crate::BUILD_VARIANT.to_owned(),
            protocol_version: PROTOCOL_VERSION,
            request_id,
            method: Some(PUSH_UI_METHOD.to_owned()),
            params: Some(inner.clone()),
            ok: None,
            result: None,
            error: None,
        }
    }

    pub fn reply(request_id: u64, result: Result<Value, RpcError>) -> Self {
        match result {
            Ok(value) => Self {
                build_variant: crate::BUILD_VARIANT.to_owned(),
                protocol_version: PROTOCOL_VERSION,
                request_id,
                method: None,
                params: None,
                ok: Some(true),
                result: Some(value),
                error: None,
            },
            Err(error) => Self {
                build_variant: crate::BUILD_VARIANT.to_owned(),
                protocol_version: PROTOCOL_VERSION,
                request_id,
                method: None,
                params: None,
                ok: Some(false),
                result: None,
                error: Some(error),
            },
        }
    }

    /// Converts a legacy response into its wire-frame equivalent (used when
    /// responses must be serialized on a session connection alongside
    /// pushes).
    pub fn from_response(response: &RpcResponse) -> Self {
        Self::from(response)
    }

    /// A request frame (client -> server), i.e. a method without an `ok` and
    /// not a server push.
    pub fn is_request(&self) -> bool {
        self.ok.is_none()
            && self
                .method
                .as_deref()
                .is_some_and(|method| !method.starts_with("push."))
    }

    /// A reply/response frame (`ok` set), from either direction.
    pub fn is_reply(&self) -> bool {
        self.ok.is_some()
    }
}

impl From<&RpcResponse> for WireMessage {
    fn from(response: &RpcResponse) -> Self {
        Self {
            build_variant: crate::BUILD_VARIANT.to_owned(),
            protocol_version: response.protocol_version,
            request_id: response.request_id,
            method: None,
            params: None,
            ok: Some(response.ok),
            result: response.result.clone(),
            error: response.error.clone(),
        }
    }
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct RpcRequest {
    #[serde(default)]
    pub build_variant: String,
    pub protocol_version: u32,
    pub request_id: u64,
    #[serde(flatten)]
    pub method: RpcMethod,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(tag = "method", content = "params")]
pub enum RpcMethod {
    #[serde(rename = "command.dispatch")]
    CommandDispatch { command: AppCommand },
    #[serde(rename = "operation.get")]
    OperationGet { operation_id: OperationId },
    #[serde(rename = "operation.wait")]
    OperationWait { operation_id: OperationId },
    #[serde(rename = "state.dump")]
    StateDump,
    #[serde(rename = "event.list")]
    EventList { after_sequence: Option<u64> },
    #[serde(rename = "debug.memory")]
    DebugMemory,
    #[serde(rename = "debug.metrics")]
    DebugMetrics,
    #[serde(rename = "terminal.contains")]
    TerminalContains {
        terminal_id: TerminalId,
        text: String,
        timeout_ms: u64,
    },
    #[serde(rename = "terminal.wait_exit")]
    TerminalWaitExit {
        terminal_id: TerminalId,
        timeout_ms: u64,
    },
    /// Bounded raw replay history; the caller replays it through a
    /// temporary local terminal to inspect the screen.
    #[serde(rename = "terminal.replay", alias = "terminal.snapshot")]
    TerminalSnapshot { terminal_id: TerminalId },
    /// Live raw-stream attachment: the response carries the ordered
    /// historical replay, then `push.terminal` frames stream the live tail
    /// on a GUI session connection.
    #[serde(rename = "terminal.attach")]
    TerminalAttach { terminal_id: TerminalId },
    #[serde(rename = "ui.keystroke")]
    UiKeystroke { keystroke: String },
    #[serde(rename = "ui.snapshot")]
    UiSnapshot,
    #[serde(rename = "ui.screenshot")]
    UiScreenshot { path: String },
    #[serde(rename = "ui.wheel")]
    UiWheel { x: f32, y: f32, dx: f32, dy: f32 },
    /// Long-lived GUI session registration. Once open, the server pushes
    /// `push.snapshot` frames on this connection and forwards UI automation
    /// requests as `push.ui`.
    #[serde(rename = "session.open")]
    SessionOpen {
        role: String,
        /// Modern clients reconstruct the compatibility `workspace` alias
        /// from `workspaces`, avoiding a second full copy of the active
        /// terminal grid in every pushed snapshot.
        #[serde(default)]
        compact_snapshots: bool,
    },
    /// Server process metadata for attach/attach diagnostics.
    #[serde(rename = "server.info")]
    ServerInfo,
    /// Requested graceful server shutdown (PTYs terminate with the model).
    #[serde(rename = "server.shutdown")]
    ServerShutdown,
    #[serde(rename = "ping")]
    Ping,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct RpcResponse {
    pub protocol_version: u32,
    pub request_id: u64,
    pub ok: bool,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub result: Option<Value>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub error: Option<RpcError>,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct RpcError {
    pub code: String,
    pub message: String,
}

impl RpcError {
    pub fn new(code: impl Into<String>, message: impl Into<String>) -> Self {
        Self {
            code: code.into(),
            message: message.into(),
        }
    }
}

impl From<CommandError> for RpcError {
    fn from(error: CommandError) -> Self {
        Self {
            code: error.code,
            message: error.message,
        }
    }
}

impl RpcResponse {
    pub fn success<T: Serialize>(request_id: u64, value: &T) -> Self {
        match serde_json::to_value(value) {
            Ok(result) => Self {
                protocol_version: PROTOCOL_VERSION,
                request_id,
                ok: true,
                result: Some(result),
                error: None,
            },
            Err(error) => Self::failure(
                request_id,
                RpcError::new("SERIALIZATION_FAILED", error.to_string()),
            ),
        }
    }

    pub fn failure(request_id: u64, error: RpcError) -> Self {
        Self {
            protocol_version: PROTOCOL_VERSION,
            request_id,
            ok: false,
            result: None,
            error: Some(error),
        }
    }
}

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(untagged)]
pub enum RpcResult {
    OperationId { operation_id: OperationId },
    Operation(OperationSnapshot),
    State(StateDump),
    Events(Vec<AppEvent>),
    Memory(MemoryStats),
    Metrics(Value),
    TerminalReplay(TerminalReplay),
    UiKeystroke(UiKeystrokeResult),
    Ui(UiSnapshot),
    Screenshot(UiScreenshot),
    UiWheel(UiWheelResult),
    Pong { protocol_version: u32 },
    SessionOpen(SessionOpenResponse),
    ServerInfo(ServerInfoResponse),
    ServerShutdown { ack: bool },
}

/// Result of `terminal.attach`. `first_seq` is `None` when the ring is
/// empty; `size` is the replay-start geometry (the dimensions to use before
/// applying the events).
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct TerminalAttachResponse {
    pub terminal_id: TerminalId,
    pub first_seq: Option<u64>,
    pub last_seq: u64,
    pub size: crate::terminal::TerminalSize,
    pub replay: Vec<WireTerminalEvent>,
}

/// Result of `session.open`.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct SessionOpenResponse {
    pub server_pid: u32,
    pub protocol_version: u32,
    #[serde(default)]
    pub server_version: String,
    pub socket_path: String,
}

/// Result of `server.info`.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ServerInfoResponse {
    #[serde(default)]
    pub build_variant: String,
    pub server_pid: u32,
    pub protocol_version: u32,
    #[serde(default)]
    pub server_version: String,
    pub socket_path: String,
    pub ui_sessions: u32,
}

/// Parsed `push.terminal` frame.
#[derive(Debug, Clone, Deserialize)]
pub struct TerminalPush {
    pub terminal_id: TerminalId,
    pub event: WireTerminalEvent,
}

/// A frame read from the long-lived GUI session. JSON remains the control
/// plane; terminal events use a compact binary data plane without Base64.
pub(crate) enum SessionWireFrame {
    Json(Vec<u8>),
    Terminal {
        terminal_id: TerminalId,
        event: TerminalStreamEvent,
    },
}

pub fn write_frame<W, T>(writer: &mut W, message: &T) -> io::Result<()>
where
    W: Write,
    T: Serialize,
{
    let payload = serde_json::to_vec(message).map_err(io::Error::other)?;
    write_frame_payload(writer, &payload)
}

/// Writes a control-plane snapshot push without first expanding the model
/// into an intermediate `serde_json::Value`.
pub fn write_snapshot_frame<W>(writer: &mut W, state: &StateDump, compact: bool) -> io::Result<()>
where
    W: Write,
{
    #[derive(Serialize)]
    struct SnapshotPush<'a, T> {
        protocol_version: u32,
        request_id: u64,
        method: &'static str,
        params: &'a T,
    }

    #[derive(Serialize)]
    struct CompactStateDump<'a> {
        state_revision: u64,
        workspaces: &'a [WorkspaceDump],
        active_workspace: Option<WorkspaceId>,
        focused_pane: Option<PaneId>,
        agents: &'a [AgentDump],
    }

    let payload = if compact {
        let compact_state = CompactStateDump {
            state_revision: state.state_revision,
            workspaces: &state.workspaces,
            active_workspace: state.active_workspace,
            focused_pane: state.focused_pane,
            agents: &state.agents,
        };
        serde_json::to_vec(&SnapshotPush {
            protocol_version: PROTOCOL_VERSION,
            request_id: 0,
            method: PUSH_SNAPSHOT_METHOD,
            params: &compact_state,
        })
    } else {
        serde_json::to_vec(&SnapshotPush {
            protocol_version: PROTOCOL_VERSION,
            request_id: 0,
            method: PUSH_SNAPSHOT_METHOD,
            params: state,
        })
    }
    .map_err(io::Error::other)?;
    write_frame_payload(writer, &payload)
}

fn write_frame_payload<W>(writer: &mut W, payload: &[u8]) -> io::Result<()>
where
    W: Write,
{
    if payload.len() > MAX_FRAME_BYTES {
        return Err(io::Error::new(
            io::ErrorKind::InvalidData,
            "control message exceeds maximum frame size",
        ));
    }
    let length = u32::try_from(payload.len()).map_err(|_| {
        io::Error::new(
            io::ErrorKind::InvalidData,
            "control message length overflow",
        )
    })?;
    writer.write_all(&length.to_be_bytes())?;
    writer.write_all(payload)?;
    writer.flush()
}

/// Writes one terminal event without JSON serialization or Base64 expansion.
/// The existing outer length prefix is retained so terminal and control
/// frames can share one ordered Unix stream.
pub(crate) fn write_terminal_frame<W>(
    writer: &mut W,
    terminal_id: TerminalId,
    event: &TerminalStreamEvent,
) -> io::Result<()>
where
    W: Write,
{
    let output_len = event.output_bytes();
    let payload_len = match event {
        TerminalStreamEvent::Output { .. } | TerminalStreamEvent::Resize { .. } => {
            TERMINAL_FRAME_PREFIX.len() + 1 + 16 + 8 + 2 + 2 + output_len
        }
        TerminalStreamEvent::Exit { .. } => TERMINAL_FRAME_PREFIX.len() + 1 + 16 + 8 + 1 + 4,
    };
    if payload_len > MAX_FRAME_BYTES {
        return Err(io::Error::new(
            io::ErrorKind::InvalidData,
            "terminal frame exceeds maximum frame size",
        ));
    }
    let mut payload = Vec::with_capacity(payload_len);
    payload.extend_from_slice(TERMINAL_FRAME_PREFIX);
    match event {
        TerminalStreamEvent::Output {
            seq, size, bytes, ..
        } => {
            payload.push(TERMINAL_FRAME_OUTPUT);
            payload.extend_from_slice(&terminal_id.get().to_be_bytes());
            payload.extend_from_slice(&seq.to_be_bytes());
            payload.extend_from_slice(&(size.columns as u16).to_be_bytes());
            payload.extend_from_slice(&(size.lines as u16).to_be_bytes());
            payload.extend_from_slice(bytes);
        }
        TerminalStreamEvent::Resize { seq, size } => {
            payload.push(TERMINAL_FRAME_RESIZE);
            payload.extend_from_slice(&terminal_id.get().to_be_bytes());
            payload.extend_from_slice(&seq.to_be_bytes());
            payload.extend_from_slice(&(size.columns as u16).to_be_bytes());
            payload.extend_from_slice(&(size.lines as u16).to_be_bytes());
        }
        TerminalStreamEvent::Exit { seq, code } => {
            payload.push(TERMINAL_FRAME_EXIT);
            payload.extend_from_slice(&terminal_id.get().to_be_bytes());
            payload.extend_from_slice(&seq.to_be_bytes());
            payload.push(u8::from(code.is_some()));
            payload.extend_from_slice(&code.unwrap_or_default().to_be_bytes());
        }
    }
    write_frame_payload(writer, &payload)
}

pub(crate) fn read_session_frame<R>(reader: &mut R) -> io::Result<SessionWireFrame>
where
    R: Read,
{
    let length = read_frame_length(reader)?;
    if length < TERMINAL_FRAME_PREFIX.len() {
        let mut payload = vec![0_u8; length];
        reader.read_exact(&mut payload)?;
        return Ok(SessionWireFrame::Json(payload));
    }

    let mut prefix = [0_u8; 4];
    reader.read_exact(&mut prefix)?;
    if &prefix != TERMINAL_FRAME_PREFIX {
        let mut payload = vec![0_u8; length];
        payload[..4].copy_from_slice(&prefix);
        reader.read_exact(&mut payload[4..])?;
        return Ok(SessionWireFrame::Json(payload));
    }

    if length < 29 {
        return Err(io::Error::new(
            io::ErrorKind::InvalidData,
            "terminal frame header is truncated",
        ));
    }
    let mut header = [0_u8; 25];
    reader.read_exact(&mut header)?;
    let kind = header[0];
    let terminal_id = TerminalId::from_u128(u128::from_be_bytes(header[1..17].try_into().unwrap()));
    let seq = u64::from_be_bytes(header[17..25].try_into().unwrap());
    let remaining = length - 29;
    let event = match kind {
        TERMINAL_FRAME_OUTPUT | TERMINAL_FRAME_RESIZE => {
            if remaining < 4 {
                return Err(io::Error::new(
                    io::ErrorKind::InvalidData,
                    "terminal geometry is truncated",
                ));
            }
            let mut geometry = [0_u8; 4];
            reader.read_exact(&mut geometry)?;
            let size = TerminalSize::new(
                u16::from_be_bytes(geometry[..2].try_into().unwrap()) as usize,
                u16::from_be_bytes(geometry[2..].try_into().unwrap()) as usize,
            );
            if kind == TERMINAL_FRAME_RESIZE {
                if remaining != 4 {
                    return Err(io::Error::new(
                        io::ErrorKind::InvalidData,
                        "terminal resize frame has trailing bytes",
                    ));
                }
                TerminalStreamEvent::Resize { seq, size }
            } else {
                let mut bytes = vec![0_u8; remaining - 4];
                reader.read_exact(&mut bytes)?;
                TerminalStreamEvent::Output {
                    seq,
                    size,
                    bytes: std::sync::Arc::from(bytes),
                }
            }
        }
        TERMINAL_FRAME_EXIT => {
            if remaining != 5 {
                return Err(io::Error::new(
                    io::ErrorKind::InvalidData,
                    "invalid terminal exit frame length",
                ));
            }
            let mut exit = [0_u8; 5];
            reader.read_exact(&mut exit)?;
            let code = if exit[0] == 0 {
                None
            } else {
                Some(i32::from_be_bytes(exit[1..].try_into().unwrap()))
            };
            TerminalStreamEvent::Exit { seq, code }
        }
        _ => {
            return Err(io::Error::new(
                io::ErrorKind::InvalidData,
                "unknown terminal frame kind",
            ));
        }
    };
    Ok(SessionWireFrame::Terminal { terminal_id, event })
}

fn read_frame_length<R>(reader: &mut R) -> io::Result<usize>
where
    R: Read,
{
    let mut length_bytes = [0_u8; 4];
    reader.read_exact(&mut length_bytes)?;
    let length = u32::from_be_bytes(length_bytes) as usize;
    if length == 0 || length > MAX_FRAME_BYTES {
        return Err(io::Error::new(
            io::ErrorKind::InvalidData,
            "invalid control message frame length",
        ));
    }
    Ok(length)
}

pub fn read_frame<R, T>(reader: &mut R) -> io::Result<T>
where
    R: Read,
    T: DeserializeOwned,
{
    let length = read_frame_length(reader)?;
    let mut payload = vec![0_u8; length];
    reader.read_exact(&mut payload)?;
    serde_json::from_slice(&payload).map_err(io::Error::other)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn direct_snapshot_frame_preserves_the_wire_shape() {
        let state = StateDump {
            state_revision: 7,
            workspace: None,
            workspaces: Vec::new(),
            active_workspace: None,
            focused_pane: None,
            agents: Vec::new(),
        };
        let mut frame = Vec::new();
        write_snapshot_frame(&mut frame, &state, true).unwrap();

        let message: WireMessage = read_frame(&mut frame.as_slice()).unwrap();
        assert_eq!(message.method.as_deref(), Some(PUSH_SNAPSHOT_METHOD));
        assert_eq!(message.request_id, 0);
        let params = message.params.unwrap();
        assert!(params.get("workspace").is_none());
        assert_eq!(
            serde_json::from_value::<StateDump>(params)
                .unwrap()
                .state_revision,
            7
        );
    }

    #[test]
    fn binary_terminal_frames_round_trip_without_base64() {
        let terminal_id = TerminalId::from_u128(0xfedcba98765443218abcdef012345678);
        let size = TerminalSize::new(132, 43);
        let events = [
            TerminalStreamEvent::Output {
                seq: 17,
                size,
                bytes: std::sync::Arc::from(&b"\0raw\xffterminal"[..]),
            },
            TerminalStreamEvent::Resize { seq: 18, size },
            TerminalStreamEvent::Exit {
                seq: 19,
                code: Some(7),
            },
        ];
        let mut bytes = Vec::new();
        for event in &events {
            write_terminal_frame(&mut bytes, terminal_id, event).unwrap();
        }
        let mut reader = bytes.as_slice();
        for expected in events {
            let SessionWireFrame::Terminal {
                terminal_id: actual_id,
                event,
            } = read_session_frame(&mut reader).unwrap()
            else {
                panic!("expected binary terminal frame");
            };
            assert_eq!(actual_id, terminal_id);
            assert_eq!(event, expected);
        }
    }

    #[test]
    fn session_reader_still_accepts_json_frames() {
        let message = WireMessage::reply(42, Ok(serde_json::json!({ "ok": true })));
        let mut bytes = Vec::new();
        write_frame(&mut bytes, &message).unwrap();
        let SessionWireFrame::Json(payload) = read_session_frame(&mut bytes.as_slice()).unwrap()
        else {
            panic!("expected JSON frame");
        };
        let decoded: WireMessage = serde_json::from_slice(&payload).unwrap();
        assert_eq!(decoded.request_id, 42);
    }
}
