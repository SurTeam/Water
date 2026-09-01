use std::collections::BTreeMap;
use std::sync::mpsc::{self, Receiver, Sender};
use std::sync::{Arc, Condvar, Mutex};
use std::thread::{self, JoinHandle};
use std::time::{Duration, Instant};

use thiserror::Error;

use crate::ids::TerminalId;

use super::snapshot::{
    MAX_RECENT_OUTPUT_BYTES, TerminalProcessState, TerminalSize, TerminalSnapshot,
};

pub(crate) const TERMINAL_WAKE_KEY: usize = usize::MAX - 1;
pub(crate) type WakeupCallback = Arc<dyn Fn() + Send + Sync + 'static>;
pub(crate) type WakeupSlot = Arc<Mutex<Option<WakeupCallback>>>;

#[derive(Debug)]
pub(crate) enum TerminalWorkerCommand {
    SendText(Vec<u8>),
    SendBytes(Vec<u8>),
    Resize(TerminalSize),
    Scroll(i32),
    Shutdown,
}

#[derive(Debug, Clone)]
pub(crate) enum TerminalManagerEvent {
    OutputChanged {
        terminal_id: TerminalId,
    },
    TitleChanged {
        terminal_id: TerminalId,
        title: String,
    },
    Exited {
        terminal_id: TerminalId,
        code: Option<i32>,
    },
}

#[derive(Debug, Error)]
pub enum TerminalError {
    #[error("terminal {0} was not found")]
    NotFound(TerminalId),
    #[error("terminal {0} is not running")]
    NotRunning(TerminalId),
    #[error("terminal spawn failed: {0}")]
    SpawnFailed(String),
    #[error("terminal worker channel closed for {0}")]
    WorkerClosed(TerminalId),
    #[error("terminal wait timed out after {0}ms")]
    Timeout(u64),
    #[error("terminal {0} exited before the requested condition")]
    ProcessExited(TerminalId),
}

#[derive(Clone, Debug)]
pub struct TerminalRegistry {
    entries: Arc<Mutex<BTreeMap<TerminalId, Arc<TerminalEntry>>>>,
}

struct TerminalEntry {
    state: Mutex<TerminalEntryState>,
    changed: Condvar,
    command_tx: Sender<TerminalWorkerCommand>,
    wakeup: WakeupSlot,
}

impl std::fmt::Debug for TerminalEntry {
    fn fmt(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        formatter
            .debug_struct("TerminalEntry")
            .field("state", &self.state)
            .finish_non_exhaustive()
    }
}

#[derive(Debug)]
struct TerminalEntryState {
    snapshot: TerminalSnapshot,
    recent_output: String,
}

impl Default for TerminalRegistry {
    fn default() -> Self {
        Self::new()
    }
}

impl TerminalRegistry {
    pub fn new() -> Self {
        Self {
            entries: Arc::new(Mutex::new(BTreeMap::new())),
        }
    }

    pub(crate) fn register(
        &self,
        terminal_id: TerminalId,
        snapshot: TerminalSnapshot,
        command_tx: Sender<TerminalWorkerCommand>,
        wakeup: WakeupSlot,
    ) -> Result<(), TerminalError> {
        let entry = Arc::new(TerminalEntry {
            state: Mutex::new(TerminalEntryState {
                snapshot,
                recent_output: String::new(),
            }),
            changed: Condvar::new(),
            command_tx,
            wakeup,
        });
        let mut entries = self.entries.lock().expect("terminal registry poisoned");
        if entries.insert(terminal_id, entry).is_some() {
            return Err(TerminalError::SpawnFailed(format!(
                "terminal ID {terminal_id} already exists"
            )));
        }
        Ok(())
    }

    pub(crate) fn remove(&self, terminal_id: TerminalId) {
        self.entries
            .lock()
            .expect("terminal registry poisoned")
            .remove(&terminal_id);
    }

    pub fn snapshot(&self, terminal_id: TerminalId) -> Result<TerminalSnapshot, TerminalError> {
        let entry = self.entry(terminal_id)?;
        Ok(entry
            .state
            .lock()
            .expect("terminal entry poisoned")
            .snapshot
            .clone())
    }

    pub fn contains_text(
        &self,
        terminal_id: TerminalId,
        needle: &str,
        timeout: Duration,
    ) -> Result<TerminalSnapshot, TerminalError> {
        let entry = self.entry(terminal_id)?;
        let deadline = Instant::now() + timeout;
        let mut state = entry.state.lock().expect("terminal entry poisoned");
        loop {
            if state.recent_output.contains(needle) || state.snapshot.contains_text(needle) {
                return Ok(state.snapshot.clone());
            }
            if matches!(state.snapshot.process, TerminalProcessState::Exited { .. }) {
                return Err(TerminalError::ProcessExited(terminal_id));
            }
            let now = Instant::now();
            if now >= deadline {
                return Err(TerminalError::Timeout(timeout.as_millis() as u64));
            }
            let remaining = deadline.saturating_duration_since(now);
            let (new_state, result) = entry
                .changed
                .wait_timeout(state, remaining)
                .expect("terminal entry poisoned");
            state = new_state;
            if result.timed_out() {
                return Err(TerminalError::Timeout(timeout.as_millis() as u64));
            }
        }
    }

    pub fn wait_process_exit(
        &self,
        terminal_id: TerminalId,
        timeout: Duration,
    ) -> Result<TerminalSnapshot, TerminalError> {
        let entry = self.entry(terminal_id)?;
        let deadline = Instant::now() + timeout;
        let mut state = entry.state.lock().expect("terminal entry poisoned");
        loop {
            if matches!(state.snapshot.process, TerminalProcessState::Exited { .. }) {
                return Ok(state.snapshot.clone());
            }
            let now = Instant::now();
            if now >= deadline {
                return Err(TerminalError::Timeout(timeout.as_millis() as u64));
            }
            let remaining = deadline.saturating_duration_since(now);
            let (new_state, result) = entry
                .changed
                .wait_timeout(state, remaining)
                .expect("terminal entry poisoned");
            state = new_state;
            if result.timed_out() {
                return Err(TerminalError::Timeout(timeout.as_millis() as u64));
            }
        }
    }

    pub(crate) fn send(
        &self,
        terminal_id: TerminalId,
        command: TerminalWorkerCommand,
    ) -> Result<(), TerminalError> {
        let entry = self.entry(terminal_id)?;
        {
            let state = entry.state.lock().expect("terminal entry poisoned");
            if matches!(state.snapshot.process, TerminalProcessState::Exited { .. }) {
                return Err(TerminalError::NotRunning(terminal_id));
            }
        }
        entry
            .command_tx
            .send(command)
            .map_err(|_| TerminalError::WorkerClosed(terminal_id))?;
        if let Some(wakeup) = entry
            .wakeup
            .lock()
            .expect("terminal wakeup poisoned")
            .as_ref()
            .cloned()
        {
            wakeup();
        }
        Ok(())
    }

    pub(crate) fn publish(
        &self,
        terminal_id: TerminalId,
        snapshot: TerminalSnapshot,
        output: &[u8],
    ) {
        let Some(entry) = self
            .entries
            .lock()
            .expect("terminal registry poisoned")
            .get(&terminal_id)
            .cloned()
        else {
            return;
        };
        let mut state = entry.state.lock().expect("terminal entry poisoned");
        state.snapshot = snapshot;
        if !output.is_empty() {
            state
                .recent_output
                .push_str(&String::from_utf8_lossy(output));
            trim_recent_output(&mut state.recent_output);
        }
        entry.changed.notify_all();
    }

    pub(crate) fn mark_exited(&self, terminal_id: TerminalId, code: Option<i32>) {
        let Some(entry) = self
            .entries
            .lock()
            .expect("terminal registry poisoned")
            .get(&terminal_id)
            .cloned()
        else {
            return;
        };
        let mut state = entry.state.lock().expect("terminal entry poisoned");
        state.snapshot.process = TerminalProcessState::Exited { code };
        entry.changed.notify_all();
    }

    pub fn terminal_count(&self) -> usize {
        self.entries
            .lock()
            .expect("terminal registry poisoned")
            .len()
    }

    fn entry(&self, terminal_id: TerminalId) -> Result<Arc<TerminalEntry>, TerminalError> {
        self.entries
            .lock()
            .expect("terminal registry poisoned")
            .get(&terminal_id)
            .cloned()
            .ok_or(TerminalError::NotFound(terminal_id))
    }
}

fn trim_recent_output(output: &mut String) {
    if output.len() <= MAX_RECENT_OUTPUT_BYTES {
        return;
    }
    let mut remove = output.len() - MAX_RECENT_OUTPUT_BYTES;
    while remove < output.len() && !output.is_char_boundary(remove) {
        remove += 1;
    }
    output.drain(..remove);
}

pub struct TerminalManager {
    registry: TerminalRegistry,
    event_tx: Sender<TerminalManagerEvent>,
    event_rx: Receiver<TerminalManagerEvent>,
    event_wakeup: Option<WakeupCallback>,
    workers: BTreeMap<TerminalId, WorkerHandle>,
}

struct WorkerHandle {
    command_tx: Sender<TerminalWorkerCommand>,
    wakeup: WakeupSlot,
    join_handle: Option<JoinHandle<()>>,
}

impl std::fmt::Debug for TerminalManager {
    fn fmt(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        formatter
            .debug_struct("TerminalManager")
            .field("terminal_count", &self.workers.len())
            .finish()
    }
}

impl TerminalManager {
    pub fn new() -> Self {
        Self::new_with_wakeup(None)
    }

    pub(crate) fn new_with_wakeup(event_wakeup: Option<WakeupCallback>) -> Self {
        let (event_tx, event_rx) = mpsc::channel();
        Self {
            registry: TerminalRegistry::new(),
            event_tx,
            event_rx,
            event_wakeup,
            workers: BTreeMap::new(),
        }
    }

    pub fn registry(&self) -> TerminalRegistry {
        self.registry.clone()
    }

    pub fn spawn(
        &mut self,
        terminal_id: TerminalId,
        program: String,
        args: Vec<String>,
        size: TerminalSize,
    ) -> Result<(), TerminalError> {
        if program.is_empty() {
            return Err(TerminalError::SpawnFailed(
                "terminal program must not be empty".to_owned(),
            ));
        }
        if self.workers.contains_key(&terminal_id) {
            return Err(TerminalError::SpawnFailed(format!(
                "terminal ID {terminal_id} already exists"
            )));
        }

        alacritty_terminal::tty::setup_env();
        let options = alacritty_terminal::tty::Options {
            shell: Some(alacritty_terminal::tty::Shell::new(program, args)),
            working_directory: None,
            drain_on_exit: false,
            env: Default::default(),
        };
        let window_size = alacritty_terminal::event::WindowSize {
            num_lines: size.lines as u16,
            num_cols: size.columns as u16,
            cell_width: 0,
            cell_height: 0,
        };
        let pty = alacritty_terminal::tty::new(&options, window_size, terminal_id.get())
            .map_err(|error| TerminalError::SpawnFailed(error.to_string()))?;
        let initial_snapshot = TerminalSnapshot::empty(terminal_id, size);
        let (command_tx, command_rx) = mpsc::channel();
        let wakeup = Arc::new(Mutex::new(None));
        self.registry.register(
            terminal_id,
            initial_snapshot,
            command_tx.clone(),
            wakeup.clone(),
        )?;

        let event_tx = self.event_tx.clone();
        let event_wakeup = self.event_wakeup.clone();
        let registry = self.registry.clone();
        let worker_wakeup = wakeup.clone();
        let join_handle = match thread::Builder::new()
            .name(format!("water-terminal-{terminal_id}"))
            .spawn(move || {
                super::worker::run(
                    super::worker::WorkerConfig::new(
                        terminal_id,
                        size,
                        command_rx,
                        registry,
                        event_tx,
                        event_wakeup,
                        worker_wakeup,
                    ),
                    pty,
                )
            }) {
            Ok(join_handle) => join_handle,
            Err(error) => {
                self.registry.remove(terminal_id);
                return Err(TerminalError::SpawnFailed(error.to_string()));
            }
        };
        self.workers.insert(
            terminal_id,
            WorkerHandle {
                command_tx,
                wakeup,
                join_handle: Some(join_handle),
            },
        );
        Ok(())
    }

    pub fn send_text(&self, terminal_id: TerminalId, text: String) -> Result<(), TerminalError> {
        self.registry.send(
            terminal_id,
            TerminalWorkerCommand::SendText(text.into_bytes()),
        )
    }

    pub fn send_bytes(&self, terminal_id: TerminalId, bytes: Vec<u8>) -> Result<(), TerminalError> {
        self.registry
            .send(terminal_id, TerminalWorkerCommand::SendBytes(bytes))
    }

    pub fn resize(&self, terminal_id: TerminalId, size: TerminalSize) -> Result<(), TerminalError> {
        self.registry
            .send(terminal_id, TerminalWorkerCommand::Resize(size))
    }

    pub fn scroll(&self, terminal_id: TerminalId, lines: i32) -> Result<(), TerminalError> {
        self.registry
            .send(terminal_id, TerminalWorkerCommand::Scroll(lines))
    }

    pub fn remove(&mut self, terminal_id: TerminalId) {
        if let Some(worker) = self.workers.remove(&terminal_id) {
            let _ = worker.command_tx.send(TerminalWorkerCommand::Shutdown);
            if let Some(wakeup) = worker
                .wakeup
                .lock()
                .expect("terminal wakeup poisoned")
                .as_ref()
                .cloned()
            {
                wakeup();
            }
            if let Some(join_handle) = worker.join_handle {
                let _ = join_handle.join();
            }
        }
        self.registry.remove(terminal_id);
    }

    pub(crate) fn drain_events(&mut self) -> Vec<TerminalManagerEvent> {
        self.event_rx.try_iter().collect()
    }

    pub fn terminal_count(&self) -> usize {
        self.workers.len()
    }

    pub fn shutdown_all(&mut self) {
        let terminal_ids: Vec<_> = self.workers.keys().copied().collect();
        for terminal_id in terminal_ids {
            self.remove(terminal_id);
        }
    }
}

impl Default for TerminalManager {
    fn default() -> Self {
        Self::new()
    }
}

impl Drop for TerminalManager {
    fn drop(&mut self) {
        self.shutdown_all();
    }
}
