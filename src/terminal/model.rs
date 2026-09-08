use std::collections::{BTreeMap, VecDeque};
use std::path::PathBuf;
use std::sync::mpsc::{self, Receiver, Sender};
use std::sync::{Arc, Condvar, Mutex};
use std::thread::{self, JoinHandle};
use std::time::{Duration, Instant};

use thiserror::Error;

use crate::ids::TerminalId;

use super::TerminalSnapshot;
use super::TerminalTheme;
use super::snapshot::{
    DEFAULT_INACTIVE_SCROLLBACK_LINES, DEFAULT_MAX_TOTAL_SCROLLBACK_BYTES,
    DEFAULT_SCROLLBACK_LINES, MAX_RECENT_OUTPUT_BYTES, MAX_SCROLLBACK_LINES,
    MAX_TOTAL_SCROLLBACK_BYTES, MIN_MAX_TOTAL_SCROLLBACK_BYTES, TerminalProcessState, TerminalSize,
    TerminalSummary, scrollback_row_bytes,
};

/// Retired terminals keep a compact final snapshot for waiters that race
/// with pane auto-close. The window stays small so closed tabs cannot pin
/// memory long after they were closed.
const MAX_RETIRED_TERMINALS: usize = 16;
/// Viewport rows kept when a retired terminal's snapshot is compacted.
const RETIRED_SNAPSHOT_TAIL_LINES: usize = 24;
/// Recent-output bytes kept when a retired terminal is compacted.
const RETIRED_RECENT_OUTPUT_BYTES: usize = 8 * 1024;
pub(crate) type WakeupCallback = Arc<dyn Fn() + Send + Sync + 'static>;
pub(crate) type WakeupSlot = Arc<Mutex<Option<WakeupCallback>>>;

/// Per-terminal and aggregate scrollback limits resolved from `AppConfig`.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct TerminalLimits {
    /// Scrollback rows a focused terminal may retain.
    pub scrollback_lines: usize,
    /// Scrollback rows a terminal retains once it loses focus.
    pub inactive_scrollback_lines: usize,
    /// Aggregate byte budget shared by all terminal scrollback grids.
    pub max_total_scrollback_bytes: usize,
}

impl Default for TerminalLimits {
    fn default() -> Self {
        Self {
            scrollback_lines: DEFAULT_SCROLLBACK_LINES,
            inactive_scrollback_lines: DEFAULT_INACTIVE_SCROLLBACK_LINES,
            max_total_scrollback_bytes: DEFAULT_MAX_TOTAL_SCROLLBACK_BYTES,
        }
    }
}

impl TerminalLimits {
    pub(crate) fn normalized(mut self) -> Self {
        self.scrollback_lines = self.scrollback_lines.clamp(1, MAX_SCROLLBACK_LINES);
        self.inactive_scrollback_lines = self
            .inactive_scrollback_lines
            .clamp(1, MAX_SCROLLBACK_LINES)
            .min(self.scrollback_lines);
        self.max_total_scrollback_bytes = self
            .max_total_scrollback_bytes
            .clamp(MIN_MAX_TOTAL_SCROLLBACK_BYTES, MAX_TOTAL_SCROLLBACK_BYTES);
        self
    }
}

/// Coordinates scrollback growth across terminal workers in bytes.
///
/// The budget is accounted in estimated heap bytes, not rows, because a
/// row's cost scales with the terminal width. A focused terminal that is
/// scrolled away from live output borrows the unused global budget so new
/// output cannot evict the rows the user is looking at. A terminal that
/// loses focus is trimmed to its inactive tail and releases the rest of its
/// reservation, mirroring how tmux keeps only `history-limit` rows per
/// window and how herdr keeps background panes cheap.
#[derive(Clone, Debug)]
pub(crate) struct ScrollbackBudget {
    state: Arc<Mutex<ScrollbackBudgetState>>,
    max_bytes: usize,
}

#[derive(Debug, Default)]
struct ScrollbackBudgetState {
    entries: BTreeMap<TerminalId, ScrollbackBudgetEntry>,
}

#[derive(Debug, Clone, Copy)]
struct ScrollbackBudgetEntry {
    retained_rows: usize,
    retained_bytes: usize,
    reservation_bytes: usize,
}

impl ScrollbackBudget {
    pub(crate) fn new(max_bytes: usize) -> Self {
        Self {
            state: Arc::new(Mutex::new(ScrollbackBudgetState::default())),
            max_bytes: max_bytes.clamp(MIN_MAX_TOTAL_SCROLLBACK_BYTES, MAX_TOTAL_SCROLLBACK_BYTES),
        }
    }

    pub(crate) fn register(&self, terminal_id: TerminalId, base_limit_rows: usize, columns: usize) {
        let mut state = self.state.lock().expect("scrollback budget poisoned");
        if state.entries.contains_key(&terminal_id) {
            return;
        }
        let reserved = state
            .entries
            .values()
            .map(|entry| entry.reservation_bytes)
            .sum::<usize>();
        let reservation = base_limit_rows
            .saturating_mul(scrollback_row_bytes(columns))
            .min(self.max_bytes.saturating_sub(reserved));
        state.entries.insert(
            terminal_id,
            ScrollbackBudgetEntry {
                retained_rows: 0,
                retained_bytes: 0,
                reservation_bytes: reservation,
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

    /// Returns the history row limit this terminal may use right now.
    ///
    /// `base_limit_rows` already reflects focus (an unfocused terminal passes
    /// its smaller inactive limit). Only a focused terminal that is scrolled
    /// away from live output (`borrow`) may expand into the unused budget.
    /// Updating the reservation before the worker changes its `Grid` makes
    /// concurrent PTY workers observe the same global ceiling.
    pub(crate) fn limit_for(
        &self,
        terminal_id: TerminalId,
        retained_rows: usize,
        columns: usize,
        base_limit_rows: usize,
        borrow: bool,
    ) -> usize {
        let row_bytes = scrollback_row_bytes(columns);
        let mut state = self.state.lock().expect("scrollback budget poisoned");
        let reserved_by_others = state
            .entries
            .iter()
            .filter(|(id, _)| **id != terminal_id)
            .map(|(_, entry)| entry.reservation_bytes)
            .sum::<usize>();
        let available_bytes = self.max_bytes.saturating_sub(reserved_by_others);
        let base_bytes = base_limit_rows.saturating_mul(row_bytes);
        let limit_bytes = if borrow {
            available_bytes.max(base_bytes)
        } else {
            base_bytes.min(available_bytes)
        };
        let limit_rows = limit_bytes / row_bytes;
        if let Some(entry) = state.entries.get_mut(&terminal_id) {
            entry.retained_rows = retained_rows;
            entry.retained_bytes = retained_rows.saturating_mul(row_bytes);
            entry.reservation_bytes = limit_rows.saturating_mul(row_bytes);
        }
        limit_rows
    }

    pub(crate) fn sync(&self, terminal_id: TerminalId, retained_rows: usize, columns: usize) {
        let row_bytes = scrollback_row_bytes(columns);
        if let Some(entry) = self
            .state
            .lock()
            .expect("scrollback budget poisoned")
            .entries
            .get_mut(&terminal_id)
        {
            entry.retained_rows = retained_rows;
            entry.retained_bytes = retained_rows.saturating_mul(row_bytes);
        }
    }

    pub(crate) fn retained_rows(&self) -> usize {
        self.state
            .lock()
            .expect("scrollback budget poisoned")
            .entries
            .values()
            .map(|entry| entry.retained_rows)
            .sum()
    }

    pub(crate) fn retained_bytes(&self) -> usize {
        self.state
            .lock()
            .expect("scrollback budget poisoned")
            .entries
            .values()
            .map(|entry| entry.retained_bytes)
            .sum()
    }
}

#[derive(Debug)]
pub(crate) enum TerminalWorkerCommand {
    SendText(Vec<u8>),
    SendBytes(Vec<u8>),
    Resize(TerminalSize),
    Scroll(i32),
    SetViewportPosition {
        target: i64,
    },
    /// Informs the worker whether its terminal is the focused one, so the
    /// scrollback reconciler applies the focused or the inactive limit.
    SetFocused(bool),
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
        /// Bounded foreground argv used by the model for agent detection.
        cmdline: Vec<String>,
        /// True while the PTY has produced output within the worker's
        /// activity window; drives the agent busy/idle indicator.
        active: bool,
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
    snapshot: Arc<TerminalSnapshot>,
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
                snapshot: Arc::new(snapshot),
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
        Ok(self.snapshot_arc(terminal_id)?.as_ref().clone())
    }

    /// Shares the registry's snapshot without copying its grid cells. State
    /// dumps and the UI projection layer use this so exactly one full grid
    /// exists per terminal (the tmux/herdr single-source-of-truth split).
    pub fn snapshot_arc(
        &self,
        terminal_id: TerminalId,
    ) -> Result<Arc<TerminalSnapshot>, TerminalError> {
        let entry = self.entry(terminal_id)?;
        Ok(entry
            .state
            .lock()
            .expect("terminal entry poisoned")
            .snapshot
            .clone())
    }

    /// Cell-free metadata view for terminals whose grid is not projected.
    pub fn summary(&self, terminal_id: TerminalId) -> Option<TerminalSummary> {
        let entry = self
            .entries
            .lock()
            .expect("terminal registry poisoned")
            .get(&terminal_id)
            .cloned()?;
        Some(
            entry
                .state
                .lock()
                .expect("terminal entry poisoned")
                .snapshot
                .summary(),
        )
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
                return Ok(state.snapshot.as_ref().clone());
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

    /// Waits for the PTY worker to publish an acknowledgement of an absolute
    /// viewport target. This observes revisioned snapshots and never exposes
    /// the mutable terminal grid to callers.
    pub fn wait_viewport_position(
        &self,
        terminal_id: TerminalId,
        target: i64,
        timeout: Duration,
    ) -> Result<TerminalSnapshot, TerminalError> {
        let entry = self.entry(terminal_id)?;
        let deadline = Instant::now() + timeout;
        let mut state = entry.state.lock().expect("terminal entry poisoned");
        loop {
            if state.snapshot.viewport_position == target {
                return Ok(state.snapshot.as_ref().clone());
            }
            if matches!(state.snapshot.process, TerminalProcessState::Exited { .. }) {
                return Err(TerminalError::ProcessExited(terminal_id));
            }
            let now = Instant::now();
            if now >= deadline {
                return Err(TerminalError::Timeout(timeout.as_millis() as u64));
            }
            let (new_state, result) = entry
                .changed
                .wait_timeout(state, deadline.saturating_duration_since(now))
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
                return Ok(state.snapshot.as_ref().clone());
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
        state.snapshot = Arc::new(snapshot);
        if !output.is_empty() {
            // The waiters only ever match against the trailing
            // MAX_RECENT_OUTPUT_BYTES of the stream. Pushing just the chunk
            // tail keeps that invariant (the stream tail always survives the
            // trim) while a large drain chunk no longer costs a full
            // lossy-conversion copy per tick.
            let tail_start = output.len().saturating_sub(MAX_RECENT_OUTPUT_BYTES);
            state
                .recent_output
                .push_str(&String::from_utf8_lossy(&output[tail_start..]));
            trim_recent_output(&mut state.recent_output, MAX_RECENT_OUTPUT_BYTES);
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
    ) -> Result<Arc<TerminalSnapshot>, TerminalError> {
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
        let exited = Arc::make_mut(&mut state.snapshot);
        exited.process = TerminalProcessState::Exited { code };
        entry.changed.notify_all();
    }

    /// Replaces a retired terminal's retained state with a bounded tail so
    /// closed tabs cannot pin full grids in memory until the retirement
    /// limit evicts them.
    pub(crate) fn compact_for_retirement(&self, terminal_id: TerminalId) {
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
        state.snapshot = Arc::new(state.snapshot.compacted_tail(RETIRED_SNAPSHOT_TAIL_LINES));
        trim_recent_output(&mut state.recent_output, RETIRED_RECENT_OUTPUT_BYTES);
        entry.changed.notify_all();
    }

    pub fn terminal_count(&self) -> usize {
        self.entries
            .lock()
            .expect("terminal registry poisoned")
            .len()
    }

    /// Estimated heap bytes pinned by the snapshots and recent-output
    /// buffers the registry retains. Exposed through `waterctl debug
    /// memory` so operators can watch closed tabs release memory.
    pub fn retained_snapshot_bytes(&self) -> usize {
        let entries: Vec<_> = self
            .entries
            .lock()
            .expect("terminal registry poisoned")
            .values()
            .cloned()
            .collect();
        entries
            .iter()
            .map(|entry| {
                let state = entry.state.lock().expect("terminal entry poisoned");
                let snapshot = &state.snapshot;
                let overscan_cells: usize = snapshot
                    .rows_before
                    .iter()
                    .chain(snapshot.rows_after.iter())
                    .map(|row| row.len())
                    .sum();
                (snapshot.cell_count() + overscan_cells)
                    * std::mem::size_of::<crate::terminal::TerminalCell>()
                    + state.recent_output.len()
                    + snapshot.process_name.len()
                    + snapshot.cwd.len()
            })
            .sum()
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

fn trim_recent_output(output: &mut String, max_bytes: usize) {
    if output.len() <= max_bytes {
        return;
    }
    let mut remove = output.len() - max_bytes;
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

/// Makes the repo-bundled `alacritty` terminfo discoverable for child shells
/// when the system has none. The bundled entry's `clear` capability appends
/// CSI 3 J (erase scrollback), matching kitty semantics; without it, macOS
/// `xterm-256color`'s `clear` leaves the command line ghosted at the top of
/// the scrollback because alacritty's ESC[2J pushes the screen into history.
fn ensure_bundled_terminfo_env() {
    static READY: std::sync::OnceLock<()> = std::sync::OnceLock::new();
    READY.get_or_init(|| {
        let Some(dir) = bundled_terminfo_dir() else {
            return;
        };
        let existing = std::env::var("TERMINFO_DIRS").unwrap_or_default();
        if existing.split(':').any(|entry| {
            PathBuf::from(entry).join("61").join("alacritty").exists()
                || PathBuf::from(entry).join("a").join("alacritty").exists()
        }) {
            return;
        }
        // A trailing empty entry keeps the compiled-in system directories
        // searchable for every other TERM entry.
        let merged = if existing.is_empty() {
            format!("{}:", dir.display())
        } else {
            format!("{}:{}", dir.display(), existing)
        };
        // SAFETY: called once from the model thread before any terminal
        // child is spawned; the process reads this variable only through
        // alacritty_terminal's setup_env immediately afterward.
        unsafe {
            std::env::set_var("TERMINFO_DIRS", merged);
        }
    });
}

fn bundled_terminfo_dir() -> Option<PathBuf> {
    let from_env = std::env::var_os("WATER_TERMINFO_DIR").map(PathBuf::from);
    let exe_dir = std::env::current_exe().ok().and_then(|exe| {
        exe.parent().map(|parent| {
            [
                parent.join("terminfo"),
                parent.join("../Resources/terminfo"),
                parent.join("../share/terminfo"),
                parent.join("../../assets/terminfo"),
                parent.join("../../../assets/terminfo"),
            ]
        })
    });
    let candidates = from_env
        .into_iter()
        .chain(exe_dir.into_iter().flatten())
        .chain([PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("assets/terminfo")]);
    candidates.into_iter().find(|dir| {
        dir.join("61").join("alacritty").exists() || dir.join("a").join("alacritty").exists()
    })
}

/// Best-effort hint for the macOS zone allocator to consolidate freed
/// regions after large terminal grids were dropped. This is a nudge, not a
/// guarantee: libmalloc commonly keeps `MADV_FREE` pages counted in `ps`
/// RSS for reuse (the classic tmux "cleared history, RSS stayed 1GB"
/// report). The authoritative accounting is `waterctl debug memory`
/// (`retained_scrollback_bytes` + `registry_snapshot_bytes`), which does
/// drop as soon as workers and registry entries are released.
fn release_allocator_pressure() {
    #[cfg(target_os = "macos")]
    unsafe extern "C" {
        fn malloc_zone_pressure_relief(
            zone: *mut std::ffi::c_void,
            tosize: usize,
        ) -> std::ffi::c_int;
    }
    #[cfg(target_os = "macos")]
    unsafe {
        malloc_zone_pressure_relief(std::ptr::null_mut(), 0);
    }
}

pub struct TerminalManager {
    registry: TerminalRegistry,
    theme: TerminalTheme,
    limits: TerminalLimits,
    scrollback_budget: ScrollbackBudget,
    focused_terminal: Option<TerminalId>,
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
        Self::new_with_wakeup_and_limits(None, TerminalLimits::default(), TerminalTheme::default())
    }

    pub fn new_with_scrollback(scrollback_lines: usize) -> Self {
        Self::new_with_wakeup_and_limits(
            None,
            TerminalLimits {
                scrollback_lines,
                ..TerminalLimits::default()
            },
            TerminalTheme::default(),
        )
    }

    pub(crate) fn new_with_wakeup_and_limits(
        event_wakeup: Option<WakeupCallback>,
        limits: TerminalLimits,
        theme: TerminalTheme,
    ) -> Self {
        let limits = limits.normalized();
        let (event_tx, event_rx) = mpsc::channel();
        Self {
            registry: TerminalRegistry::new(),
            theme,
            limits,
            scrollback_budget: ScrollbackBudget::new(limits.max_total_scrollback_bytes),
            focused_terminal: None,
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
        let fallback_cmdline = std::iter::once(program.clone())
            .chain(args.iter().cloned())
            .collect::<Vec<_>>();
        let shell_env = super::shell_integration::child_environment(&program);
        ensure_bundled_terminfo_env();
        alacritty_terminal::tty::setup_env();
        let options = alacritty_terminal::tty::Options {
            shell: Some(alacritty_terminal::tty::Shell::new(program, args)),
            working_directory,
            drain_on_exit: false,
            env: shell_env,
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
            .register(terminal_id, self.limits.scrollback_lines, size.columns);

        let event_tx = self.event_tx.clone();
        let event_wakeup = self.event_wakeup.clone();
        let registry = self.registry.clone();
        let worker_wakeup = wakeup.clone();
        let scrollback_lines = self.limits.scrollback_lines;
        let inactive_scrollback_lines = self.limits.inactive_scrollback_lines;
        let scrollback_budget = self.scrollback_budget.clone();
        let metadata_executor = self.metadata_executor.clone();
        let theme = self.theme;
        let join_handle = match thread::Builder::new()
            .name(format!("water-terminal-{terminal_id}"))
            .spawn(move || {
                super::worker::run(
                    super::worker::WorkerConfig::new(
                        terminal_id,
                        size,
                        scrollback_lines,
                        inactive_scrollback_lines,
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
                            fallback_cmdline,
                        },
                    )
                    .with_theme(theme),
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

    pub fn set_viewport_position(
        &self,
        terminal_id: TerminalId,
        target: i64,
    ) -> Result<(), TerminalError> {
        self.registry.send(
            terminal_id,
            TerminalWorkerCommand::SetViewportPosition { target },
        )
    }

    /// Updates which terminal currently owns the user's attention. The
    /// focused terminal keeps its full scrollback reservation and may borrow
    /// unused budget; a demoted terminal is trimmed to its inactive tail by
    /// its own worker as soon as the command is applied.
    pub(crate) fn set_focused_terminal(&mut self, focused: Option<TerminalId>) {
        if self.focused_terminal == focused {
            return;
        }
        let previous = std::mem::replace(&mut self.focused_terminal, focused);
        for terminal_id in previous.into_iter().chain(focused) {
            let value = Some(terminal_id) == focused;
            let _ = self
                .registry
                .send(terminal_id, TerminalWorkerCommand::SetFocused(value));
        }
    }

    pub fn remove(&mut self, terminal_id: TerminalId) {
        self.stop_worker(terminal_id);
        if self.focused_terminal == Some(terminal_id) {
            self.focused_terminal = None;
        }
        self.retired.retain(|retired| *retired != terminal_id);
        self.registry.remove(terminal_id);
        release_allocator_pressure();
    }

    /// Stops a terminal worker after its pane has been closed, but retains a
    /// compact final snapshot for waiters that race with the auto-close
    /// event. Retired snapshots are bounded and removed at the limit.
    pub(crate) fn retire(&mut self, terminal_id: TerminalId) {
        self.stop_worker(terminal_id);
        if self.focused_terminal == Some(terminal_id) {
            self.focused_terminal = None;
        }
        self.retired.retain(|retired| *retired != terminal_id);
        self.retired.push_back(terminal_id);
        self.registry.compact_for_retirement(terminal_id);
        while self.retired.len() > MAX_RETIRED_TERMINALS {
            if let Some(retired) = self.retired.pop_front() {
                self.registry.remove(retired);
            }
        }
        release_allocator_pressure();
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
        self.limits.scrollback_lines
    }

    pub fn inactive_scrollback_lines(&self) -> usize {
        self.limits.inactive_scrollback_lines
    }

    pub fn max_total_scrollback_bytes(&self) -> usize {
        self.limits.max_total_scrollback_bytes
    }

    pub fn retained_scrollback_lines(&self) -> usize {
        self.scrollback_budget.retained_rows()
    }

    pub fn retained_scrollback_bytes(&self) -> usize {
        self.scrollback_budget.retained_bytes()
    }

    pub fn retained_snapshot_bytes(&self) -> usize {
        self.registry.retained_snapshot_bytes()
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
        let columns = 16;
        let row = scrollback_row_bytes(columns);
        let budget = ScrollbackBudget::new(row * 1_000);
        let first = TerminalId::new(1);
        let second = TerminalId::new(2);
        budget.register(first, 100, columns);
        budget.register(second, 100, columns);

        assert_eq!(budget.limit_for(first, 0, columns, 100, true), 900);
        assert_eq!(budget.limit_for(second, 0, columns, 100, false), 100);

        assert_eq!(budget.limit_for(first, 0, columns, 100, false), 100);
        assert_eq!(budget.limit_for(second, 0, columns, 100, true), 900);

        budget.sync(second, 12, columns);
        assert_eq!(budget.retained_rows(), 12);
        assert_eq!(budget.retained_bytes(), 12 * row);
        budget.unregister(second);
        assert_eq!(budget.retained_rows(), 0);
    }

    #[test]
    fn budget_accounts_wide_rows_in_bytes_not_rows() {
        let row80 = scrollback_row_bytes(80);
        let row160 = scrollback_row_bytes(160);
        let budget = ScrollbackBudget::new(row80 * 40);
        let terminal_id = TerminalId::new(7);
        // A 40-row 80-column history fills the budget exactly when borrowing.
        assert_eq!(budget.limit_for(terminal_id, 0, 80, 4, true), 40);
        // 160-column rows cost roughly twice as many bytes, so the same
        // budget yields far fewer of them and never exceeds the byte cap.
        let wide_rows = budget.limit_for(terminal_id, 0, 160, 4, true);
        assert!(wide_rows < 40);
        assert!(wide_rows.saturating_mul(row160) <= row80 * 40);
    }

    #[test]
    fn retired_snapshot_compaction_keeps_the_visible_tail() {
        use crate::terminal::TerminalCell;
        let terminal_id = TerminalId::new(3);
        let size = TerminalSize::new(4, 60);
        let mut snapshot = TerminalSnapshot::empty(terminal_id, size);
        for row in 0..size.lines {
            *snapshot.cell_mut(row, 0).unwrap() = TerminalCell {
                character: char::from(b'0' + (row % 10) as u8),
                ..TerminalCell::default()
            };
        }
        snapshot.revision = 41;
        let compacted = snapshot.compacted_tail(24);
        assert_eq!(compacted.size.lines, 24);
        assert_eq!(compacted.cell_count(), 24 * size.columns);
        assert_eq!(compacted.cell(0, 0).unwrap().character, '6');
        assert_eq!(compacted.cell(23, 0).unwrap().character, '9');
        assert!(compacted.rows_before.is_empty());
        assert_eq!(compacted.revision, 41);
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
