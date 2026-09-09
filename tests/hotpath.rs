//! Mandatory acceptance item: sustained PTY output must not trigger
//! `state_dump` or ModelSnapshot pushes. Only real model/control changes do.

#![cfg(unix)]

use std::time::Duration;

use water::app::ModelHost;
use water::command::{AppCommand, OperationStatus, TerminalCommand};
use water::control::{ControlClient, ControlServer, connect_water_session};
use water::terminal::WireTerminalEvent;
use water::ui::ui_control_channel;

#[test]
fn sustained_output_does_not_trigger_state_dumps_or_snapshot_pushes() {
    let dir = std::env::temp_dir().join(format!("water-hotpath-{}", std::process::id()));
    std::fs::create_dir_all(&dir).unwrap();
    let socket = dir.join("water.sock");
    let _ = std::fs::remove_file(&socket);

    let mut host = ModelHost::start();
    let client = host.client();
    let (_server, _shutdown) = ControlServer::start(
        socket.clone(),
        client.clone(),
        None,
        Some(host.take_snapshot_receiver()),
    )
    .unwrap();
    let (ui_client, _ui_rx) = ui_control_channel();
    let session = connect_water_session(&socket, ui_client).unwrap();

    // Create a workspace with a real terminal pane.
    let created = client
        .dispatch(AppCommand::Workspace(water::command::WorkspaceCommand::New))
        .unwrap();
    let operation = client.wait_operation(created).unwrap();
    assert_eq!(operation.status, OperationStatus::Succeeded);
    let state = client.state_dump().unwrap();
    let active = state.active_workspace.unwrap();
    let workspace = state
        .workspaces
        .iter()
        .find(|workspace| workspace.id == active)
        .unwrap();
    let mut terminal_id = None;
    fn find(tree: &water::app::model::PaneTreeDump) -> Option<water::ids::TerminalId> {
        match tree {
            water::app::model::PaneTreeDump::Leaf {
                surface_state: water::surface::SurfaceState::Terminal(t),
                ..
            } => Some(t.terminal_id),
            water::app::model::PaneTreeDump::Leaf { .. } => None,
            water::app::model::PaneTreeDump::Split { first, second, .. } => {
                find(first).or_else(|| find(second))
            }
        }
    }
    for tab in &workspace.tabs {
        terminal_id = terminal_id.or_else(|| find(&tab.tree));
    }
    let terminal_id = terminal_id.expect("terminal leaf");
    let (_response, mut stream) = session.attach(terminal_id).unwrap();

    // Start a sustained flood in the shell.
    client
        .dispatch(AppCommand::Terminal(TerminalCommand::SendText {
            terminal_id: Some(terminal_id),
            pane_id: None,
            text: "for i in $(seq 1 40000); do echo HOTPATH_FLOOD_$i; done\n".to_owned(),
        }))
        .unwrap();
    client
        .terminal_contains(terminal_id, "HOTPATH_FLOOD_200", Duration::from_secs(10))
        .unwrap();

    let control = ControlClient::new(&socket);
    let before = control.metrics().unwrap();
    let start = std::time::Instant::now();
    let mut terminal_events = 0_u64;
    while start.elapsed() < Duration::from_secs(3) {
        let Some(event) = stream.recv_timeout(Duration::from_millis(500)).ok() else {
            continue;
        };
        match &event {
            WireTerminalEvent::Output { .. } => terminal_events += 1,
            _ => {}
        }
    }
    let after = control.metrics().unwrap();
    let secs = start.elapsed().as_secs_f64();
    let delta = |name: &str| {
        after[name]
            .as_u64()
            .unwrap_or(0)
            .saturating_sub(before[name].as_u64().unwrap_or(0))
    };

    eprintln!(
        "hotpath_probe: terminal_events={terminal_events} state_dumps={}/{}s snapshot_pushes={}/{}s",
        delta("state_dumps"),
        secs,
        delta("model_snapshot_pushes"),
        secs
    );
    assert!(
        terminal_events > 10,
        "the flood must produce live terminal events"
    );
    assert!(
        delta("state_dumps") as f64 / secs < 5.0,
        "sustained output must not trigger state dumps: {} in {secs}s",
        delta("state_dumps")
    );
    assert!(
        delta("model_snapshot_pushes") as f64 / secs < 5.0,
        "sustained output must not push ModelSnapshots: {} in {secs}s",
        delta("model_snapshot_pushes")
    );

    drop(session);
    host.shutdown();
}
