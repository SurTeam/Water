use std::path::PathBuf;
use std::sync::Arc;
use std::sync::atomic::{AtomicU64, Ordering};
use std::time::Duration;

use serde::de::DeserializeOwned;
use thiserror::Error;

use crate::app::model::{MemoryStats, StateDump};
use crate::command::{AppCommand, OperationSnapshot};
use crate::event::AppEvent;
use crate::ids::{OperationId, TerminalId};
use crate::terminal::TerminalSnapshot;
use crate::ui::{UiKeystrokeResult, UiSnapshot};

use super::protocol::{
    PROTOCOL_VERSION, RpcMethod, RpcRequest, RpcResponse, read_frame, write_frame,
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

    pub fn ping(&self) -> Result<(), ControlClientError> {
        Err(ControlClientError::Unsupported)
    }
}
