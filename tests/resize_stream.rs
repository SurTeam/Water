//! Ordered-stream guarantees around PTY resize, plus the detach/reconnect
//! (replay -> live) round trip.

use std::time::{Duration, Instant};

use water::command::{
    AppCommand, CommandDispatcher, OperationResult, OperationStatus, TabCommand, TerminalCommand,
    WorkspaceCommand,
};
use water::terminal::{TerminalSize, TerminalStreamEvent, snapshot_from_replay};

fn dispatch_ok(dispatcher: &mut CommandDispatcher, command: AppCommand) -> OperationResult {
    let operation_id = dispatcher.dispatch(command);
    let operation = dispatcher.wait_operation(operation_id).unwrap();
    assert_eq!(operation.status, OperationStatus::Succeeded);
    operation.result.unwrap()
}

fn dispatch_client(client: &water::app::CommandClient, command: AppCommand) -> OperationResult {
    let operation_id = client.dispatch(command).unwrap();
    let operation = client.wait_operation(operation_id).unwrap();
    assert_eq!(operation.status, OperationStatus::Succeeded);
    operation.result.unwrap()
}

fn terminal_of(dispatcher: &CommandDispatcher) -> water::ids::TerminalId {
    let state = dispatcher.state_dump();
    let workspace = state.workspace.as_ref().unwrap();
    let tab = &workspace.tabs[workspace.tabs.len() - 1];
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
    find(&tab.tree).expect("terminal leaf")
}

fn replay(dispatcher: &CommandDispatcher, id: water::ids::TerminalId) -> Vec<TerminalStreamEvent> {
    let replay = dispatcher.terminal_replay(id).unwrap();
    let mut size = replay.size;
    let mut events = Vec::new();
    for wire in &replay.events {
        let Some(event) = TerminalStreamEvent::from_wire(wire, size) else {
            continue;
        };
        if let TerminalStreamEvent::Resize { size: next, .. } = event {
            size = next;
        }
        events.push(event);
    }
    events
}

fn resize_columns(events: &[TerminalStreamEvent]) -> Vec<usize> {
    events
        .iter()
        .filter_map(|event| match event {
            TerminalStreamEvent::Resize { size, .. } => Some(size.columns),
            _ => None,
        })
        .collect()
}

#[test]
fn resize_event_is_emitted_into_the_ordered_stream() {
    let mut dispatcher = CommandDispatcher::new();
    dispatch_ok(
        &mut dispatcher,
        AppCommand::Workspace(WorkspaceCommand::Create),
    );
    dispatch_ok(
        &mut dispatcher,
        AppCommand::Tab(TabCommand::New { title: None }),
    );
    let terminal_id = terminal_of(&dispatcher);

    let before = replay(&dispatcher, terminal_id);
    assert_eq!(resize_columns(&before), vec![80]);

    let pane_id = dispatcher.state_dump().focused_pane.unwrap();
    dispatch_ok(
        &mut dispatcher,
        AppCommand::Terminal(TerminalCommand::Resize {
            terminal_id: Some(terminal_id),
            pane_id: Some(pane_id),
            columns: 120,
            lines: 30,
        }),
    );

    // The Resize command is applied on the PTY worker loop; poll the
    // ordered stream (bounded, no fixed sleep) until the event is visible.
    let deadline = Instant::now() + Duration::from_secs(5);
    let after = loop {
        dispatcher.pump_background_events();
        let events = replay(&dispatcher, terminal_id);
        if resize_columns(&events) == vec![80, 120] {
            break events;
        }
        if Instant::now() >= deadline {
            break events;
        }
    };
    assert_eq!(
        resize_columns(&after),
        vec![80, 120],
        "the ordered stream must carry the Resize event"
    );

    // A replaying GUI local term must observe the new geometry.
    let snapshot = snapshot_from_replay(&dispatcher.terminal_replay(terminal_id).unwrap(), 200);
    assert_eq!(snapshot.size, TerminalSize::new(120, 30));
}

#[test]
fn resize_drains_pending_output_before_the_resize_event() {
    let mut dispatcher = CommandDispatcher::new();
    dispatch_ok(
        &mut dispatcher,
        AppCommand::Workspace(WorkspaceCommand::Create),
    );
    dispatch_ok(
        &mut dispatcher,
        AppCommand::Tab(TabCommand::New { title: None }),
    );
    let terminal_id = terminal_of(&dispatcher);
    let pane_id = dispatcher.state_dump().focused_pane.unwrap();

    // Flood the terminal so output is in flight at resize time.
    dispatch_ok(
        &mut dispatcher,
        AppCommand::Terminal(TerminalCommand::SendText {
            terminal_id: Some(terminal_id),
            pane_id: Some(pane_id),
            text: "for i in $(seq 1 400); do echo WATER_FLOOD_$i; done\n".to_owned(),
        }),
    );
    dispatch_ok(
        &mut dispatcher,
        AppCommand::Terminal(TerminalCommand::Resize {
            terminal_id: Some(terminal_id),
            pane_id: Some(pane_id),
            columns: 100,
            lines: 28,
        }),
    );
    dispatcher.pump_background_events();
    dispatcher
        .terminal_registry()
        .contains_text(terminal_id, "WATER_FLOOD_400", Duration::from_secs(5))
        .unwrap();

    let events = replay(&dispatcher, terminal_id);
    let resize_pos = events
        .iter()
        .position(|event| matches!(event, TerminalStreamEvent::Resize { .. }))
        .expect("resize event present");
    // Every Output before the Resize was produced at the old geometry.
    for event in &events[..resize_pos] {
        if let TerminalStreamEvent::Output { size, .. } = event {
            assert_eq!(
                size.columns, 80,
                "old-geometry output after the Resize event"
            );
        }
    }
    // Every Output after the Resize was produced at the new geometry.
    for event in &events[resize_pos + 1..] {
        if let TerminalStreamEvent::Output { size, .. } = event {
            assert_eq!(
                size.columns, 100,
                "new-geometry output before the Resize event"
            );
        }
    }
}

#[cfg(unix)]
#[test]
fn detach_then_reattach_replays_history_and_resumes_live() {
    use water::app::ModelHost;
    use water::control::{ControlServer, connect_water_session};
    use water::terminal::WireTerminalEvent;
    use water::terminal::decode_base64;
    use water::ui::ui_control_channel;

    let dir = std::env::temp_dir().join(format!("water-reattach-{}", std::process::id()));
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

    let created = dispatch_client(&client, AppCommand::Workspace(WorkspaceCommand::New));
    let water::command::OperationResult::WorkspaceCreated { workspace_id } = created else {
        panic!("unexpected workspace result {created:?}");
    };
    let state = client.state_dump().unwrap();
    let workspace = state
        .workspaces
        .iter()
        .find(|workspace| workspace.id == workspace_id)
        .unwrap();
    let mut pane_id = None;
    for tab in &workspace.tabs {
        if matches!(&tab.tree, water::app::model::PaneTreeDump::Leaf { .. }) {
            pane_id = match &tab.tree {
                water::app::model::PaneTreeDump::Leaf { pane_id, .. } => Some(*pane_id),
                _ => None,
            };
        }
    }
    let pane_id = pane_id.expect("created workspace contains a leaf pane");
    let spawn = dispatch_client(
        &client,
        AppCommand::Terminal(TerminalCommand::Spawn {
            pane_id: Some(pane_id),
            program: "/bin/sh".to_owned(),
            args: vec![
                "-c".to_owned(),
                "printf 'WATER_MARKER_A\\n'; sleep 1; printf 'WATER_MARKER_B\\n'; exec sleep 30"
                    .to_owned(),
            ],
            columns: 80,
            lines: 24,
        }),
    );
    let terminal_id = match spawn {
        OperationResult::TerminalSpawned { terminal_id } => terminal_id,
        result => panic!("unexpected result {result:?}"),
    };

    let (ui_client, _ui_rx) = ui_control_channel();
    let session = connect_water_session(&socket, ui_client).unwrap();

    // Phase 1: attach, observe marker A, then detach.
    let (_response, mut stream) = session.attach(terminal_id).unwrap();
    let mut seen_a = false;
    while !seen_a {
        let event = stream.recv_timeout(Duration::from_secs(5)).unwrap();
        if let TerminalStreamEvent::Output { bytes, .. } = &event
            && String::from_utf8_lossy(bytes).contains("WATER_MARKER_A")
        {
            seen_a = true;
        }
    }
    session.detach(terminal_id);

    // The terminal keeps running while the GUI is detached: wait for marker B
    // through the server-side lifecycle path, not the stream.
    let replay = client
        .terminal_contains(terminal_id, "WATER_MARKER_B", Duration::from_secs(5))
        .unwrap();
    assert!(
        replay.visible_text().contains("WATER_MARKER_B"),
        "the detached terminal must keep producing raw events"
    );

    // Phase 2: reattach. The replay must contain the history (markers A and
    // B), and the live tail must continue seamlessly.
    let (response, mut stream) = session.attach(terminal_id).unwrap();
    let replay_text = response
        .replay
        .iter()
        .filter_map(|wire| match wire {
            WireTerminalEvent::Output { bytes, .. } => {
                decode_base64(bytes).map(|data| String::from_utf8_lossy(&data).into_owned())
            }
            _ => None,
        })
        .collect::<String>();
    assert!(
        replay_text.contains("WATER_MARKER_A"),
        "replay lost historical output"
    );
    assert!(
        replay_text.contains("WATER_MARKER_B"),
        "replay lost detached output"
    );

    // Live continuation: a new marker sent after reattach must arrive live.
    client
        .dispatch(AppCommand::Terminal(TerminalCommand::SendText {
            terminal_id: Some(terminal_id),
            pane_id: Some(pane_id),
            text: "echo WATER_MARKER_C\n".to_owned(),
        }))
        .unwrap();
    let mut live_text = String::new();
    let deadline = Instant::now() + Duration::from_secs(5);
    while !live_text.contains("WATER_MARKER_C") {
        if Instant::now() >= deadline {
            break;
        }
        let Some(event) = stream.recv_timeout(Duration::from_millis(500)).ok() else {
            continue;
        };
        if let TerminalStreamEvent::Output { bytes, .. } = &event {
            live_text.push_str(&String::from_utf8_lossy(bytes));
        }
    }
    assert!(
        live_text.contains("WATER_MARKER_C"),
        "live stream after reattach must resume"
    );

    drop(session);
    host.shutdown();
}
