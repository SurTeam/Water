use std::io::{self, ErrorKind, Read, Write};
use std::path::PathBuf;
#[cfg(not(any(target_os = "linux", target_os = "macos")))]
use std::process::Command;
use std::sync::Arc;
use std::sync::atomic::AtomicBool;

#[cfg(unix)]
use std::os::fd::AsRawFd;
use std::sync::mpsc::{self, Receiver, Sender, SyncSender, TryRecvError, TrySendError};
use std::time::{Duration, Instant};

use alacritty_terminal::event::{Event, EventListener, OnResize, WindowSize};
use alacritty_terminal::grid::{Dimensions, Scroll};
use alacritty_terminal::term::{Config, Term, TermMode};
use alacritty_terminal::tty::{ChildEvent, EventedPty, EventedReadWrite, Pty};
use alacritty_terminal::vte::ansi::{Processor, Rgb};
use polling::{Event as PollEvent, Events, PollMode, Poller};

use crate::ids::TerminalId;

use super::TerminalTheme;
use super::model::{
    ScrollbackBudget, TerminalManagerEvent, TerminalRegistry, TerminalWorkerCommand,
    WakeupCallback, WakeupSlot,
};
use super::snapshot::{TerminalProcessState, TerminalSize, TerminalSnapshot};

const PTY_READ_WRITE_KEY: usize = 0;
const PTY_CHILD_EVENT_KEY: usize = 1;
const READ_BUFFER_BYTES: usize = 128 * 1024;
const MAX_COMMANDS_PER_TICK: usize = 64;
const MAX_PENDING_METADATA_PROBES: usize = 64;
// The drain budget bounds one poll tick, not total output: a large burst
// (for example `cat` of a multi-megabyte file) is delivered across many
// ticks, and each tick publishes at most one snapshot. The byte budget must
// stay large enough that a fast parser is throughput-bound instead of paying
// poll/publish overhead per megabyte (256 KB forced ~1500 ticks on the 61 MB
// benchmark stream); the time budget keeps input coalescing, metadata
// refresh, and shutdown latency bounded while a burst is in flight.
const MAX_PTY_BYTES_PER_TICK: usize = 16 * 1024 * 1024;
const MAX_PTY_DRAIN_TIME: Duration = Duration::from_millis(100);
/// The dedicated PTY reader pushes a batch after accumulating this much, or
/// when the writer goes quiet (burst -> idle transition).
const READER_PUSH_BYTES: usize = 64 * 1024;
/// In-flight batch ceiling (1024 x up to 256KB ~= 256MB): a large burst must
/// not backpressure the writer down to our parse rate, or `cat bigfile`
/// would be measured at parse speed instead of PTY speed.
const READER_CHANNEL_CAPACITY: usize = 1024;
/// The reader keeps spin-reading while data has been seen within this
/// window; afterwards it blocks on kqueue until the next chunk (zero idle
/// CPU). macOS refills land within tens of microseconds of a drain, so the
/// spin catches them without a kqueue round trip.
const READER_BURST_IDLE: Duration = Duration::from_millis(1);
/// Idle poll timeout; also bounds the reader thread's shutdown latency.
const READER_IDLE_POLL: Duration = Duration::from_millis(50);
/// A partially filled batch older than this is flushed even though the
/// stream has not paused (protects slow-but-continuous output, which
/// would otherwise wait for the 64KB push threshold).
const READER_MAX_BATCH_AGE: Duration = Duration::from_millis(5);
const PROCESS_METADATA_REFRESH_INTERVAL: Duration = Duration::from_millis(500);
/// A held key repeats every ~20ms on macOS fast repeat settings, while a
/// themed shell prompt (starship + git/async segments) needs a comparable
/// amount of time to accept a line and finish redrawing it. Delivering each
/// keystroke the instant it arrives lets repeats land inside the shell's own
/// redraw window, which makes ZLE abort the redisplay and fall back to raw
/// line feeds (visible as irregular blank rows between prompts). Terminals
/// with a run-loop-paced input path do not show this; water's direct write
/// path needs an explicit merge. While input commands keep arriving inside
/// this gap, consecutive bytes are coalesced into one PTY write, bounded by
/// the max wait and size below. Isolated keystrokes stay on the zero-extra-
/// latency immediate path.
const INPUT_COALESCE_GAP: Duration = Duration::from_millis(60);
const INPUT_COALESCE_MAX_WAIT: Duration = Duration::from_millis(24);
const INPUT_COALESCE_MAX_BYTES: usize = 4096;

/// PTY output within this window marks the foreground process as active for
/// agent-status purposes. The flip to quiet is observed on the regular
/// metadata refresh cycle, so it adds at most one event per output burst.
const PROCESS_ACTIVE_WINDOW: Duration = Duration::from_millis(2000);
/// Bound the argv probe so a pathological command line cannot inflate
/// metadata events or the model's detection input.
const MAX_CMDLINE_TOKENS: usize = 12;
const MAX_CMDLINE_TOKEN_BYTES: usize = 256;

/// Shared background executor for process-name/cwd lookups. PTY workers only
/// enqueue a probe and consume its result; no metadata query runs on the I/O
/// thread.
#[derive(Clone)]
pub(crate) struct ProcessMetadataExecutor {
    request_tx: SyncSender<ProcessMetadataRequest>,
}

struct ProcessMetadataRequest {
    pid: Option<u32>,
    fallback: Arc<ProcessMetadataFallback>,
    result_tx: Sender<ProcessMetadata>,
    wakeup: WakeupCallback,
}

impl ProcessMetadataExecutor {
    pub(crate) fn new() -> Self {
        Self::new_with_query(query_process_metadata)
    }

    fn new_with_query(
        query: impl Fn(Option<u32>, &ProcessMetadataFallback) -> ProcessMetadata + Send + 'static,
    ) -> Self {
        let (request_tx, request_rx) =
            mpsc::sync_channel::<ProcessMetadataRequest>(MAX_PENDING_METADATA_PROBES);
        if let Err(error) = std::thread::Builder::new()
            .name("water-terminal-metadata".to_owned())
            .spawn(move || {
                while let Ok(request) = request_rx.recv() {
                    let metadata = query(request.pid, &request.fallback);
                    if request.result_tx.send(metadata).is_ok() {
                        (request.wakeup)();
                    }
                }
            })
        {
            tracing::warn!(
                target: "water::pty",
                ?error,
                "failed to start terminal metadata worker"
            );
        }
        Self { request_tx }
    }

    fn request(&self, request: ProcessMetadataRequest) -> bool {
        match self.request_tx.try_send(request) {
            Ok(()) => true,
            Err(TrySendError::Full(_) | TrySendError::Disconnected(_)) => false,
        }
    }
}

pub(crate) struct WorkerConfig {
    terminal_id: TerminalId,
    size: TerminalSize,
    scrollback_lines: usize,
    inactive_scrollback_lines: usize,
    scrollback_budget: ScrollbackBudget,
    command_rx: Receiver<TerminalWorkerCommand>,
    fallback_process_name: String,
    fallback_cwd: PathBuf,
    fallback_cmdline: Vec<String>,
    registry: TerminalRegistry,
    event_tx: Sender<TerminalManagerEvent>,
    event_wakeup: Option<WakeupCallback>,
    wakeup_slot: WakeupSlot,
    metadata_executor: ProcessMetadataExecutor,
    theme: TerminalTheme,
}

pub(crate) struct WorkerMetadata {
    pub(crate) fallback_process_name: String,
    pub(crate) fallback_cwd: PathBuf,
    pub(crate) fallback_cmdline: Vec<String>,
}

pub(crate) struct WorkerChannels {
    pub(crate) registry: TerminalRegistry,
    pub(crate) event_tx: Sender<TerminalManagerEvent>,
    pub(crate) event_wakeup: Option<WakeupCallback>,
    pub(crate) wakeup_slot: WakeupSlot,
    pub(crate) metadata_executor: ProcessMetadataExecutor,
}

impl WorkerChannels {
    pub(crate) fn new(
        registry: TerminalRegistry,
        event_tx: Sender<TerminalManagerEvent>,
        event_wakeup: Option<WakeupCallback>,
        wakeup_slot: WakeupSlot,
        metadata_executor: ProcessMetadataExecutor,
    ) -> Self {
        Self {
            registry,
            event_tx,
            event_wakeup,
            wakeup_slot,
            metadata_executor,
        }
    }
}

impl WorkerConfig {
    #[allow(clippy::too_many_arguments)]
    pub(crate) fn new(
        terminal_id: TerminalId,
        size: TerminalSize,
        scrollback_lines: usize,
        inactive_scrollback_lines: usize,
        scrollback_budget: ScrollbackBudget,
        command_rx: Receiver<TerminalWorkerCommand>,
        channels: WorkerChannels,
        metadata: WorkerMetadata,
    ) -> Self {
        Self {
            terminal_id,
            size,
            scrollback_lines,
            inactive_scrollback_lines,
            scrollback_budget,
            command_rx,
            fallback_process_name: metadata.fallback_process_name,
            fallback_cwd: metadata.fallback_cwd,
            fallback_cmdline: metadata.fallback_cmdline,
            registry: channels.registry,
            event_tx: channels.event_tx,
            event_wakeup: channels.event_wakeup,
            wakeup_slot: channels.wakeup_slot,
            metadata_executor: channels.metadata_executor,
            theme: TerminalTheme::default(),
        }
    }

    pub(crate) fn with_theme(mut self, theme: TerminalTheme) -> Self {
        self.theme = theme;
        self
    }
}

pub(crate) fn run(config: WorkerConfig, mut pty: Pty) {
    boost_drain_thread_qos();
    let WorkerConfig {
        terminal_id,
        size,
        scrollback_lines,
        inactive_scrollback_lines,
        scrollback_budget,
        command_rx,
        fallback_process_name,
        fallback_cwd,
        fallback_cmdline,
        registry,
        event_tx,
        event_wakeup,
        wakeup_slot,
        metadata_executor,
        theme,
    } = config;
    let config = Config {
        scrolling_history: scrollback_lines,
        ..Config::default()
    };
    let proxy_events = std::sync::mpsc::channel();
    let proxy = WorkerEventProxy {
        sender: proxy_events.0,
        theme,
    };
    let mut term = Term::new(config, &size, proxy);
    let mut scrollback = ScrollbackState::new(
        terminal_id,
        scrollback_lines,
        inactive_scrollback_lines,
        scrollback_budget,
    );
    let _ = scrollback.reconcile(&mut term);
    let metadata_fallback = Arc::new(ProcessMetadataFallback {
        process_name: fallback_process_name,
        cwd: fallback_cwd,
        cmdline: fallback_cmdline,
    });
    let mut process_metadata = ProcessMetadata::from_fallback(&metadata_fallback);
    let mut processor = Processor::new();
    let poller = match Poller::new() {
        Ok(poller) => Arc::new(poller),
        Err(error) => {
            tracing::error!(
                target: "water::pty",
                terminal_id = %terminal_id,
                ?error,
                "failed to create PTY poller"
            );
            emit_manager_event(
                &event_tx,
                event_wakeup.as_ref(),
                TerminalManagerEvent::Exited {
                    terminal_id,
                    code: None,
                },
            );
            registry.mark_exited(terminal_id, None);
            scrollback.unregister();
            return;
        }
    };

    if let Err(error) = unsafe {
        pty.register(
            &poller,
            PollEvent::readable(PTY_READ_WRITE_KEY),
            PollMode::Level,
        )
    } {
        tracing::error!(
            target: "water::pty",
            terminal_id = %terminal_id,
            ?error,
            "failed to register PTY"
        );
        emit_manager_event(
            &event_tx,
            event_wakeup.as_ref(),
            TerminalManagerEvent::Exited {
                terminal_id,
                code: None,
            },
        );
        registry.mark_exited(terminal_id, None);
        scrollback.unregister();
        return;
    }

    let poller_for_wakeup = poller.clone();
    let worker_wakeup: WakeupCallback = Arc::new(move || {
        let _ = poller_for_wakeup.notify();
    });
    *wakeup_slot.lock().expect("terminal wakeup poisoned") = Some(worker_wakeup.clone());

    // Dedicated PTY reader thread: the macOS slave->master queue holds only
    // ~1KB ahead of the reader, so a reader that parses while reading makes
    // the writer wait once per 1KB (the 61MB benchmark paid ~34k kqueue
    // wakes for exactly that). The reader drains the master at PTY speed
    // and pushes batches; the worker parses from the channel and is woken
    // per batch.
    let pty_reader_stopped = Arc::new(AtomicBool::new(false));
    let (pty_data_rx, mut pty_reader_handle) =
        match spawn_pty_reader(&pty, worker_wakeup.clone(), pty_reader_stopped.clone()) {
            Ok((rx, handle)) => (rx, Some(handle)),
            Err(error) => {
                tracing::error!(
                    target: "water::pty",
                    terminal_id = %terminal_id,
                    ?error,
                    "failed to start PTY reader"
                );
                emit_manager_event(
                    &event_tx,
                    event_wakeup.as_ref(),
                    TerminalManagerEvent::Exited {
                        terminal_id,
                        code: None,
                    },
                );
                registry.mark_exited(terminal_id, None);
                scrollback.unregister();
                return;
            }
        };
    // The reader thread owns master reads; drop the master from the worker's
    // poller so a level-triggered readable event cannot race the reader and
    // spin the worker while the channel is being filled.
    let _ = poller.delete(pty.file());
    let reader_eof = Arc::new(AtomicBool::new(false));
    // Set when a drain hit the tick byte/time budget mid-burst; the next
    // poll must not sleep before the channel is drained again.
    let mut pty_data_pending = false;

    let (metadata_result_tx, metadata_result_rx) = mpsc::channel();
    let mut metadata_probe_in_flight = false;
    let mut last_output_at = Instant::now();
    let mut events = Events::new();
    let mut output_buffer = Vec::with_capacity(READ_BUFFER_BYTES);
    let mut snapshot_revision = 0_u64;
    let mut viewport_position = 0_i64;
    let mut last_process_metadata_request = Instant::now();
    let mut stop_requested = false;
    // Held-key repeats are merged into single PTY writes (see
    // INPUT_COALESCE_GAP). `pending_input` only ever holds input bytes.
    let mut pending_input: Vec<u8> = Vec::new();
    let mut pending_started = Instant::now();
    let mut last_input_at: Option<Instant> = None;
    let publisher = SnapshotPublisher {
        terminal_id,
        registry: &registry,
        event_tx: &event_tx,
        event_wakeup: event_wakeup.as_ref(),
    };
    publish_snapshot(
        &term,
        TerminalProcessState::Running,
        snapshot_revision,
        &publisher,
        &[],
        viewport_position,
        &process_metadata,
    );
    emit_process_metadata(
        &event_tx,
        event_wakeup.as_ref(),
        terminal_id,
        &process_metadata,
    );
    let _ = request_process_metadata_refresh(
        &metadata_executor,
        &pty,
        metadata_fallback.clone(),
        &metadata_result_tx,
        &worker_wakeup,
        &mut metadata_probe_in_flight,
    );

    'worker: loop {
        let mut command_batch_full = false;
        let mut input_activity = false;
        // Sticky: once the reader has disconnected (EOF), stop draining.
        let pty_eof = reader_eof.load(std::sync::atomic::Ordering::Relaxed);
        for command_index in 0..MAX_COMMANDS_PER_TICK {
            let command = match command_rx.try_recv() {
                Ok(command) => command,
                Err(TryRecvError::Empty) => break,
                Err(TryRecvError::Disconnected) => {
                    stop_requested = true;
                    break;
                }
            };
            command_batch_full = command_index + 1 == MAX_COMMANDS_PER_TICK;
            let effect = match command {
                TerminalWorkerCommand::SendText(bytes)
                | TerminalWorkerCommand::SendBytes(bytes) => {
                    let now = Instant::now();
                    let coalescing = !pending_input.is_empty()
                        || last_input_at.is_some_and(|at| {
                            now.saturating_duration_since(at) <= INPUT_COALESCE_GAP
                        });
                    last_input_at = Some(now);
                    input_activity = true;
                    if coalescing {
                        if pending_input.is_empty() {
                            pending_started = now;
                        }
                        pending_input.extend_from_slice(&bytes);
                        if pending_input.len() < INPUT_COALESCE_MAX_BYTES {
                            continue;
                        }
                        write_pending_input(
                            &mut pending_input,
                            &mut pty,
                            &mut term,
                            &mut scrollback,
                        )
                    } else {
                        apply_command(
                            TerminalWorkerCommand::SendBytes(bytes),
                            &mut pty,
                            &mut term,
                            &mut scrollback,
                        )
                    }
                }
                command => {
                    if !pending_input.is_empty() {
                        handle_input_effect(
                            write_pending_input(
                                &mut pending_input,
                                &mut pty,
                                &mut term,
                                &mut scrollback,
                            ),
                            terminal_id,
                            &term,
                            &publisher,
                            &mut snapshot_revision,
                            &mut viewport_position,
                            &mut input_activity,
                            &process_metadata,
                        );
                    }
                    apply_command(command, &mut pty, &mut term, &mut scrollback)
                }
            };
            match effect {
                Ok(CommandEffect::Continue {
                    dirty,
                    viewport_delta,
                    refresh_process,
                }) => {
                    input_activity |= refresh_process;
                    viewport_position = viewport_position.saturating_add(viewport_delta);
                    if dirty {
                        snapshot_revision = snapshot_revision.saturating_add(1);
                        publish_snapshot(
                            &term,
                            TerminalProcessState::Running,
                            snapshot_revision,
                            &publisher,
                            &[],
                            viewport_position,
                            &process_metadata,
                        );
                    }
                }
                Ok(CommandEffect::Stop) => {
                    stop_requested = true;
                    break;
                }
                Err(error) => {
                    tracing::warn!(
                        target: "water::pty",
                        terminal_id = %terminal_id,
                        ?error,
                        "terminal worker command failed"
                    );
                }
            }
        }
        if stop_requested {
            break 'worker;
        }

        events.clear();
        let metadata_timeout = PROCESS_METADATA_REFRESH_INTERVAL
            .saturating_sub(last_process_metadata_request.elapsed());
        let mut poll_timeout = if command_batch_full || pty_data_pending {
            // A burst is still in flight: the reader channel may hold
            // unparsed data, so do not sleep the poll before the next drain.
            Duration::ZERO
        } else {
            metadata_timeout
        };
        if !pending_input.is_empty() && !command_batch_full {
            poll_timeout =
                poll_timeout.min(INPUT_COALESCE_MAX_WAIT.saturating_sub(pending_started.elapsed()));
        }
        let poll_started = Instant::now();
        let poll_result = poller.wait(&mut events, Some(poll_timeout));
        if std::env::var("WATER_PTY_STATS").is_ok() {
            let el = poll_started.elapsed();
            POLL_NANOS.fetch_add(el.as_nanos() as u64, std::sync::atomic::Ordering::Relaxed);
            let ms = el.as_secs_f64() * 1e3;
            if ms > 5.0 {
                tracing::info!(
                    target: "water::pty",
                    ms = format_args!("{ms:.0}"),
                    timeout_ms = format_args!("{:?}", poll_timeout.as_millis()),
                    "poller wait"
                );
            }
        }
        if let Err(error) = poll_result {
            tracing::warn!(
                target: "water::pty",
                terminal_id = %terminal_id,
                ?error,
                "PTY poll failed"
            );
            break 'worker;
        }

        output_buffer.clear();
        let mut child_exited = None;
        let mut worker_stop = false;
        // A raw binary stream can contain bytes that happen to look like a
        // terminal query (for example, CSI `c`). Never feed the automatic
        // response for such a query into the shell's input queue.
        let mut binary_output = false;
        // The reader thread signals batches through poller.notify(); its
        // event carries the polling crate's private key, so it never
        // matches the child key below - the drain after the loop is the
        // handler for those wakes.
        let mut child_event = None;
        for event in events.iter() {
            if event.key == PTY_CHILD_EVENT_KEY {
                child_event = pty.next_child_event();
            }
        }
        if let Some(ChildEvent::Exited(status)) = child_event {
            child_exited = Some(status.and_then(|status| status.code()));
        }
        if !pty_eof {
            // A pinned viewport must be allowed to borrow the
            // remaining global history before parsing new rows. If
            // the normal limit were reached first, alacritty would
            // discard the very rows the user is looking at.
            let _ = scrollback.prepare_for_output(&mut term);
            let output_start = output_buffer.len();
            let drain_result =
                drain_data(&pty_data_rx, &mut processor, &mut term, &mut output_buffer);
            binary_output |= output_buffer[output_start..].contains(&0);
            match drain_result {
                Ok(ReadEffect::BudgetExhausted) => pty_data_pending = true,
                Ok(ReadEffect::Continue) => pty_data_pending = false,
                Ok(ReadEffect::Eof) => {
                    pty_data_pending = false;
                    reader_eof.store(true, std::sync::atomic::Ordering::Relaxed);
                }
                Err(error) if error.kind() == ErrorKind::WouldBlock => {}
                Err(error) => {
                    pty_data_pending = false;
                    tracing::debug!(
                        target: "water::pty",
                        terminal_id = %terminal_id,
                        ?error,
                        "PTY read ended"
                    );
                    reader_eof.store(true, std::sync::atomic::Ordering::Relaxed);
                }
            }
        }

        if process_proxy_events(
            &proxy_events.1,
            &mut pty,
            &event_tx,
            event_wakeup.as_ref(),
            terminal_id,
            binary_output,
        ) {
            worker_stop = true;
        }
        if !output_buffer.is_empty() {
            last_output_at = Instant::now();
        }
        let mut metadata_changed = stats_phase("metadata_apply", || {
            apply_process_metadata_result(
                &metadata_result_rx,
                &mut metadata_probe_in_flight,
                &mut process_metadata,
            )
        });
        // The worker owns the activity signal: probe results carry it through
        // unchanged, and it flips on output-burst edges without waiting for
        // the next probe round trip.
        let output_active = last_output_at.elapsed() < PROCESS_ACTIVE_WINDOW;
        if output_active != process_metadata.active {
            process_metadata.active = output_active;
            metadata_changed = true;
        }
        let metadata_due =
            last_process_metadata_request.elapsed() >= PROCESS_METADATA_REFRESH_INTERVAL;
        // The lookup itself runs on the shared metadata thread. Defer merely
        // scheduling new probes while output/input is active to avoid wasting
        // work on short-lived foreground-process transitions.
        if metadata_due {
            if output_buffer.is_empty() && !input_activity {
                let _ = request_process_metadata_refresh(
                    &metadata_executor,
                    &pty,
                    metadata_fallback.clone(),
                    &metadata_result_tx,
                    &worker_wakeup,
                    &mut metadata_probe_in_flight,
                );
            }
            last_process_metadata_request = Instant::now();
        }
        if !output_buffer.is_empty() {
            stats_phase("scrollback_sync", || {
                let _ = scrollback.sync(&mut term);
            });
        }
        if !output_buffer.is_empty() || metadata_changed {
            snapshot_revision = snapshot_revision.saturating_add(1);
            stats_phase("publish", || {
                let p_started = Instant::now();
                publish_snapshot(
                    &term,
                    TerminalProcessState::Running,
                    snapshot_revision,
                    &publisher,
                    &output_buffer,
                    viewport_position,
                    &process_metadata,
                );
                if std::env::var("WATER_PTY_STATS").is_ok() {
                    let el = p_started.elapsed();
                    PUBLISH_NANOS
                        .fetch_add(el.as_nanos() as u64, std::sync::atomic::Ordering::Relaxed);
                    PUBLISH_COUNT.fetch_add(1, std::sync::atomic::Ordering::Relaxed);
                    if el.as_millis() > 1 {
                        tracing::info!(
                            target: "water::pty",
                            ms = format_args!("{:.1}", el.as_secs_f64() * 1e3),
                            output_kb = output_buffer.len() / 1024,
                            "publish"
                        );
                    }
                }
            });
        }
        if metadata_changed {
            emit_process_metadata(
                &event_tx,
                event_wakeup.as_ref(),
                terminal_id,
                &process_metadata,
            );
        }

        // A coalesced burst must not stall waiting for more keys: once the
        // batch has waited long enough it goes out, even if the shell stayed
        // quiet and the poll returned only on the coalescing deadline.
        if !pending_input.is_empty() && pending_started.elapsed() >= INPUT_COALESCE_MAX_WAIT {
            let mut deadline_flush_activity = false;
            handle_input_effect(
                write_pending_input(&mut pending_input, &mut pty, &mut term, &mut scrollback),
                terminal_id,
                &term,
                &publisher,
                &mut snapshot_revision,
                &mut viewport_position,
                &mut deadline_flush_activity,
                &process_metadata,
            );
        }

        if let Some(code) = child_exited {
            // Stop the reader first so its in-flight batch is flushed into
            // the channel; the final drain below then picks it up.
            if let Some(reader_handle) = pty_reader_handle.take() {
                pty_reader_stopped.store(true, std::sync::atomic::Ordering::Relaxed);
                let _ = reader_handle.join();
            }
            // A final non-blocking drain avoids losing bytes that were already
            // queued in the PTY when SIGCHLD arrived.
            output_buffer.clear();
            let _ = scrollback.prepare_for_output(&mut term);
            let _ = drain_data(&pty_data_rx, &mut processor, &mut term, &mut output_buffer);
            if !output_buffer.is_empty() {
                let _ = scrollback.sync(&mut term);
                snapshot_revision = snapshot_revision.saturating_add(1);
                publish_snapshot(
                    &term,
                    TerminalProcessState::Running,
                    snapshot_revision,
                    &publisher,
                    &output_buffer,
                    viewport_position,
                    &process_metadata,
                );
            }
            emit_manager_event(
                &event_tx,
                event_wakeup.as_ref(),
                TerminalManagerEvent::Exited { terminal_id, code },
            );
            registry.mark_exited(terminal_id, code);
            break 'worker;
        }

        if worker_stop {
            if !stop_requested {
                emit_manager_event(
                    &event_tx,
                    event_wakeup.as_ref(),
                    TerminalManagerEvent::Exited {
                        terminal_id,
                        code: None,
                    },
                );
                registry.mark_exited(terminal_id, None);
            }
            break 'worker;
        }
    }

    if let Some(reader_handle) = pty_reader_handle.take() {
        pty_reader_stopped.store(true, std::sync::atomic::Ordering::Relaxed);
        let _ = reader_handle.join();
    }
    let _ = pty.deregister(&poller);
    report_pty_stats_if_enabled();
    *wakeup_slot.lock().expect("terminal wakeup poisoned") = None;
    scrollback.unregister();
}

struct ScrollbackState {
    terminal_id: TerminalId,
    base_limit: usize,
    inactive_limit: usize,
    current_limit: usize,
    focused: bool,
    budget: ScrollbackBudget,
}

impl ScrollbackState {
    fn new(
        terminal_id: TerminalId,
        base_limit: usize,
        inactive_limit: usize,
        budget: ScrollbackBudget,
    ) -> Self {
        Self {
            terminal_id,
            base_limit,
            inactive_limit,
            current_limit: base_limit,
            focused: false,
            budget,
        }
    }

    /// Applies a focus transition. Returns whether the state changed.
    fn set_focused(&mut self, focused: bool) -> bool {
        if self.focused == focused {
            return false;
        }
        self.focused = focused;
        true
    }

    /// Rows a terminal keeps without borrowing: the full configured limit
    /// while focused, the smaller inactive tail after focus is lost.
    fn effective_base(&self) -> usize {
        if self.focused {
            self.base_limit
        } else {
            self.base_limit.min(self.inactive_limit)
        }
    }

    fn prepare_for_output(&mut self, term: &mut Term<WorkerEventProxy>) -> bool {
        self.reconcile(term)
    }

    fn focus_latest(&mut self, term: &mut Term<WorkerEventProxy>) -> bool {
        let old_offset = term.grid().display_offset();
        if old_offset != 0 {
            term.scroll_display(Scroll::Bottom);
        }
        let changed = self.reconcile(term);
        changed || old_offset != term.grid().display_offset()
    }

    fn reconcile(&mut self, term: &mut Term<WorkerEventProxy>) -> bool {
        let old_offset = term.grid().display_offset();
        let old_limit = self.current_limit;
        let columns = term.columns();
        let alternate_screen = term.mode().contains(TermMode::ALT_SCREEN);
        let retained = if alternate_screen {
            0
        } else {
            terminal_history_size(term)
        };
        let target_limit = if alternate_screen {
            // The alternate screen has no user scrollback. Release any
            // temporary reservation held by the normal screen while keeping
            // the alternate grid bounded as well.
            let _ = self
                .budget
                .limit_for(self.terminal_id, 0, columns, 0, false);
            0
        } else {
            // Only a focused terminal that has been scrolled away from live
            // output borrows the unused global budget. Background tabs are
            // trimmed to their inactive tail and release their reservation.
            let borrow = self.focused && self.is_pinned(term);
            self.budget.limit_for(
                self.terminal_id,
                retained,
                columns,
                self.effective_base(),
                borrow,
            )
        };
        if target_limit != self.current_limit {
            term.grid_mut().update_history(target_limit);
            self.current_limit = target_limit;
        }
        let new_offset = term.grid().display_offset();
        self.budget
            .sync(self.terminal_id, terminal_history_size(term), columns);
        old_limit != self.current_limit || old_offset != new_offset
    }

    fn sync(&mut self, term: &mut Term<WorkerEventProxy>) -> bool {
        self.reconcile(term)
    }

    fn unregister(&self) {
        self.budget.unregister(self.terminal_id);
    }

    fn is_pinned(&self, term: &Term<WorkerEventProxy>) -> bool {
        !term.mode().contains(TermMode::ALT_SCREEN) && term.grid().display_offset() != 0
    }
}

fn terminal_history_size(term: &Term<WorkerEventProxy>) -> usize {
    term.grid()
        .total_lines()
        .saturating_sub(term.grid().screen_lines())
}

fn viewport_delta(old_offset: usize, new_offset: usize) -> i64 {
    new_offset as i64 - old_offset as i64
}

enum CommandEffect {
    Continue {
        dirty: bool,
        viewport_delta: i64,
        refresh_process: bool,
    },
    Stop,
}

fn apply_command(
    command: TerminalWorkerCommand,
    pty: &mut Pty,
    term: &mut Term<WorkerEventProxy>,
    scrollback: &mut ScrollbackState,
) -> io::Result<CommandEffect> {
    match command {
        TerminalWorkerCommand::SendText(bytes) | TerminalWorkerCommand::SendBytes(bytes) => {
            let old_offset = term.grid().display_offset();
            pty.writer().write_all(&bytes)?;
            let dirty = scrollback.focus_latest(term);
            Ok(CommandEffect::Continue {
                dirty,
                viewport_delta: viewport_delta(old_offset, term.grid().display_offset()),
                refresh_process: true,
            })
        }
        TerminalWorkerCommand::Resize(size) => {
            let old_offset = term.grid().display_offset();
            let window_size = WindowSize {
                num_lines: size.lines as u16,
                num_cols: size.columns as u16,
                cell_width: 0,
                cell_height: 0,
            };
            pty.on_resize(window_size);
            term.resize(size);
            let _ = scrollback.reconcile(term);
            // Resizing changes the published cell grid even when scrollback
            // limits and the viewport offset remain unchanged.
            Ok(CommandEffect::Continue {
                dirty: true,
                viewport_delta: viewport_delta(old_offset, term.grid().display_offset()),
                refresh_process: false,
            })
        }
        TerminalWorkerCommand::Scroll(lines) => {
            let old_offset = term.grid().display_offset();
            term.scroll_display(Scroll::Delta(lines));
            let dirty = scrollback.reconcile(term);
            let new_offset = term.grid().display_offset();
            Ok(CommandEffect::Continue {
                dirty: dirty || old_offset != new_offset,
                viewport_delta: viewport_delta(old_offset, new_offset),
                refresh_process: false,
            })
        }
        TerminalWorkerCommand::SetFocused(focused) => {
            scrollback.set_focused(focused);
            let old_offset = term.grid().display_offset();
            let dirty = scrollback.reconcile(term);
            let new_offset = term.grid().display_offset();
            Ok(CommandEffect::Continue {
                dirty: dirty || old_offset != new_offset,
                viewport_delta: viewport_delta(old_offset, new_offset),
                refresh_process: false,
            })
        }
        TerminalWorkerCommand::Shutdown => {
            kill_process_group(&mut *pty);
            Ok(CommandEffect::Stop)
        }
    }
}

/// Writes merged held-key input to the PTY in one pass.
fn write_pending_input(
    pending: &mut Vec<u8>,
    pty: &mut Pty,
    term: &mut Term<WorkerEventProxy>,
    scrollback: &mut ScrollbackState,
) -> io::Result<CommandEffect> {
    let bytes = std::mem::take(pending);
    let old_offset = term.grid().display_offset();
    pty.writer().write_all(&bytes)?;
    let dirty = scrollback.focus_latest(term);
    Ok(CommandEffect::Continue {
        dirty,
        viewport_delta: viewport_delta(old_offset, term.grid().display_offset()),
        refresh_process: true,
    })
}

#[allow(clippy::too_many_arguments)]
fn handle_input_effect(
    effect: io::Result<CommandEffect>,
    terminal_id: TerminalId,
    term: &Term<WorkerEventProxy>,
    publisher: &SnapshotPublisher<'_>,
    snapshot_revision: &mut u64,
    viewport_position: &mut i64,
    input_activity: &mut bool,
    process_metadata: &ProcessMetadata,
) {
    match effect {
        Ok(CommandEffect::Continue {
            dirty,
            viewport_delta,
            refresh_process,
        }) => {
            *input_activity |= refresh_process;
            *viewport_position = viewport_position.saturating_add(viewport_delta);
            if dirty {
                *snapshot_revision = snapshot_revision.saturating_add(1);
                publish_snapshot(
                    term,
                    TerminalProcessState::Running,
                    *snapshot_revision,
                    publisher,
                    &[],
                    *viewport_position,
                    process_metadata,
                );
            }
        }
        Ok(CommandEffect::Stop) => {}
        Err(error) => {
            tracing::warn!(
                target: "water::pty",
                %terminal_id,
                ?error,
                "terminal worker coalesced input write failed"
            );
        }
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum ReadEffect {
    Continue,
    BudgetExhausted,
    Eof,
}

#[derive(Debug)]
struct ProcessMetadataFallback {
    process_name: String,
    cwd: PathBuf,
    cmdline: Vec<String>,
}

#[derive(Debug, Clone, PartialEq, Eq)]
struct ProcessMetadata {
    process_name: String,
    cwd: String,
    /// Foreground process argv (bounded) used for coding-agent detection.
    cmdline: Vec<String>,
    /// True while the PTY has produced output inside PROCESS_ACTIVE_WINDOW.
    /// Computed on the worker thread, never by the metadata probe.
    active: bool,
}

impl ProcessMetadata {
    fn from_fallback(fallback: &ProcessMetadataFallback) -> Self {
        Self {
            process_name: fallback.process_name.clone(),
            cwd: fallback.cwd.display().to_string(),
            cmdline: fallback.cmdline.clone(),
            active: false,
        }
    }
}

fn query_process_metadata(pid: Option<u32>, fallback: &ProcessMetadataFallback) -> ProcessMetadata {
    let process_name = pid
        .and_then(query_process_name)
        .filter(|name| !name.is_empty())
        .unwrap_or_else(|| fallback.process_name.clone());
    let cwd = pid
        .and_then(query_process_cwd)
        .unwrap_or_else(|| fallback.cwd.display().to_string());
    let cmdline = pid
        .and_then(query_process_cmdline)
        .filter(|argv| !argv.is_empty())
        .unwrap_or_else(|| fallback.cmdline.clone());
    ProcessMetadata {
        process_name,
        cwd,
        cmdline,
        active: false,
    }
}

#[cfg(unix)]
fn foreground_process_id(pty: &Pty) -> Option<u32> {
    let foreground = unsafe { libc::tcgetpgrp(pty.file().as_raw_fd()) };
    if foreground > 0 {
        return u32::try_from(foreground).ok();
    }
    let child = pty.child().id();
    (child > 0).then_some(child)
}

#[cfg(not(unix))]
fn foreground_process_id(_pty: &Pty) -> Option<u32> {
    None
}

// Common platforms use native queries on the metadata worker instead of
// spawning `ps`/`lsof`, keeping probes cheap even with many terminals.
#[cfg(target_os = "linux")]
fn query_process_name(pid: u32) -> Option<String> {
    let name = std::fs::read_to_string(format!("/proc/{pid}/comm")).ok()?;
    let name = name.trim().trim_start_matches('-');
    (!name.is_empty()).then(|| name.to_owned())
}

#[cfg(target_os = "macos")]
fn query_process_name(pid: u32) -> Option<String> {
    let mut buffer = [0_u8; libc::PROC_PIDPATHINFO_MAXSIZE as usize];
    let length = unsafe {
        libc::proc_name(
            pid as libc::c_int,
            buffer.as_mut_ptr().cast(),
            buffer.len() as u32,
        )
    };
    let length = usize::try_from(length).ok()?.min(buffer.len());
    let name = String::from_utf8_lossy(&buffer[..length]);
    let name = name.trim_end_matches('\0').trim().trim_start_matches('-');
    (!name.is_empty()).then(|| name.to_owned())
}

#[cfg(not(any(target_os = "linux", target_os = "macos")))]
fn query_process_name(pid: u32) -> Option<String> {
    let output = Command::new("ps")
        .args(["-o", "comm=", "-p", &pid.to_string()])
        .output()
        .ok()?;
    if !output.status.success() {
        return None;
    }
    let name = String::from_utf8_lossy(&output.stdout);
    let name = name.trim().rsplit('/').next()?.trim_start_matches('-');
    (!name.is_empty()).then(|| name.to_owned())
}

#[cfg(target_os = "linux")]
fn query_process_cwd(pid: u32) -> Option<String> {
    std::fs::read_link(format!("/proc/{pid}/cwd"))
        .ok()
        .map(|path| path.display().to_string())
}

#[cfg(target_os = "macos")]
fn query_process_cwd(pid: u32) -> Option<String> {
    let mut info = std::mem::MaybeUninit::<libc::proc_vnodepathinfo>::zeroed();
    let info_size = std::mem::size_of::<libc::proc_vnodepathinfo>();
    let bytes = unsafe {
        libc::proc_pidinfo(
            pid as libc::c_int,
            libc::PROC_PIDVNODEPATHINFO,
            0,
            info.as_mut_ptr().cast(),
            info_size as libc::c_int,
        )
    };
    if bytes < info_size as libc::c_int {
        return None;
    }
    let info = unsafe { info.assume_init() };
    let path = info
        .pvi_cdir
        .vip_path
        .iter()
        .flatten()
        .copied()
        .take_while(|byte| *byte != 0)
        .map(|byte| byte as u8)
        .collect::<Vec<_>>();
    let path = String::from_utf8_lossy(&path);
    (!path.is_empty()).then(|| path.into_owned())
}

#[cfg(not(any(target_os = "linux", target_os = "macos")))]
fn query_process_cwd(_pid: u32) -> Option<String> {
    None
}

/// Bounded, control-character-free token for a cmdline argv entry.
#[cfg(any(target_os = "linux", target_os = "macos"))]
fn cmdline_token(token: &[u8]) -> String {
    let token = &token[..token.len().min(MAX_CMDLINE_TOKEN_BYTES)];
    let mut text = String::from_utf8_lossy(token).into_owned();
    text.retain(|ch| !ch.is_control());
    text
}

#[cfg(target_os = "macos")]
fn query_process_cmdline(pid: u32) -> Option<Vec<String>> {
    let mut mib = [libc::CTL_KERN, libc::KERN_PROCARGS2, pid as libc::c_int];
    let mut size = 0_usize;
    let ret = unsafe {
        libc::sysctl(
            mib.as_mut_ptr(),
            mib.len() as libc::c_uint,
            std::ptr::null_mut(),
            &mut size,
            std::ptr::null_mut(),
            0,
        )
    };
    if ret != 0 || size <= std::mem::size_of::<libc::c_int>() {
        return None;
    }
    let mut buffer = vec![0_u8; size];
    let ret = unsafe {
        libc::sysctl(
            mib.as_mut_ptr(),
            mib.len() as libc::c_uint,
            buffer.as_mut_ptr().cast(),
            &mut size,
            std::ptr::null_mut(),
            0,
        )
    };
    if ret != 0 {
        return None;
    }
    buffer.truncate(size);
    parse_kern_procargs2(&buffer)
}

/// KERN_PROCARGS2 layout: `argc`, the NUL-terminated exec path, alignment
/// padding, then `argc` NUL-terminated argv strings followed by the
/// environment. Only argv is parsed; malformed input yields `None`.
#[cfg(target_os = "macos")]
fn parse_kern_procargs2(buffer: &[u8]) -> Option<Vec<String>> {
    let int_size = std::mem::size_of::<libc::c_int>();
    let argc: usize = libc::c_int::from_ne_bytes(buffer[..int_size].try_into().ok()?)
        .try_into()
        .ok()?;
    if argc == 0 || argc > 4096 {
        return None;
    }
    let mut cursor = int_size;
    let exec_end = buffer[cursor..].iter().position(|byte| *byte == 0)?;
    cursor += exec_end + 1;
    while buffer.get(cursor).copied() == Some(0) {
        cursor += 1;
    }
    let mut argv = Vec::with_capacity(argc.min(MAX_CMDLINE_TOKENS));
    for _ in 0..argc {
        let Some(end) = buffer[cursor..].iter().position(|byte| *byte == 0) else {
            break;
        };
        argv.push(cmdline_token(&buffer[cursor..cursor + end]));
        cursor += end + 1;
        if argv.len() >= MAX_CMDLINE_TOKENS {
            break;
        }
    }
    (!argv.is_empty()).then_some(argv)
}

#[cfg(target_os = "linux")]
fn query_process_cmdline(pid: u32) -> Option<Vec<String>> {
    let raw = std::fs::read(format!("/proc/{pid}/cmdline")).ok()?;
    let argv: Vec<String> = raw
        .split(|byte| *byte == 0)
        .filter(|token| !token.is_empty())
        .take(MAX_CMDLINE_TOKENS)
        .map(cmdline_token)
        .collect();
    (!argv.is_empty()).then_some(argv)
}

#[cfg(not(any(target_os = "linux", target_os = "macos")))]
fn query_process_cmdline(_pid: u32) -> Option<Vec<String>> {
    None
}

fn request_process_metadata_refresh(
    executor: &ProcessMetadataExecutor,
    pty: &Pty,
    fallback: Arc<ProcessMetadataFallback>,
    result_tx: &Sender<ProcessMetadata>,
    wakeup: &WakeupCallback,
    in_flight: &mut bool,
) -> bool {
    if *in_flight {
        return false;
    }
    let request = ProcessMetadataRequest {
        pid: foreground_process_id(pty),
        fallback,
        result_tx: result_tx.clone(),
        wakeup: wakeup.clone(),
    };
    if !executor.request(request) {
        return false;
    }
    *in_flight = true;
    true
}

fn apply_process_metadata_result(
    result_rx: &Receiver<ProcessMetadata>,
    in_flight: &mut bool,
    current: &mut ProcessMetadata,
) -> bool {
    let mut next = match result_rx.try_recv() {
        Ok(next) => next,
        Err(TryRecvError::Empty) => return false,
        Err(TryRecvError::Disconnected) => {
            *in_flight = false;
            return false;
        }
    };
    // Activity is measured by the worker loop, not the probe thread; the
    // probe result always reports `false` here, so keep the live value.
    next.active = current.active;
    *in_flight = false;
    if next == *current {
        return false;
    }
    *current = next;
    true
}

fn emit_process_metadata(
    event_tx: &Sender<TerminalManagerEvent>,
    event_wakeup: Option<&WakeupCallback>,
    terminal_id: TerminalId,
    metadata: &ProcessMetadata,
) {
    emit_manager_event(
        event_tx,
        event_wakeup,
        TerminalManagerEvent::ProcessChanged {
            terminal_id,
            process_name: metadata.process_name.clone(),
            cwd: metadata.cwd.clone(),
            cmdline: metadata.cmdline.clone(),
            active: metadata.active,
        },
    );
}

fn kill_process_group(pty: &mut Pty) {
    #[cfg(unix)]
    {
        let current_pid = std::process::id() as libc::pid_t;
        let current_pgid = unsafe { libc::getpgrp() };
        let child_pid = pty.child().id() as libc::pid_t;
        let foreground_pid = unsafe { libc::tcgetpgrp(pty.file().as_raw_fd()) };
        let process_group = if foreground_pid > 1 {
            foreground_pid
        } else {
            (child_pid > 1)
                .then(|| foreground_process_id(pty).map(|pid| pid as libc::pid_t))
                .flatten()
                .unwrap_or_else(|| {
                    if child_pid > 1 {
                        unsafe { libc::getpgid(child_pid) }
                    } else {
                        -1
                    }
                })
        };
        if process_group > 1 && process_group != current_pid && process_group != current_pgid {
            unsafe {
                libc::killpg(process_group, libc::SIGKILL);
            }
        }
        if child_pid > 1 && child_pid != current_pid && child_pid != current_pgid {
            unsafe {
                libc::kill(child_pid, libc::SIGKILL);
            }
        }
    }

    #[cfg(not(unix))]
    let _ = pty;
}

fn stats_phase<T>(name: &str, f: impl FnOnce() -> T) -> T {
    if std::env::var("WATER_PTY_STATS").is_ok() {
        let started = Instant::now();
        let result = f();
        let ms = started.elapsed().as_secs_f64() * 1e3;
        if ms > 5.0 {
            tracing::info!(
                target: "water::pty",
                phase = name,
                ms = format_args!("{ms:.0}"),
                "slow worker phase"
            );
        }
        result
    } else {
        f()
    }
}

/// Consume reader-thread batches from the channel until the tick budget is
/// exhausted or the channel is empty. Non-blocking: the reader thread wakes
/// this poller on every push, so an empty read just means "no burst in
/// flight right now".
fn drain_data(
    rx: &Receiver<Vec<u8>>,
    processor: &mut Processor,
    term: &mut Term<WorkerEventProxy>,
    output_buffer: &mut Vec<u8>,
) -> io::Result<ReadEffect> {
    let drain_started = Instant::now();
    let result = {
        let stats = std::env::var("WATER_PTY_STATS").is_ok();
        let mut bytes_this_tick = 0_usize;
        let started = Instant::now();
        let mut effect = ReadEffect::Continue;

        loop {
            match rx.try_recv() {
                Ok(chunk) => {
                    if feed_chunk(
                        output_buffer,
                        processor,
                        term,
                        &chunk,
                        &mut bytes_this_tick,
                        &started,
                        stats,
                    ) {
                        effect = ReadEffect::BudgetExhausted;
                        break;
                    }
                }
                Err(TryRecvError::Empty) => break,
                Err(TryRecvError::Disconnected) => {
                    effect = ReadEffect::Eof;
                    break;
                }
            }
        }
        effect
    };
    if std::env::var("WATER_PTY_STATS").is_ok() {
        DRAIN_NANOS.fetch_add(
            drain_started.elapsed().as_nanos() as u64,
            std::sync::atomic::Ordering::Relaxed,
        );
        let now = std::time::UNIX_EPOCH.elapsed().expect("clock").as_nanos() as u64;
        let last = LAST_DRAIN_END.swap(now, std::sync::atomic::Ordering::Relaxed);
        let gap_ms = (now.saturating_sub(last)) as f64 / 1e6;
        if gap_ms > 5.0 {
            tracing::info!(
                target: "water::pty",
                gap_ms = format_args!("{gap_ms:.0}"),
                "drain gap (time between drain calls)"
            );
        }
    }
    Ok(result)
}

/// Dedicated PTY reader thread.
///
/// The macOS slave->master PTY queue holds only ~1KB ahead of the reader, so
/// a reader that parses while reading (the pre-split design) makes the
/// writer wait once per 1KB - the 61MB benchmark paid ~34k kqueue wakes for
/// that. This thread owns the master reads: it spin-reads while a burst is
/// flowing (catches the microsecond-scale refills without a kqueue round
/// trip), blocks on kqueue once the writer goes quiet (zero idle CPU), and
/// pushes byte batches into a bounded channel the worker parses. Each push
/// wakes the worker through its poller, so the PTY keeps draining at writer
/// speed even while the worker is busy parsing the previous batch.
fn spawn_pty_reader(
    pty: &Pty,
    wakeup: WakeupCallback,
    stopped: Arc<AtomicBool>,
) -> io::Result<(Receiver<Vec<u8>>, std::thread::JoinHandle<()>)> {
    let mut reader_file = pty.file().try_clone()?;
    let reader_poller = Poller::new()?;
    unsafe {
        reader_poller.add_with_mode(&reader_file, PollEvent::readable(0), PollMode::Level)?;
    }
    let (tx, rx) = mpsc::sync_channel(READER_CHANNEL_CAPACITY);

    let handle = std::thread::Builder::new()
        .name("water-terminal-reader".into())
        .spawn(move || {
            boost_drain_thread_qos();
            let stats = std::env::var("WATER_PTY_STATS").is_ok();
            let mut buf = [0_u8; 128 * 1024];
            let mut batch = Vec::with_capacity(256 * 1024);
            let mut out = Vec::new();
            let mut events = Events::new();
            let mut batch_started: Option<Instant> = None;
            // Move a full batch into the channel without reallocating the
            // reader's buffers; returns whether the worker is still attached.
            let push = |batch: &mut Vec<u8>, out: &mut Vec<u8>| -> bool {
                std::mem::swap(batch, out);
                tx.send(std::mem::take(out)).is_ok()
            };

            loop {
                // Burst phase: spin-read everything the writer has produced.
                let mut spin_start: Option<Instant> = None;
                loop {
                    match reader_file.read(&mut buf) {
                        Ok(0) => {
                            if !batch.is_empty() && push(&mut batch, &mut out) {
                                wakeup();
                            }
                            return;
                        }
                        Ok(bytes_read) => {
                            if batch.is_empty() {
                                batch_started = Some(Instant::now());
                            }
                            batch.extend_from_slice(&buf[..bytes_read]);
                            spin_start = None;
                            if batch.len() >= READER_PUSH_BYTES {
                                if push(&mut batch, &mut out) {
                                    wakeup();
                                } else {
                                    return;
                                }
                                batch_started = None;
                            }
                        }
                        Err(error) if error.kind() == ErrorKind::WouldBlock => {
                            // A stale partial batch must not wait for the
                            // 64KB threshold (slow-but-continuous streams).
                            let batch_stale = batch_started
                                .is_some_and(|at| at.elapsed() >= READER_MAX_BATCH_AGE);
                            match spin_start {
                                None => spin_start = Some(Instant::now()),
                                Some(start)
                                    if start.elapsed() < READER_BURST_IDLE && !batch_stale => {}
                                _ => break,
                            }
                        }
                        Err(error) if error.kind() == ErrorKind::Interrupted => continue,
                        Err(_) => {
                            if !batch.is_empty() && push(&mut batch, &mut out) {
                                wakeup();
                            }
                            return;
                        }
                    }
                }
                // Idle phase: the writer has been quiet - flush what we have
                // and block on kqueue until the next chunk (the bounded poll
                // also lets the thread notice the worker dropping the
                // channel on shutdown).
                if !batch.is_empty() {
                    if !push(&mut batch, &mut out) {
                        return;
                    }
                    batch_started = None;
                    wakeup();
                }
                let wait_started = Instant::now();
                let waited = reader_poller.wait(&mut events, Some(READER_IDLE_POLL));
                if stats {
                    GRACE_POLLS.fetch_add(1, std::sync::atomic::Ordering::Relaxed);
                    GRACE_NANOS.fetch_add(
                        wait_started.elapsed().as_nanos() as u64,
                        std::sync::atomic::Ordering::Relaxed,
                    );
                    if waited.map(|count| count > 0).unwrap_or(false) {
                        GRACE_WAKES.fetch_add(1, std::sync::atomic::Ordering::Relaxed);
                    }
                }
                if stopped.load(std::sync::atomic::Ordering::Relaxed) {
                    if !batch.is_empty() && push(&mut batch, &mut out) {
                        wakeup();
                    }
                    return;
                }
            }
        })?;
    Ok((rx, handle))
}

/// Optional throughput diagnostics (`WATER_PTY_STATS=1`): cumulative parse
/// time/bytes and total drain time, printed when a worker exits.
static PARSE_NANOS: std::sync::atomic::AtomicU64 = std::sync::atomic::AtomicU64::new(0);
static PARSE_BYTES: std::sync::atomic::AtomicU64 = std::sync::atomic::AtomicU64::new(0);
static DRAIN_NANOS: std::sync::atomic::AtomicU64 = std::sync::atomic::AtomicU64::new(0);
static POLL_NANOS: std::sync::atomic::AtomicU64 = std::sync::atomic::AtomicU64::new(0);
static PUBLISH_NANOS: std::sync::atomic::AtomicU64 = std::sync::atomic::AtomicU64::new(0);
static PUBLISH_COUNT: std::sync::atomic::AtomicU64 = std::sync::atomic::AtomicU64::new(0);
static GRACE_POLLS: std::sync::atomic::AtomicU64 = std::sync::atomic::AtomicU64::new(0);
static GRACE_WAKES: std::sync::atomic::AtomicU64 = std::sync::atomic::AtomicU64::new(0);
static GRACE_NANOS: std::sync::atomic::AtomicU64 = std::sync::atomic::AtomicU64::new(0);
static LAST_DRAIN_END: std::sync::atomic::AtomicU64 = std::sync::atomic::AtomicU64::new(0);

/// Feed one read chunk into the parser. Returns true when the tick budget
/// (bytes or time) is exhausted.
fn feed_chunk(
    output_buffer: &mut Vec<u8>,
    processor: &mut Processor,
    term: &mut Term<WorkerEventProxy>,
    chunk: &[u8],
    bytes_this_tick: &mut usize,
    started: &Instant,
    stats: bool,
) -> bool {
    output_buffer.extend_from_slice(chunk);
    let parse_started = if stats { Some(Instant::now()) } else { None };
    processor.advance(term, chunk);
    if let Some(parse_at) = parse_started {
        PARSE_NANOS.fetch_add(
            parse_at.elapsed().as_nanos() as u64,
            std::sync::atomic::Ordering::Relaxed,
        );
        PARSE_BYTES.fetch_add(chunk.len() as u64, std::sync::atomic::Ordering::Relaxed);
    }
    *bytes_this_tick = bytes_this_tick.saturating_add(chunk.len());
    *bytes_this_tick >= MAX_PTY_BYTES_PER_TICK || started.elapsed() >= MAX_PTY_DRAIN_TIME
}

fn report_pty_stats_if_enabled() {
    if std::env::var("WATER_PTY_STATS").is_ok() {
        let nanos = PARSE_NANOS.load(std::sync::atomic::Ordering::Relaxed);
        let bytes = PARSE_BYTES.load(std::sync::atomic::Ordering::Relaxed);
        let drain_nanos = DRAIN_NANOS.load(std::sync::atomic::Ordering::Relaxed);
        if bytes > 0 {
            tracing::info!(
                target: "water::pty",
                parsed_mb = bytes as f64 / 1e6,
                parse_secs = PARSE_NANOS.load(std::sync::atomic::Ordering::Relaxed) as f64 / 1e9,
                parse_mbs = if nanos > 0 { bytes as f64 / (nanos as f64 / 1e9) / 1e6 } else { 0.0 },
                drain_secs = drain_nanos as f64 / 1e9,
                poll_secs = POLL_NANOS.load(std::sync::atomic::Ordering::Relaxed) as f64 / 1e9,
                publish_secs = PUBLISH_NANOS.load(std::sync::atomic::Ordering::Relaxed) as f64 / 1e9,
                publish_count = PUBLISH_COUNT.load(std::sync::atomic::Ordering::Relaxed),
                grace_polls = GRACE_POLLS.load(std::sync::atomic::Ordering::Relaxed),
                grace_wakes = GRACE_WAKES.load(std::sync::atomic::Ordering::Relaxed),
                grace_secs = GRACE_NANOS.load(std::sync::atomic::Ordering::Relaxed) as f64 / 1e9,
                "pty throughput stats"
            );
        }
    }
}

fn process_proxy_events(
    proxy_events: &Receiver<ProxyAction>,
    pty: &mut Pty,
    event_tx: &Sender<TerminalManagerEvent>,
    event_wakeup: Option<&WakeupCallback>,
    terminal_id: TerminalId,
    binary_output: bool,
) -> bool {
    let mut should_stop = false;
    loop {
        match proxy_events.try_recv() {
            Ok(ProxyAction::TerminalResponse(_)) if binary_output => {}
            Ok(ProxyAction::TerminalResponse(bytes)) => {
                if let Err(error) = pty.writer().write_all(&bytes) {
                    tracing::debug!(
                        target: "water::pty",
                        terminal_id = %terminal_id,
                        ?error,
                        "failed to write terminal response"
                    );
                    should_stop = true;
                }
            }
            Ok(ProxyAction::Title(title)) => {
                emit_manager_event(
                    event_tx,
                    event_wakeup,
                    TerminalManagerEvent::TitleChanged { terminal_id, title },
                );
            }
            Ok(ProxyAction::Stop) => should_stop = true,
            Err(TryRecvError::Empty) => break,
            Err(TryRecvError::Disconnected) => {
                should_stop = true;
                break;
            }
        }
    }
    should_stop
}

struct SnapshotPublisher<'a> {
    terminal_id: TerminalId,
    registry: &'a TerminalRegistry,
    event_tx: &'a Sender<TerminalManagerEvent>,
    event_wakeup: Option<&'a WakeupCallback>,
}

fn publish_snapshot(
    term: &Term<WorkerEventProxy>,
    process: TerminalProcessState,
    revision: u64,
    publisher: &SnapshotPublisher<'_>,
    output: &[u8],
    viewport_position: i64,
    metadata: &ProcessMetadata,
) {
    let mut snapshot = TerminalSnapshot::from_term_with_viewport_position(
        publisher.terminal_id,
        term,
        process,
        revision,
        viewport_position,
    );
    snapshot.process_name = metadata.process_name.clone();
    snapshot.cwd = metadata.cwd.clone();
    if publisher
        .registry
        .publish(publisher.terminal_id, snapshot, output)
    {
        emit_manager_event(
            publisher.event_tx,
            publisher.event_wakeup,
            TerminalManagerEvent::OutputChanged {
                terminal_id: publisher.terminal_id,
            },
        );
    }
}

fn emit_manager_event(
    event_tx: &Sender<TerminalManagerEvent>,
    event_wakeup: Option<&WakeupCallback>,
    event: TerminalManagerEvent,
) {
    if event_tx.send(event).is_ok()
        && let Some(wakeup) = event_wakeup
    {
        wakeup();
    }
}

#[derive(Debug)]
enum ProxyAction {
    TerminalResponse(Vec<u8>),
    Title(String),
    Stop,
}

struct WorkerEventProxy {
    sender: Sender<ProxyAction>,
    theme: TerminalTheme,
}

fn terminal_dynamic_color(theme: TerminalTheme, index: usize) -> Option<Rgb> {
    let color = match index {
        256 => theme.foreground,
        257 => theme.background,
        258 => theme.cursor,
        _ => return None,
    };
    Some(Rgb {
        r: ((color >> 16) & 0xff) as u8,
        g: ((color >> 8) & 0xff) as u8,
        b: (color & 0xff) as u8,
    })
}

impl EventListener for WorkerEventProxy {
    fn send_event(&self, event: Event) {
        let action = match event {
            Event::PtyWrite(text) => Some(ProxyAction::TerminalResponse(text.into_bytes())),
            Event::Title(title) => Some(ProxyAction::Title(title)),
            Event::ResetTitle => Some(ProxyAction::Title(String::new())),
            Event::Exit => Some(ProxyAction::Stop),
            Event::ChildExit(_) => None,
            Event::MouseCursorDirty
            | Event::ClipboardStore(_, _)
            | Event::ClipboardLoad(_, _)
            | Event::TextAreaSizeRequest(_)
            | Event::CursorBlinkingChange
            | Event::Wakeup
            | Event::Bell => None,
            Event::ColorRequest(index, format) => terminal_dynamic_color(self.theme, index)
                .map(|color| ProxyAction::TerminalResponse(format(color).into_bytes())),
        };
        if let Some(action) = action {
            let _ = self.sender.send(action);
        }
    }
}

/// Raise this thread's QoS to USER_INTERACTIVE.
///
/// The PTY drain + VTE parse loop is the pipeline's throughput-critical
/// path. On Apple Silicon the scheduler tends to park long-lived threads
/// that have been idle on E-cores, where the same parse runs at roughly
/// half speed (measured: the 61MB ingest benchmark takes ~2.5x longer). macOS
/// has no supported hard-affinity API (THREAD_AFFINITY_POLICY is ignored on
/// arm64); QoS is the documented lever that biases placement toward
/// P-cores.
#[cfg(target_os = "macos")]
fn boost_drain_thread_qos() {
    unsafe extern "C" {
        fn pthread_set_qos_class_self_np(class: i32, relative_priority: i32) -> i32;
    }
    const QOS_CLASS_USER_INTERACTIVE: i32 = 0x21;
    if unsafe { pthread_set_qos_class_self_np(QOS_CLASS_USER_INTERACTIVE, 0) } != 0 {
        tracing::debug!(target: "water::pty", "worker QoS boost unavailable");
    }
}

#[cfg(not(target_os = "macos"))]
fn boost_drain_thread_qos() {}

#[cfg(test)]
mod tests {
    use super::*;

    fn test_term(size: TerminalSize, scrollback_lines: usize) -> Term<WorkerEventProxy> {
        let (sender, _receiver) = std::sync::mpsc::channel();
        Term::new(
            Config {
                scrolling_history: scrollback_lines,
                ..Config::default()
            },
            &size,
            WorkerEventProxy {
                sender,
                theme: TerminalTheme::default(),
            },
        )
    }

    #[test]
    fn dynamic_color_queries_are_written_back_to_the_pty() {
        let (sender, receiver) = mpsc::channel();
        let theme = TerminalTheme::new(0x123456, 0xabcdef, 0x654321);
        let proxy = WorkerEventProxy { sender, theme };
        let format =
            Arc::new(|color: Rgb| format!("rgb:{:02x}{:02x}{:02x}", color.r, color.g, color.b));

        proxy.send_event(Event::ColorRequest(257, format));

        assert!(matches!(
            receiver.recv_timeout(Duration::from_secs(1)).unwrap(),
            ProxyAction::TerminalResponse(bytes) if bytes == b"rgb:abcdef"
        ));
    }

    #[test]
    fn process_metadata_query_does_not_block_the_requesting_worker() {
        let (query_started_tx, query_started_rx) = mpsc::channel();
        let (query_release_tx, query_release_rx) = mpsc::channel();
        let executor = ProcessMetadataExecutor::new_with_query(move |_pid, fallback| {
            query_started_tx.send(()).unwrap();
            query_release_rx.recv().unwrap();
            ProcessMetadata::from_fallback(fallback)
        });
        let fallback = Arc::new(ProcessMetadataFallback {
            process_name: "fallback".to_owned(),
            cwd: std::env::current_dir().unwrap(),
            cmdline: Vec::new(),
        });
        let (result_tx, result_rx) = mpsc::channel();
        let (wakeup_tx, wakeup_rx) = mpsc::channel();
        let wakeup: WakeupCallback = Arc::new(move || {
            let _ = wakeup_tx.send(());
        });
        let (request_done_tx, request_done_rx) = mpsc::channel();
        let requester = std::thread::spawn(move || {
            let accepted = executor.request(ProcessMetadataRequest {
                pid: None,
                fallback,
                result_tx,
                wakeup,
            });
            request_done_tx.send(accepted).unwrap();
        });

        query_started_rx
            .recv_timeout(Duration::from_secs(1))
            .unwrap();
        let accepted = request_done_rx
            .recv_timeout(Duration::from_millis(100))
            .unwrap_or_else(|error| {
                let _ = query_release_tx.send(());
                panic!("metadata request blocked behind its query: {error}");
            });
        assert!(accepted);
        assert!(matches!(result_rx.try_recv(), Err(TryRecvError::Empty)));

        query_release_tx.send(()).unwrap();
        let metadata = result_rx.recv_timeout(Duration::from_secs(1)).unwrap();
        wakeup_rx.recv_timeout(Duration::from_secs(1)).unwrap();
        requester.join().unwrap();

        assert_eq!(metadata.process_name, "fallback");
        assert!(!metadata.cwd.is_empty());
    }

    #[test]
    fn sustained_output_yields_after_the_read_budget() {
        // Pre-fill the reader channel with enough chunks to exceed the tick
        // byte budget.
        let chunk = vec![b'x'; 4 * 1024];
        let (tx, rx) = mpsc::channel::<Vec<u8>>();
        for _ in 0..=MAX_PTY_BYTES_PER_TICK / chunk.len() {
            tx.send(chunk.clone()).unwrap();
        }
        drop(tx);
        let mut term = test_term(TerminalSize::new(80, 24), 100);
        let mut processor = Processor::new();
        let mut output = Vec::new();

        let effect = drain_data(&rx, &mut processor, &mut term, &mut output).unwrap();

        assert_eq!(effect, ReadEffect::BudgetExhausted);
        assert!(!output.is_empty());
        assert!(output.len() <= MAX_PTY_BYTES_PER_TICK);
    }

    #[test]
    fn pinned_view_stays_fixed_while_output_grows_temporary_scrollback() {
        use crate::terminal::scrollback_row_bytes;
        let terminal_id = TerminalId::new(1);
        let size = TerminalSize::new(16, 3);
        let budget = ScrollbackBudget::new(scrollback_row_bytes(size.columns) * 100);
        budget.register(terminal_id, 2, size.columns);
        let mut scrollback = ScrollbackState::new(terminal_id, 2, 2, budget);
        scrollback.set_focused(true);
        let mut term = test_term(size, 2);
        let mut processor = Processor::<alacritty_terminal::vte::ansi::StdSyncHandler>::new();

        processor.advance(
            &mut term,
            b"one\r\ntwo\r\nthree\r\nfour\r\nfive\r\nsix\r\nseven\r\n",
        );
        let _ = scrollback.sync(&mut term);
        term.scroll_display(Scroll::Delta(1));
        assert!(scrollback.reconcile(&mut term));
        let before =
            TerminalSnapshot::from_term(terminal_id, &term, TerminalProcessState::Running, 1);

        assert!(scrollback.current_limit > scrollback.base_limit);
        processor.advance(
            &mut term,
            b"eight\r\nnine\r\nten\r\neleven\r\ntwelve\r\nthirteen\r\n",
        );
        let _ = scrollback.sync(&mut term);
        let after =
            TerminalSnapshot::from_term(terminal_id, &term, TerminalProcessState::Running, 2);

        assert_eq!(after.visible_text(), before.visible_text());
        assert!(after.display_offset > before.display_offset);

        assert!(scrollback.focus_latest(&mut term));
        assert_eq!(term.grid().display_offset(), 0);
        assert_eq!(scrollback.current_limit, scrollback.base_limit);
        assert!(terminal_history_size(&term) <= scrollback.base_limit);
    }

    #[test]
    fn losing_focus_trims_history_to_the_inactive_tail() {
        use crate::terminal::scrollback_row_bytes;
        let terminal_id = TerminalId::new(2);
        let size = TerminalSize::new(16, 3);
        let budget = ScrollbackBudget::new(scrollback_row_bytes(size.columns) * 1_000);
        budget.register(terminal_id, 50, size.columns);
        let mut scrollback = ScrollbackState::new(terminal_id, 50, 5, budget);
        scrollback.set_focused(true);
        let mut term = test_term(size, 50);
        let mut processor = Processor::<alacritty_terminal::vte::ansi::StdSyncHandler>::new();

        let filler: String = (0..40).map(|row| format!("line {row}\r\n")).collect();
        processor.advance(&mut term, filler.as_bytes());
        let _ = scrollback.sync(&mut term);
        assert_eq!(scrollback.current_limit, 50);

        // Losing focus reclaims the history down to the inactive tail.
        scrollback.set_focused(false);
        let dirty = scrollback.reconcile(&mut term);
        assert!(dirty);
        assert_eq!(scrollback.current_limit, 5);
        assert!(terminal_history_size(&term) <= 5);
    }

    /// Always-on smoke check: parse a small chunk through the real VTE path
    /// and confirm the grid actually received the text. The full 61 MB
    /// benchmark stream only runs when `WATER_PARSE_BENCH` is set (it is a
    /// throughput probe for the terminal pipeline, not a correctness check).
    #[test]
    fn parse_throughput_smoke() {
        let path = "/Users/clearain/tmp/computer_use/benchmark.data";
        let data = match std::fs::read(path) {
            Ok(d) => d,
            Err(_) => return,
        };
        // WATER_PARSE_BENCH_SIZE=cxrows lets throughput probes compare grid
        // shapes (scroll cost is proportional to column count).
        let size = match std::env::var("WATER_PARSE_BENCH_SIZE") {
            Ok(spec) => {
                let (cols, rows) = spec
                    .split_once('x')
                    .and_then(|(c, r)| c.parse::<usize>().ok().zip(r.parse::<usize>().ok()))
                    .unwrap_or((113, 34));
                TerminalSize::new(cols, rows)
            }
            Err(_) => TerminalSize::new(113, 34),
        };
        let (sender, _receiver) = std::sync::mpsc::channel();
        let mut term = Term::new(
            Config {
                scrolling_history: 2000,
                ..Config::default()
            },
            &size,
            WorkerEventProxy {
                sender,
                theme: TerminalTheme::default(),
            },
        );
        let mut processor: Processor = Processor::new();
        let sample = &data[..64 * 1024.min(data.len())];
        let start = Instant::now();
        processor.advance(&mut term, sample);
        let elapsed = start.elapsed();
        let mb = data.len() as f64 / 1_000_000.0;
        println!(
            "PARSE-SMOKE: {:.1} KB in {:?} = {:.1} MB/s",
            sample.len() as f64 / 1024.0,
            elapsed,
            sample.len() as f64 / 1_000_000.0 / elapsed.as_secs_f64().max(f64::EPSILON),
        );
        // The stream starts with printable text; the grid must not be empty.
        assert!(term.grid().display_iter().any(|cell| cell.c != ' '));

        if std::env::var("WATER_PARSE_BENCH").is_ok() {
            // Interleaved A/B measurement: one-shot advance vs advancing in
            // 1KB slices (the worker's real PTY pattern). Interleaving
            // cancels out machine-state drift between the two variants.
            let run = |label: &str, chunk: usize| {
                let (s2, _r2) = std::sync::mpsc::channel();
                let mut term2 = Term::new(
                    Config {
                        scrolling_history: 2000,
                        ..Config::default()
                    },
                    &size,
                    WorkerEventProxy {
                        sender: s2,
                        theme: TerminalTheme::default(),
                    },
                );
                let mut p2: Processor = Processor::new();
                let start = Instant::now();
                if chunk >= data.len() {
                    p2.advance(&mut term2, &data);
                } else {
                    for piece in data.chunks(chunk) {
                        p2.advance(&mut term2, piece);
                    }
                }
                let secs = start.elapsed().as_secs_f64();
                println!(
                    "PARSE-BENCH [{label}]: {mb:.1} MB in {:.3}s = {:.1} MB/s",
                    secs,
                    mb / secs,
                );
                secs
            };
            let a1 = run("oneshot-A", data.len());
            let b1 = run("1kb-A", 1024);
            let a2 = run("oneshot-B", data.len());
            let b2 = run("1kb-B", 1024);
            println!(
                "PARSE-RATIO: oneshot={:.1} MB/s vs 1kb={:.1} MB/s (ratio {:.2})",
                mb / ((a1 + a2) / 2.0),
                mb / ((b1 + b2) / 2.0),
                (b1 + b2) / (a1 + a2),
            );
        }
    }
}
