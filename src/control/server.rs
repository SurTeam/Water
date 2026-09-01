use std::io;
use std::path::PathBuf;
use std::sync::{
    Arc,
    atomic::{AtomicBool, Ordering},
};
use std::thread::{self, JoinHandle};

use crate::app::CommandClient;
use crate::command::DispatchError;

use super::protocol::{
    PROTOCOL_VERSION, RpcError, RpcMethod, RpcRequest, RpcResponse, read_frame, write_frame,
};

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
        }
        let _ = std::fs::remove_file(&self.socket_path);
    }
}

impl Drop for ControlServerHandle {
    fn drop(&mut self) {
        self.shutdown();
    }
}

#[cfg(unix)]
impl ControlServer {
    pub fn start(socket_path: PathBuf, client: CommandClient) -> io::Result<ControlServerHandle> {
        if socket_path.exists() {
            std::fs::remove_file(&socket_path)?;
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
        let join_handle = thread::Builder::new()
            .name("water-control".to_owned())
            .spawn(move || {
                tracing::info!(
                    target: "water::automation",
                    socket = %server_path.display(),
                    "control socket listening"
                );
                while !server_stop.load(Ordering::Acquire) {
                    match listener.accept() {
                        Ok((mut stream, _)) => {
                            if server_stop.load(Ordering::Acquire) {
                                break;
                            }
                            handle_connection(&mut stream, &client);
                        }
                        Err(error) => {
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

        Ok(ControlServerHandle {
            stop,
            socket_path,
            join_handle: Some(join_handle),
        })
    }
}

#[cfg(not(unix))]
impl ControlServer {
    pub fn start(_socket_path: PathBuf, _client: CommandClient) -> io::Result<ControlServerHandle> {
        Err(io::Error::new(
            io::ErrorKind::Unsupported,
            "Unix domain sockets are not available on this platform yet",
        ))
    }
}

#[cfg(unix)]
fn handle_connection(stream: &mut std::os::unix::net::UnixStream, client: &CommandClient) {
    loop {
        let request = match read_frame::<_, RpcRequest>(stream) {
            Ok(request) => request,
            Err(error) if error.kind() == io::ErrorKind::UnexpectedEof => break,
            Err(error) => {
                let response =
                    RpcResponse::failure(0, RpcError::new("INVALID_REQUEST", error.to_string()));
                let _ = write_frame(stream, &response);
                break;
            }
        };
        let response = handle_request(request, client);
        if write_frame(stream, &response).is_err() {
            break;
        }
    }
}

#[cfg(unix)]
fn handle_request(request: RpcRequest, client: &CommandClient) -> RpcResponse {
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
        RpcMethod::CommandDispatch { command } => match client.dispatch(command) {
            Ok(operation_id) => {
                RpcResponse::success(request.request_id, &OperationIdResponse { operation_id })
            }
            Err(error) => RpcResponse::failure(request.request_id, dispatch_error(error)),
        },
        RpcMethod::OperationGet { operation_id } => match client.get_operation(operation_id) {
            Some(operation) => RpcResponse::success(request.request_id, &operation),
            None => RpcResponse::failure(
                request.request_id,
                RpcError::new(
                    "OPERATION_NOT_FOUND",
                    format!("operation {operation_id} does not exist"),
                ),
            ),
        },
        RpcMethod::OperationWait { operation_id } => match client.wait_operation(operation_id) {
            Ok(operation) => RpcResponse::success(request.request_id, &operation),
            Err(error) => RpcResponse::failure(request.request_id, dispatch_error(error)),
        },
        RpcMethod::StateDump => match client.state_dump() {
            Ok(state) => RpcResponse::success(request.request_id, &state),
            Err(error) => RpcResponse::failure(request.request_id, dispatch_error(error)),
        },
        RpcMethod::EventList { after_sequence } => {
            match client.events_since(after_sequence.unwrap_or(0)) {
                Ok(events) => RpcResponse::success(request.request_id, &events),
                Err(error) => RpcResponse::failure(request.request_id, dispatch_error(error)),
            }
        }
        RpcMethod::DebugMemory => match client.memory_stats() {
            Ok(memory) => RpcResponse::success(request.request_id, &memory),
            Err(error) => RpcResponse::failure(request.request_id, dispatch_error(error)),
        },
        RpcMethod::TerminalContains {
            terminal_id,
            text,
            timeout_ms,
        } => match client.terminal_contains(
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
        } => match client
            .wait_terminal_exit(terminal_id, std::time::Duration::from_millis(timeout_ms))
        {
            Ok(snapshot) => RpcResponse::success(request.request_id, &snapshot),
            Err(error) => RpcResponse::failure(request.request_id, dispatch_error(error)),
        },
        RpcMethod::TerminalSnapshot { terminal_id } => {
            match client.terminal_snapshot(terminal_id) {
                Ok(snapshot) => RpcResponse::success(request.request_id, &snapshot),
                Err(error) => RpcResponse::failure(request.request_id, dispatch_error(error)),
            }
        }
        RpcMethod::Ping => RpcResponse::success(
            request.request_id,
            &PingResponse {
                protocol_version: PROTOCOL_VERSION,
            },
        ),
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

fn dispatch_error(error: DispatchError) -> RpcError {
    match error {
        DispatchError::Command(error) => error.into(),
        DispatchError::ChannelClosed => RpcError::new("MODEL_UNAVAILABLE", "model thread stopped"),
    }
}
