//! Smoke test: the `ctl` control interface (integrated from the former
//! `waterctl` binary) works through the `water` binary's `ctl`-prefixed and
//! bare subcommands, against a real control socket.
#![cfg(unix)]

use std::path::{Path, PathBuf};
use std::process::{Command, Output};
use std::time::Duration;

use water::app::ModelHost;
use water::control::{ControlClient, ControlServer};

fn water_bin() -> PathBuf {
    PathBuf::from(env!("CARGO_BIN_EXE_water"))
}

/// Starts a model + control server on a unique socket; returns the socket
/// path and a thread that keeps the server alive for the test's lifetime.
fn start_fixture() -> (PathBuf, std::thread::JoinHandle<()>) {
    let nonce = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .unwrap()
        .as_nanos();
    let socket = std::env::temp_dir().join(format!(
        "water-ctl-smoke-{}-{nonce}.sock",
        std::process::id()
    ));
    let _ = std::fs::remove_file(&socket);
    let thread_socket = socket.clone();
    let handle = std::thread::spawn(move || {
        let mut host = ModelHost::start();
        let (mut server, _shutdown_rx) = ControlServer::start(
            thread_socket,
            host.client(),
            None,
            Some(host.take_snapshot_receiver()),
        )
        .expect("fixture server starts");
        std::thread::park();
        server.shutdown();
        host.shutdown();
    });
    let deadline = std::time::Instant::now() + Duration::from_secs(10);
    let probe = ControlClient::new(&socket);
    while std::time::Instant::now() < deadline {
        if probe.ping().is_ok() {
            return (socket, handle);
        }
        std::thread::sleep(Duration::from_millis(50));
    }
    panic!("fixture server did not come up at {}", socket.display());
}

fn run(socket: &Path, args: &[&str]) -> Output {
    // The socket comes from WATER_CONTROL_SOCKET; the water binary's dispatch
    // layers (server, control, GUI) parse the argument list separately.
    Command::new(water_bin())
        .env("WATER_CONTROL_SOCKET", socket)
        .args(args)
        .output()
        .expect("spawns the water binary")
}

#[test]
fn ctl_prefix_and_bare_alias_both_reach_the_control_server() {
    let (socket, server) = start_fixture();

    for command in [
        vec!["ctl", "state"],
        vec!["state"],
        vec!["ctl", "debug", "memory"],
        vec!["debug", "memory"],
        vec!["ctl", "workspace", "new"],
        vec!["workspace", "new"],
    ] {
        let output = run(&socket, &command);
        assert!(
            output.status.success(),
            "water {} failed: {}",
            command.join(" "),
            String::from_utf8_lossy(&output.stderr)
        );
    }

    // `ctl` with no arguments prints the usage help and exits cleanly.
    let help = run(&socket, &["ctl"]);
    assert!(help.status.success());
    assert!(
        String::from_utf8_lossy(&help.stdout).contains("water ctl"),
        "usage should mention the ctl interface"
    );

    std::mem::drop(server);
    let _ = std::fs::remove_file(&socket);
}