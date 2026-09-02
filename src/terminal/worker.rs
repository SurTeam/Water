use std::io::{self, ErrorKind, Read, Write};
use std::path::{Path, PathBuf};
use std::process::Command;
use std::sync::Arc;

#[cfg(unix)]
use std::os::fd::AsRawFd;
use std::sync::mpsc::{Receiver, Sender, TryRecvError};
use std::time::{Duration, Instant};

use alacritty_terminal::event::{Event, EventListener, OnResize, WindowSize};
use alacritty_terminal::grid::{Dimensions, Scroll};
use alacritty_terminal::term::{Config, Term, TermMode};
use alacritty_terminal::tty::{ChildEvent, EventedPty, EventedReadWrite, Pty};
use alacritty_terminal::vte::ansi::Processor;
use polling::{Event as PollEvent, Events, PollMode, Poller};

use crate::ids::TerminalId;

use super::model::{
    ScrollbackBudget, TERMINAL_WAKE_KEY, TerminalManagerEvent, TerminalRegistry,
    TerminalWorkerCommand, WakeupCallback, WakeupSlot,
};
use super::snapshot::{TerminalProcessState, TerminalSize, TerminalSnapshot};

const PTY_READ_WRITE_KEY: usize = 0;
const PTY_CHILD_EVENT_KEY: usize = 1;
const READ_BUFFER_BYTES: usize = 16 * 1024;
const PROCESS_METADATA_REFRESH_INTERVAL: Duration = Duration::from_millis(500);

pub(crate) struct WorkerConfig {
    terminal_id: TerminalId,
    size: TerminalSize,
    scrollback_lines: usize,
    scrollback_budget: ScrollbackBudget,
    command_rx: Receiver<TerminalWorkerCommand>,
    fallback_process_name: String,
    fallback_cwd: PathBuf,
    registry: TerminalRegistry,
    event_tx: Sender<TerminalManagerEvent>,
    event_wakeup: Option<WakeupCallback>,
    wakeup_slot: WakeupSlot,
}

pub(crate) struct WorkerMetadata {
    pub(crate) fallback_process_name: String,
    pub(crate) fallback_cwd: PathBuf,
}

pub(crate) struct WorkerChannels {
    pub(crate) registry: TerminalRegistry,
    pub(crate) event_tx: Sender<TerminalManagerEvent>,
    pub(crate) event_wakeup: Option<WakeupCallback>,
    pub(crate) wakeup_slot: WakeupSlot,
}

impl WorkerChannels {
    pub(crate) fn new(
        registry: TerminalRegistry,
        event_tx: Sender<TerminalManagerEvent>,
        event_wakeup: Option<WakeupCallback>,
        wakeup_slot: WakeupSlot,
    ) -> Self {
        Self {
            registry,
            event_tx,
            event_wakeup,
            wakeup_slot,
        }
    }
}

impl WorkerConfig {
    pub(crate) fn new(
        terminal_id: TerminalId,
        size: TerminalSize,
        scrollback_lines: usize,
        scrollback_budget: ScrollbackBudget,
        command_rx: Receiver<TerminalWorkerCommand>,
        channels: WorkerChannels,
        metadata: WorkerMetadata,
    ) -> Self {
        Self {
            terminal_id,
            size,
            scrollback_lines,
            scrollback_budget,
            command_rx,
            fallback_process_name: metadata.fallback_process_name,
            fallback_cwd: metadata.fallback_cwd,
            registry: channels.registry,
            event_tx: channels.event_tx,
            event_wakeup: channels.event_wakeup,
            wakeup_slot: channels.wakeup_slot,
        }
    }
}

pub(crate) fn run(config: WorkerConfig, mut pty: Pty) {
    let WorkerConfig {
        terminal_id,
        size,
        scrollback_lines,
        scrollback_budget,
        command_rx,
        fallback_process_name,
        fallback_cwd,
        registry,
        event_tx,
        event_wakeup,
        wakeup_slot,
    } = config;
    let config = Config {
        scrolling_history: scrollback_lines,
        ..Config::default()
    };
    let proxy_events = std::sync::mpsc::channel();
    let proxy = WorkerEventProxy {
        sender: proxy_events.0,
    };
    let mut term = Term::new(config, &size, proxy);
    let mut scrollback = ScrollbackState::new(terminal_id, scrollback_lines, scrollback_budget);
    let _ = scrollback.reconcile(&mut term, false);
    let mut process_metadata = query_process_metadata(&pty, &fallback_process_name, &fallback_cwd);
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
    *wakeup_slot.lock().expect("terminal wakeup poisoned") = Some(Arc::new(move || {
        let _ = poller_for_wakeup.notify();
    }) as WakeupCallback);

    let mut events = Events::new();
    let mut read_buffer = [0_u8; READ_BUFFER_BYTES];
    let mut output_buffer = Vec::with_capacity(READ_BUFFER_BYTES);
    let mut snapshot_revision = 0_u64;
    let mut viewport_position = 0_i64;
    let mut last_process_metadata_refresh = Instant::now();
    let mut stop_requested = false;
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

    'worker: loop {
        while let Ok(command) = command_rx.try_recv() {
            match apply_command(command, &mut pty, &mut term, &mut scrollback) {
                Ok(CommandEffect::Continue {
                    dirty,
                    viewport_delta,
                    refresh_process,
                }) => {
                    viewport_position = viewport_position.saturating_add(viewport_delta);
                    let metadata_due = refresh_process
                        && last_process_metadata_refresh.elapsed()
                            >= PROCESS_METADATA_REFRESH_INTERVAL;
                    let metadata_changed = metadata_due
                        && refresh_process_metadata(
                            &pty,
                            &fallback_process_name,
                            &fallback_cwd,
                            &mut process_metadata,
                        );
                    if metadata_due {
                        last_process_metadata_refresh = Instant::now();
                    }
                    if dirty || metadata_changed {
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
                    if metadata_changed {
                        emit_process_metadata(
                            &event_tx,
                            event_wakeup.as_ref(),
                            terminal_id,
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
            .saturating_sub(last_process_metadata_refresh.elapsed());
        if let Err(error) = poller.wait(&mut events, Some(metadata_timeout)) {
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
        let mut pty_eof = false;
        for event in events.iter() {
            match event.key {
                PTY_READ_WRITE_KEY if event.readable && !pty_eof => {
                    // A pinned viewport must be allowed to borrow the
                    // remaining global history before parsing new rows. If
                    // the normal limit were reached first, alacritty would
                    // discard the very rows the user is looking at.
                    let _ = scrollback.prepare_for_output(&mut term);
                    match drain_pty(
                        &mut pty,
                        &mut processor,
                        &mut term,
                        &mut read_buffer,
                        &mut output_buffer,
                    ) {
                        Ok(ReadEffect::Continue) => {}
                        Ok(ReadEffect::Eof) => {
                            pty_eof = true;
                            let _ = poller.delete(pty.file());
                        }
                        Err(error) if error.kind() == ErrorKind::WouldBlock => {}
                        Err(error) => {
                            tracing::debug!(
                                target: "water::pty",
                                terminal_id = %terminal_id,
                                ?error,
                                "PTY read ended"
                            );
                            pty_eof = true;
                            let _ = poller.delete(pty.file());
                        }
                    }
                }
                PTY_CHILD_EVENT_KEY => {
                    if let Some(ChildEvent::Exited(status)) = pty.next_child_event() {
                        child_exited = Some(status.and_then(|status| status.code()));
                    }
                }
                TERMINAL_WAKE_KEY => {}
                _ => {}
            }
        }

        if process_proxy_events(
            &proxy_events.1,
            &mut pty,
            &event_tx,
            event_wakeup.as_ref(),
            terminal_id,
        ) {
            worker_stop = true;
        }

        let metadata_due =
            last_process_metadata_refresh.elapsed() >= PROCESS_METADATA_REFRESH_INTERVAL;
        // Avoid spawning metadata helper processes while a terminal is
        // actively streaming output. Once output goes quiet, the timeout
        // path below refreshes the foreground process and cwd promptly.
        let metadata_changed = metadata_due
            && output_buffer.is_empty()
            && refresh_process_metadata(
                &pty,
                &fallback_process_name,
                &fallback_cwd,
                &mut process_metadata,
            );
        if metadata_due {
            last_process_metadata_refresh = Instant::now();
        }
        if !output_buffer.is_empty() {
            let _ = scrollback.sync(&mut term);
        }
        if !output_buffer.is_empty() || metadata_changed {
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
        if metadata_changed {
            emit_process_metadata(
                &event_tx,
                event_wakeup.as_ref(),
                terminal_id,
                &process_metadata,
            );
        }

        if let Some(code) = child_exited {
            // A final non-blocking drain avoids losing bytes that were already
            // queued in the PTY when SIGCHLD arrived.
            output_buffer.clear();
            let _ = scrollback.prepare_for_output(&mut term);
            let _ = drain_pty(
                &mut pty,
                &mut processor,
                &mut term,
                &mut read_buffer,
                &mut output_buffer,
            );
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

    let _ = pty.deregister(&poller);
    *wakeup_slot.lock().expect("terminal wakeup poisoned") = None;
    scrollback.unregister();
}

struct ScrollbackState {
    terminal_id: TerminalId,
    base_limit: usize,
    current_limit: usize,
    budget: ScrollbackBudget,
}

impl ScrollbackState {
    fn new(terminal_id: TerminalId, base_limit: usize, budget: ScrollbackBudget) -> Self {
        Self {
            terminal_id,
            base_limit,
            current_limit: base_limit,
            budget,
        }
    }

    fn prepare_for_output(&mut self, term: &mut Term<WorkerEventProxy>) -> bool {
        let active = self.is_pinned(term);
        self.reconcile(term, active)
    }

    fn focus_latest(&mut self, term: &mut Term<WorkerEventProxy>) -> bool {
        let old_offset = term.grid().display_offset();
        if old_offset != 0 {
            term.scroll_display(Scroll::Bottom);
        }
        let changed = self.reconcile(term, false);
        changed || old_offset != term.grid().display_offset()
    }

    fn reconcile(&mut self, term: &mut Term<WorkerEventProxy>, active: bool) -> bool {
        let old_offset = term.grid().display_offset();
        let old_limit = self.current_limit;
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
                .limit_for(self.terminal_id, 0, self.base_limit, false);
            0
        } else {
            self.budget
                .limit_for(self.terminal_id, retained, self.base_limit, active)
        };
        if target_limit != self.current_limit {
            term.grid_mut().update_history(target_limit);
            self.current_limit = target_limit;
        }
        let new_offset = term.grid().display_offset();
        self.budget.sync(
            self.terminal_id,
            terminal_history_size(term),
            self.is_pinned(term),
        );
        old_limit != self.current_limit || old_offset != new_offset
    }

    fn sync(&mut self, term: &mut Term<WorkerEventProxy>) -> bool {
        let active = self.is_pinned(term);
        self.reconcile(term, active)
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
            let dirty = scrollback.reconcile(term, scrollback.is_pinned(term));
            Ok(CommandEffect::Continue {
                dirty,
                viewport_delta: viewport_delta(old_offset, term.grid().display_offset()),
                refresh_process: false,
            })
        }
        TerminalWorkerCommand::Scroll(lines) => {
            let old_offset = term.grid().display_offset();
            term.scroll_display(Scroll::Delta(lines));
            let dirty = scrollback.reconcile(term, scrollback.is_pinned(term));
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

enum ReadEffect {
    Continue,
    Eof,
}

#[derive(Debug, Clone, PartialEq, Eq)]
struct ProcessMetadata {
    process_name: String,
    cwd: String,
}

fn query_process_metadata(
    pty: &Pty,
    fallback_process_name: &str,
    fallback_cwd: &Path,
) -> ProcessMetadata {
    let pid = foreground_process_id(pty);
    let process_name = pid
        .and_then(query_process_name)
        .filter(|name| !name.is_empty())
        .unwrap_or_else(|| fallback_process_name.to_owned());
    let cwd = pid
        .and_then(query_process_cwd)
        .unwrap_or_else(|| fallback_cwd.display().to_string());
    ProcessMetadata { process_name, cwd }
}

#[cfg(unix)]
fn foreground_process_id(pty: &Pty) -> Option<u32> {
    let fd = pty.file().as_raw_fd();
    let foreground = unsafe { libc::tcgetpgrp(fd) };
    if foreground > 0 {
        return u32::try_from(foreground).ok();
    }
    let child = pty.child().id();
    query_terminal_foreground_process(child).or((child > 0).then_some(child))
}

#[cfg(not(unix))]
fn foreground_process_id(_pty: &Pty) -> Option<u32> {
    None
}

#[cfg(unix)]
fn query_terminal_foreground_process(pid: u32) -> Option<u32> {
    let output = Command::new("ps")
        .args(["-o", "tpgid=", "-p", &pid.to_string()])
        .output()
        .ok()?;
    if !output.status.success() {
        return None;
    }
    String::from_utf8_lossy(&output.stdout)
        .trim()
        .parse::<i32>()
        .ok()
        .filter(|process_group| *process_group > 0)
        .and_then(|process_group| u32::try_from(process_group).ok())
}

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

fn query_process_cwd(pid: u32) -> Option<String> {
    #[cfg(target_os = "linux")]
    {
        return std::fs::read_link(format!("/proc/{pid}/cwd"))
            .ok()
            .map(|path| path.display().to_string());
    }

    #[cfg(target_os = "macos")]
    {
        let output = Command::new("lsof")
            .args(["-a", "-p", &pid.to_string(), "-d", "cwd", "-Fn"])
            .output()
            .ok()?;
        if !output.status.success() {
            return None;
        }
        String::from_utf8_lossy(&output.stdout)
            .lines()
            .find_map(|line| line.strip_prefix('n'))
            .filter(|path| !path.is_empty())
            .map(ToOwned::to_owned)
    }

    #[cfg(not(any(target_os = "linux", target_os = "macos")))]
    {
        let _ = pid;
        None
    }
}

fn refresh_process_metadata(
    pty: &Pty,
    fallback_process_name: &str,
    fallback_cwd: &Path,
    current: &mut ProcessMetadata,
) -> bool {
    let next = query_process_metadata(pty, fallback_process_name, fallback_cwd);
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

fn drain_pty(
    pty: &mut Pty,
    processor: &mut Processor,
    term: &mut Term<WorkerEventProxy>,
    read_buffer: &mut [u8],
    output_buffer: &mut Vec<u8>,
) -> io::Result<ReadEffect> {
    loop {
        match pty.reader().read(read_buffer) {
            Ok(0) => return Ok(ReadEffect::Eof),
            Ok(bytes_read) => {
                output_buffer.extend_from_slice(&read_buffer[..bytes_read]);
                processor.advance(term, &read_buffer[..bytes_read]);
            }
            Err(error) if error.kind() == ErrorKind::WouldBlock => return Ok(ReadEffect::Continue),
            Err(error) => return Err(error),
        }
    }
}

fn process_proxy_events(
    proxy_events: &Receiver<ProxyAction>,
    pty: &mut Pty,
    event_tx: &Sender<TerminalManagerEvent>,
    event_wakeup: Option<&WakeupCallback>,
    terminal_id: TerminalId,
) -> bool {
    let mut should_stop = false;
    loop {
        match proxy_events.try_recv() {
            Ok(ProxyAction::Write(bytes)) => {
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
    publisher
        .registry
        .publish(publisher.terminal_id, snapshot, output);
    emit_manager_event(
        publisher.event_tx,
        publisher.event_wakeup,
        TerminalManagerEvent::OutputChanged {
            terminal_id: publisher.terminal_id,
        },
    );
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
    Write(Vec<u8>),
    Title(String),
    Stop,
}

struct WorkerEventProxy {
    sender: Sender<ProxyAction>,
}

impl EventListener for WorkerEventProxy {
    fn send_event(&self, event: Event) {
        let action = match event {
            Event::PtyWrite(text) => Some(ProxyAction::Write(text.into_bytes())),
            Event::Title(title) => Some(ProxyAction::Title(title)),
            Event::ResetTitle => Some(ProxyAction::Title(String::new())),
            Event::Exit => Some(ProxyAction::Stop),
            Event::ChildExit(_) => None,
            Event::MouseCursorDirty
            | Event::ClipboardStore(_, _)
            | Event::ClipboardLoad(_, _)
            | Event::ColorRequest(_, _)
            | Event::TextAreaSizeRequest(_)
            | Event::CursorBlinkingChange
            | Event::Wakeup
            | Event::Bell => None,
        };
        if let Some(action) = action {
            let _ = self.sender.send(action);
        }
    }
}

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
            WorkerEventProxy { sender },
        )
    }

    #[test]
    fn pinned_view_stays_fixed_while_output_grows_temporary_scrollback() {
        let terminal_id = TerminalId::new(1);
        let size = TerminalSize::new(16, 3);
        let budget = ScrollbackBudget::new(100);
        budget.register(terminal_id, 2);
        let mut scrollback = ScrollbackState::new(terminal_id, 2, budget);
        let mut term = test_term(size, 2);
        let mut processor = Processor::<alacritty_terminal::vte::ansi::StdSyncHandler>::new();

        processor.advance(
            &mut term,
            b"one\r\ntwo\r\nthree\r\nfour\r\nfive\r\nsix\r\nseven\r\n",
        );
        let _ = scrollback.sync(&mut term);
        term.scroll_display(Scroll::Delta(1));
        assert!(scrollback.reconcile(&mut term, true));
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
}
