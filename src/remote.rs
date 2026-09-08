use std::ffi::{OsStr, OsString};
use std::hash::{Hash, Hasher};
use std::path::{Path, PathBuf};
use std::process::{Command, Output, Stdio};
use std::time::{Duration, Instant};

use thiserror::Error;

use crate::control::ControlClient;

const REMOTE_START_TIMEOUT: Duration = Duration::from_secs(12);
const REMOTE_RETRY_INTERVAL: Duration = Duration::from_millis(50);
const DEFAULT_REMOTE_CONTROL_SOCKET: &str = "/tmp/water.sock";

#[derive(Debug, Error)]
pub enum SshConnectionError {
    #[error("SSH destination must not be empty, start with '-', or contain whitespace")]
    InvalidDestination,
    #[error("could not {action}: {source}")]
    Io {
        action: &'static str,
        #[source]
        source: std::io::Error,
    },
    #[error("could not {action}: {message}")]
    Command {
        action: &'static str,
        message: String,
    },
    #[error("remote Water server at {destination} did not become ready within 12 seconds")]
    ServerTimeout { destination: String },
}

/// A local Unix-socket forward owned by a reusable OpenSSH ControlMaster.
/// Dropping this value cancels only Water's forward; the authenticated master
/// remains available for subsequent windows until ControlPersist expires.
#[derive(Debug)]
pub struct SshTunnel {
    destination: String,
    local_socket: PathBuf,
    control_socket: PathBuf,
    forward_spec: String,
}

impl SshTunnel {
    pub fn connect(destination: &str) -> Result<Self, SshConnectionError> {
        let destination = validate_ssh_destination(destination)?;
        let remote_socket = remote_control_socket();
        let (local_socket, control_socket) = connection_paths(&destination, &remote_socket);
        remove_known_socket(&local_socket, "remove stale local Water forward")?;
        ensure_control_master(&destination, &control_socket)?;

        let forward_spec = format!("{}:{}", local_socket.display(), remote_socket.display());
        let forward = ssh_output(
            [
                OsStr::new("-S"),
                control_socket.as_os_str(),
                OsStr::new("-O"),
                OsStr::new("forward"),
                OsStr::new("-o"),
                OsStr::new("ExitOnForwardFailure=yes"),
                OsStr::new("-L"),
                OsStr::new(&forward_spec),
                OsStr::new(&destination),
            ],
            "establish SSH Water socket forward",
        )?;
        ensure_success(forward, "establish SSH Water socket forward")?;

        let tunnel = Self {
            destination,
            local_socket,
            control_socket,
            forward_spec,
        };
        if tunnel.client().ping().is_err() {
            start_remote_server(&tunnel.destination, &tunnel.control_socket, &remote_socket)?;
            tunnel.wait_until_ready()?;
        }
        Ok(tunnel)
    }

    pub fn local_socket(&self) -> &Path {
        &self.local_socket
    }

    fn client(&self) -> ControlClient {
        ControlClient::new(self.local_socket.clone())
    }

    fn wait_until_ready(&self) -> Result<(), SshConnectionError> {
        let deadline = Instant::now() + REMOTE_START_TIMEOUT;
        while Instant::now() < deadline {
            if self.client().ping().is_ok() {
                return Ok(());
            }
            std::thread::sleep(REMOTE_RETRY_INTERVAL);
        }
        Err(SshConnectionError::ServerTimeout {
            destination: self.destination.clone(),
        })
    }
}

impl Drop for SshTunnel {
    fn drop(&mut self) {
        let _ = ssh_output(
            [
                OsStr::new("-S"),
                self.control_socket.as_os_str(),
                OsStr::new("-O"),
                OsStr::new("cancel"),
                OsStr::new("-L"),
                OsStr::new(&self.forward_spec),
                OsStr::new(&self.destination),
            ],
            "cancel SSH Water socket forward",
        );
        let _ = std::fs::remove_file(&self.local_socket);
    }
}

pub fn validate_ssh_destination(destination: &str) -> Result<String, SshConnectionError> {
    let destination = destination.trim();
    if destination.is_empty()
        || destination.starts_with('-')
        || destination.len() > 255
        || destination.chars().any(char::is_whitespace)
        || destination.chars().any(char::is_control)
    {
        return Err(SshConnectionError::InvalidDestination);
    }
    Ok(destination.to_owned())
}

fn connection_paths(destination: &str, remote_socket: &Path) -> (PathBuf, PathBuf) {
    let mut hasher = std::collections::hash_map::DefaultHasher::new();
    destination.hash(&mut hasher);
    remote_socket.hash(&mut hasher);
    let identity = hasher.finish();
    #[cfg(unix)]
    let user = unsafe { libc::geteuid() };
    #[cfg(not(unix))]
    let user = 0_u32;
    (
        PathBuf::from(format!("/tmp/water-ssh-{user}-{identity:016x}.sock")),
        PathBuf::from(format!("/tmp/water-ssh-{user}-{identity:016x}.ctl")),
    )
}

fn ensure_control_master(
    destination: &str,
    control_socket: &Path,
) -> Result<(), SshConnectionError> {
    let check = ssh_output(
        [
            OsStr::new("-S"),
            control_socket.as_os_str(),
            OsStr::new("-O"),
            OsStr::new("check"),
            OsStr::new(destination),
        ],
        "check reusable SSH connection",
    )?;
    if check.status.success() {
        return Ok(());
    }

    remove_known_socket(control_socket, "remove stale SSH control socket")?;
    let master = ssh_output(
        [
            OsStr::new("-M"),
            OsStr::new("-N"),
            OsStr::new("-f"),
            OsStr::new("-o"),
            OsStr::new("ControlMaster=yes"),
            OsStr::new("-o"),
            OsStr::new("ControlPersist=600"),
            OsStr::new("-o"),
            OsStr::new("BatchMode=yes"),
            OsStr::new("-o"),
            OsStr::new("Compression=yes"),
            OsStr::new("-o"),
            OsStr::new("ServerAliveInterval=15"),
            OsStr::new("-o"),
            OsStr::new("ServerAliveCountMax=2"),
            OsStr::new("-o"),
            OsStr::new("ConnectTimeout=10"),
            OsStr::new("-S"),
            control_socket.as_os_str(),
            OsStr::new(destination),
        ],
        "open reusable SSH connection",
    )?;
    ensure_success(master, "open reusable SSH connection")
}

fn start_remote_server(
    destination: &str,
    control_socket: &Path,
    remote_socket: &Path,
) -> Result<(), SshConnectionError> {
    let remote_socket = shell_quote(&remote_socket.display().to_string());
    let launch = if let Ok(server_program) = std::env::var("WATER_REMOTE_SERVER_COMMAND") {
        let server_program = shell_quote(&server_program);
        format!(
            "command -v {server_program} >/dev/null 2>&1 || exit 127; nohup {server_program} --control-socket {remote_socket} >/tmp/water-server.log 2>&1 </dev/null &"
        )
    } else {
        format!(
            "if command -v water-server >/dev/null 2>&1; then nohup water-server --control-socket {remote_socket} >/tmp/water-server.log 2>&1 </dev/null & elif command -v water >/dev/null 2>&1; then nohup water server --control-socket {remote_socket} >/tmp/water-server.log 2>&1 </dev/null & else exit 127; fi"
        )
    };
    let command = format!("if test ! -S {remote_socket}; then {launch} fi");
    let output = ssh_output(
        [
            OsStr::new("-S"),
            control_socket.as_os_str(),
            OsStr::new("-o"),
            OsStr::new("BatchMode=yes"),
            OsStr::new(destination),
            OsStr::new(&command),
        ],
        "start remote Water server",
    )?;
    ensure_success(output, "start remote Water server")
}

fn remote_control_socket() -> PathBuf {
    std::env::var_os("WATER_REMOTE_CONTROL_SOCKET")
        .map(PathBuf::from)
        .unwrap_or_else(|| PathBuf::from(DEFAULT_REMOTE_CONTROL_SOCKET))
}

fn ssh_output<'a>(
    arguments: impl IntoIterator<Item = &'a OsStr>,
    action: &'static str,
) -> Result<Output, SshConnectionError> {
    let mut command = Command::new(ssh_program());
    if let Some(config) = std::env::var_os("WATER_SSH_CONFIG") {
        command.arg("-F").arg(config);
    }
    command
        .args(arguments)
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::piped())
        .output()
        .map_err(|source| SshConnectionError::Io { action, source })
}

fn ssh_program() -> OsString {
    std::env::var_os("WATER_SSH_PROGRAM").unwrap_or_else(|| OsString::from("ssh"))
}

fn ensure_success(output: Output, action: &'static str) -> Result<(), SshConnectionError> {
    if output.status.success() {
        return Ok(());
    }
    let message = String::from_utf8_lossy(&output.stderr).trim().to_owned();
    Err(SshConnectionError::Command {
        action,
        message: if message.is_empty() {
            format!("SSH exited with {}", output.status)
        } else {
            message
        },
    })
}

fn remove_known_socket(path: &Path, action: &'static str) -> Result<(), SshConnectionError> {
    match std::fs::remove_file(path) {
        Ok(()) => Ok(()),
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => Ok(()),
        Err(source) => Err(SshConnectionError::Io { action, source }),
    }
}

fn shell_quote(value: &str) -> String {
    format!("'{}'", value.replace('\'', "'\\''"))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn ssh_destination_accepts_config_aliases_and_user_hosts() {
        assert_eq!(
            validate_ssh_destination(" build-box ").unwrap(),
            "build-box"
        );
        assert_eq!(
            validate_ssh_destination("alice@example.com").unwrap(),
            "alice@example.com"
        );
    }

    #[test]
    fn ssh_destination_rejects_options_and_shell_whitespace() {
        for invalid in ["", "-oProxyCommand=bad", "host name", "host\ncommand"] {
            assert!(validate_ssh_destination(invalid).is_err());
        }
    }

    #[test]
    fn connection_paths_are_short_stable_and_destination_specific() {
        let remote = Path::new("/tmp/water.sock");
        let first = connection_paths("alpha", remote);
        assert_eq!(first, connection_paths("alpha", remote));
        assert_ne!(first, connection_paths("beta", remote));
        assert!(first.0.as_os_str().len() < 100);
        assert!(first.1.as_os_str().len() < 100);
    }

    #[test]
    fn remote_shell_arguments_are_single_quoted() {
        assert_eq!(shell_quote("water-server"), "'water-server'");
        assert_eq!(shell_quote("a'b"), "'a'\\''b'");
    }

    /// Opt-in end-to-end check used by development/CI environments that can
    /// provide an SSH destination. The normal test suite stays hermetic and
    /// never waits on network state.
    #[test]
    #[ignore = "requires WATER_SSH_TEST_DESTINATION and a reachable sshd"]
    fn live_ssh_tunnel_reaches_remote_water_server() {
        let destination = std::env::var("WATER_SSH_TEST_DESTINATION")
            .expect("WATER_SSH_TEST_DESTINATION must name the test SSH host");
        let tunnel = SshTunnel::connect(&destination).expect("SSH tunnel should connect");
        tunnel
            .client()
            .ping()
            .expect("remote Water server should reply");
        drop(tunnel);

        let reused = SshTunnel::connect(&destination).expect("SSH master should be reusable");
        reused
            .client()
            .ping()
            .expect("re-established forward should reply");
    }
}
