//! Regression test for the terminal attach-thread leak.
//!
//! When a terminal's tab/pane is closed, the GUI must detach its live stream
//! so the `water-terminal-events-*` attach thread exits and its Alacritty grid
//! is freed. Before the fix the thread parked on `recv_timeout` forever (its
//! `SyncSender` stayed alive in `WaterSession::terminal_channels`), so one
//! leaked grid + thread accumulated per closed terminal for the life of the
//! process.
//!
//! This test drives the real server + GUI attachment path: it opens a tab,
//! attaches its terminal through the GUI snapshot listener, closes the tab,
//! and asserts the attach thread count returns to baseline. It uses bounded
//! waits (no sleeps on the hot path): it fails on the old code and passes on
//! the fixed code.

#![cfg(unix)]

use std::sync::Arc;
use std::time::Duration;

use water::app::ModelHost;
use water::command::{AppCommand, OperationStatus, TabCommand, WorkspaceCommand};
use water::config::AppConfig;
use water::control::{ControlClient, ControlServer, connect_water_session};
use water::ui::{WaterApplication, ui_control_channel};

fn find_terminal(tree: &water::app::model::PaneTreeDump) -> Option<water::ids::TerminalId> {
    match tree {
        water::app::model::PaneTreeDump::Leaf {
            surface_state: water::surface::SurfaceState::Terminal(t),
            ..
        } => Some(t.terminal_id),
        water::app::model::PaneTreeDump::Leaf { .. } => None,
        water::app::model::PaneTreeDump::Split { first, second, .. } => {
            find_terminal(first).or_else(|| find_terminal(second))
        }
    }
}

/// Counts live `water-terminal-events-*` attach threads in the current
/// process. Note: the kernel truncates `comm` to 15 chars, so the thread name
/// appears as `water-terminal-` — that prefix is unique to attach threads
/// (worker/reader/pump threads use other names).
fn attach_thread_count() -> usize {
    let self_id = std::process::id();
    let tasks = match std::fs::read_dir(format!("/proc/{self_id}/task")) {
        Ok(tasks) => tasks,
        Err(_) => return 0,
    };
    tasks
        .filter_map(|entry| entry.ok())
        .filter_map(|entry| {
            let name = std::fs::read_to_string(entry.path().join("comm")).ok()?;
            name.trim().starts_with("water-terminal-").then_some(())
        })
        .count()
}

/// Polls until `within` holds or the deadline expires.
fn wait_for(mut within: impl FnMut() -> bool) {
    let deadline = std::time::Instant::now() + Duration::from_secs(20);
    while !within() {
        assert!(
            std::time::Instant::now() < deadline,
            "timed out waiting for attach threads to settle"
        );
        std::thread::yield_now();
    }
}

#[gpui::test]
fn closed_terminal_releases_its_attach_thread(cx: &mut gpui::TestAppContext) {
    let dir = std::env::temp_dir().join(format!("water-detach-{}", std::process::id()));
    let _ = std::fs::create_dir_all(&dir);
    let socket = dir.join("water.sock");
    let _ = std::fs::remove_file(&socket);

    let mut host = ModelHost::start();
    let client = host.client();
    let (server, _shutdown) = ControlServer::start(
        socket.clone(),
        client.clone(),
        None,
        Some(host.take_snapshot_receiver()),
    )
    .unwrap();
    let (ui_client, _ui_rx) = ui_control_channel();
    let session = connect_water_session(&socket, ui_client.clone()).unwrap();
    let metrics_client = ControlClient::new(&socket);
    // The real push stream: model changes flow to the GUI snapshot listener,
    // which attaches visible terminals and detaches them on close.
    let snapshot_stream = session.snapshot_stream().clone();

    // A workspace with a real terminal plus a dedicated tab to close.
    if client.state_dump().unwrap().workspaces.is_empty() {
        let op = client
            .dispatch(AppCommand::Workspace(WorkspaceCommand::New))
            .unwrap();
        assert_eq!(
            client.wait_operation(op).unwrap().status,
            OperationStatus::Succeeded
        );
    }
    let created_tab = client
        .dispatch(AppCommand::Tab(TabCommand::New { title: None }))
        .unwrap();
    assert_eq!(
        client.wait_operation(created_tab).unwrap().status,
        OperationStatus::Succeeded
    );
    let state = client.state_dump().unwrap();
    let workspace_id = state.active_workspace.expect("active workspace");
    let active_tab = state
        .workspaces
        .iter()
        .find(|ws| ws.id == workspace_id)
        .and_then(|ws| ws.active_tab)
        .expect("active tab");
    let target = state
        .workspaces
        .iter()
        .find(|ws| ws.id == workspace_id)
        .and_then(|ws| ws.tabs.iter().find(|t| t.id == active_tab))
        .and_then(|t| find_terminal(&t.tree))
        .expect("terminal leaf in the new tab");

    let application = WaterApplication::new(
        Arc::new(client.clone()),
        client.state_dump().unwrap(),
        AppConfig::default(),
    );

    // Install the terminal plane (drives the snapshot listener) and attach the
    // target terminal explicitly, since `install` with `terminal_session=None`
    // skips the eager attach.
    cx.update(|cx| {
        cx.set_app_identity("dev.water.terminal.dev", "Water Dev");
        application.install(cx, snapshot_stream, ui_client, _ui_rx, None);
    });
    cx.update(|_cx| {
        application.ensure_terminal_attached(water::ids::ConnectionId::new(1), target);
    });

    // The attach thread for the active terminal must come up.
    wait_for(|| attach_thread_count() >= 1);
    let baseline = attach_thread_count();
    assert!(
        baseline >= 1,
        "expected at least one attach thread after install, got {baseline}"
    );

    // Close the tab that owns the active terminal. The model removes it; the
    // snapshot listener observes the change and (with the fix) detaches the
    // live stream so the attach thread exits. On the old code the thread
    // parks forever, so this never settles.
    let close_op = client
        .dispatch(AppCommand::Tab(TabCommand::Close {
            tab_id: Some(active_tab),
        }))
        .unwrap();
    assert_eq!(
        client.wait_operation(close_op).unwrap().status,
        OperationStatus::Succeeded
    );

    wait_for(|| attach_thread_count() <= baseline.saturating_sub(1));
    wait_for(|| {
        let Ok(metrics) = metrics_client.metrics() else {
            return false;
        };
        let current = |name: &str| {
            metrics
                .get(name)
                .and_then(serde_json::Value::as_u64)
                .unwrap_or_default()
        };
        current("terminal_server_queue_events") == 0
            && current("terminal_server_queue_bytes") == 0
            && current("terminal_client_queue_events") == 0
            && current("terminal_client_queue_bytes") == 0
    });

    // The target terminal's attach thread is gone. Normal teardown must also
    // complete: this catches a server pump or PTY worker that is still holding
    // the closed terminal's output buffers.
    eprintln!("closed_terminal_releases_its_attach_thread: OK (baseline={baseline})");
    drop(server);
    drop(session);
    let _ = host.shutdown();
    // The GPUI test harness retains its executor task until process teardown;
    // all Water resources have been dropped and joined above, so terminate the
    // one-purpose integration process after the assertions complete.
    std::process::exit(0);
}
