//! Sustained raw-terminal-stream benchmark.
//!
//! Gated behind `WATER_BENCH=1` so the ordinary test suite stays fast. The
//! workload is `yes` by default; set `WATER_BENCH_WORKLOAD=bulk` for repeated
//! large `seq` writes. The benchmark owns a GUI-equivalent local Alacritty
//! emulator and reports server/control and GUI/parser counters separately.
//!
//! Run: `WATER_BENCH=1 cargo test --release --test bench_terminal -- --nocapture --ignored`

use std::time::Duration;

use water::app::ModelHost;
use water::command::{AppCommand, OperationStatus, PaneCommand, TerminalCommand, WorkspaceCommand};
use water::control::{ControlClient, ControlServer, connect_water_session};
use water::terminal::{TerminalEmulator, TerminalStreamEvent};
use water::ui::ui_control_channel;

const BENCH_SECONDS: u64 = 8;

#[test]
#[ignore]
fn bench_sustained_output() {
    if std::env::var_os("WATER_BENCH").is_none() {
        eprintln!("set WATER_BENCH=1 to run this benchmark");
        return;
    }

    let workload = std::env::var("WATER_BENCH_WORKLOAD").unwrap_or_else(|_| "yes".to_owned());
    let command = match workload.as_str() {
        "bulk" => "while :; do seq 1 200000; done",
        _ => "yes",
    };
    let dir = std::env::temp_dir().join(format!("water-bench-{}", std::process::id()));
    std::fs::create_dir_all(&dir).unwrap();
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

    let operation = client
        .dispatch(AppCommand::Workspace(WorkspaceCommand::New))
        .unwrap();
    assert_eq!(
        client.wait_operation(operation).unwrap().status,
        OperationStatus::Succeeded
    );
    let pane_id = client.state_dump().unwrap().focused_pane.unwrap();
    let spawn = client
        .dispatch(AppCommand::Terminal(TerminalCommand::Spawn {
            pane_id: Some(pane_id),
            program: "/bin/sh".to_owned(),
            args: vec!["-c".to_owned(), command.to_owned()],
            columns: 80,
            lines: 24,
        }))
        .unwrap();
    let operation = client.wait_operation(spawn).unwrap();
    assert_eq!(operation.status, OperationStatus::Succeeded);
    let terminal_id = match operation.result.unwrap() {
        water::command::OperationResult::TerminalSpawned { terminal_id } => terminal_id,
        result => panic!("unexpected result {result:?}"),
    };

    let control = ControlClient::new(&socket);
    let (ui_client, _ui_receiver) = ui_control_channel();
    let session = connect_water_session(&socket, ui_client).unwrap();
    let (response, mut stream) = session.attach(terminal_id).unwrap();
    let mut emulator = TerminalEmulator::new(terminal_id, response.size, 10_000);
    let mut tracked_size = response.size;
    let replay = response
        .replay
        .iter()
        .filter_map(|wire| {
            let event = TerminalStreamEvent::from_wire(wire, tracked_size)?;
            if let TerminalStreamEvent::Resize { size, .. } = event {
                tracked_size = size;
            }
            Some(event)
        })
        .collect::<Vec<_>>();
    emulator.apply_batch(&replay);
    emulator.start_live();

    let before = control.metrics().unwrap();
    let start_cpu = process_cpu_seconds();
    let start = std::time::Instant::now();
    let mut received_events = 0_u64;
    let mut received_bytes = 0_u64;
    while start.elapsed() < Duration::from_secs(BENCH_SECONDS) {
        let first = match stream.recv_timeout(Duration::from_millis(250)) {
            Ok(event) => event,
            Err(std::sync::mpsc::RecvTimeoutError::Timeout) => continue,
            Err(error) => panic!("terminal stream ended: {error}"),
        };
        let mut wires = vec![first];
        while let Ok(event) = stream.try_recv() {
            wires.push(event);
        }
        let mut events = Vec::with_capacity(wires.len());
        for wire in &wires {
            let Some(event) = TerminalStreamEvent::from_wire(wire, tracked_size) else {
                continue;
            };
            if let TerminalStreamEvent::Resize { size, .. } = event {
                tracked_size = size;
            }
            received_events += 1;
            received_bytes += event.output_bytes() as u64;
            events.push(event);
        }
        emulator.apply_batch(&events);
    }
    let secs = start.elapsed().as_secs_f64();
    let cpu = process_cpu_seconds() - start_cpu;
    let after = control.metrics().unwrap();

    let close = client
        .dispatch(AppCommand::Pane(PaneCommand::Close {
            pane_id: Some(pane_id),
        }))
        .unwrap();
    let _ = client.wait_operation(close);

    let advances = delta(&before, &after, "processor_advances");
    eprintln!("=== WATER RAW TERMINAL BENCH: {workload} ({secs:.2}s) ===");
    eprintln!("combined_cpu_pct        = {:.1}", cpu / secs * 100.0);
    eprintln!(
        "pty_bytes_read/sec      = {:.1} MB/s",
        delta(&before, &after, "pty_bytes_read") as f64 / secs / 1e6
    );
    eprintln!(
        "pty_read_calls/sec      = {:.1}",
        delta(&before, &after, "pty_read_calls") as f64 / secs
    );
    eprintln!(
        "stream_events/sec       = {:.1}",
        delta(&before, &after, "terminal_stream_events") as f64 / secs
    );
    eprintln!(
        "stream_bytes/sec        = {:.1} MB/s",
        delta(&before, &after, "terminal_output_bytes_sent") as f64 / secs / 1e6
    );
    eprintln!(
        "state_dump/sec          = {:.1}",
        delta(&before, &after, "state_dumps") as f64 / secs
    );
    eprintln!(
        "model_snapshot_push/sec = {:.1}",
        delta(&before, &after, "model_snapshot_pushes") as f64 / secs
    );
    eprintln!("processor_advance/sec   = {:.1}", advances as f64 / secs);
    eprintln!(
        "bytes_per_advance       = {:.1}",
        received_bytes as f64 / advances.max(1) as f64
    );
    eprintln!(
        "received_events/sec     = {:.1}",
        received_events as f64 / secs
    );
    eprintln!(
        "replay_ring_bytes       = {}",
        metric(&after, "replay_ring_bytes")
    );

    drop(session);
    drop(server);
    host.shutdown();
}

fn metric(value: &serde_json::Value, name: &str) -> u64 {
    value
        .get(name)
        .and_then(serde_json::Value::as_u64)
        .unwrap_or(0)
}

fn delta(before: &serde_json::Value, after: &serde_json::Value, name: &str) -> u64 {
    metric(after, name).saturating_sub(metric(before, name))
}

fn process_cpu_seconds() -> f64 {
    let mut usage: libc::rusage = unsafe { std::mem::zeroed() };
    unsafe {
        libc::getrusage(libc::RUSAGE_SELF, &mut usage);
    }
    usage.ru_utime.tv_sec as f64
        + usage.ru_utime.tv_usec as f64 / 1e6
        + usage.ru_stime.tv_sec as f64
        + usage.ru_stime.tv_usec as f64 / 1e6
}
