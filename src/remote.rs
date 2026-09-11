use std::ffi::{OsStr, OsString};
use std::hash::{Hash, Hasher};
use std::io::Write as _;
use std::path::{Path, PathBuf};
use std::process::{Command, Output, Stdio};
use std::time::{Duration, Instant};

use thiserror::Error;

use crate::control::{ControlClient, PROTOCOL_VERSION};

const REMOTE_START_TIMEOUT: Duration = Duration::from_secs(12);
const REMOTE_RETRY_INTERVAL: Duration = Duration::from_millis(50);

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
    #[error("remote platform {system}/{machine} has no bundled Water server")]
    UnsupportedPlatform { system: String, machine: String },
}

/// A local Unix-socket forward owned by a reusable OpenSSH ControlMaster.
/// Dropping this value cancels only Water's forward; the authenticated master
/// remains available for subsequent windows until ControlPersist expires.
#[derive(Debug)]
pub struct SshTunnel {
    destination: String,
    local_socket: PathBuf,
    control_socket: PathBuf,
    remote_socket: PathBuf,
    forward_spec: String,
}

impl SshTunnel {
    pub fn connect(destination: &str) -> Result<Self, SshConnectionError> {
        let destination = validate_ssh_destination(destination)?;
        let remote_socket = remote_control_socket(&destination);
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
        ensure_success(&forward, "establish SSH Water socket forward")?;

        let tunnel = Self {
            destination,
            local_socket,
            control_socket,
            remote_socket: remote_socket.clone(),
            forward_spec,
        };
        if !tunnel.server_is_compatible() {
            if tunnel.client().server_info().is_ok() || tunnel.client().ping().is_ok() {
                return Err(SshConnectionError::Command {
                    action: "attach remote Water server",
                    message: "socket belongs to an incompatible server; refusing to replace it"
                        .to_owned(),
                });
            }
            start_remote_server(&tunnel.destination, &tunnel.control_socket, &remote_socket)?;
            tunnel.wait_until_ready()?;
        }
        Ok(tunnel)
    }

    pub fn local_socket(&self) -> &Path {
        &self.local_socket
    }

    pub fn remote_socket(&self) -> &Path {
        &self.remote_socket
    }

    fn client(&self) -> ControlClient {
        ControlClient::new(self.local_socket.clone())
    }

    fn server_is_compatible(&self) -> bool {
        self.client().server_info().is_ok_and(|info| {
            info.protocol_version == PROTOCOL_VERSION
                && info.server_version == env!("CARGO_PKG_VERSION")
                && info.build_variant == crate::BUILD_VARIANT
        })
    }

    fn wait_until_ready(&self) -> Result<(), SshConnectionError> {
        let deadline = Instant::now() + REMOTE_START_TIMEOUT;
        while Instant::now() < deadline {
            if self.server_is_compatible() {
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
    let mut forward_hasher = std::collections::hash_map::DefaultHasher::new();
    destination.hash(&mut forward_hasher);
    remote_socket.hash(&mut forward_hasher);
    crate::BUILD_VARIANT.hash(&mut forward_hasher);
    let forward_identity = forward_hasher.finish();
    let mut master_hasher = std::collections::hash_map::DefaultHasher::new();
    destination.hash(&mut master_hasher);
    crate::BUILD_VARIANT.hash(&mut master_hasher);
    let master_identity = master_hasher.finish();
    #[cfg(unix)]
    let user = unsafe { libc::geteuid() };
    #[cfg(not(unix))]
    let user = 0_u32;
    (
        PathBuf::from(format!(
            "/tmp/water-ssh-{user}-{forward_identity:016x}.sock"
        )),
        PathBuf::from(format!("/tmp/water-ssh-{user}-{master_identity:016x}.ctl")),
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
    ensure_success(&master, "open reusable SSH connection")
}

fn start_remote_server(
    destination: &str,
    control_socket: &Path,
    remote_socket: &Path,
) -> Result<(), SshConnectionError> {
    let namespace = crate::APP_NAMESPACE;
    let variant = crate::BUILD_VARIANT;
    let remote_socket_quoted = shell_quote(&remote_socket.display().to_string());
    if let Ok(server_program) = std::env::var("WATER_REMOTE_SERVER_COMMAND") {
        let server_program = shell_quote(&server_program);
        return run_remote_command(
            destination,
            control_socket,
            &format!(
                "command -v {server_program} >/dev/null 2>&1 || exit 127; test \"$( {server_program} --build-variant )\" = {variant} || exit 1; {server_program} --daemonize --control-socket {remote_socket_quoted} >/tmp/{namespace}-server.log 2>&1 </dev/null"
            ),
            "start configured remote Water server",
        );
    }

    let target = detect_remote_target(destination, control_socket)?;
    if let Some(payload) = crate::embedded_servers::payload_for_target(target) {
        return install_and_start_embedded_server(
            destination,
            control_socket,
            remote_socket,
            payload,
        );
    }

    // An arbitrary installed binary may belong to release. Never launch it
    // as a fallback for a dev client without matching bundled payloads.
    Err(SshConnectionError::Command {
        action: "start remote Water server",
        message: "matching embedded server payload is missing; build the app bundle or set WATER_REMOTE_SERVER_COMMAND explicitly".to_owned(),
    })
}

fn detect_remote_target(
    destination: &str,
    control_socket: &Path,
) -> Result<&'static str, SshConnectionError> {
    let output = ssh_capture(
        [
            OsStr::new("-S"),
            control_socket.as_os_str(),
            OsStr::new("-o"),
            OsStr::new("BatchMode=yes"),
            OsStr::new(destination),
            OsStr::new("uname -s; uname -m"),
        ],
        "detect remote platform",
    )?;
    ensure_success(&output, "detect remote platform")?;
    let stdout = String::from_utf8_lossy(&output.stdout);
    let mut lines = stdout.lines();
    let system = lines.next().unwrap_or_default().trim().to_owned();
    let machine = lines.next().unwrap_or_default().trim().to_owned();
    crate::embedded_servers::target_for_uname(&system, &machine)
        .ok_or(SshConnectionError::UnsupportedPlatform { system, machine })
}

fn install_and_start_embedded_server(
    destination: &str,
    control_socket: &Path,
    remote_socket: &Path,
    payload: crate::embedded_servers::EmbeddedServerPayload,
) -> Result<(), SshConnectionError> {
    let namespace = crate::APP_NAMESPACE;
    let variant = crate::BUILD_VARIANT;
    let payload_id = stable_payload_id(payload.gzip);
    let relative_directory = format!(
        ".cache/{}/server/{}-{payload_id:016x}/{}",
        crate::APP_NAMESPACE,
        env!("CARGO_PKG_VERSION"),
        payload.target
    );
    // The on-disk program name carries the dev identity so ps/Activity
    // Monitor shows water-srv-dev without relying on setprogname.
    let program_name = if variant == "dev" { "water-srv-dev" } else { "water-server" };
    let quoted_directory = format!("\"$HOME/{relative_directory}\"");
    let quoted_program = format!("\"$HOME/{relative_directory}/{program_name}\"");

    let present_command =
        format!("test -x {quoted_program} && {quoted_program} --version >/dev/null 2>&1");
    let present = ssh_capture(
        [
            OsStr::new("-S"),
            control_socket.as_os_str(),
            OsStr::new("-o"),
            OsStr::new("BatchMode=yes"),
            OsStr::new(destination),
            OsStr::new(&present_command),
        ],
        "check cached remote Water server",
    )?;
    if !present.status.success() {
        let install = format!(
            "umask 077; directory={quoted_directory}; program={quoted_program}; mkdir -p -- \"$directory\" || exit 1; temporary=\"$directory/.water-server.$$\"; trap 'rm -f -- \"$temporary\"' EXIT HUP INT TERM; gzip -dc >\"$temporary\" && chmod 700 \"$temporary\" && mv -f -- \"$temporary\" \"$program\""
        );
        let output = ssh_input(
            [
                OsStr::new("-S"),
                control_socket.as_os_str(),
                OsStr::new("-o"),
                OsStr::new("BatchMode=yes"),
                OsStr::new(destination),
                OsStr::new(&install),
            ],
            payload.gzip,
            "install bundled remote Water server",
        )?;
        ensure_success(&output, "install bundled remote Water server")?;
    }

    let remote_socket = shell_quote(&remote_socket.display().to_string());
    run_remote_command(
        destination,
        control_socket,
        &format!(
            "test \"$( {quoted_program} --build-variant )\" = {variant} || exit 1; {quoted_program} --daemonize --control-socket {remote_socket} >/tmp/{namespace}-server.log 2>&1 </dev/null"
        ),
        "start bundled remote Water server",
    )
}

fn run_remote_command(
    destination: &str,
    control_socket: &Path,
    command: &str,
    action: &'static str,
) -> Result<(), SshConnectionError> {
    let output = ssh_output(
        [
            OsStr::new("-S"),
            control_socket.as_os_str(),
            OsStr::new("-o"),
            OsStr::new("BatchMode=yes"),
            OsStr::new(destination),
            OsStr::new(command),
        ],
        action,
    )?;
    ensure_success(&output, action)
}

fn versioned_remote_control_socket(
    destination: &str,
    version: &str,
    protocol_version: u32,
) -> PathBuf {
    let identity = stable_payload_id(destination.as_bytes());
    PathBuf::from(format!(
        "/tmp/{}-v{version}-p{protocol_version}-{identity:016x}.sock",
        crate::APP_NAMESPACE
    ))
}

fn remote_control_socket(destination: &str) -> PathBuf {
    std::env::var_os("WATER_REMOTE_CONTROL_SOCKET")
        .map(PathBuf::from)
        .unwrap_or_else(|| {
            versioned_remote_control_socket(
                destination,
                env!("CARGO_PKG_VERSION"),
                PROTOCOL_VERSION,
            )
        })
}

fn ssh_output<'a>(
    arguments: impl IntoIterator<Item = &'a OsStr>,
    action: &'static str,
) -> Result<Output, SshConnectionError> {
    ssh_command(arguments)
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::piped())
        .output()
        .map_err(|source| SshConnectionError::Io { action, source })
}

fn ssh_capture<'a>(
    arguments: impl IntoIterator<Item = &'a OsStr>,
    action: &'static str,
) -> Result<Output, SshConnectionError> {
    ssh_command(arguments)
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .output()
        .map_err(|source| SshConnectionError::Io { action, source })
}

fn ssh_input<'a>(
    arguments: impl IntoIterator<Item = &'a OsStr>,
    input: &[u8],
    action: &'static str,
) -> Result<Output, SshConnectionError> {
    let mut child = ssh_command(arguments)
        .stdin(Stdio::piped())
        .stdout(Stdio::null())
        .stderr(Stdio::piped())
        .spawn()
        .map_err(|source| SshConnectionError::Io { action, source })?;
    let write_result = child
        .stdin
        .take()
        .expect("piped SSH stdin")
        .write_all(input);
    let output = child
        .wait_with_output()
        .map_err(|source| SshConnectionError::Io { action, source })?;
    if let Err(source) = write_result
        && output.status.success()
    {
        return Err(SshConnectionError::Io { action, source });
    }
    Ok(output)
}

fn ssh_command<'a>(arguments: impl IntoIterator<Item = &'a OsStr>) -> Command {
    let mut command = Command::new(ssh_program());
    if let Some(config) = std::env::var_os("WATER_SSH_CONFIG") {
        command.arg("-F").arg(config);
    }
    command.args(arguments);
    command
}

fn ssh_program() -> OsString {
    std::env::var_os("WATER_SSH_PROGRAM").unwrap_or_else(|| OsString::from("ssh"))
}

fn ensure_success(output: &Output, action: &'static str) -> Result<(), SshConnectionError> {
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

fn stable_payload_id(payload: &[u8]) -> u64 {
    payload.iter().fold(0xcbf29ce484222325_u64, |hash, byte| {
        (hash ^ u64::from(*byte)).wrapping_mul(0x100000001b3)
    })
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
        assert_eq!(
            first.1,
            connection_paths("alpha", Path::new("/tmp/another.sock")).1
        );
        assert!(first.0.as_os_str().len() < 100);
        assert!(first.1.as_os_str().len() < 100);
    }

    #[test]
    fn remote_socket_is_isolated_by_client_version_and_protocol() {
        let current = versioned_remote_control_socket("alpha", "1.2.3", 4);
        assert_eq!(
            current,
            versioned_remote_control_socket("alpha", "1.2.3", 4)
        );
        assert_ne!(
            current,
            versioned_remote_control_socket("alpha", "1.2.4", 4)
        );
        assert_ne!(
            current,
            versioned_remote_control_socket("alpha", "1.2.3", 5)
        );
        assert_ne!(current, versioned_remote_control_socket("beta", "1.2.3", 4));
    }

    #[test]
    fn remote_shell_arguments_are_single_quoted() {
        assert_eq!(shell_quote("water-server"), "'water-server'");
        assert_eq!(shell_quote("a'b"), "'a'\\''b'");
    }

    #[test]
    fn payload_identity_is_stable_and_content_sensitive() {
        assert_eq!(stable_payload_id(b"water"), 0xd3ca_cd4c_82e5_be70);
        assert_ne!(stable_payload_id(b"water"), stable_payload_id(b"Water"));
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
        let server_pid = tunnel
            .client()
            .server_info()
            .expect("remote Water server should report its identity")
            .server_pid;
        let remote_socket = tunnel.remote_socket().to_path_buf();
        drop(tunnel);

        let tunnel = SshTunnel::connect(&destination).expect("SSH master should be reusable");
        assert_eq!(
            tunnel
                .client()
                .server_info()
                .expect("disconnect must leave the remote server alive")
                .server_pid,
            server_pid
        );
        tunnel
            .client()
            .server_shutdown()
            .expect("remote Water server should accept shutdown");
        let socket_gone = format!(
            "test ! -S {}",
            shell_quote(&remote_socket.display().to_string())
        );
        let deadline = Instant::now() + REMOTE_START_TIMEOUT;
        while Instant::now() < deadline {
            let state = ssh_capture(
                [
                    OsStr::new("-S"),
                    tunnel.control_socket.as_os_str(),
                    OsStr::new("-o"),
                    OsStr::new("BatchMode=yes"),
                    OsStr::new(&destination),
                    OsStr::new(&socket_gone),
                ],
                "wait for test server shutdown",
            )
            .expect("shutdown probe should run");
            if state.status.success() {
                break;
            }
            std::thread::yield_now();
        }
        assert!(
            ssh_capture(
                [
                    OsStr::new("-S"),
                    tunnel.control_socket.as_os_str(),
                    OsStr::new("-o"),
                    OsStr::new("BatchMode=yes"),
                    OsStr::new(&destination),
                    OsStr::new(&socket_gone),
                ],
                "confirm test server shutdown",
            )
            .expect("shutdown confirmation should run")
            .status
            .success()
        );
        drop(tunnel);

        let reused = SshTunnel::connect(&destination).expect("SSH master should remain reusable");
        reused
            .client()
            .ping()
            .expect("re-established forward should reply");
        reused
            .client()
            .server_shutdown()
            .expect("restarted test server should shut down during cleanup");
    }
}
