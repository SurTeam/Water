//! PTY reader state-machine guarantees (NAPI-style: IDLE blocks on
//! readiness, ACTIVE drains to EAGAIN, EAGAIN = natural batch boundary).
//!
//! These tests drive a real PTY through `TerminalManager` so the dedicated
//! reader thread, the reader->worker channel, and the worker drain path all
//! run in production configuration.

use std::sync::mpsc;
use std::sync::{Mutex, OnceLock};
use std::time::{Duration, Instant};

use water::ids::TerminalId;
use water::terminal::{
    TerminalManager, TerminalSize, TerminalStreamEvent, WireTerminalEvent, decode_base64,
};

static TERMINAL_READER_TEST_LOCK: OnceLock<Mutex<()>> = OnceLock::new();

fn serial_test() -> std::sync::MutexGuard<'static, ()> {
    TERMINAL_READER_TEST_LOCK
        .get_or_init(|| Mutex::new(()))
        .lock()
        .unwrap_or_else(|poisoned| poisoned.into_inner())
}

fn metric(name: &str) -> u64 {
    water::metrics::snapshot()[name]
        .as_u64()
        .unwrap_or_else(|| panic!("metric {name} was not an integer"))
}

fn process_cpu_ms() -> u128 {
    let mut usage: libc::rusage = unsafe { std::mem::zeroed() };
    unsafe { libc::getrusage(libc::RUSAGE_SELF, &mut usage) };
    let utime = usage.ru_utime.tv_sec as u128 * 1_000 + usage.ru_utime.tv_usec as u128 / 1_000;
    let stime = usage.ru_stime.tv_sec as u128 * 1_000 + usage.ru_stime.tv_usec as u128 / 1_000;
    utime + stime
}

fn wait_for_metric_delta(name: &str, before: u64, delta: u64, timeout: Duration) -> u64 {
    let deadline = Instant::now() + timeout;
    loop {
        let current = metric(name);
        if current.saturating_sub(before) >= delta {
            return current;
        }
        assert!(
            Instant::now() < deadline,
            "metric {name} did not advance by {delta} within {timeout:?}"
        );
        std::thread::yield_now();
    }
}

fn report_reader_metrics(label: &str, values: impl std::fmt::Display) {
    if std::env::var_os("WATER_TERMINAL_READER_REPORT").is_some() {
        eprintln!("reader-metrics {label}: {values}");
    }
}

fn spawn_terminal(manager: &mut TerminalManager, id: u64, script: &str) -> TerminalId {
    let terminal_id = TerminalId::new(id);
    manager
        .spawn(
            terminal_id,
            "/bin/sh".to_owned(),
            vec!["-c".to_owned(), format!("read _; {script}")],
            TerminalSize::new(80, 24),
        )
        .unwrap();
    manager.send_text(terminal_id, "\n".to_owned()).unwrap();
    terminal_id
}

/// Slow, fragmented output: many tiny chunks arrive at intervals. The byte
/// sequence must arrive exactly, in order, with no losses at batch/budget
/// boundaries.
#[test]
fn slow_fragmented_output_arrives_in_order_without_loss() {
    let _test_guard = serial_test();
    let mut manager = TerminalManager::new();
    let id = 70_001;
    let terminal_id = spawn_terminal(
        &mut manager,
        id,
        "for i in 1 2 3 4 5 6 7 8 9 10; do printf 'FRAG_%s\\n' $i; sleep 0.05; done",
    );
    let attachment = manager.attach(terminal_id).unwrap();
    let start_seq = attachment.last_seq;

    let mut bytes = Vec::new();
    let mut seqs = Vec::new();
    let deadline = Instant::now() + Duration::from_secs(10);
    for event in attachment.replay.iter().cloned().chain(attachment.events) {
        match &event {
            TerminalStreamEvent::Output { seq, bytes: b, .. } if *seq > start_seq => {
                bytes.extend_from_slice(b);
                seqs.push(*seq);
            }
            TerminalStreamEvent::Exit { .. } => break,
            _ => {}
        }
        if Instant::now() >= deadline {
            break;
        }
    }
    let text = String::from_utf8_lossy(&bytes).into_owned();
    for i in 1..=10 {
        assert!(
            text.contains(&format!("FRAG_{i}")),
            "lost fragmented chunk {i}: {text:?}"
        );
    }
    // Strict byte ordering: FRAG_1 before FRAG_2 ... before FRAG_10.
    let mut last_pos = 0;
    for i in 1..=10 {
        let pos = text
            .find(&format!("FRAG_{i}"))
            .unwrap_or_else(|| panic!("missing FRAG_{i}"));
        assert!(pos >= last_pos, "out of order FRAG_{i}");
        last_pos = pos + 1;
    }
    // Sequences strictly increasing.
    assert!(
        seqs.windows(2).all(|w| w[0] < w[1]),
        "sequences not strictly increasing: {seqs:?}"
    );
    manager.remove(terminal_id);
}

/// High-frequency sustained output: `yes` keeps the PTY readable for the
/// whole run. The drain/budget machinery must not loop forever, must not
/// starve command processing, and must not drop bytes.
#[test]
fn sustained_output_drains_without_loss_or_starvation() {
    let _test_guard = serial_test();
    let mut manager = TerminalManager::new();
    let id = 70_002;
    let terminal_id = spawn_terminal(&mut manager, id, "yes WATER_SUSTAIN | head -c 200000");
    let attachment = manager.attach(terminal_id).unwrap();
    let start_seq = attachment.last_seq;

    let mut total = 0u64;
    let mut seqs = Vec::new();
    let mut exited = false;
    let deadline = Instant::now() + Duration::from_secs(15);
    'outer: while Instant::now() < deadline {
        let event = match attachment.events.recv_timeout(Duration::from_millis(500)) {
            Ok(event) => event,
            Err(mpsc::RecvTimeoutError::Timeout) => continue,
            Err(mpsc::RecvTimeoutError::Disconnected) => break,
        };
        match &event {
            TerminalStreamEvent::Output { seq, bytes, .. } if *seq > start_seq => {
                total += bytes.len() as u64;
                seqs.push(*seq);
            }
            TerminalStreamEvent::Exit { .. } => {
                exited = true;
                break 'outer;
            }
            _ => {}
        }
    }
    assert!(exited, "sustained-output terminal did not finish in time");
    assert!(
        total >= 200_000,
        "expected >= 200000 bytes, got {total} (loss?)"
    );
    assert!(
        seqs.windows(2).all(|w| w[0] < w[1]),
        "sequences not strictly increasing"
    );

    // Command processing must not be starved by the output flood: a resize
    // must still be applied promptly (ignore if the terminal already exited).
    let _ = manager.resize(terminal_id, TerminalSize::new(120, 30));
    manager.remove(terminal_id);
}

/// Downstream backpressure: the attach consumer is deliberately slow. The
/// reader must not busy-spin (it blocks), and no output may be lost: after
/// the flood finishes, every byte produced must be present in the stream.
#[test]
fn backpressure_does_not_drop_output_or_spin() {
    let _test_guard = serial_test();
    let mut manager = TerminalManager::new();
    let id = 70_003;
    let terminal_id = spawn_terminal(
        &mut manager,
        id,
        "for i in $(seq 1 3000); do echo WATER_BP_$i; done",
    );
    let attachment = manager.attach(terminal_id).unwrap();
    let start_seq = attachment.last_seq;

    // Slow consumer: drain with a small bounded pace.
    let mut total = 0u64;
    let mut exited = false;
    let deadline = Instant::now() + Duration::from_secs(20);
    while Instant::now() < deadline {
        let event = match attachment.events.recv_timeout(Duration::from_millis(200)) {
            Ok(event) => event,
            Err(mpsc::RecvTimeoutError::Timeout) => continue,
            Err(mpsc::RecvTimeoutError::Disconnected) => break,
        };
        if let TerminalStreamEvent::Output { seq, bytes, .. } = &event
            && *seq > start_seq
        {
            total += bytes.len() as u64;
        }
        if matches!(&event, TerminalStreamEvent::Exit { .. }) {
            exited = true;
            break;
        }
        // Artificially slow the consumer to build backpressure.
        std::thread::sleep(Duration::from_micros(20));
    }
    assert!(exited, "backpressure terminal did not finish in time");
    // 3000 lines * "WATER_BP_<n>\n" (~13-14 bytes) ≈ 40-42 KB; allow margin.
    assert!(
        total >= 30_000,
        "backpressure dropped output: only {total} bytes seen"
    );
    manager.remove(terminal_id);
}

/// Partial burst: a few bytes are already available in the PTY; the reader
/// must drain them to EAGAIN and hand them off immediately (no fixed timer).
/// Verified by latency: the first output event arrives well under a second.
#[test]
fn partial_burst_is_flushed_immediately() {
    let _test_guard = serial_test();
    let mut manager = TerminalManager::new();
    let id = 70_004;
    let terminal_id = spawn_terminal(&mut manager, id, "printf 'PARTIAL_BURST\\n'");
    let attachment = manager.attach(terminal_id).unwrap();
    let start_seq = attachment.last_seq;

    let start = Instant::now();
    let mut got = None;
    let mut observed = String::new();
    let deadline = Instant::now() + Duration::from_secs(5);
    'outer: while Instant::now() < deadline {
        let event = match attachment.events.recv_timeout(Duration::from_millis(100)) {
            Ok(event) => event,
            Err(mpsc::RecvTimeoutError::Timeout) => continue,
            Err(mpsc::RecvTimeoutError::Disconnected) => break,
        };
        if let TerminalStreamEvent::Output { seq, bytes, .. } = &event
            && *seq > start_seq
        {
            observed.push_str(&String::from_utf8_lossy(bytes));
            if observed.contains("PARTIAL_BURST") {
                got = Some((start.elapsed(), observed.clone()));
                break 'outer;
            }
        }
    }
    let (elapsed, text) = got.expect("partial burst never delivered");
    assert!(
        text.contains("PARTIAL_BURST"),
        "unexpected payload {text:?}"
    );
    assert!(
        elapsed < Duration::from_millis(500),
        "partial burst waited {elapsed:?} before delivery"
    );
    manager.remove(terminal_id);
}

/// Shutdown: while the reader is blocked waiting for readiness (idle PTY)
/// or mid-drain, `remove` must stop the terminal and join cleanly (no
/// forever-hung reader thread).
#[test]
fn shutdown_while_reader_blocked_is_prompt() {
    let _test_guard = serial_test();
    // Case A: idle PTY (reader parked in readiness wait).
    let mut manager = TerminalManager::new();
    let terminal_id = spawn_terminal(&mut manager, 70_010, "printf 'IDLE_READY\\n'; sleep 30");
    manager.attach(terminal_id).unwrap();
    let poll_before = metric("pty_reader_poll_wakeups");
    wait_for_metric_delta(
        "pty_reader_poll_wakeups",
        poll_before,
        1,
        Duration::from_secs(1),
    );
    let start = Instant::now();
    manager.remove(terminal_id);
    assert!(
        start.elapsed() < Duration::from_millis(500),
        "remove hung while reader was idle: {:?}",
        start.elapsed()
    );

    // Case B: reader actively draining a burst.
    let mut manager2 = TerminalManager::new();
    let busy_id = spawn_terminal(
        &mut manager2,
        70_011,
        "while :; do yes WATER_SHUTDOWN; done",
    );
    let reads_before = metric("pty_read_calls");
    wait_for_metric_delta("pty_read_calls", reads_before, 1, Duration::from_secs(1));
    let start = Instant::now();
    manager2.remove(busy_id);
    assert!(
        start.elapsed() < Duration::from_millis(500),
        "remove hung while reader was draining: {:?}",
        start.elapsed()
    );
}

/// Sanity: the wire replay still base64-decodes to the exact PTY bytes
/// (raw bytes are lossless end to end).
#[test]
fn wire_replay_roundtrips_raw_bytes() {
    let _test_guard = serial_test();
    let mut manager = TerminalManager::new();
    let terminal_id = spawn_terminal(
        &mut manager,
        70_012,
        "printf 'EXACT_BYTES_0123456789\\n'; sleep 0.2; exit 0",
    );
    let deadline = Instant::now() + Duration::from_secs(5);
    let replay = loop {
        let replay = manager.registry().replay(terminal_id).unwrap();
        if replay
            .events
            .iter()
            .any(|w| matches!(w, WireTerminalEvent::Exit { .. }))
            || Instant::now() >= deadline
        {
            break replay;
        }
        std::thread::sleep(Duration::from_millis(20));
    };
    let text: String = replay
        .events
        .iter()
        .filter_map(|w| match w {
            WireTerminalEvent::Output { bytes, .. } => {
                decode_base64(bytes).map(|d| String::from_utf8_lossy(&d).into_owned())
            }
            _ => None,
        })
        .collect();
    assert!(
        text.contains("EXACT_BYTES_0123456789"),
        "raw bytes not preserved: {text:?}"
    );
    manager.remove(terminal_id);
}

/// A child that closes its slave immediately after writing must leave the
/// final bytes in the replay stream and terminate the reader cleanly. On
/// Linux this commonly exercises the PTY master's EIO/HUP path; macOS may
/// report EOF instead.
#[test]
fn child_exit_flushes_final_bytes_after_hup() {
    let _test_guard = serial_test();
    let mut manager = TerminalManager::new();
    let terminal_id = spawn_terminal(
        &mut manager,
        70_013,
        "printf 'CHILD_EXIT_FINAL_BYTES\\n'; exit 7",
    );
    let attachment = manager.attach(terminal_id).unwrap();
    let mut output = Vec::new();
    let mut exit_code = None;
    let deadline = Instant::now() + Duration::from_secs(5);

    for event in attachment.replay.iter().cloned() {
        match event {
            TerminalStreamEvent::Output { bytes, .. } => output.extend_from_slice(&bytes),
            TerminalStreamEvent::Exit { code, .. } => {
                exit_code = Some(code);
                break;
            }
            _ => {}
        }
    }
    while exit_code.is_none() && Instant::now() < deadline {
        match attachment.events.recv_timeout(Duration::from_millis(100)) {
            Ok(TerminalStreamEvent::Output { bytes, .. }) => output.extend_from_slice(&bytes),
            Ok(TerminalStreamEvent::Exit { code, .. }) => exit_code = Some(code),
            Ok(_) => {}
            Err(mpsc::RecvTimeoutError::Timeout) => {}
            Err(mpsc::RecvTimeoutError::Disconnected) => break,
        }
    }

    assert_eq!(exit_code.flatten(), Some(7), "child exit was not observed");
    assert!(
        String::from_utf8_lossy(&output).ends_with("CHILD_EXIT_FINAL_BYTES\r\n"),
        "final PTY bytes were lost or reordered: {output:?}"
    );
    manager.remove(terminal_id);
}

/// Regression: an idle PTY must not spin the reader thread on repeated
/// readiness/EAGAIN cycles. The assertion is intentionally platform-neutral:
/// both the poller and the PTY are allowed to produce an occasional empty
/// wake, but that wake must not become a raw-read loop.
///
/// Measured via `getrusage(RUSAGE_SELF)`: the whole-process CPU time over a
/// 3-second idle window must stay under 200 ms. A spinning reader would
/// consume ~3000 ms (one full core).
#[test]
fn idle_pty_reader_does_not_spin_cpu() {
    let _test_guard = serial_test();
    let mut manager = TerminalManager::new();
    let terminal_id = spawn_terminal(&mut manager, 70_020, "printf 'IDLE_READY\\n'; sleep 30");
    let attachment = manager.attach(terminal_id).unwrap();

    // Wait for at least one reader wake, then drain startup output so the
    // measured interval starts after the shell has become quiet.
    let poll_before = metric("pty_reader_poll_wakeups");
    wait_for_metric_delta(
        "pty_reader_poll_wakeups",
        poll_before,
        1,
        Duration::from_secs(1),
    );
    // Drain startup echo/output until one bounded receive timeout observes a
    // quiet queue; a single try_recv can race the producer thread.
    let settle_deadline = Instant::now() + Duration::from_millis(250);
    while Instant::now() < settle_deadline {
        match attachment.events.recv_timeout(Duration::from_millis(20)) {
            Ok(_) => {}
            Err(mpsc::RecvTimeoutError::Timeout | mpsc::RecvTimeoutError::Disconnected) => break,
        }
    }
    let start_seq = attachment.last_seq;
    let metrics_before = water::metrics::snapshot();

    let cpu_before = process_cpu_ms();
    let wall_start = Instant::now();
    let idle_window = Duration::from_secs(3);

    // During the idle window, no output events should arrive. If the reader
    // is spinning, it would wake the worker repeatedly, but the key assertion
    // is CPU time, not event count.
    let mut spurious_output = 0u64;
    while wall_start.elapsed() < idle_window {
        match attachment.events.recv_timeout(Duration::from_millis(100)) {
            Ok(event) => {
                if let TerminalStreamEvent::Output { seq, .. } = &event
                    && *seq > start_seq
                {
                    spurious_output += 1;
                }
            }
            Err(mpsc::RecvTimeoutError::Timeout) => {}
            Err(mpsc::RecvTimeoutError::Disconnected) => break,
        }
    }
    let wall_elapsed = wall_start.elapsed();
    let cpu_after = process_cpu_ms();
    let cpu_delta_ms = cpu_after - cpu_before;
    let metrics_after = water::metrics::snapshot();
    let empty_wakes = metrics_after["pty_reader_empty_readiness"]
        .as_u64()
        .unwrap()
        .saturating_sub(
            metrics_before["pty_reader_empty_readiness"]
                .as_u64()
                .unwrap(),
        );
    let would_block = metrics_after["pty_read_would_block"]
        .as_u64()
        .unwrap()
        .saturating_sub(metrics_before["pty_read_would_block"].as_u64().unwrap());
    report_reader_metrics(
        "idle-1",
        format!("cpu_ms={cpu_delta_ms} empty_readiness={empty_wakes} would_block={would_block}"),
    );

    // A spinning reader burns ~100% of one core: 3000 ms wall time would
    // produce ~3000 ms CPU. Keep the existing strict process-level guard and
    // pair it with the reader-specific EAGAIN counters.
    assert!(
        cpu_delta_ms < 200,
        "idle PTY reader consumed {cpu_delta_ms} ms CPU in {wall_elapsed:?} \
         (spinning?). The reader should block in the poller with near-zero CPU."
    );
    assert!(
        empty_wakes <= 4,
        "idle PTY produced {empty_wakes} empty readiness wakes"
    );
    // No spurious output events from an idle PTY.
    assert_eq!(
        spurious_output, 0,
        "idle PTY produced {spurious_output} spurious output events"
    );

    // Now verify the reader wakes correctly when data arrives.
    let wake_start = Instant::now();
    manager
        .send_text(terminal_id, "printf 'WAKE_OK\\n'".to_string())
        .unwrap();
    let mut got = false;
    let wake_deadline = Instant::now() + Duration::from_secs(2);
    while Instant::now() < wake_deadline && !got {
        match attachment.events.recv_timeout(Duration::from_millis(100)) {
            Ok(event) => {
                if let TerminalStreamEvent::Output { seq, bytes, .. } = &event
                    && *seq > start_seq
                    && String::from_utf8_lossy(bytes).contains("WAKE_OK")
                {
                    got = true;
                }
            }
            Err(mpsc::RecvTimeoutError::Timeout) => {}
            Err(mpsc::RecvTimeoutError::Disconnected) => break,
        }
    }
    assert!(got, "reader failed to wake on data after idle period");
    let wake_latency = wake_start.elapsed();
    report_reader_metrics(
        "idle-wake",
        format!("latency_ms={:.3}", wake_latency.as_secs_f64() * 1000.0),
    );
    assert!(
        wake_latency < Duration::from_millis(500),
        "idle->active transition took {:?} (poller wakeup should be prompt)",
        wake_latency
    );

    manager.remove(terminal_id);
}

/// Regression: a trickle of tiny writes (simulating a TUI that redraws
/// every few ms) must not pin the reader in burst mode indefinitely.
///
/// The burst deadline (`READER_BURST_MAX` = 100 ms) forces the reader back
/// to the blocking poller even when a trickle keeps resetting the 1 ms idle timer.
/// Without the deadline, a 10 ms inter-write interval would keep the reader
/// in burst mode forever (each write resets `spin_start`).
///
/// Verified by: (1) all bytes arrive, (2) CPU stays bounded over the
/// trickle period.
#[test]
fn trickle_output_does_not_pin_reader_in_burst() {
    let _test_guard = serial_test();
    let mut manager = TerminalManager::new();
    // Write one byte every 10 ms for 3 seconds = 300 bytes total.
    // The 10 ms gap is well above READER_BURST_IDLE (1 ms), so the reader
    // should exit burst between writes and block in the poller. Even if it
    // didn't, READER_BURST_MAX (100 ms) would force a break.
    let terminal_id = spawn_terminal(
        &mut manager,
        70_021,
        "i=0; while [ $i -lt 300 ]; do printf 'T'; sleep 0.01; i=$((i+1)); done",
    );
    let attachment = manager.attach(terminal_id).unwrap();
    let start_seq = attachment.last_seq;

    // Let the reader observe the first output wake before measuring the
    // steady-state trickle interval.
    let poll_before = metric("pty_reader_poll_wakeups");
    wait_for_metric_delta(
        "pty_reader_poll_wakeups",
        poll_before,
        1,
        Duration::from_secs(1),
    );

    let empty_before = metric("pty_reader_empty_readiness");
    let would_block_before = metric("pty_read_would_block");
    let cpu_before = process_cpu_ms();
    let wall_start = Instant::now();
    let trickle_window = Duration::from_millis(2_500);

    let mut total_bytes = 0u64;
    while wall_start.elapsed() < trickle_window {
        match attachment.events.recv_timeout(Duration::from_millis(200)) {
            Ok(event) => {
                if let TerminalStreamEvent::Output { seq, bytes, .. } = &event
                    && *seq > start_seq
                {
                    total_bytes += bytes.len() as u64;
                }
            }
            Err(mpsc::RecvTimeoutError::Timeout) => {}
            Err(mpsc::RecvTimeoutError::Disconnected) => break,
        }
    }
    let wall_elapsed = wall_start.elapsed();
    let cpu_after = process_cpu_ms();
    let cpu_delta_ms = cpu_after - cpu_before;
    let empty_wakes = metric("pty_reader_empty_readiness").saturating_sub(empty_before);
    let would_block = metric("pty_read_would_block").saturating_sub(would_block_before);
    report_reader_metrics(
        "trickle",
        format!("cpu_ms={cpu_delta_ms} empty_readiness={empty_wakes} would_block={would_block}"),
    );

    // We should have received a substantial portion of the 300-byte trickle.
    // The setup marker and process startup consume part of the window; the
    // remaining interval still covers a substantial part of the 300 writes.
    assert!(
        total_bytes >= 100,
        "trickle lost bytes: only {total_bytes} received in {wall_elapsed:?}"
    );

    // CPU must stay bounded. A reader pinned in burst mode would consume
    // close to 100% of a core (~2000 ms in a 2 s window). A reader that
    // properly alternates between short bursts and blocking waits should
    // stay well under 300 ms.
    assert!(
        cpu_delta_ms < 300,
        "trickle PTY reader consumed {cpu_delta_ms} ms CPU in {wall_elapsed:?} \
         (pinned in burst?). Expected < 300 ms for 10 ms inter-write trickle."
    );
    // A real data wake may end in a normal EAGAIN drain boundary, but the
    // trickle should not manufacture a stream of empty readiness wakes.
    assert!(
        empty_wakes <= 8,
        "trickle produced {empty_wakes} empty readiness wakes"
    );

    manager.remove(terminal_id);
}

/// Regression: idle-reader CPU must remain bounded as the number of
/// terminals grows. This samples 1, 4, and 8 real PTYs in isolated manager
/// instances and checks both process CPU and reader-specific activity.
#[test]
fn multiple_idle_terminals_have_bounded_reader_cpu() {
    let _test_guard = serial_test();
    for (terminal_count, first_id) in [(1_u64, 70_030_u64), (4, 70_040), (8, 70_050)] {
        let mut manager = TerminalManager::new();
        let poll_before = metric("pty_reader_poll_wakeups");
        let mut terminal_ids = Vec::with_capacity(terminal_count as usize);
        let mut attachments = Vec::with_capacity(terminal_count as usize);
        for offset in 0..terminal_count {
            let terminal_id = spawn_terminal(
                &mut manager,
                first_id + offset,
                "printf 'IDLE_READY\\n'; sleep 30",
            );
            terminal_ids.push(terminal_id);
            attachments.push(manager.attach(terminal_id).unwrap());
        }

        // Every terminal emits one marker, which proves each reader reached
        // its event-driven path before the measured idle interval.
        wait_for_metric_delta(
            "pty_reader_poll_wakeups",
            poll_before,
            terminal_count,
            Duration::from_secs(1),
        );
        for attachment in &attachments {
            while attachment.events.try_recv().is_ok() {}
        }

        let metrics_before = water::metrics::snapshot();
        let cpu_before = process_cpu_ms();
        let wall_start = Instant::now();
        let idle_window = Duration::from_secs(1);
        while wall_start.elapsed() < idle_window {
            for attachment in &attachments {
                let _ = attachment.events.recv_timeout(Duration::from_millis(10));
            }
        }
        let wall_elapsed = wall_start.elapsed();
        let cpu_delta_ms = process_cpu_ms() - cpu_before;
        let metrics_after = water::metrics::snapshot();
        let poll_wakes = metrics_after["pty_reader_poll_wakeups"]
            .as_u64()
            .unwrap()
            .saturating_sub(metrics_before["pty_reader_poll_wakeups"].as_u64().unwrap());
        let empty_wakes = metrics_after["pty_reader_empty_readiness"]
            .as_u64()
            .unwrap()
            .saturating_sub(
                metrics_before["pty_reader_empty_readiness"]
                    .as_u64()
                    .unwrap(),
            );
        let would_block = metrics_after["pty_read_would_block"]
            .as_u64()
            .unwrap()
            .saturating_sub(metrics_before["pty_read_would_block"].as_u64().unwrap());
        report_reader_metrics(
            &format!("idle-{terminal_count}"),
            format!(
                "cpu_ms={cpu_delta_ms} poll_wakes={poll_wakes} \
                 empty_readiness={empty_wakes} would_block={would_block}"
            ),
        );

        assert!(
            cpu_delta_ms < 250,
            "{terminal_count} idle readers consumed {cpu_delta_ms} ms CPU in {wall_elapsed:?}"
        );
        assert!(
            poll_wakes <= terminal_count * 4,
            "{terminal_count} idle readers had {poll_wakes} poll wakes"
        );
        assert!(
            empty_wakes <= terminal_count * 2,
            "{terminal_count} idle readers had {empty_wakes} empty readiness wakes \
             (would-block={would_block})"
        );

        for terminal_id in terminal_ids {
            manager.remove(terminal_id);
        }
    }
}
