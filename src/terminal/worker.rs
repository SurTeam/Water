use std::io::{self, ErrorKind, Read, Write};
use std::sync::Arc;
use std::sync::mpsc::{Receiver, Sender, TryRecvError};

use alacritty_terminal::event::{Event, EventListener, OnResize, WindowSize};
use alacritty_terminal::grid::Scroll;
use alacritty_terminal::term::{Config, Term};
use alacritty_terminal::tty::{ChildEvent, EventedPty, EventedReadWrite, Pty};
use alacritty_terminal::vte::ansi::Processor;
use polling::{Event as PollEvent, Events, PollMode, Poller};

use crate::ids::TerminalId;

use super::model::{
    TERMINAL_WAKE_KEY, TerminalManagerEvent, TerminalRegistry, TerminalWorkerCommand,
    WakeupCallback, WakeupSlot,
};
use super::snapshot::{MAX_SCROLLBACK_LINES, TerminalProcessState, TerminalSize, TerminalSnapshot};

const PTY_READ_WRITE_KEY: usize = 0;
const PTY_CHILD_EVENT_KEY: usize = 1;
const READ_BUFFER_BYTES: usize = 16 * 1024;

pub(crate) struct WorkerConfig {
    terminal_id: TerminalId,
    size: TerminalSize,
    command_rx: Receiver<TerminalWorkerCommand>,
    registry: TerminalRegistry,
    event_tx: Sender<TerminalManagerEvent>,
    event_wakeup: Option<WakeupCallback>,
    wakeup_slot: WakeupSlot,
}

impl WorkerConfig {
    pub(crate) fn new(
        terminal_id: TerminalId,
        size: TerminalSize,
        command_rx: Receiver<TerminalWorkerCommand>,
        registry: TerminalRegistry,
        event_tx: Sender<TerminalManagerEvent>,
        event_wakeup: Option<WakeupCallback>,
        wakeup_slot: WakeupSlot,
    ) -> Self {
        Self {
            terminal_id,
            size,
            command_rx,
            registry,
            event_tx,
            event_wakeup,
            wakeup_slot,
        }
    }
}

pub(crate) fn run(config: WorkerConfig, mut pty: Pty) {
    let WorkerConfig {
        terminal_id,
        size,
        command_rx,
        registry,
        event_tx,
        event_wakeup,
        wakeup_slot,
    } = config;
    let config = Config {
        scrolling_history: MAX_SCROLLBACK_LINES,
        ..Config::default()
    };
    let proxy_events = std::sync::mpsc::channel();
    let proxy = WorkerEventProxy {
        sender: proxy_events.0,
    };
    let mut term = Term::new(config, &size, proxy);
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
            registry.mark_exited(terminal_id, None);
            emit_manager_event(
                &event_tx,
                event_wakeup.as_ref(),
                TerminalManagerEvent::Exited {
                    terminal_id,
                    code: None,
                },
            );
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
        registry.mark_exited(terminal_id, None);
        emit_manager_event(
            &event_tx,
            event_wakeup.as_ref(),
            TerminalManagerEvent::Exited {
                terminal_id,
                code: None,
            },
        );
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
    let mut stop_requested = false;
    let publisher = SnapshotPublisher {
        registry: &registry,
        event_tx: &event_tx,
        event_wakeup: event_wakeup.as_ref(),
    };
    publish_snapshot(
        terminal_id,
        &term,
        TerminalProcessState::Running,
        snapshot_revision,
        &publisher,
        &[],
    );

    'worker: loop {
        while let Ok(command) = command_rx.try_recv() {
            match apply_command(command, &mut pty, &mut term) {
                Ok(CommandEffect::Continue { dirty }) => {
                    if dirty {
                        snapshot_revision = snapshot_revision.saturating_add(1);
                        publish_snapshot(
                            terminal_id,
                            &term,
                            TerminalProcessState::Running,
                            snapshot_revision,
                            &publisher,
                            &[],
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
        if let Err(error) = poller.wait(&mut events, None) {
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
                    if let Some(ChildEvent::Exited(Some(status))) = pty.next_child_event() {
                        child_exited = Some(status.code());
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

        if !output_buffer.is_empty() {
            snapshot_revision = snapshot_revision.saturating_add(1);
            publish_snapshot(
                terminal_id,
                &term,
                TerminalProcessState::Running,
                snapshot_revision,
                &publisher,
                &output_buffer,
            );
        }

        if let Some(code) = child_exited {
            // A final non-blocking drain avoids losing bytes that were already
            // queued in the PTY when SIGCHLD arrived.
            output_buffer.clear();
            let _ = drain_pty(
                &mut pty,
                &mut processor,
                &mut term,
                &mut read_buffer,
                &mut output_buffer,
            );
            if !output_buffer.is_empty() {
                snapshot_revision = snapshot_revision.saturating_add(1);
                publish_snapshot(
                    terminal_id,
                    &term,
                    TerminalProcessState::Running,
                    snapshot_revision,
                    &publisher,
                    &output_buffer,
                );
            }
            registry.mark_exited(terminal_id, code);
            emit_manager_event(
                &event_tx,
                event_wakeup.as_ref(),
                TerminalManagerEvent::Exited { terminal_id, code },
            );
            break 'worker;
        }

        if worker_stop {
            if !stop_requested {
                registry.mark_exited(terminal_id, None);
                emit_manager_event(
                    &event_tx,
                    event_wakeup.as_ref(),
                    TerminalManagerEvent::Exited {
                        terminal_id,
                        code: None,
                    },
                );
            }
            break 'worker;
        }
    }

    let _ = pty.deregister(&poller);
    *wakeup_slot.lock().expect("terminal wakeup poisoned") = None;
}

enum CommandEffect {
    Continue { dirty: bool },
    Stop,
}

fn apply_command(
    command: TerminalWorkerCommand,
    pty: &mut Pty,
    term: &mut Term<WorkerEventProxy>,
) -> io::Result<CommandEffect> {
    match command {
        TerminalWorkerCommand::SendText(bytes) | TerminalWorkerCommand::SendBytes(bytes) => {
            pty.writer().write_all(&bytes)?;
            Ok(CommandEffect::Continue { dirty: false })
        }
        TerminalWorkerCommand::Resize(size) => {
            let window_size = WindowSize {
                num_lines: size.lines as u16,
                num_cols: size.columns as u16,
                cell_width: 0,
                cell_height: 0,
            };
            pty.on_resize(window_size);
            term.resize(size);
            Ok(CommandEffect::Continue { dirty: true })
        }
        TerminalWorkerCommand::Scroll(lines) => {
            term.scroll_display(Scroll::Delta(lines));
            Ok(CommandEffect::Continue { dirty: true })
        }
        TerminalWorkerCommand::Shutdown => Ok(CommandEffect::Stop),
    }
}

enum ReadEffect {
    Continue,
    Eof,
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
    registry: &'a TerminalRegistry,
    event_tx: &'a Sender<TerminalManagerEvent>,
    event_wakeup: Option<&'a WakeupCallback>,
}

fn publish_snapshot(
    terminal_id: TerminalId,
    term: &Term<WorkerEventProxy>,
    process: TerminalProcessState,
    revision: u64,
    publisher: &SnapshotPublisher<'_>,
    output: &[u8],
) {
    let snapshot = TerminalSnapshot::from_term(terminal_id, term, process, revision);
    publisher.registry.publish(terminal_id, snapshot, output);
    emit_manager_event(
        publisher.event_tx,
        publisher.event_wakeup,
        TerminalManagerEvent::OutputChanged { terminal_id },
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
