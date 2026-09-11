use std::sync::Arc;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::mpsc::{self, Receiver, Sender};
use std::sync::{Condvar, Mutex};
use std::thread::{self, JoinHandle};
use std::time::Duration;

use crate::app::model::{MemoryStats, ModelSnapshot, StateDump};
use crate::command::{
    AppCommand, CommandDispatcher, DispatchError, OperationRegistry, OperationSnapshot,
    terminal_dispatch_error,
};
use crate::config::AppConfig;
use crate::event::AppEvent;
use crate::ids::{OperationId, TerminalId};
use crate::terminal::{
    DEFAULT_SCROLLBACK_LINES, TerminalAttachment, TerminalReplay, TerminalSnapshot, WakeupCallback,
    snapshot_from_replay,
};

/// A single-consumer, latest-only model snapshot stream. Socket and SSH
/// readers can continue receiving at transport speed while a slower renderer
/// skips obsolete revisions instead of replaying an unbounded frame backlog.
#[derive(Clone)]
pub struct SnapshotStream(ModelSnapshotReceiver);

impl std::fmt::Debug for SnapshotStream {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("SnapshotStream").finish()
    }
}

impl SnapshotStream {
    /// Blocks until the next model snapshot; returns `None` when the source
    /// (model host or server session) has ended.
    pub fn recv(&self) -> Option<ModelSnapshot> {
        self.0.recv().ok()
    }
}

#[derive(Debug)]
pub(crate) struct SnapshotStreamSender(SnapshotSender);

impl SnapshotStreamSender {
    pub(crate) fn send(&self, snapshot: ModelSnapshot) -> Result<(), SnapshotChannelClosed> {
        self.0.send(snapshot)
    }
}

impl Drop for SnapshotStreamSender {
    fn drop(&mut self) {
        self.0.close();
    }
}

pub(crate) fn snapshot_stream_channel() -> (SnapshotStreamSender, SnapshotStream) {
    let (sender, receiver) = snapshot_channel();
    (SnapshotStreamSender(sender), SnapshotStream(receiver))
}

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
        reply: Sender<Result<TerminalReplay, DispatchError>>,
    },
    TerminalExit {
        terminal_id: TerminalId,
        timeout: Duration,
        reply: Sender<Result<TerminalReplay, DispatchError>>,
    },
    TerminalReplay {
        terminal_id: TerminalId,
        reply: Sender<Result<TerminalReplay, DispatchError>>,
    },
    TerminalAttach {
        terminal_id: TerminalId,
        reply: Sender<Result<TerminalAttachment, DispatchError>>,
    },
    TerminalEventWake,
    Shutdown,
}

/// A single-consumer snapshot stream that keeps only the newest pending model
/// snapshot. Terminal output can produce snapshots faster than GPUI can paint;
/// intermediate projections are not useful once a newer revision exists.
#[derive(Debug, Clone)]
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
        let replay = self.terminal_contains_replay(terminal_id, text, timeout)?;
        Ok(snapshot_from_replay(&replay, DEFAULT_SCROLLBACK_LINES))
    }

    /// Waits on the server's raw-output predicate and returns raw history;
    /// callers that need cells replay it locally.
    pub fn terminal_contains_replay(
        &self,
        terminal_id: TerminalId,
        text: impl Into<String>,
        timeout: Duration,
    ) -> Result<TerminalReplay, DispatchError> {
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
        let replay = self.wait_terminal_exit_replay(terminal_id, timeout)?;
        Ok(snapshot_from_replay(&replay, DEFAULT_SCROLLBACK_LINES))
    }

    /// Waits only on the server-owned lifecycle state and returns raw replay
    /// history without constructing a terminal emulator on the model thread.
    pub fn wait_terminal_exit_replay(
        &self,
        terminal_id: TerminalId,
        timeout: Duration,
    ) -> Result<TerminalReplay, DispatchError> {
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

    /// Returns the bounded raw replay history; callers build a temporary
    /// local terminal and replay the events to inspect the screen.
    pub fn terminal_replay(
        &self,
        terminal_id: TerminalId,
    ) -> Result<TerminalReplay, DispatchError> {
        let (reply_tx, reply_rx) = mpsc::channel();
        self.request_tx
            .send(ModelRequest::TerminalReplay {
                terminal_id,
                reply: reply_tx,
            })
            .map_err(|_| DispatchError::ChannelClosed)?;
        reply_rx.recv().map_err(|_| DispatchError::ChannelClosed)?
    }

    /// Registers a live event channel with the PTY worker and returns the
    /// ordered historical replay. The returned attachment's `events`
    /// receiver must be pumped by exactly one thread.
    pub fn terminal_attach(
        &self,
        terminal_id: TerminalId,
    ) -> Result<TerminalAttachment, DispatchError> {
        let (reply_tx, reply_rx) = mpsc::channel();
        self.request_tx
            .send(ModelRequest::TerminalAttach {
                terminal_id,
                reply: reply_tx,
            })
            .map_err(|_| DispatchError::ChannelClosed)?;
        reply_rx.recv().map_err(|_| DispatchError::ChannelClosed)?
    }
}

/// A cloneable, thread-safe command/query handle to the application model.
///
/// Two transports exist:
/// - [`CommandClient`]: in-process, used by unit tests and the headless
///   server internals (mpsc channel into the model thread).
/// - `RemoteCommandClient` (in `crate::control`): the socket transport used
///   by the GUI client in the split client/server architecture. Every method
///   crosses the control socket as a length-prefixed JSON frame.
///
/// The mutation path is unchanged: UI handlers, `water ctl`, and scenarios
/// all funnel into `AppCommand -> CommandDispatcher` on the model thread.
pub trait CommandTransport: Send + Sync {
    /// Dispatches a command and returns its operation id (blocks until the
    /// model thread accepts it).
    fn dispatch(&self, command: AppCommand) -> Result<OperationId, DispatchError>;

    /// Enqueues a command without waiting for an operation id. Used for
    /// high-frequency terminal input; implementations must not block the
    /// caller on the model thread.
    fn enqueue(&self, command: AppCommand) -> Result<(), DispatchError>;

    fn get_operation(&self, operation_id: OperationId) -> Option<OperationSnapshot>;

    fn wait_operation(&self, operation_id: OperationId)
    -> Result<OperationSnapshot, DispatchError>;

    fn state_dump(&self) -> Result<crate::app::model::ModelSnapshot, DispatchError>;

    fn memory_stats(&self) -> Result<crate::app::model::MemoryStats, DispatchError>;

    fn events_since(&self, sequence: u64) -> Result<Vec<AppEvent>, DispatchError>;

    fn terminal_contains(
        &self,
        terminal_id: TerminalId,
        text: String,
        timeout: Duration,
    ) -> Result<TerminalSnapshot, DispatchError>;

    fn wait_terminal_exit(
        &self,
        terminal_id: TerminalId,
        timeout: Duration,
    ) -> Result<TerminalSnapshot, DispatchError>;

    fn terminal_replay(&self, terminal_id: TerminalId) -> Result<TerminalReplay, DispatchError>;
}

impl CommandTransport for CommandClient {
    fn dispatch(&self, command: AppCommand) -> Result<OperationId, DispatchError> {
        CommandClient::dispatch(self, command)
    }

    fn enqueue(&self, command: AppCommand) -> Result<(), DispatchError> {
        CommandClient::enqueue(self, command)
    }

    fn get_operation(&self, operation_id: OperationId) -> Option<OperationSnapshot> {
        CommandClient::get_operation(self, operation_id)
    }

    fn wait_operation(
        &self,
        operation_id: OperationId,
    ) -> Result<OperationSnapshot, DispatchError> {
        CommandClient::wait_operation(self, operation_id)
    }

    fn state_dump(&self) -> Result<crate::app::model::ModelSnapshot, DispatchError> {
        CommandClient::state_dump(self)
    }

    fn memory_stats(&self) -> Result<crate::app::model::MemoryStats, DispatchError> {
        CommandClient::memory_stats(self)
    }

    fn events_since(&self, sequence: u64) -> Result<Vec<AppEvent>, DispatchError> {
        CommandClient::events_since(self, sequence)
    }

    fn terminal_contains(
        &self,
        terminal_id: TerminalId,
        text: String,
        timeout: Duration,
    ) -> Result<TerminalSnapshot, DispatchError> {
        CommandClient::terminal_contains(self, terminal_id, text, timeout)
    }

    fn wait_terminal_exit(
        &self,
        terminal_id: TerminalId,
        timeout: Duration,
    ) -> Result<TerminalSnapshot, DispatchError> {
        CommandClient::wait_terminal_exit(self, terminal_id, timeout)
    }

    fn terminal_replay(&self, terminal_id: TerminalId) -> Result<TerminalReplay, DispatchError> {
        CommandClient::terminal_replay(self, terminal_id)
    }
}

#[derive(Debug, Clone)]
struct ModelThreadConfig {
    app_config: AppConfig,
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
        let terminal_config = ModelThreadConfig {
            app_config: config.clone(),
        };
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
                    terminal_config,
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
    terminal_config: ModelThreadConfig,
) {
    let ModelThreadConfig { app_config } = terminal_config;
    let mut dispatcher =
        CommandDispatcher::with_operations_and_config(operations, terminal_wakeup, app_config);
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
                let registry = dispatcher.terminal_registry();
                std::thread::spawn(move || {
                    let result = registry
                        .contains_text(terminal_id, &text, timeout)
                        .map_err(terminal_dispatch_error);
                    let _ = reply.send(result);
                });
            }
            ModelRequest::TerminalExit {
                terminal_id,
                timeout,
                reply,
            } => {
                let registry = dispatcher.terminal_registry();
                std::thread::spawn(move || {
                    let result = registry
                        .wait_process_exit(terminal_id, timeout)
                        .map_err(terminal_dispatch_error);
                    let _ = reply.send(result);
                });
            }
            ModelRequest::TerminalReplay { terminal_id, reply } => {
                let _ = reply.send(dispatcher.terminal_replay(terminal_id));
            }
            ModelRequest::TerminalAttach { terminal_id, reply } => {
                let _ = reply.send(dispatcher.terminal_attach(terminal_id));
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
            agents: Vec::new(),
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
