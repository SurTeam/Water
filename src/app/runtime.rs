use std::sync::Arc;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::mpsc::{self, Receiver, Sender};
use std::sync::{Condvar, Mutex};
use std::thread::{self, JoinHandle};
use std::time::Duration;

use crate::app::model::{MemoryStats, ModelSnapshot, StateDump};
use crate::command::{
    AppCommand, CommandDispatcher, DispatchError, OperationRegistry, OperationSnapshot,
};
use crate::config::AppConfig;
use crate::event::AppEvent;
use crate::ids::{OperationId, TerminalId};
use crate::terminal::{TerminalSnapshot, WakeupCallback};

#[derive(Debug)]
enum ModelRequest {
    Dispatch {
        command: AppCommand,
        reply: Sender<OperationId>,
    },
    StateDump {
        reply: Sender<StateDump>,
    },
    MemoryStats {
        reply: Sender<MemoryStats>,
    },
    EventsSince {
        sequence: u64,
        reply: Sender<Vec<AppEvent>>,
    },
    TerminalContains {
        terminal_id: TerminalId,
        text: String,
        timeout: Duration,
        reply: Sender<Result<TerminalSnapshot, DispatchError>>,
    },
    TerminalExit {
        terminal_id: TerminalId,
        timeout: Duration,
        reply: Sender<Result<TerminalSnapshot, DispatchError>>,
    },
    TerminalSnapshot {
        terminal_id: TerminalId,
        reply: Sender<Result<crate::terminal::TerminalSnapshot, DispatchError>>,
    },
    TerminalEventWake,
    Shutdown,
}

/// A single-consumer snapshot stream that keeps only the newest pending model
/// snapshot. Terminal output can produce snapshots faster than GPUI can paint;
/// intermediate projections are not useful once a newer revision exists.
#[derive(Debug)]
pub struct ModelSnapshotReceiver {
    channel: Arc<SnapshotChannel>,
}

#[derive(Debug)]
struct SnapshotSender {
    channel: Arc<SnapshotChannel>,
}

#[derive(Debug)]
struct SnapshotChannel {
    state: Mutex<SnapshotChannelState>,
    changed: Condvar,
}

#[derive(Debug, Default)]
struct SnapshotChannelState {
    latest: Option<ModelSnapshot>,
    sender_open: bool,
    receiver_open: bool,
}

#[derive(Debug)]
pub(crate) struct SnapshotChannelClosed;

fn snapshot_channel() -> (SnapshotSender, ModelSnapshotReceiver) {
    let channel = Arc::new(SnapshotChannel {
        state: Mutex::new(SnapshotChannelState {
            sender_open: true,
            receiver_open: true,
            ..Default::default()
        }),
        changed: Condvar::new(),
    });
    (
        SnapshotSender {
            channel: channel.clone(),
        },
        ModelSnapshotReceiver { channel },
    )
}

impl SnapshotSender {
    fn send(&self, snapshot: ModelSnapshot) -> Result<(), SnapshotChannelClosed> {
        let mut state = self
            .channel
            .state
            .lock()
            .expect("snapshot channel poisoned");
        if !state.receiver_open {
            return Err(SnapshotChannelClosed);
        }
        state.latest = Some(snapshot);
        self.channel.changed.notify_one();
        Ok(())
    }

    fn close(&self) {
        let mut state = self
            .channel
            .state
            .lock()
            .expect("snapshot channel poisoned");
        state.sender_open = false;
        self.channel.changed.notify_all();
    }
}

impl ModelSnapshotReceiver {
    pub(crate) fn recv(&self) -> Result<ModelSnapshot, SnapshotChannelClosed> {
        let mut state = self
            .channel
            .state
            .lock()
            .expect("snapshot channel poisoned");
        loop {
            if let Some(snapshot) = state.latest.take() {
                return Ok(snapshot);
            }
            if !state.sender_open {
                return Err(SnapshotChannelClosed);
            }
            state = self
                .channel
                .changed
                .wait(state)
                .expect("snapshot channel poisoned");
        }
    }
}

impl Drop for ModelSnapshotReceiver {
    fn drop(&mut self) {
        if let Ok(mut state) = self.channel.state.lock() {
            state.receiver_open = false;
            state.latest = None;
            self.channel.changed.notify_all();
        }
    }
}

/// A cloneable command/query handle. It never exposes mutable application state.
#[derive(Clone, Debug)]
pub struct CommandClient {
    request_tx: Sender<ModelRequest>,
    operations: OperationRegistry,
}

impl CommandClient {
    pub fn dispatch(&self, command: AppCommand) -> Result<OperationId, DispatchError> {
        let (reply_tx, reply_rx) = mpsc::channel();
        self.request_tx
            .send(ModelRequest::Dispatch {
                command,
                reply: reply_tx,
            })
            .map_err(|_| DispatchError::ChannelClosed)?;
        reply_rx.recv().map_err(|_| DispatchError::ChannelClosed)
    }

    /// Enqueues a command without waiting for an operation ID. This is used
    /// for high-frequency terminal input so the GPUI event handler never
    /// blocks on the model thread. The command still follows the normal
    /// `AppCommand -> CommandDispatcher` mutation path.
    pub fn enqueue(&self, command: AppCommand) -> Result<(), DispatchError> {
        let (reply_tx, _reply_rx) = mpsc::channel();
        self.request_tx
            .send(ModelRequest::Dispatch {
                command,
                reply: reply_tx,
            })
            .map_err(|_| DispatchError::ChannelClosed)
    }

    pub fn get_operation(&self, operation_id: OperationId) -> Option<OperationSnapshot> {
        self.operations.get(operation_id)
    }

    pub fn wait_operation(
        &self,
        operation_id: OperationId,
    ) -> Result<OperationSnapshot, DispatchError> {
        self.operations.wait(operation_id).ok_or_else(|| {
            DispatchError::Command(crate::command::CommandError::new(
                "OPERATION_NOT_FOUND",
                format!("operation {operation_id} does not exist"),
            ))
        })
    }

    pub fn state_dump(&self) -> Result<StateDump, DispatchError> {
        let (reply_tx, reply_rx) = mpsc::channel();
        self.request_tx
            .send(ModelRequest::StateDump { reply: reply_tx })
            .map_err(|_| DispatchError::ChannelClosed)?;
        reply_rx.recv().map_err(|_| DispatchError::ChannelClosed)
    }

    pub fn memory_stats(&self) -> Result<MemoryStats, DispatchError> {
        let (reply_tx, reply_rx) = mpsc::channel();
        self.request_tx
            .send(ModelRequest::MemoryStats { reply: reply_tx })
            .map_err(|_| DispatchError::ChannelClosed)?;
        reply_rx.recv().map_err(|_| DispatchError::ChannelClosed)
    }

    pub fn events_since(&self, sequence: u64) -> Result<Vec<AppEvent>, DispatchError> {
        let (reply_tx, reply_rx) = mpsc::channel();
        self.request_tx
            .send(ModelRequest::EventsSince {
                sequence,
                reply: reply_tx,
            })
            .map_err(|_| DispatchError::ChannelClosed)?;
        reply_rx.recv().map_err(|_| DispatchError::ChannelClosed)
    }

    pub fn terminal_contains(
        &self,
        terminal_id: TerminalId,
        text: impl Into<String>,
        timeout: Duration,
    ) -> Result<TerminalSnapshot, DispatchError> {
        let (reply_tx, reply_rx) = mpsc::channel();
        self.request_tx
            .send(ModelRequest::TerminalContains {
                terminal_id,
                text: text.into(),
                timeout,
                reply: reply_tx,
            })
            .map_err(|_| DispatchError::ChannelClosed)?;
        reply_rx.recv().map_err(|_| DispatchError::ChannelClosed)?
    }

    pub fn wait_terminal_exit(
        &self,
        terminal_id: TerminalId,
        timeout: Duration,
    ) -> Result<TerminalSnapshot, DispatchError> {
        let (reply_tx, reply_rx) = mpsc::channel();
        self.request_tx
            .send(ModelRequest::TerminalExit {
                terminal_id,
                timeout,
                reply: reply_tx,
            })
            .map_err(|_| DispatchError::ChannelClosed)?;
        reply_rx.recv().map_err(|_| DispatchError::ChannelClosed)?
    }

    pub fn terminal_snapshot(
        &self,
        terminal_id: TerminalId,
    ) -> Result<TerminalSnapshot, DispatchError> {
        let (reply_tx, reply_rx) = mpsc::channel();
        self.request_tx
            .send(ModelRequest::TerminalSnapshot {
                terminal_id,
                reply: reply_tx,
            })
            .map_err(|_| DispatchError::ChannelClosed)?;
        reply_rx.recv().map_err(|_| DispatchError::ChannelClosed)?
    }
}

/// Owns the model thread and the one snapshot stream consumed by the GPUI view.
pub struct ModelHost {
    client: CommandClient,
    snapshot_rx: Option<ModelSnapshotReceiver>,
    join_handle: Option<JoinHandle<()>>,
}

impl ModelHost {
    pub fn start() -> Self {
        Self::start_with_config(AppConfig::default())
    }

    pub fn start_with_config(config: AppConfig) -> Self {
        let config = config.normalized();
        let terminal_scrollback_lines = config.terminal.scrollback_lines;
        let terminal_max_total_scrollback_lines = config.terminal.max_total_scrollback_lines;
        let (request_tx, request_rx) = mpsc::channel();
        let (snapshot_tx, snapshot_rx) = snapshot_channel();
        let operations = OperationRegistry::new();
        let thread_operations = operations.clone();
        let terminal_wakeup_tx = request_tx.clone();
        let terminal_wakeup_pending = Arc::new(AtomicBool::new(false));
        let terminal_wakeup_pending_for_callback = terminal_wakeup_pending.clone();
        let terminal_wakeup: WakeupCallback = Arc::new(move || {
            if !terminal_wakeup_pending_for_callback.swap(true, Ordering::AcqRel) {
                let _ = terminal_wakeup_tx.send(ModelRequest::TerminalEventWake);
            }
        });
        let join_handle = thread::Builder::new()
            .name("water-model".to_owned())
            .spawn(move || {
                run_model_thread(
                    request_rx,
                    snapshot_tx,
                    thread_operations,
                    Some(terminal_wakeup),
                    terminal_wakeup_pending,
                    terminal_scrollback_lines,
                    terminal_max_total_scrollback_lines,
                )
            })
            .expect("failed to start water model thread");

        Self {
            client: CommandClient {
                request_tx,
                operations,
            },
            snapshot_rx: Some(snapshot_rx),
            join_handle: Some(join_handle),
        }
    }

    pub fn client(&self) -> CommandClient {
        self.client.clone()
    }

    pub fn take_snapshot_receiver(&mut self) -> ModelSnapshotReceiver {
        self.snapshot_rx
            .take()
            .expect("snapshot receiver can only be taken once")
    }

    pub fn shutdown(&mut self) {
        if self.join_handle.is_some() {
            let _ = self.client.request_tx.send(ModelRequest::Shutdown);
            if let Some(join_handle) = self.join_handle.take() {
                let _ = join_handle.join();
            }
        }
    }
}

impl Drop for ModelHost {
    fn drop(&mut self) {
        self.shutdown();
    }
}

fn run_model_thread(
    request_rx: Receiver<ModelRequest>,
    snapshot_tx: SnapshotSender,
    operations: OperationRegistry,
    terminal_wakeup: Option<WakeupCallback>,
    terminal_wakeup_pending: Arc<AtomicBool>,
    terminal_scrollback_lines: usize,
    terminal_max_total_scrollback_lines: usize,
) {
    let mut dispatcher =
        CommandDispatcher::with_operations_and_terminal_wakeup_and_scrollback_and_total(
            operations,
            terminal_wakeup,
            terminal_scrollback_lines,
            terminal_max_total_scrollback_lines,
        );
    let _ = snapshot_tx.send(dispatcher.state_dump());

    while let Ok(request) = request_rx.recv() {
        terminal_wakeup_pending.store(false, Ordering::Release);
        if dispatcher.pump_background_events() {
            let _ = snapshot_tx.send(dispatcher.state_dump());
        }
        match request {
            ModelRequest::Dispatch { command, reply } => {
                let before_revision = dispatcher.model().state_revision();
                let operation_id = dispatcher.dispatch(command);
                let _ = reply.send(operation_id);
                if dispatcher.model().state_revision() != before_revision {
                    let _ = snapshot_tx.send(dispatcher.state_dump());
                }
            }
            ModelRequest::StateDump { reply } => {
                let _ = reply.send(dispatcher.state_dump());
            }
            ModelRequest::MemoryStats { reply } => {
                let _ = reply.send(dispatcher.memory_stats());
            }
            ModelRequest::EventsSince { sequence, reply } => {
                let _ = reply.send(dispatcher.events_since(sequence));
            }
            ModelRequest::TerminalContains {
                terminal_id,
                text,
                timeout,
                reply,
            } => {
                let _ = reply.send(dispatcher.wait_terminal_contains(terminal_id, &text, timeout));
            }
            ModelRequest::TerminalExit {
                terminal_id,
                timeout,
                reply,
            } => {
                let _ = reply.send(dispatcher.wait_terminal_exit(terminal_id, timeout));
            }
            ModelRequest::TerminalSnapshot { terminal_id, reply } => {
                let _ = reply.send(dispatcher.terminal_snapshot(terminal_id));
            }
            ModelRequest::TerminalEventWake => {}
            ModelRequest::Shutdown => break,
        }
    }
    snapshot_tx.close();
}

#[cfg(test)]
mod tests {
    use super::*;

    fn snapshot(revision: u64) -> ModelSnapshot {
        StateDump {
            state_revision: revision,
            workspace: None,
            workspaces: Vec::new(),
            active_workspace: None,
            focused_pane: None,
        }
    }

    #[test]
    fn snapshot_channel_keeps_only_the_latest_revision() {
        let (sender, receiver) = snapshot_channel();
        sender.send(snapshot(1)).unwrap();
        sender.send(snapshot(2)).unwrap();

        assert_eq!(receiver.recv().unwrap().state_revision, 2);

        sender.close();
        assert!(receiver.recv().is_err());
    }
}
