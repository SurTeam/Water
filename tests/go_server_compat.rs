#![cfg(unix)]

use std::io;
use std::path::PathBuf;
use std::process::{Child, Command, Stdio};
use std::time::{Duration, Instant};

use water::command::{
    AppCommand, OperationResult, OperationStatus, TabCommand, TerminalCommand, WorkspaceCommand,
};
use water::control::{ControlClient, connect_water_session};
use water::terminal::TerminalStreamEvent;
use water::ui::ui_control_channel;

struct ServerGuard {
    child: Option<Child>,
    client: ControlClient,
    socket: PathBuf,
    config: PathBuf,
}

impl Drop for ServerGuard {
    fn drop(&mut self) {
        let _ = self.client.server_shutdown();
        if let Some(mut child) = self.child.take() {
            let deadline = Instant::now() + Duration::from_secs(2);
            loop {
                match child.try_wait() {
                    Ok(Some(_)) => break,
                    Ok(None) if Instant::now() < deadline => {
                        std::thread::sleep(Duration::from_millis(20));
                    }
                    _ => {
                        let _ = child.kill();
                        let _ = child.wait();
                        break;
                    }
                }
            }
        }
        let _ = std::fs::remove_file(&self.socket);
        let _ = std::fs::remove_file(&self.config);
    }
}

fn start_go_server() -> Option<ServerGuard> {
    let binary = std::env::var_os("WATER_GO_SERVER_BIN")?;
    let socket = PathBuf::from(format!(
        "/tmp/water-go-compat-{}.sock",
        uuid::Uuid::new_v4()
    ));
    let config = PathBuf::from(format!(
        "/tmp/water-go-compat-{}.json",
        uuid::Uuid::new_v4()
    ));
    std::fs::write(
        &config,
        r#"{
          "startup":{"initial_workspace":false,"initial_terminal":false},
          "shell":{"program":"/bin/sh","args":["-l"]}
        }"#,
    )
    .expect("write portable Go server config");

    let child = Command::new(binary)
        .arg("--control-socket")
        .arg(&socket)
        .arg("--config")
        .arg(&config)
        .arg("--empty-workspace")
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::inherit())
        .spawn()
        .expect("start Go water-server");
    let client = ControlClient::new(socket.clone());
    let deadline = Instant::now() + Duration::from_secs(10);
    while Instant::now() < deadline {
        if client.ping().is_ok() {
            return Some(ServerGuard {
                child: Some(child),
                client,
                socket,
                config,
            });
        }
        std::thread::sleep(Duration::from_millis(25));
    }
    panic!("Go water-server did not become ready");
}

fn dispatch(client: &ControlClient, command: AppCommand) -> OperationResult {
    let id = client.dispatch(command).expect("dispatch");
    let op = client.wait_operation(id).expect("wait operation");
    assert_eq!(op.status, OperationStatus::Succeeded, "{:?}", op.error);
    op.result.expect("operation result")
}

#[test]
fn rust_client_talks_to_go_server_and_reads_live_wt4() -> io::Result<()> {
    let Some(server) = start_go_server() else {
        eprintln!("WATER_GO_SERVER_BIN is not set; skipping cross-language compatibility test");
        return Ok(());
    };

    let info = server.client.server_info().expect("server.info");
    assert_eq!(info.protocol_version, water::control::PROTOCOL_VERSION);
    assert_eq!(info.api_signature, water::control::API_SIGNATURE);

    dispatch(
        &server.client,
        AppCommand::Workspace(WorkspaceCommand::Create),
    );
    dispatch(
        &server.client,
        AppCommand::Tab(TabCommand::New {
            title: Some("Cross".to_owned()),
        }),
    );

    let terminal_id = match dispatch(
        &server.client,
        AppCommand::Terminal(TerminalCommand::Spawn {
            pane_id: None,
            program: "/bin/sh".to_owned(),
            args: vec![
                "-c".to_owned(),
                "read line; printf 'CROSS:%s\\n' \"$line\"".to_owned(),
            ],
            columns: 80,
            lines: 24,
        }),
    ) {
        OperationResult::TerminalSpawned { terminal_id } => terminal_id,
        other => panic!("unexpected spawn result: {other:?}"),
    };

    let (ui_client, _ui_rx) = ui_control_channel();
    let session =
        connect_water_session(server.socket.clone(), ui_client).expect("open Go GUI session");
    let (attached, mut events) = session.attach(terminal_id).expect("attach terminal");

    dispatch(
        &server.client,
        AppCommand::Terminal(TerminalCommand::SendText {
            terminal_id: Some(terminal_id),
            pane_id: None,
            text: "hello-cross\n".to_owned(),
        }),
    );

    let deadline = Instant::now() + Duration::from_secs(5);
    let mut live = Vec::new();
    while Instant::now() < deadline {
        match events.recv_timeout(Duration::from_millis(250)) {
            Ok(TerminalStreamEvent::Output { seq, bytes, .. }) if seq > attached.last_seq => {
                live.extend_from_slice(&bytes);
                if live
                    .windows(b"CROSS:hello-cross".len())
                    .any(|window| window == b"CROSS:hello-cross")
                {
                    return Ok(());
                }
            }
            Ok(TerminalStreamEvent::Exit { seq, .. }) if seq > attached.last_seq => {
                break;
            }
            Ok(_) => {}
            Err(std::sync::mpsc::RecvTimeoutError::Timeout) => {}
            Err(error) => panic!("Go session terminal stream ended: {error}"),
        }
    }
    panic!(
        "Rust client did not receive expected Go WT4 live output: {:?}",
        String::from_utf8_lossy(&live)
    );
}
