use std::collections::{BTreeMap, VecDeque};
use std::path::PathBuf;
use std::sync::mpsc::{self, Receiver, Sender};
use std::sync::{Arc, Condvar, Mutex};
use std::thread::{self, JoinHandle};
use std::time::{Duration, Instant};

use thiserror::Error;

use crate::ids::TerminalId;

use super::snapshot::{
    MAX_RECENT_OUTPUT_BYTES, MAX_SCROLLBACK_LINES, MAX_TOTAL_SCROLLBACK_LINES,
    TerminalProcessState, TerminalSize, TerminalSnapshot,
};

pub(crate) const TERMINAL_WAKE_KEY: usize = usize::MAX - 1;
const MAX_RETIRED_TERMINALS: usize = 64;
pub(crate) type WakeupCallback = Arc<dyn Fn() + Send + Sync + 'static>;
pub(crate) type WakeupSlot = Arc<Mutex<Option<WakeupCallback>>>;

/// Coordinates temporary scrollback growth across terminal workers.
///
/// A terminal that is scrolled away from the live output gets a reservation
/// for the unused global budget. The reservation is released when the user
/// returns to the live end or sends input, so a busy terminal cannot grow
/// without bound while another terminal is open.
#[derive(Clone, Debug)]
pub(crate) struct ScrollbackBudget {
    state: Arc<Mutex<ScrollbackBudgetState>>,
    max_lines: usize,
}

#[derive(Debug, Default)]
struct ScrollbackBudgetState {
    entries: BTreeMap<TerminalId, ScrollbackBudgetEntry>,
}

#[derive(Debug, Clone, Copy)]
struct ScrollbackBudgetEntry {
    retained: usize,
    reservation: usize,
    active: bool,
}

impl ScrollbackBudget {
    pub(crate) fn new(max_lines: usize) -> Self {
        Self {
            state: Arc::new(Mutex::new(ScrollbackBudgetState::default())),
            max_lines: max_lines.clamp(1, MAX_TOTAL_SCROLLBACK_LINES),
        }
    }

    pub(crate) fn register(&self, terminal_id: TerminalId, base_limit: usize) {
        let mut state = self.state.lock().expect("scrollback budget poisoned");
        if state.entries.contains_key(&terminal_id) {
            return;
        }
        let reserved = state
            .entries
            .values()
            .map(|entry| entry.reservation)
            .sum::<usize>();
        let reservation = base_limit.min(self.max_lines.saturating_sub(reserved));
        state.entries.insert(
            terminal_id,
            ScrollbackBudgetEntry {
                retained: 0,
                reservation,
                active: false,
            },
        );
    }

    pub(crate) fn unregister(&self, terminal_id: TerminalId) {
        self.state
            .lock()
            .expect("scrollback budget poisoned")
            .entries
            .remove(&terminal_id);
    }

    /// Returns the history limit this terminal may use right now.
    ///
    /// Inactive terminals retain their normal per-terminal reservation. An
    /// active terminal may borrow all budget not reserved by the other
    /// terminals. Updating the reservation before the worker changes its
    /// `Grid` makes concurrent PTY workers observe the same global ceiling.
    pub(crate) fn limit_for(
        &self,
        terminal_id: TerminalId,
        retained: usize,
        base_limit: usize,
        active: bool,
    ) -> usize {
        let mut state = self.state.lock().expect("scrollback budget poisoned");
        let reserved_by_others = state
            .entries
            .iter()
            .filter(|(id, _)| **id != terminal_id)
            .map(|(_, entry)| entry.reservation)
            .sum::<usize>();
        let available = self.max_lines.saturating_sub(reserved_by_others);
        let limit = if active {
            available
        } else {
            base_limit.min(available)
        };
        if let Some(entry) = state.entries.get_mut(&terminal_id) {
            entry.retained = retained;
            entry.reservation = limit;
            entry.active = active;
        }
        limit
    }

    pub(crate) fn sync(&self, terminal_id: TerminalId, retained: usize, active: bool) {
        if let Some(entry) = self
            .state
            .lock()
            .expect("scrollback budget poisoned")
            .entries
            .get_mut(&terminal_id)
        {
            entry.retained = retained;
            entry.active = active;
        }
    }

    pub(crate) fn retained_lines(&self) -> usize {
        self.state
            .lock()
            .expect("scrollback budget poisoned")
            .entries
            .values()
            .map(|entry| entry.retained)
            .sum()
    }
}

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
    ProcessChanged {
        terminal_id: TerminalId,
        process_name: String,
        cwd: String,
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
    output_event_pending: bool,
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
                output_event_pending: false,
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

    /// Publishes the latest worker snapshot and returns whether the model
    /// needs a new output notification. At most one output event per terminal
    /// is queued until the model atomically takes the latest snapshot.
    pub(crate) fn publish(
        &self,
        terminal_id: TerminalId,
        snapshot: TerminalSnapshot,
        output: &[u8],
    ) -> bool {
        let Some(entry) = self
            .entries
            .lock()
            .expect("terminal registry poisoned")
            .get(&terminal_id)
            .cloned()
        else {
            return false;
        };
        let mut state = entry.state.lock().expect("terminal entry poisoned");
        state.snapshot = snapshot;
        if !output.is_empty() {
            state
                .recent_output
                .push_str(&String::from_utf8_lossy(output));
            trim_recent_output(&mut state.recent_output);
        }
        let should_notify = !state.output_event_pending;
        state.output_event_pending = true;
        entry.changed.notify_all();
        should_notify
    }

    /// Takes the newest snapshot represented by a queued output event and
    /// clears that event while holding the same lock used by publishers. A
    /// later publication will therefore always queue a fresh notification.
    pub(crate) fn take_output_snapshot(
        &self,
        terminal_id: TerminalId,
    ) -> Result<TerminalSnapshot, TerminalError> {
        let entry = self.entry(terminal_id)?;
        let mut state = entry.state.lock().expect("terminal entry poisoned");
        let snapshot = state.snapshot.clone();
        state.output_event_pending = false;
        Ok(snapshot)
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

fn process_name_from_program(program: &str) -> String {
    std::path::Path::new(program)
        .file_name()
        .and_then(|name| name.to_str())
        .filter(|name| !name.is_empty())
        .unwrap_or("shell")
        .trim_start_matches('-')
        .to_owned()
}

pub struct TerminalManager {
    registry: TerminalRegistry,
    scrollback_lines: usize,
    max_total_scrollback_lines: usize,
    scrollback_budget: ScrollbackBudget,
    event_tx: Sender<TerminalManagerEvent>,
    event_rx: Receiver<TerminalManagerEvent>,
    event_wakeup: Option<WakeupCallback>,
    metadata_executor: super::worker::ProcessMetadataExecutor,
    workers: BTreeMap<TerminalId, WorkerHandle>,
    retired: VecDeque<TerminalId>,
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
        Self::new_with_scrollback(MAX_SCROLLBACK_LINES)
    }

    pub fn new_with_scrollback(scrollback_lines: usize) -> Self {
        Self::new_with_wakeup_and_scrollback(None, scrollback_lines)
    }

    pub(crate) fn new_with_wakeup_and_scrollback(
        event_wakeup: Option<WakeupCallback>,
        scrollback_lines: usize,
    ) -> Self {
        Self::new_with_wakeup_and_scrollback_and_total(
            event_wakeup,
            scrollback_lines,
            MAX_TOTAL_SCROLLBACK_LINES,
        )
    }

    pub(crate) fn new_with_wakeup_and_scrollback_and_total(
        event_wakeup: Option<WakeupCallback>,
        scrollback_lines: usize,
        max_total_scrollback_lines: usize,
    ) -> Self {
        let (event_tx, event_rx) = mpsc::channel();
        let scrollback_lines = scrollback_lines.clamp(1, MAX_SCROLLBACK_LINES);
        let max_total_scrollback_lines =
            max_total_scrollback_lines.clamp(1, MAX_TOTAL_SCROLLBACK_LINES);
        Self {
            registry: TerminalRegistry::new(),
            scrollback_lines,
            max_total_scrollback_lines,
            scrollback_budget: ScrollbackBudget::new(max_total_scrollback_lines),
            event_tx,
            event_rx,
            event_wakeup,
            metadata_executor: super::worker::ProcessMetadataExecutor::new(),
            workers: BTreeMap::new(),
            retired: VecDeque::new(),
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
        self.spawn_with_working_directory(terminal_id, program, args, size, None)
    }

    pub fn spawn_with_working_directory(
        &mut self,
        terminal_id: TerminalId,
        program: String,
        args: Vec<String>,
        size: TerminalSize,
        working_directory: Option<PathBuf>,
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

        let working_directory = working_directory.filter(|path| path.is_dir());
        let fallback_cwd = working_directory
            .clone()
            .or_else(|| std::env::current_dir().ok())
            .unwrap_or_else(|| PathBuf::from("."));
        let fallback_process_name = process_name_from_program(&program);
        alacritty_terminal::tty::setup_env();
        let options = alacritty_terminal::tty::Options {
            shell: Some(alacritty_terminal::tty::Shell::new(program, args)),
            working_directory,
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
        let mut initial_snapshot = TerminalSnapshot::empty(terminal_id, size);
        initial_snapshot.process_name = fallback_process_name.clone();
        initial_snapshot.cwd = fallback_cwd.display().to_string();
        let (command_tx, command_rx) = mpsc::channel();
        let wakeup = Arc::new(Mutex::new(None));
        self.registry.register(
            terminal_id,
            initial_snapshot,
            command_tx.clone(),
            wakeup.clone(),
        )?;
        self.scrollback_budget
            .register(terminal_id, self.scrollback_lines);

        let event_tx = self.event_tx.clone();
        let event_wakeup = self.event_wakeup.clone();
        let registry = self.registry.clone();
        let worker_wakeup = wakeup.clone();
        let scrollback_lines = self.scrollback_lines;
        let scrollback_budget = self.scrollback_budget.clone();
        let metadata_executor = self.metadata_executor.clone();
        let join_handle = match thread::Builder::new()
            .name(format!("water-terminal-{terminal_id}"))
            .spawn(move || {
                super::worker::run(
                    super::worker::WorkerConfig::new(
                        terminal_id,
                        size,
                        scrollback_lines,
                        scrollback_budget,
                        command_rx,
                        super::worker::WorkerChannels::new(
                            registry,
                            event_tx,
                            event_wakeup,
                            worker_wakeup,
                            metadata_executor,
                        ),
                        super::worker::WorkerMetadata {
                            fallback_process_name,
                            fallback_cwd,
                        },
                    ),
                    pty,
                )
            }) {
            Ok(join_handle) => join_handle,
            Err(error) => {
                self.scrollback_budget.unregister(terminal_id);
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
        self.stop_worker(terminal_id);
        self.retired.retain(|retired| *retired != terminal_id);
        self.registry.remove(terminal_id);
    }

    /// Stops a terminal worker after its pane has been closed, but retains the
    /// final registry snapshot for waiters that race with the auto-close event.
    /// Retired snapshots are bounded and are removed when the limit is reached.
    pub(crate) fn retire(&mut self, terminal_id: TerminalId) {
        self.stop_worker(terminal_id);
        self.retired.retain(|retired| *retired != terminal_id);
        self.retired.push_back(terminal_id);
        while self.retired.len() > MAX_RETIRED_TERMINALS {
            if let Some(retired) = self.retired.pop_front() {
                self.registry.remove(retired);
            }
        }
    }

    fn stop_worker(&mut self, terminal_id: TerminalId) {
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
    }

    pub(crate) fn drain_events(&mut self) -> Vec<TerminalManagerEvent> {
        self.event_rx.try_iter().collect()
    }

    pub fn terminal_count(&self) -> usize {
        self.workers.len()
    }

    pub fn scrollback_lines(&self) -> usize {
        self.scrollback_lines
    }

    pub fn scrollback_capacity_lines(&self) -> usize {
        self.workers
            .len()
            .saturating_mul(self.scrollback_lines)
            .min(self.max_total_scrollback_lines)
    }

    pub fn retained_scrollback_lines(&self) -> usize {
        self.scrollback_budget.retained_lines()
    }

    pub fn max_total_scrollback_lines(&self) -> usize {
        self.max_total_scrollback_lines
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

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn active_terminal_borrows_and_releases_global_scrollback_budget() {
        let budget = ScrollbackBudget::new(100);
        let first = TerminalId::new(1);
        let second = TerminalId::new(2);
        budget.register(first, 10);
        budget.register(second, 10);

        assert_eq!(budget.limit_for(first, 0, 10, true), 90);
        assert_eq!(budget.limit_for(second, 0, 10, false), 10);

        assert_eq!(budget.limit_for(first, 0, 10, false), 10);
        assert_eq!(budget.limit_for(second, 0, 10, true), 90);

        budget.sync(second, 12, true);
        assert_eq!(budget.retained_lines(), 12);
        budget.unregister(second);
        assert_eq!(budget.retained_lines(), 0);
    }

    #[test]
    fn terminal_output_notifications_coalesce_until_latest_snapshot_is_taken() {
        let registry = TerminalRegistry::new();
        let terminal_id = TerminalId::new(1);
        let size = TerminalSize::new(8, 2);
        let (command_tx, _command_rx) = mpsc::channel();
        registry
            .register(
                terminal_id,
                TerminalSnapshot::empty(terminal_id, size),
                command_tx,
                Arc::new(Mutex::new(None)),
            )
            .unwrap();

        let mut first = TerminalSnapshot::empty(terminal_id, size);
        first.revision = 1;
        assert!(registry.publish(terminal_id, first, b"first"));

        let mut latest = TerminalSnapshot::empty(terminal_id, size);
        latest.revision = 2;
        assert!(!registry.publish(terminal_id, latest, b"latest"));
        assert_eq!(
            registry.take_output_snapshot(terminal_id).unwrap().revision,
            2
        );

        let mut next = TerminalSnapshot::empty(terminal_id, size);
        next.revision = 3;
        assert!(registry.publish(terminal_id, next, b"next"));
    }
}
