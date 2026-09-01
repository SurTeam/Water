pub mod client;
pub mod protocol;
pub mod server;

pub use client::{ControlClient, ControlClientError};
pub use protocol::{
    PROTOCOL_VERSION, RpcError, RpcMethod, RpcRequest, RpcResponse, read_frame, write_frame,
};
pub use server::{ControlServer, ControlServerHandle};

pub fn default_socket_path() -> std::path::PathBuf {
    std::env::var_os("WATER_CONTROL_SOCKET")
        .map(std::path::PathBuf::from)
        .unwrap_or_else(|| std::path::PathBuf::from("/tmp/water.sock"))
}
