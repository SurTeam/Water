//! PTY worker: owns the PTY, raw output, replay history, and fanout.
//!
//! The steady-state output path is deliberately tiny:
//!
//! ```text
//! read PTY bytes
//! -> lightweight O(n) metadata scan (OSC title)
//! -> append to bounded replay ring
//! -> fanout ordered raw events to attached clients
//! ```
//!
//! There is no terminal emulator here. The GUI owns the Alacritty
//! `Term`/`Processor` and replays these raw events into it.

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

use alacritty_terminal::event::OnResize;
use alacritty_terminal::tty::{ChildEvent, EventedPty, EventedReadWrite, Pty};
use polling::{Event as PollEvent, Events, PollMode, Poller};

use crate::ids::TerminalId;
use crate::metrics;

use super::model::{
    TerminalManagerEvent, TerminalRegistry, TerminalWorkerCommand, WakeupCallback, WakeupSlot,
};
use super::replay::ReplayRing;
use super::snapshot::TerminalSize;
use super::stream::TerminalStreamEvent;
use super::MAX_OUTPUT_EVENT_BYTES;

const PTY_READ_WRITE_KEY: usize = 0;
const PTY_CHILD_EVENT_KEY: usize = 1;
const READER_BLOCK_BYTES: usize = 128 * 1024;
const MAX_COMMANDS_PER_TICK: usize = 64;
const MAX_PENDING_METADATA_PROBES: usize = 64;
/// One tick of the worker loop will coalesce at most this much PTY output
/// before yielding to command processing.
const MAX_PTY_BYTES_PER_TICK: usize = 16 * 1024 * 1024;
/// In-flight block ceiling (512 x 128KiB = 64MiB): a large burst must not
/// backpressure the writer down to our fanout rate, while keeping allocation
/// bounded and every consumed block reusable by the reader.
const READER_CHANNEL_CAPACITY: usize = 512;
/// The reader keeps spin-reading while data has been seen within this
/// window; afterwards it blocks on kqueue until the next chunk (zero idle
/// CPU). macOS refills land within tens of microseconds of a drain, so the
/// spin catches them without a kqueue round trip.
const READER_BURST_IDLE: Duration = Duration::from_millis(1);
/// Idle poll timeout; also bounds the reader thread's shutdown latency.
const READER_IDLE_POLL: Duration = Duration::from_millis(50);
/// A partially filled batch older than this is flushed even though the
/// stream has not paused (protects slow-but-continuous output, which
/// would otherwise wait for the 128KB push threshold).
const READER_MAX_BATCH_AGE: Duration = Duration::from_millis(5);
const PROCESS_METADATA_REFRESH_INTERVAL: Duration = Duration::from_millis(500);
/// PTY output within this window marks the foreground process as active for
/// agent-status purposes. The flip to quiet is observed on the regular
/// metadata refresh cycle, so it adds at most one event per output burst.
const PROCESS_ACTIVE_WINDOW: Duration = Duration::from_millis(2000);
/// Bound the argv probe so a pathological command line cannot inflate
/// metadata events or the model's detection input.
const MAX_CMDLINE_TOKENS: usize = 12;
const MAX_CMDLINE_TOKEN_BYTES: usize = 256;
/// A held key repeats every ~20ms on macOS fast repeat settings, while a
/// themed shell prompt (starship + git/async segments) needs a comparable
/// amount of time to accept a line and finish redrawing it. Delivering each
/// keystroke the instant it arrives lets repeats land inside the shell's own
/// redraw window, which makes ZLE abort the redisplay and fall back to raw
/// line feeds (visible as irregular blank rows between prompts). While input
/// commands keep arriving inside this gap, consecutive bytes are coalesced
/// into one PTY write, bounded by the max wait and size below. Isolated
/// keystrokes stay on the zero-extra-latency immediate path.
const INPUT_COALESCE_GAP: Duration = Duration::from_millis(60);
const INPUT_COALESCE_MAX_WAIT: Duration = Duration::from_millis(24);
const INPUT_COALESCE_MAX_BYTES: usize = 4096;
/// Bound for OSC title payloads; longer titles are ignored.
const MAX_TITLE_BYTES: usize = 1024;

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
    /// Shared bounded replay history (the registry reads it for attach and
    /// capture; the worker is its only writer).
    replay: Arc<ReplayRing>,
    command_rx: Receiver<TerminalWorkerCommand>,
    fallback_process_name: String,
    fallback_cwd: PathBuf,
    fallback_cmdline: Vec<String>,
    registry: TerminalRegistry,
    event_tx: Sender<TerminalManagerEvent>,
    event_wakeup: Option<WakeupCallback>,
    wakeup_slot: WakeupSlot,
    metadata_executor: ProcessMetadataExecutor,
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
    pub(crate) fn new(
        terminal_id: TerminalId,
        size: TerminalSize,
        replay: Arc<ReplayRing>,
        command_rx: Receiver<TerminalWorkerCommand>,
        channels: WorkerChannels,
        metadata: WorkerMetadata,
    ) -> Self {
        Self {
            terminal_id,
            size,
            replay,
            command_rx,
            fallback_process_name: metadata.fallback_process_name,
            fallback_cwd: metadata.fallback_cwd,
            fallback_cmdline: metadata.fallback_cmdline,
            registry: channels.registry,
            event_tx: channels.event_tx,
            event_wakeup: channels.event_wakeup,
            wakeup_slot: channels.wakeup_slot,
            metadata_executor: channels.metadata_executor,
        }
    }
}

pub(crate) fn run(config: WorkerConfig, mut pty: Pty) {
    boost_drain_thread_qos();
    let WorkerConfig {
        terminal_id,
        size,
        replay,
        command_rx,
        fallback_process_name,
        fallback_cwd,
        fallback_cmdline,
        registry,
        event_tx,
        event_wakeup,
        wakeup_slot,
        metadata_executor,
    } = config;
    let metadata_fallback = Arc::new(ProcessMetadataFallback {
        process_name: fallback_process_name,
        cwd: fallback_cwd,
        cmdline: fallback_cmdline,
    });
    let mut process_metadata = ProcessMetadata::from_fallback(&metadata_fallback);
    let mut current_size = size;
    // Attached client fanout. A slow or dead client backpressures the PTY
    // (blocking send) or is dropped (disconnected channel) — never the other
    // way around; events are already in the replay ring either way.
    let mut subscribers: Vec<SyncSender<TerminalStreamEvent>> = Vec::new();

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
        return;
    }

    let poller_for_wakeup = poller.clone();
    let worker_wakeup: WakeupCallback = Arc::new(move || {
        let _ = poller_for_wakeup.notify();
    });
    *wakeup_slot.lock().expect("terminal wakeup poisoned") = Some(worker_wakeup.clone());

    // Dedicated PTY reader thread: the macOS slave->master queue holds only
    // ~1KB ahead of the reader, so a reader that works while reading makes
    // the writer wait once per 1KB. The reader drains the master at PTY
    // speed and pushes reusable byte blocks; the worker coalesces them per
    // tick and appends raw output to the replay ring.
    let pty_reader_stopped = Arc::new(AtomicBool::new(false));
    let PtyReader {
        filled_rx: pty_data_rx,
        free_tx: pty_free_tx,
        wakeup_pending: parser_wakeup_pending,
        handle,
    } = match spawn_pty_reader(&pty, worker_wakeup.clone(), pty_reader_stopped.clone()) {
        Ok(reader) => reader,
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
            return;
        }
    };
    let mut pty_reader_handle = Some(handle);
    // The reader thread owns master reads; drop the master from the worker's
    // poller so a level-triggered readable event cannot race the reader and
    // spin the worker while the channel is being filled.
    let _ = poller.delete(pty.file());
    let reader_eof = Arc::new(AtomicBool::new(false));
    // Set when a drain hit the tick byte budget mid-burst; the next poll
    // must not sleep before the channel is drained again.
    let mut pty_data_pending = false;

    let (metadata_result_tx, metadata_result_rx) = mpsc::channel();
    let mut metadata_probe_in_flight = false;
    let mut last_output_at = Instant::now();
    let mut last_title: Option<String> = None;
    let mut last_process_metadata_request = Instant::now();
    let mut stop_requested = false;
    // Held-key repeats are merged into single PTY writes (see
    // INPUT_COALESCE_GAP). `pending_input` only ever holds input bytes.
    let mut pending_input: Vec<u8> = Vec::new();
    let mut pending_started = Instant::now();
    let mut last_input_at: Option<Instant> = None;
    let mut batch: Vec<u8> = Vec::with_capacity(READER_BLOCK_BYTES);
    let mut title_scanner = TitleScanner::new();

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

            // Flush coalesced input before a non-input command so ordering
            // with the PTY stays predictable.
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
                        write_pending_input(&mut pending_input, &mut pty)
                    } else {
                        let _ = pty.writer().write_all(&bytes);
                        Ok(())
                    }
                }
                TerminalWorkerCommand::Resize(new_size) => {
                    // Drain every byte already observed by the reader before
                    // applying the PTY geometry. This makes the stream order
                    // authoritative: Output(old) -> Resize -> Output(new).
                    let drain_result = drain_data(
                        &pty_data_rx,
                        &pty_free_tx,
                        &parser_wakeup_pending,
                        &mut batch,
                        usize::MAX,
                    );
                    if !batch.is_empty() {
                        publish_raw_output(
                            terminal_id,
                            current_size,
                            &batch,
                            &replay,
                            &mut subscribers,
                            &registry,
                            &mut title_scanner,
                            &mut last_title,
                            &event_tx,
                            event_wakeup.as_ref(),
                        );
                        batch.clear();
                        last_output_at = Instant::now();
                    }
                    if matches!(drain_result, Ok(ReadEffect::Eof)) {
                        reader_eof.store(true, std::sync::atomic::Ordering::Relaxed);
                    }
                    let window_size = alacritty_terminal::event::WindowSize {
                        num_lines: new_size.lines as u16,
                        num_cols: new_size.columns as u16,
                        cell_width: 0,
                        cell_height: 0,
                    };
                    let result = (|| {
                        pty.on_resize(window_size);
                        Ok(())
                    })();
                    if result.is_ok() {
                        current_size = new_size;
                        let seq = replay.push_resize(new_size);
                        metrics::inc(metrics::terminal_resize_events());
                        metrics::inc(metrics::terminal_stream_events());
                        fanout(
                            &mut subscribers,
                            &TerminalStreamEvent::Resize {
                                seq,
                                size: new_size,
                            },
                        );
                    }
                    result
                }
                TerminalWorkerCommand::Attach { sender, reply } => {
                    // Register the subscriber first, then snapshot the ring:
                    // events delivered between the two are deduplicated by
                    // the client via sequence, so nothing is ever lost.
                    subscribers.push(sender);
                    let replay_events = replay.to_stream();
                    let _ = reply.send((replay_events, replay.last_seq()));
                    continue;
                }
                TerminalWorkerCommand::Shutdown => {
                    kill_process_group(&mut pty);
                    stop_requested = true;
                    break;
                }
            };
            if let Err(error) = effect {
                tracing::warn!(
                    target: "water::pty",
                    terminal_id = %terminal_id,
                    ?error,
                    "terminal worker command failed"
                );
            }
        }

        if stop_requested {
            break 'worker;
        }

        {
            let mut events = Events::new();
            let metadata_timeout = PROCESS_METADATA_REFRESH_INTERVAL
                .saturating_sub(last_process_metadata_request.elapsed());
            let mut poll_timeout = if command_batch_full || pty_data_pending {
                // A burst is still in flight: the reader channel may hold
                // unconsumed data, so do not sleep the poll before the next
                // drain.
                Duration::ZERO
            } else {
                metadata_timeout
            };
            if !pending_input.is_empty() && !command_batch_full {
                poll_timeout = poll_timeout
                    .min(INPUT_COALESCE_MAX_WAIT.saturating_sub(pending_started.elapsed()));
            }
            let poll_result = poller.wait(&mut events, Some(poll_timeout));
            if let Err(error) = poll_result {
                tracing::warn!(
                    target: "water::pty",
                    terminal_id = %terminal_id,
                    ?error,
                    "PTY poll failed"
                );
                break 'worker;
            }

            let mut child_exited = None;
            let mut child_event = None;
            for event in events.iter() {
                if event.key == PTY_CHILD_EVENT_KEY {
                    child_event = pty.next_child_event();
                }
            }
            if let Some(ChildEvent::Exited(status)) = child_event {
                child_exited = Some(status.and_then(|status| status.code()));
            }

            // Drain everything the reader queued this tick into one batch.
            let mut had_output = false;
            if !reader_eof.load(std::sync::atomic::Ordering::Relaxed) {
                let drain_result = drain_data(
                    &pty_data_rx,
                    &pty_free_tx,
                    &parser_wakeup_pending,
                    &mut batch,
                    MAX_PTY_BYTES_PER_TICK,
                );
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
            if !batch.is_empty() {
                had_output = true;
                publish_raw_output(
                    terminal_id,
                    current_size,
                    &batch,
                    &replay,
                    &mut subscribers,
                    &registry,
                    &mut title_scanner,
                    &mut last_title,
                    &event_tx,
                    event_wakeup.as_ref(),
                );
                batch.clear();
            }
            if had_output {
                last_output_at = Instant::now();
            }

            let mut metadata_changed = apply_process_metadata_result(
                &metadata_result_rx,
                &mut metadata_probe_in_flight,
                &mut process_metadata,
            );
            // The worker owns the activity signal: probe results carry it
            // through unchanged, and it flips on output-burst edges without
            // waiting for the next probe round trip.
            let output_active = last_output_at.elapsed() < PROCESS_ACTIVE_WINDOW;
            if output_active != process_metadata.active {
                process_metadata.active = output_active;
                metadata_changed = true;
            }
            let metadata_due =
                last_process_metadata_request.elapsed() >= PROCESS_METADATA_REFRESH_INTERVAL;
            if metadata_due {
                if !had_output && !input_activity {
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
            if metadata_changed {
                emit_process_metadata(
                    &event_tx,
                    event_wakeup.as_ref(),
                    terminal_id,
                    &process_metadata,
                );
            }

            // Deadline flush for coalesced input.
            if !pending_input.is_empty() && pending_started.elapsed() >= INPUT_COALESCE_MAX_WAIT {
                let _ = write_pending_input(&mut pending_input, &mut pty);
            }

            if let Some(code) = child_exited {
                // Stop the reader first so its in-flight batch is flushed
                // into the channel; the final drain below picks it up.
                if let Some(reader_handle) = pty_reader_handle.take() {
                    pty_reader_stopped.store(true, std::sync::atomic::Ordering::Relaxed);
                    let _ = reader_handle.join();
                }
                // A final non-blocking drain avoids losing bytes that were
                // already queued in the PTY when SIGCHLD arrived.
                let mut final_batch = Vec::new();
                let _ = drain_data(
                    &pty_data_rx,
                    &pty_free_tx,
                    &parser_wakeup_pending,
                    &mut final_batch,
                    MAX_PTY_BYTES_PER_TICK,
                );
                if !final_batch.is_empty() {
                    publish_raw_output(
                        terminal_id,
                        current_size,
                        &final_batch,
                        &replay,
                        &mut subscribers,
                        &registry,
                        &mut title_scanner,
                        &mut last_title,
                        &event_tx,
                        event_wakeup.as_ref(),
                    );
                }
                // Record the exit in the ordered stream, then fanout.
                let seq = replay.finish(code);
                metrics::inc(metrics::terminal_stream_events());
                fanout(&mut subscribers, &TerminalStreamEvent::Exit { seq, code });
                emit_manager_event(
                    &event_tx,
                    event_wakeup.as_ref(),
                    TerminalManagerEvent::Exited { terminal_id, code },
                );
                registry.mark_exited(terminal_id, code);
                break 'worker;
            }
        }
    }

    if let Some(reader_handle) = pty_reader_handle.take() {
        pty_reader_stopped.store(true, std::sync::atomic::Ordering::Relaxed);
        let _ = reader_handle.join();
    }
    let _ = pty.deregister(&poller);
    *wakeup_slot.lock().expect("terminal wakeup poisoned") = None;
}

/// Appends one raw PTY batch to replay and fans out bounded output events.
/// The only parsing here is the linear OSC title scanner used by control
/// metadata; no screen/cursor/viewport state is constructed.
#[allow(clippy::too_many_arguments)]
fn publish_raw_output(
    terminal_id: TerminalId,
    size: TerminalSize,
    bytes: &[u8],
    replay: &ReplayRing,
    subscribers: &mut Vec<SyncSender<TerminalStreamEvent>>,
    registry: &TerminalRegistry,
    title_scanner: &mut TitleScanner,
    last_title: &mut Option<String>,
    event_tx: &Sender<TerminalManagerEvent>,
    event_wakeup: Option<&WakeupCallback>,
) {
    if bytes.is_empty() {
        return;
    }
    title_scanner.feed(bytes, |title| {
        if last_title.as_deref() != Some(title) {
            let title = title.to_owned();
            *last_title = Some(title.clone());
            emit_manager_event(
                event_tx,
                event_wakeup,
                TerminalManagerEvent::TitleChanged { terminal_id, title },
            );
        }
    });

    for chunk in bytes.chunks(MAX_OUTPUT_EVENT_BYTES) {
        let piece: Arc<[u8]> = chunk.to_vec().into();
        let seq = replay.push_output(size, piece.clone());
        metrics::inc(metrics::terminal_stream_events());
        metrics::add(metrics::terminal_output_bytes_sent(), piece.len());
        fanout(
            subscribers,
            &TerminalStreamEvent::Output {
                seq,
                size,
                bytes: piece,
            },
        );
    }
    registry.publish_output(terminal_id, bytes);
}

/// Sends an event to every attached client.
///
/// `SyncSender::send` blocks while the client queue is full: a slow client
/// backpressures the PTY (tmux semantics) instead of dropping output. Only a
/// disconnected receiver is dropped; the replay ring remains the resync
/// source for any client that falls behind and re-attaches.
fn fanout(subscribers: &mut Vec<SyncSender<TerminalStreamEvent>>, event: &TerminalStreamEvent) {
    subscribers.retain(|sender| sender.send(event.clone()).is_ok());
}

fn write_pending_input(pending: &mut Vec<u8>, pty: &mut Pty) -> io::Result<()> {
    let bytes = std::mem::take(pending);
    pty.writer().write_all(&bytes)
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

/// Consume reader-thread batches from the channel into `batch` until the tick
/// byte budget is exhausted or the channel is empty.
fn drain_data(
    rx: &Receiver<Vec<u8>>,
    free_tx: &SyncSender<Vec<u8>>,
    parser_wakeup_pending: &AtomicBool,
    batch: &mut Vec<u8>,
    byte_budget: usize,
) -> io::Result<ReadEffect> {
    let mut effect = ReadEffect::Continue;
    loop {
        let mut chunk = match rx.try_recv() {
            Ok(chunk) => chunk,
            Err(TryRecvError::Empty) => {
                // Clear only after observing the queue empty, then recheck
                // to close the producer-wakeup race.
                parser_wakeup_pending.store(false, std::sync::atomic::Ordering::Release);
                match rx.try_recv() {
                    Ok(chunk) => {
                        parser_wakeup_pending.store(true, std::sync::atomic::Ordering::Release);
                        chunk
                    }
                    Err(TryRecvError::Empty) => break,
                    Err(TryRecvError::Disconnected) => {
                        effect = ReadEffect::Eof;
                        break;
                    }
                }
            }
            Err(TryRecvError::Disconnected) => {
                parser_wakeup_pending.store(false, std::sync::atomic::Ordering::Release);
                effect = ReadEffect::Eof;
                break;
            }
        };
        if batch.len() + chunk.len() > byte_budget {
            effect = ReadEffect::BudgetExhausted;
            let _ = free_tx.try_send(chunk);
            break;
        }
        batch.extend_from_slice(&chunk);
        chunk.clear();
        let _ = free_tx.try_send(chunk);
        if effect != ReadEffect::Continue {
            break;
        }
    }
    Ok(effect)
}

/// Dedicated PTY reader thread.
///
/// The macOS slave->master PTY queue holds only ~1KB ahead of the reader, so
/// a reader that does work while reading makes the writer wait once per 1KB.
/// This thread owns the master reads: it spin-reads while a burst is flowing
/// (catches the microsecond-scale refills without a kqueue round trip),
/// blocks on kqueue once the writer goes quiet (zero idle CPU), and pushes
/// byte blocks into a bounded channel the worker coalesces.
struct PtyReader {
    filled_rx: Receiver<Vec<u8>>,
    free_tx: SyncSender<Vec<u8>>,
    wakeup_pending: Arc<AtomicBool>,
    handle: std::thread::JoinHandle<()>,
}

fn spawn_pty_reader(
    pty: &Pty,
    wakeup: WakeupCallback,
    stopped: Arc<AtomicBool>,
) -> io::Result<PtyReader> {
    let mut reader_file = pty.file().try_clone()?;
    let reader_poller = Poller::new()?;
    unsafe {
        reader_poller.add_with_mode(&reader_file, PollEvent::readable(0), PollMode::Level)?;
    }
    let (filled_tx, filled_rx) = mpsc::sync_channel(READER_CHANNEL_CAPACITY);
    let (free_tx, free_rx) = mpsc::sync_channel(READER_CHANNEL_CAPACITY);
    let parser_wakeup_pending = Arc::new(AtomicBool::new(false));
    let reader_wakeup_pending = parser_wakeup_pending.clone();

    let handle = std::thread::Builder::new()
        .name("water-terminal-reader".into())
        .spawn(move || {
            boost_drain_thread_qos();
            let mut buf = [0_u8; READER_BLOCK_BYTES];
            let mut batch = Vec::with_capacity(READER_BLOCK_BYTES);
            let mut events = Events::new();
            let mut batch_started: Option<Instant> = None;
            // Filled blocks move to the worker and return over `free_rx` for
            // reuse. Only the false->true wake transition notifies the worker.
            let push = |batch: &mut Vec<u8>| -> bool {
                let replacement = free_rx
                    .try_recv()
                    .unwrap_or_else(|_| Vec::with_capacity(READER_BLOCK_BYTES));
                let mut filled = std::mem::replace(batch, replacement);
                loop {
                    match filled_tx.try_send(filled) {
                        Ok(()) => break,
                        Err(TrySendError::Full(returned)) => {
                            if stopped.load(std::sync::atomic::Ordering::Relaxed) {
                                return false;
                            }
                            filled = returned;
                            std::thread::yield_now();
                        }
                        Err(TrySendError::Disconnected(_)) => return false,
                    }
                }
                if !reader_wakeup_pending.swap(true, std::sync::atomic::Ordering::AcqRel) {
                    wakeup();
                }
                true
            };

            loop {
                // Burst phase: spin-read everything the writer has produced.
                let mut spin_start: Option<Instant> = None;
                loop {
                    match reader_file.read(&mut buf) {
                        Ok(0) => {
                            let _ = batch.is_empty() || push(&mut batch);
                            return;
                        }
                        Ok(bytes_read) => {
                            metrics::inc(metrics::pty_read_calls());
                            metrics::add(metrics::pty_bytes_read(), bytes_read);
                            spin_start = None;
                            let mut offset = 0;
                            while offset < bytes_read {
                                if batch.is_empty() {
                                    batch_started = Some(Instant::now());
                                }
                                let available = READER_BLOCK_BYTES.saturating_sub(batch.len());
                                let take = available.min(bytes_read - offset);
                                batch.extend_from_slice(&buf[offset..offset + take]);
                                offset += take;
                                if batch.len() == READER_BLOCK_BYTES {
                                    if !push(&mut batch) {
                                        return;
                                    }
                                    batch_started = None;
                                }
                            }
                        }
                        Err(error) if error.kind() == ErrorKind::WouldBlock => {
                            // A stale partial batch must not wait for the
                            // 128KB threshold (slow-but-continuous streams).
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
                            let _ = batch.is_empty() || push(&mut batch);
                            return;
                        }
                    }
                }
                // Idle phase: the writer has been quiet - flush what we have
                // and block on kqueue until the next chunk (the bounded poll
                // also lets the thread notice the worker dropping the
                // channel on shutdown).
                if !batch.is_empty() {
                    if !push(&mut batch) {
                        return;
                    }
                    batch_started = None;
                }
                let _ = reader_poller.wait(&mut events, Some(READER_IDLE_POLL));
                if stopped.load(std::sync::atomic::Ordering::Relaxed) {
                    let _ = batch.is_empty() || push(&mut batch);
                    return;
                }
            }
        })?;
    Ok(PtyReader {
        filled_rx,
        free_tx,
        wakeup_pending: parser_wakeup_pending,
        handle,
    })
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
    let name = name.lines().next()?.trim_start_matches('-');
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

/// The PTY drain loop is the pipeline's throughput-critical path. On Apple
/// Silicon the scheduler tends to park long-lived idle threads on E-cores;
/// QoS is the documented lever that biases placement toward P-cores.
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

/// Stateful, O(output bytes) scanner for OSC window-title sequences.
///
/// This is the only terminal-escape knowledge the server keeps: it mirrors
/// the vte parser's OSC 0/2 title rule (`OSC 0/2 ; <text>` terminated by
/// BEL, CAN/SUB, or ESC) without a grid, cursor, or full VT state machine.
struct TitleScanner {
    state: TitleState,
    params: Vec<Vec<u8>>,
    param: Vec<u8>,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum TitleState {
    Ground,
    Escape,
    Osc,
}

impl TitleScanner {
    fn new() -> Self {
        Self {
            state: TitleState::Ground,
            params: Vec::new(),
            param: Vec::new(),
        }
    }

    fn feed(&mut self, bytes: &[u8], mut on_title: impl FnMut(&str)) {
        for &byte in bytes {
            match self.state {
                TitleState::Ground => {
                    if byte == 0x1b {
                        self.state = TitleState::Escape;
                    }
                }
                TitleState::Escape => match byte {
                    0x5d => {
                        self.state = TitleState::Osc;
                        self.params.clear();
                        self.param.clear();
                    }
                    0x1b => {}
                    _ => self.state = TitleState::Ground,
                },
                TitleState::Osc => match byte {
                    // BEL, CAN, or ESC terminate the OSC (vte 0.15 rule).
                    0x07 | 0x18 | 0x1a | 0x1b => {
                        let mut params = std::mem::take(&mut self.params);
                        if !self.param.is_empty() {
                            params.push(std::mem::take(&mut self.param));
                        }
                        if let Some(title) = title_from_params(&params) {
                            on_title(&title);
                        }
                        self.state = if byte == 0x1b {
                            TitleState::Escape
                        } else {
                            TitleState::Ground
                        };
                    }
                    // Other C0 controls are skipped inside OSC payloads.
                    0x00..=0x06 | 0x08..=0x17 | 0x19 | 0x1c..=0x1f => {}
                    0x3b => {
                        self.params.push(std::mem::take(&mut self.param));
                    }
                    _ => {
                        if self.param.len() < MAX_TITLE_BYTES {
                            self.param.push(byte);
                        }
                    }
                },
            }
        }
    }
}

/// Mirrors vte's OSC 0/2 title rule: first param "0" or "2", remaining
/// params joined with ";" and trimmed.
fn title_from_params(params: &[Vec<u8>]) -> Option<String> {
    if params.is_empty() || params.len() < 2 {
        return None;
    }
    match params[0].as_slice() {
        b"0" | b"2" => {}
        _ => return None,
    }
    let title = params[1..]
        .iter()
        .filter_map(|param| str::from_utf8(param).ok())
        .collect::<Vec<&str>>()
        .join(";")
        .trim()
        .to_owned();
    Some(title)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn title_scanner_finds_osc_titles() {
        let mut scanner = TitleScanner::new();
        let mut titles = Vec::new();
        scanner.feed(b"plain text before ", |t| titles.push(t.to_owned()));
        scanner.feed(b"\x1b]0;First Title\x07after ", |t| {
            titles.push(t.to_owned())
        });
        scanner.feed(b"\x1b]2;Second\x1b\\ tail", |t| titles.push(t.to_owned()));
        // Non-title OSCs and split sequences.
        scanner.feed(b"\x1b]5;100;200;300\x07", |t| titles.push(t.to_owned()));
        scanner.feed(b"\x1b]0", |t| titles.push(t.to_owned()));
        scanner.feed(b";Split Title\x07", |t| titles.push(t.to_owned()));
        assert_eq!(titles, vec!["First Title", "Second", "Split Title"]);
    }

    #[test]
    fn title_scanner_handles_escaped_content_and_long_titles() {
        let mut scanner = TitleScanner::new();
        let mut titles = Vec::new();
        // A title with semicolon params keeps them joined.
        scanner.feed(b"\x1b]2;a;b\x07", |t| titles.push(t.to_owned()));
        assert_eq!(titles, vec!["a;b"]);
        // An ESC terminates the OSC (vte rule) and starts a new sequence.
        scanner.feed(b"\x1b]0;Via Esc\x1b[m", |t| titles.push(t.to_owned()));
        assert_eq!(titles.last(), Some(&"Via Esc".to_owned()));
        let long: Vec<u8> = [b'x'; MAX_TITLE_BYTES + 10].to_vec();
        scanner.feed(&long, |t| titles.push(t.to_owned()));
        assert_eq!(titles.len(), 2);
    }

    #[test]
    fn drain_data_coalesces_until_the_byte_budget() {
        let chunk = vec![b'x'; 4 * 1024];
        let (tx, rx) = mpsc::channel::<Vec<u8>>();
        let (free_tx, _free_rx) = mpsc::sync_channel::<Vec<u8>>(8);
        let parser_wakeup_pending = AtomicBool::new(true);
        for _ in 0..(8 * 1024 / 4096) {
            tx.send(chunk.clone()).unwrap();
        }
        drop(tx);
        let mut batch = Vec::new();
        let effect =
            drain_data(&rx, &free_tx, &parser_wakeup_pending, &mut batch, 16 * 1024).unwrap();
        assert_eq!(effect, ReadEffect::Eof);
        assert_eq!(batch.len(), 8 * 1024);
    }

    #[test]
    fn drain_data_reports_budget_exhaustion_without_losing_blocks() {
        let big = vec![b'y'; 64 * 1024];
        let (tx, rx) = mpsc::channel::<Vec<u8>>();
        let (free_tx, free_rx) = mpsc::sync_channel::<Vec<u8>>(4);
        let parser_wakeup_pending = AtomicBool::new(true);
        tx.send(big.clone()).unwrap();
        tx.send(big.clone()).unwrap();
        drop(tx);
        let mut batch = Vec::new();
        let effect =
            drain_data(&rx, &free_tx, &parser_wakeup_pending, &mut batch, 64 * 1024).unwrap();
        assert_eq!(effect, ReadEffect::BudgetExhausted);
        assert_eq!(batch.len(), 64 * 1024);
        // The unconsumed block goes back to the free pool.
        assert_eq!(free_rx.try_recv().unwrap().len(), 0);
    }
}
