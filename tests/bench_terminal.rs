//! Sustained raw-terminal-stream benchmark.
//!
//! Gated behind `WATER_BENCH=1` so the ordinary test suite stays fast. The
//! workload is `yes` by default; set `WATER_BENCH_WORKLOAD=bulk` for repeated
//! large `seq` writes. The benchmark owns a GUI-equivalent local Alacritty
//! emulator and reports server/control and GUI/parser counters separately.
//!
//! Run: `WATER_BENCH=1 cargo test --release --test bench_terminal -- --nocapture --ignored`.
//! Completion tests default to `/Users/clearain/.local/bin/testTermCat`;
//! `WATER_BENCH_COMMAND` can supply newline-heavy or larger shell workloads
//! while retaining the same attach-before-start barrier and metrics.

use std::sync::mpsc;
use std::time::{Duration, Instant};

use water::app::ModelHost;
use water::command::{AppCommand, OperationStatus, PaneCommand, TerminalCommand, WorkspaceCommand};
use water::control::{ControlClient, ControlServer, connect_water_session};
use water::ids::TerminalId;
use water::terminal::{
    TerminalEmulator, TerminalManager, TerminalSize, TerminalSnapshot, TerminalStreamEvent,
};
use water::ui::ui_control_channel;

const BENCH_SECONDS: u64 = 8;
const COMPLETION_TIMEOUT: Duration = Duration::from_secs(30);
const VISIBLE_INTERVAL: Duration = Duration::from_millis(16);
const DEFAULT_WORKLOAD: &str = "/Users/clearain/.local/bin/testTermCat";

#[derive(Clone, Debug)]
struct CompletionSample {
    process: Duration,
    emulator: Duration,
    visible: Duration,
    catchup: Duration,
    visible_gaps: Vec<Duration>,
    backlogged_visible_gaps: Vec<Duration>,
    bytes: u64,
    events: u64,
    server_queue_max_bytes: u64,
    server_queue_max_events: u64,
    client_queue_max_bytes: u64,
    client_queue_max_events: u64,
}

#[derive(Clone, Copy, Debug)]
struct InteractionSample {
    resize: Duration,
    input: Duration,
}

#[test]
#[ignore]
fn bench_interaction_parity() {
    if std::env::var_os("WATER_BENCH").is_none() {
        eprintln!("set WATER_BENCH=1 to run this benchmark");
        return;
    }
    let warmups = benchmark_count("WATER_BENCH_WARMUPS", 3);
    let runs = benchmark_count("WATER_BENCH_RUNS", 10);
    let mut direct = Vec::with_capacity(runs);
    for index in 0..warmups + runs {
        let sample = run_direct_interaction(index as u64 + 10_000);
        if index >= warmups {
            direct.push(sample);
        }
    }
    water::metrics::reset_terminal_queue_peaks();
    let mut server = Vec::with_capacity(runs);
    for index in 0..warmups + runs {
        let sample = run_server_interaction(index as u64 + 10_000);
        if index >= warmups {
            server.push(sample);
        }
    }
    print_interaction_summary("direct", &direct);
    print_interaction_summary("server_local", &server);
}

fn run_direct_interaction(id: u64) -> InteractionSample {
    let terminal_id = TerminalId::new(id);
    let size = TerminalSize::new(80, 24);
    let resized = TerminalSize::new(100, 31);
    let mut manager = TerminalManager::new();
    manager
        .spawn(
            terminal_id,
            "/bin/sh".to_owned(),
            vec![
                "-c".to_owned(),
                "read _; exec yes WATER_INTERACTION_FLOOD".to_owned(),
            ],
            size,
        )
        .unwrap();
    let attachment = manager.attach(terminal_id).unwrap();
    manager.send_text(terminal_id, "\n".to_owned()).unwrap();
    wait_for_output(|| attachment.events.recv_timeout(COMPLETION_TIMEOUT));

    let resize_start = Instant::now();
    manager.resize(terminal_id, resized).unwrap();
    wait_for_resize(resized, || {
        attachment.events.recv_timeout(COMPLETION_TIMEOUT)
    });
    let resize = resize_start.elapsed();

    let input_start = Instant::now();
    manager.send_bytes(terminal_id, vec![3]).unwrap();
    wait_for_exit(|| attachment.events.recv_timeout(COMPLETION_TIMEOUT));
    let input = input_start.elapsed();
    manager.remove(terminal_id);
    InteractionSample { resize, input }
}

fn run_server_interaction(id: u64) -> InteractionSample {
    let socket = std::env::temp_dir().join(format!(
        "water-interaction-{}-{id}.sock",
        std::process::id()
    ));
    let _ = std::fs::remove_file(&socket);
    let mut host = ModelHost::start();
    let client = host.client();
    let (server, _shutdown) = ControlServer::start(
        socket.clone(),
        client,
        None,
        Some(host.take_snapshot_receiver()),
    )
    .unwrap();
    let control = ControlClient::new(&socket);
    let operation = control
        .dispatch(AppCommand::Workspace(WorkspaceCommand::New))
        .unwrap();
    assert_eq!(
        control.wait_operation(operation).unwrap().status,
        OperationStatus::Succeeded
    );
    let pane_id = control.state_dump().unwrap().focused_pane.unwrap();
    let operation = control
        .dispatch(AppCommand::Terminal(TerminalCommand::Spawn {
            pane_id: Some(pane_id),
            program: "/bin/sh".to_owned(),
            args: vec![
                "-c".to_owned(),
                "read _; exec yes WATER_INTERACTION_FLOOD".to_owned(),
            ],
            columns: 80,
            lines: 24,
        }))
        .unwrap();
    let operation = control.wait_operation(operation).unwrap();
    let terminal_id = match operation.result.unwrap() {
        water::command::OperationResult::TerminalSpawned { terminal_id } => terminal_id,
        result => panic!("unexpected result {result:?}"),
    };
    let (ui_client, _ui_receiver) = ui_control_channel();
    let session = connect_water_session(&socket, ui_client).unwrap();
    let (_response, mut stream) = session.attach(terminal_id).unwrap();
    let operation = control
        .dispatch(AppCommand::Terminal(TerminalCommand::SendText {
            terminal_id: Some(terminal_id),
            pane_id: None,
            text: "\n".to_owned(),
        }))
        .unwrap();
    assert_eq!(
        control.wait_operation(operation).unwrap().status,
        OperationStatus::Succeeded
    );
    wait_for_output(|| stream.recv_timeout(COMPLETION_TIMEOUT));

    let resized = TerminalSize::new(100, 31);
    let resize_start = Instant::now();
    let operation = control
        .dispatch(AppCommand::Terminal(TerminalCommand::Resize {
            terminal_id: Some(terminal_id),
            pane_id: None,
            columns: resized.columns,
            lines: resized.lines,
        }))
        .unwrap();
    assert_eq!(
        control.wait_operation(operation).unwrap().status,
        OperationStatus::Succeeded
    );
    wait_for_resize(resized, || stream.recv_timeout(COMPLETION_TIMEOUT));
    let resize = resize_start.elapsed();

    let input_start = Instant::now();
    let operation = control
        .dispatch(AppCommand::Terminal(TerminalCommand::SendBytes {
            terminal_id: Some(terminal_id),
            pane_id: None,
            bytes: vec![3],
        }))
        .unwrap();
    assert_eq!(
        control.wait_operation(operation).unwrap().status,
        OperationStatus::Succeeded
    );
    wait_for_exit(|| stream.recv_timeout(COMPLETION_TIMEOUT));
    let input = input_start.elapsed();

    drop(session);
    drop(server);
    host.shutdown();
    let _ = std::fs::remove_file(&socket);
    InteractionSample { resize, input }
}

fn wait_for_output<F>(mut receive: F)
where
    F: FnMut() -> Result<TerminalStreamEvent, mpsc::RecvTimeoutError>,
{
    loop {
        if receive().expect("terminal stream timed out").output_bytes() > 1024 {
            return;
        }
    }
}

fn wait_for_resize<F>(expected: TerminalSize, mut receive: F)
where
    F: FnMut() -> Result<TerminalStreamEvent, mpsc::RecvTimeoutError>,
{
    loop {
        if matches!(
            receive().expect("terminal stream timed out"),
            TerminalStreamEvent::Resize { size, .. } if size == expected
        ) {
            return;
        }
    }
}

fn wait_for_exit<F>(mut receive: F)
where
    F: FnMut() -> Result<TerminalStreamEvent, mpsc::RecvTimeoutError>,
{
    loop {
        if matches!(
            receive().expect("terminal stream timed out"),
            TerminalStreamEvent::Exit { .. }
        ) {
            return;
        }
    }
}

fn print_interaction_summary(label: &str, samples: &[InteractionSample]) {
    eprintln!("=== WATER TERMINAL INTERACTION: {label} ===");
    for (name, values) in [
        (
            "resize",
            samples
                .iter()
                .map(|sample| sample.resize)
                .collect::<Vec<_>>(),
        ),
        (
            "input",
            samples
                .iter()
                .map(|sample| sample.input)
                .collect::<Vec<_>>(),
        ),
    ] {
        eprintln!(
            "{label}.{name} p50={:.3}ms p95={:.3}ms p99={:.3}ms max={:.3}ms",
            millis(percentile_duration(&values, 0.50)),
            millis(percentile_duration(&values, 0.95)),
            millis(percentile_duration(&values, 0.99)),
            millis(values.iter().copied().max().unwrap_or_default()),
        );
    }
}

#[test]
#[ignore]
fn bench_completion_parity() {
    if std::env::var_os("WATER_BENCH").is_none() {
        eprintln!("set WATER_BENCH=1 to run this benchmark");
        return;
    }

    let workload =
        std::env::var("WATER_BENCH_PROGRAM").unwrap_or_else(|_| DEFAULT_WORKLOAD.to_owned());
    let workload_command = std::env::var("WATER_BENCH_COMMAND").ok();
    if workload_command.is_none() {
        assert!(
            std::path::Path::new(&workload).is_file(),
            "benchmark workload does not exist: {workload}"
        );
    }
    let workload_command = workload_command.as_deref().unwrap_or("exec \"$1\"");
    let warmups = benchmark_count("WATER_BENCH_WARMUPS", 3);
    let runs = benchmark_count("WATER_BENCH_RUNS", 10);

    let mut direct = Vec::with_capacity(runs);
    for index in 0..warmups + runs {
        eprintln!("begin direct run {}/{}", index + 1, warmups + runs);
        let sample = run_direct_completion(&workload, workload_command, index as u64 + 1);
        eprintln!(
            "end direct run {} visible={:.3}ms",
            index + 1,
            millis(sample.visible)
        );
        if index >= warmups {
            direct.push(sample);
        }
    }

    water::metrics::reset_terminal_queue_peaks();
    let mut server = Vec::with_capacity(runs);
    for index in 0..warmups + runs {
        eprintln!("begin server_local run {}/{}", index + 1, warmups + runs);
        let sample = run_server_completion(&workload, workload_command, index as u64 + 1);
        eprintln!(
            "end server_local run {} visible={:.3}ms",
            index + 1,
            millis(sample.visible)
        );
        if index >= warmups {
            server.push(sample);
        }
    }

    print_completion_summary("direct", &direct);
    print_completion_summary("server_local", &server);
    let direct_visible = percentile_duration(
        &direct
            .iter()
            .map(|sample| sample.visible)
            .collect::<Vec<_>>(),
        0.5,
    );
    let server_visible = percentile_duration(
        &server
            .iter()
            .map(|sample| sample.visible)
            .collect::<Vec<_>>(),
        0.5,
    );
    let direct_process = percentile_duration(
        &direct
            .iter()
            .map(|sample| sample.process)
            .collect::<Vec<_>>(),
        0.5,
    );
    let server_process = percentile_duration(
        &server
            .iter()
            .map(|sample| sample.process)
            .collect::<Vec<_>>(),
        0.5,
    );
    eprintln!(
        "parity process={:.3}x visible={:.3}x throughput_retention={:.1}%",
        duration_ratio(server_process, direct_process),
        duration_ratio(server_visible, direct_visible),
        duration_ratio(direct_visible, server_visible) * 100.0,
    );
}

fn run_direct_completion(workload: &str, workload_command: &str, id: u64) -> CompletionSample {
    let terminal_id = TerminalId::new(id);
    let size = TerminalSize::new(80, 24);
    let mut manager = TerminalManager::new_with_scrollback(10_000);
    manager
        .spawn(
            terminal_id,
            "/bin/sh".to_owned(),
            barrier_args(workload, workload_command),
            size,
        )
        .unwrap();
    let attachment = manager.attach(terminal_id).unwrap();
    let mut emulator = TerminalEmulator::new(terminal_id, size, 10_000);
    emulator.apply_batch(&attachment.replay);
    emulator.start_live();

    let registry = manager.registry();
    let (clock_tx, clock_rx) = mpsc::channel();
    let (process_tx, process_rx) = mpsc::channel();
    let waiter = std::thread::spawn(move || {
        let start: Instant = clock_rx.recv().unwrap();
        registry
            .wait_process_exit(terminal_id, COMPLETION_TIMEOUT)
            .unwrap();
        process_tx.send(start.elapsed()).unwrap();
    });
    let start = Instant::now();
    clock_tx.send(start).unwrap();
    manager.send_text(terminal_id, "\n".to_owned()).unwrap();

    let sample = consume_terminal_stream(
        start,
        emulator,
        attachment.last_seq,
        |timeout| attachment.events.recv_timeout(timeout),
        process_rx,
    );
    waiter.join().unwrap();
    manager.remove(terminal_id);
    sample
}

fn run_server_completion(workload: &str, workload_command: &str, id: u64) -> CompletionSample {
    let socket = std::env::temp_dir().join(format!("water-bench-{}-{id}.sock", std::process::id()));
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
    let control = ControlClient::new(&socket);
    let operation = control
        .dispatch(AppCommand::Workspace(WorkspaceCommand::New))
        .unwrap();
    assert_eq!(
        control.wait_operation(operation).unwrap().status,
        OperationStatus::Succeeded
    );
    let pane_id = control.state_dump().unwrap().focused_pane.unwrap();
    let operation = control
        .dispatch(AppCommand::Terminal(TerminalCommand::Spawn {
            pane_id: Some(pane_id),
            program: "/bin/sh".to_owned(),
            args: barrier_args(workload, workload_command),
            columns: 80,
            lines: 24,
        }))
        .unwrap();
    let operation = control.wait_operation(operation).unwrap();
    assert_eq!(operation.status, OperationStatus::Succeeded);
    let terminal_id = match operation.result.unwrap() {
        water::command::OperationResult::TerminalSpawned { terminal_id } => terminal_id,
        result => panic!("unexpected result {result:?}"),
    };

    let (ui_client, _ui_receiver) = ui_control_channel();
    let session = connect_water_session(&socket, ui_client).unwrap();
    let (response, mut stream) = session.attach(terminal_id).unwrap();
    let mut tracked_size = response.size;
    let mut emulator = TerminalEmulator::new(terminal_id, tracked_size, 10_000);
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

    let (clock_tx, clock_rx) = mpsc::channel();
    let (process_tx, process_rx) = mpsc::channel();
    let exit_client = client.clone();
    let waiter = std::thread::spawn(move || {
        let start: Instant = clock_rx.recv().unwrap();
        exit_client
            .wait_terminal_exit_replay(terminal_id, COMPLETION_TIMEOUT)
            .unwrap();
        process_tx.send(start.elapsed()).unwrap();
    });
    let start = Instant::now();
    clock_tx.send(start).unwrap();
    let operation = control
        .dispatch(AppCommand::Terminal(TerminalCommand::SendText {
            terminal_id: Some(terminal_id),
            pane_id: None,
            text: "\n".to_owned(),
        }))
        .unwrap();
    assert_eq!(
        control.wait_operation(operation).unwrap().status,
        OperationStatus::Succeeded
    );
    let mut sample = consume_terminal_stream(
        start,
        emulator,
        response.last_seq,
        |timeout| stream.recv_timeout(timeout),
        process_rx,
    );
    let queue_metrics = control.metrics().unwrap();
    sample.server_queue_max_bytes = metric(&queue_metrics, "terminal_server_queue_max_bytes");
    sample.server_queue_max_events = metric(&queue_metrics, "terminal_server_queue_max_events");
    sample.client_queue_max_bytes = metric(&queue_metrics, "terminal_client_queue_max_bytes");
    sample.client_queue_max_events = metric(&queue_metrics, "terminal_client_queue_max_events");
    waiter.join().unwrap();

    drop(session);
    drop(server);
    host.shutdown();
    let _ = std::fs::remove_file(&socket);
    sample
}

fn consume_terminal_stream<F>(
    start: Instant,
    mut emulator: TerminalEmulator,
    replay_last_seq: u64,
    mut receive: F,
    process_rx: mpsc::Receiver<Duration>,
) -> CompletionSample
where
    F: FnMut(Duration) -> Result<TerminalStreamEvent, mpsc::RecvTimeoutError>,
{
    let mut previous_snapshot: Option<TerminalSnapshot> = None;
    let mut last_snapshot_at = start;
    let mut visible_gaps = Vec::new();
    let mut backlogged_visible_gaps = Vec::new();
    let mut bytes = 0_u64;
    let mut events = 0_u64;
    let mut pending_event = None;
    let mut gap_was_continuously_backlogged = false;
    let emulator_done;
    loop {
        let had_pending_event = pending_event.is_some();
        let event = match pending_event.take() {
            Some(event) => event,
            None => receive(COMPLETION_TIMEOUT).expect("terminal stream timed out"),
        };
        if !had_pending_event {
            gap_was_continuously_backlogged = false;
        }
        if event.seq() <= replay_last_seq {
            continue;
        }
        bytes += event.output_bytes() as u64;
        events += 1;
        let exited = matches!(event, TerminalStreamEvent::Exit { .. });
        emulator.apply(&event);
        let stream_backlogged = if exited {
            false
        } else {
            match receive(Duration::ZERO) {
                Ok(event) => {
                    pending_event = Some(event);
                    true
                }
                Err(mpsc::RecvTimeoutError::Timeout) => false,
                Err(mpsc::RecvTimeoutError::Disconnected) => false,
            }
        };
        gap_was_continuously_backlogged &= stream_backlogged;
        let now = Instant::now();
        if exited || now.duration_since(last_snapshot_at) >= VISIBLE_INTERVAL {
            previous_snapshot = Some(emulator.snapshot(previous_snapshot.as_ref()));
            let visible_at = Instant::now();
            visible_gaps.push(visible_at.duration_since(last_snapshot_at));
            if gap_was_continuously_backlogged {
                backlogged_visible_gaps.push(visible_at.duration_since(last_snapshot_at));
            }
            last_snapshot_at = visible_at;
            gap_was_continuously_backlogged = stream_backlogged;
        }
        if exited {
            emulator_done = Instant::now().duration_since(start);
            break;
        }
    }
    if previous_snapshot.is_none() {
        let _ = emulator.snapshot(None);
    }
    let visible = start.elapsed();
    let process = process_rx.recv_timeout(COMPLETION_TIMEOUT).unwrap();
    let catchup = emulator_done.saturating_sub(process);
    CompletionSample {
        process,
        emulator: emulator_done,
        visible,
        catchup,
        visible_gaps,
        backlogged_visible_gaps,
        bytes,
        events,
        server_queue_max_bytes: 0,
        server_queue_max_events: 0,
        client_queue_max_bytes: 0,
        client_queue_max_events: 0,
    }
}

fn barrier_args(workload: &str, workload_command: &str) -> Vec<String> {
    vec![
        "-c".to_owned(),
        format!("read _; {workload_command}"),
        "water-benchmark".to_owned(),
        workload.to_owned(),
    ]
}

fn benchmark_count(name: &str, default: usize) -> usize {
    std::env::var(name)
        .ok()
        .and_then(|value| value.parse().ok())
        .unwrap_or(default)
}

fn print_completion_summary(label: &str, samples: &[CompletionSample]) {
    eprintln!("=== WATER TERMINAL COMPLETION: {label} ===");
    print_duration_metric(label, "process", samples, |sample| sample.process);
    print_duration_metric(label, "emulator", samples, |sample| sample.emulator);
    print_duration_metric(label, "visible", samples, |sample| sample.visible);
    print_duration_metric(label, "catchup", samples, |sample| sample.catchup);
    let gaps = samples
        .iter()
        .flat_map(|sample| sample.visible_gaps.iter().copied())
        .collect::<Vec<_>>();
    eprintln!(
        "{label}.visible_gap p95={:.3}ms p99={:.3}ms max={:.3}ms frames={}",
        millis(percentile_duration(&gaps, 0.95)),
        millis(percentile_duration(&gaps, 0.99)),
        millis(gaps.iter().copied().max().unwrap_or_default()),
        gaps.len(),
    );
    let backlogged_gaps = samples
        .iter()
        .flat_map(|sample| sample.backlogged_visible_gaps.iter().copied())
        .collect::<Vec<_>>();
    eprintln!(
        "{label}.backlogged_visible_gap p95={:.3}ms p99={:.3}ms max={:.3}ms frames={}",
        millis(percentile_duration(&backlogged_gaps, 0.95)),
        millis(percentile_duration(&backlogged_gaps, 0.99)),
        millis(backlogged_gaps.iter().copied().max().unwrap_or_default()),
        backlogged_gaps.len(),
    );
    let bytes = samples.first().map(|sample| sample.bytes).unwrap_or(0);
    let events = samples.first().map(|sample| sample.events).unwrap_or(0);
    eprintln!("{label}.payload bytes={bytes} events={events}");
    eprintln!(
        "{label}.queue_peaks server_bytes={} server_events={} client_bytes={} client_events={}",
        samples
            .iter()
            .map(|sample| sample.server_queue_max_bytes)
            .max()
            .unwrap_or(0),
        samples
            .iter()
            .map(|sample| sample.server_queue_max_events)
            .max()
            .unwrap_or(0),
        samples
            .iter()
            .map(|sample| sample.client_queue_max_bytes)
            .max()
            .unwrap_or(0),
        samples
            .iter()
            .map(|sample| sample.client_queue_max_events)
            .max()
            .unwrap_or(0),
    );
}

fn print_duration_metric(
    label: &str,
    name: &str,
    samples: &[CompletionSample],
    value: impl Fn(&CompletionSample) -> Duration,
) {
    let values = samples.iter().map(value).collect::<Vec<_>>();
    let min = values.iter().copied().min().unwrap_or_default();
    let max = values.iter().copied().max().unwrap_or_default();
    eprintln!(
        "{label}.{name} median={:.3}ms p95={:.3}ms min={:.3}ms max={:.3}ms",
        millis(percentile_duration(&values, 0.5)),
        millis(percentile_duration(&values, 0.95)),
        millis(min),
        millis(max),
    );
}

fn percentile_duration(values: &[Duration], percentile: f64) -> Duration {
    if values.is_empty() {
        return Duration::ZERO;
    }
    let mut values = values.to_vec();
    values.sort_unstable();
    let index = ((values.len() - 1) as f64 * percentile).ceil() as usize;
    values[index]
}

fn duration_ratio(numerator: Duration, denominator: Duration) -> f64 {
    numerator.as_secs_f64() / denominator.as_secs_f64()
}

fn millis(duration: Duration) -> f64 {
    duration.as_secs_f64() * 1_000.0
}

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
        let mut events = vec![first];
        while let Ok(event) = stream.try_recv() {
            events.push(event);
        }
        for event in &events {
            received_events += 1;
            received_bytes += event.output_bytes() as u64;
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
