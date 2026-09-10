use std::collections::{BTreeMap, VecDeque};
use std::path::PathBuf;
use std::sync::mpsc::{self, Receiver, Sender, SyncSender};
use std::sync::{Arc, Condvar, Mutex};
use std::thread::{self, JoinHandle};
use std::time::{Duration, Instant};

use thiserror::Error;

use crate::ids::TerminalId;

use super::replay::MAX_REPLAY_BYTES;
use super::replay::ReplayRing;
use super::snapshot::{
    DEFAULT_INACTIVE_SCROLLBACK_LINES, DEFAULT_MAX_TOTAL_SCROLLBACK_BYTES,
    DEFAULT_REPLAY_HISTORY_BYTES, DEFAULT_SCROLLBACK_LINES, MAX_SCROLLBACK_LINES,
    MIN_MAX_TOTAL_SCROLLBACK_BYTES, MIN_REPLAY_HISTORY_BYTES, TerminalProcessState, TerminalSize,
};
use super::stream::{
    TerminalSeq, TerminalStreamEvent, WireTerminalEvent, decode_base64, encode_base64,
};

/// Retired terminals keep a compact final replay for waiters that race
/// with pane auto-close. The window stays small so closed tabs cannot pin
/// memory long after they were closed.
const MAX_RETIRED_TERMINALS: usize = 16;
/// Recent-output bytes kept when a retired terminal is compacted.
const RETIRED_RECENT_OUTPUT_BYTES: usize = 8 * 1024;
/// Raw replay bytes kept when a terminal is retired.
const RETIRED_REPLAY_BYTES: usize = 1024 * 1024;
/// Bounded handoff between the PTY worker and one attached stream consumer.
const TERMINAL_ATTACHMENT_QUEUE_EVENTS: usize = 64;
pub(crate) type WakeupCallback = Arc<dyn Fn() + Send + Sync + 'static>;
pub(crate) type WakeupSlot = Arc<Mutex<Option<WakeupCallback>>>;

/// Per-terminal and aggregate limits resolved from `AppConfig`.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct TerminalLimits {
    /// Scrollback rows the GUI terminal emulator retains (focused terminal).
    pub scrollback_lines: usize,
    /// Scrollback rows the GUI terminal retains once it loses focus.
    pub inactive_scrollback_lines: usize,
    /// Aggregate byte budget shared by the GUI scrollback grids.
    pub max_total_scrollback_bytes: usize,
    /// Hard byte limit for the server's per-terminal raw replay history.
    pub replay_history_bytes: usize,
}

impl Default for TerminalLimits {
    fn default() -> Self {
        Self {
            scrollback_lines: DEFAULT_SCROLLBACK_LINES,
            inactive_scrollback_lines: DEFAULT_INACTIVE_SCROLLBACK_LINES,
            max_total_scrollback_bytes: DEFAULT_MAX_TOTAL_SCROLLBACK_BYTES,
            replay_history_bytes: DEFAULT_REPLAY_HISTORY_BYTES,
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
            .clamp(MIN_MAX_TOTAL_SCROLLBACK_BYTES, 1024 * 1024 * 1024);
        self.replay_history_bytes = self
            .replay_history_bytes
            .clamp(MIN_REPLAY_HISTORY_BYTES, MAX_REPLAY_BYTES);
        self
    }
}

/// The bounded raw replay history of one terminal, as returned by
/// `waterctl terminal snapshot` and `terminal.attach`. The caller replays
/// the events through a temporary local Alacritty terminal to inspect the
/// screen; the server itself keeps no long-running emulator.
#[derive(Debug, Clone, serde::Serialize, serde::Deserialize)]
pub struct TerminalReplay {
    pub terminal_id: TerminalId,
    /// Authoritative lifecycle state owned by the server.
    pub process: TerminalProcessState,
    /// Oldest surviving sequence (`None` when the ring is empty).
    #[serde(default)]
    pub first_seq: Option<TerminalSeq>,
    /// Sequence of the newest event.
    pub last_seq: TerminalSeq,
    /// Geometry of the oldest surviving event: the screen dimensions a
    /// replier must use before applying the events.
    pub size: TerminalSize,
    /// Wire form of the ordered events (base64 output bytes).
    pub events: Vec<WireTerminalEvent>,
}

/// The ordered raw event stream a client receives when attaching to a
/// terminal: the bounded historical replay plus a live event channel.
///
/// The replay and the live tail overlap: events delivered live that are
/// already in the replay carry `seq <= last_seq` and are skipped by the
/// client (strict monotonic sequences make the handoff seamless).
#[derive(Debug)]
pub struct TerminalAttachment {
    /// Historical events, oldest first (bounded by the replay ring budget).
    pub replay: Vec<TerminalStreamEvent>,
    /// Sequence of the newest event in the replay. Live events with
    /// `seq <= last_seq` are duplicates and must be skipped.
    pub last_seq: u64,
    /// Live events from the PTY worker.
    pub events: Receiver<TerminalStreamEvent>,
}

#[derive(Debug)]
pub(crate) enum TerminalWorkerCommand {
    SendText(Vec<u8>),
    SendBytes(Vec<u8>),
    Resize(TerminalSize),
    Attach {
        sender: SyncSender<TerminalStreamEvent>,
        reply: Sender<(Vec<TerminalStreamEvent>, TerminalSeq)>,
    },
    Shutdown,
}

#[derive(Debug, Clone)]
pub(crate) enum TerminalManagerEvent {
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
    process: TerminalProcessState,
    /// Bounded raw replay history. The PTY worker is the only writer;
    /// attach/capture paths read ordered snapshots of it.
    replay: Arc<ReplayRing>,
    /// Trailing raw output used by `contains` fast paths.
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
        replay: Arc<ReplayRing>,
        command_tx: Sender<TerminalWorkerCommand>,
        wakeup: WakeupSlot,
    ) -> Result<(), TerminalError> {
        let entry = Arc::new(TerminalEntry {
            state: Mutex::new(TerminalEntryState {
                process: TerminalProcessState::Running,
                replay,
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

    /// Process state without touching the replay history.
    pub fn process_state(
        &self,
        terminal_id: TerminalId,
    ) -> Result<TerminalProcessState, TerminalError> {
        let entry = self.entry(terminal_id)?;
        Ok(entry.state.lock().expect("terminal entry poisoned").process)
    }

    /// Attaches a live client: registers a bounded event channel with the
    /// PTY worker and returns the ordered historical replay. The worker
    /// registers the subscriber before taking the replay snapshot, so the
    /// client never misses an event (overlaps are deduplicated by sequence).
    pub fn attach(&self, terminal_id: TerminalId) -> Result<TerminalAttachment, TerminalError> {
        let entry = self.entry(terminal_id)?;
        let (sender, events) = mpsc::sync_channel(TERMINAL_ATTACHMENT_QUEUE_EVENTS);
        let (reply_tx, reply_rx) = mpsc::channel();
        {
            let state = entry.state.lock().expect("terminal entry poisoned");
            if matches!(state.process, TerminalProcessState::Exited { .. })
                && state.replay.is_finished()
            {
                // The worker is gone; the replay is the whole history.
                let replay = state.replay.to_stream();
                let last_seq = state.replay.last_seq();
                return Ok(TerminalAttachment {
                    replay,
                    last_seq,
                    events,
                });
            }
        }
        entry
            .command_tx
            .send(TerminalWorkerCommand::Attach {
                sender,
                reply: reply_tx,
            })
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
        let (replay, last_seq) = reply_rx
            .recv_timeout(Duration::from_secs(5))
            .map_err(|_| TerminalError::WorkerClosed(terminal_id))?;
        Ok(TerminalAttachment {
            replay,
            last_seq,
            events,
        })
    }

    /// The full bounded replay history in wire form, plus its bounds and the
    /// replay-start geometry.
    pub fn replay(&self, terminal_id: TerminalId) -> Result<TerminalReplay, TerminalError> {
        let entry = self.entry(terminal_id)?;
        let state = entry.state.lock().expect("terminal entry poisoned");
        Ok(state.replay(terminal_id))
    }

    pub fn contains_text(
        &self,
        terminal_id: TerminalId,
        needle: &str,
        timeout: Duration,
    ) -> Result<TerminalReplay, TerminalError> {
        let entry = self.entry(terminal_id)?;
        let deadline = Instant::now() + timeout;
        let mut state = entry.state.lock().expect("terminal entry poisoned");
        loop {
            if state.recent_output.contains(needle) || state.replay.raw_contains(needle) {
                return Ok(state.replay(terminal_id));
            }
            if matches!(state.process, TerminalProcessState::Exited { .. }) {
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
    ) -> Result<TerminalReplay, TerminalError> {
        let entry = self.entry(terminal_id)?;
        let deadline = Instant::now() + timeout;
        let mut state = entry.state.lock().expect("terminal entry poisoned");
        loop {
            if matches!(state.process, TerminalProcessState::Exited { .. }) {
                return Ok(state.replay(terminal_id));
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
            if matches!(state.process, TerminalProcessState::Exited { .. }) {
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

    /// Records a raw output batch: extends the recent-output tail and wakes
    /// condition waiters. This is notification, not state — the terminal
    /// screen itself lives on the GUI.
    pub(crate) fn publish_output(&self, terminal_id: TerminalId, output: &[u8]) {
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
        if !output.is_empty() {
            let tail_start = output.len().saturating_sub(64 * 1024);
            state
                .recent_output
                .push_str(&String::from_utf8_lossy(&output[tail_start..]));
            trim_recent_output(&mut state.recent_output, 64 * 1024);
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
        state.process = TerminalProcessState::Exited { code };
        entry.changed.notify_all();
    }

    /// Bounds a retired terminal's retained history so closed tabs cannot
    /// pin full replays in memory until the retirement limit evicts them.
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
        state.replay.trim_to(RETIRED_REPLAY_BYTES);
        trim_recent_output(&mut state.recent_output, RETIRED_RECENT_OUTPUT_BYTES);
        entry.changed.notify_all();
    }

    pub fn terminal_count(&self) -> usize {
        self.entries
            .lock()
            .expect("terminal registry poisoned")
            .len()
    }

    /// Raw bytes pinned by replay rings and recent-output buffers.
    /// Exposed through `waterctl debug memory`.
    pub fn retained_replay_bytes(&self) -> usize {
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
                state.replay.total_bytes() + state.recent_output.len()
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

impl TerminalEntryState {
    fn replay(&self, terminal_id: TerminalId) -> TerminalReplay {
        TerminalReplay {
            terminal_id,
            process: self.process,
            first_seq: self.replay.first_seq(),
            last_seq: self.replay.last_seq(),
            size: self
                .replay
                .first_size()
                .or_else(|| self.replay.last_size())
                .unwrap_or_default(),
            events: self
                .replay
                .to_stream()
                .into_iter()
                .map(|event| event.to_wire())
                .collect(),
        }
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
/// regions after large terminal state was dropped.
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
    limits: TerminalLimits,
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
        Self::new_with_wakeup_and_limits(None, TerminalLimits::default())
    }

    pub fn new_with_scrollback(scrollback_lines: usize) -> Self {
        Self::new_with_wakeup_and_limits(
            None,
            TerminalLimits {
                scrollback_lines,
                ..TerminalLimits::default()
            },
        )
    }

    pub(crate) fn new_with_wakeup_and_limits(
        event_wakeup: Option<WakeupCallback>,
        limits: TerminalLimits,
    ) -> Self {
        let limits = limits.normalized();
        let (event_tx, event_rx) = mpsc::channel();
        Self {
            registry: TerminalRegistry::new(),
            limits,
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
        // The replay ring exists from spawn: its first event (sequence 1) is
        // the initial geometry anchor.
        let replay = ReplayRing::new(size, self.limits.replay_history_bytes);
        let (command_tx, command_rx) = mpsc::channel();
        let wakeup = Arc::new(Mutex::new(None));
        self.registry.register(
            terminal_id,
            replay.clone(),
            command_tx.clone(),
            wakeup.clone(),
        )?;

        let event_tx = self.event_tx.clone();
        let event_wakeup = self.event_wakeup.clone();
        let registry = self.registry.clone();
        let worker_wakeup = wakeup.clone();
        let replay_budget = self.limits.replay_history_bytes;
        let metadata_executor = self.metadata_executor.clone();
        let join_handle = match thread::Builder::new()
            .name(format!("water-terminal-{terminal_id}"))
            .spawn(move || {
                super::worker::run(
                    super::worker::WorkerConfig::new(
                        terminal_id,
                        size,
                        replay.clone(),
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
                    ),
                    pty,
                );
                let _ = replay_budget;
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

    pub fn attach(&self, terminal_id: TerminalId) -> Result<TerminalAttachment, TerminalError> {
        self.registry.attach(terminal_id)
    }

    pub fn remove(&mut self, terminal_id: TerminalId) {
        self.stop_worker(terminal_id);
        self.retired.retain(|retired| *retired != terminal_id);
        self.registry.remove(terminal_id);
        release_allocator_pressure();
    }

    /// Stops a terminal worker after its pane has been closed, but retains a
    /// bounded final replay for waiters that race with the auto-close event.
    /// Retired replays are bounded and removed at the limit.
    pub(crate) fn retire(&mut self, terminal_id: TerminalId) {
        self.stop_worker(terminal_id);
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

    pub fn replay_history_bytes(&self) -> usize {
        self.limits.replay_history_bytes
    }

    pub fn retained_replay_bytes(&self) -> usize {
        self.registry.retained_replay_bytes()
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

// Kept for wire/API compatibility of the terminal snapshot surface.
#[allow(dead_code)]
fn _base64_roundtrip(data: &[u8]) -> Option<Vec<u8>> {
    decode_base64(&encode_base64(data))
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::terminal::stream::TerminalStreamEvent;

    fn test_ring() -> Arc<ReplayRing> {
        ReplayRing::new(TerminalSize::new(80, 24), 64 * 1024)
    }

    #[test]
    fn registry_attach_delivers_replay_and_live_events() {
        let registry = TerminalRegistry::new();
        let terminal_id = TerminalId::new(1);
        let replay = test_ring();
        let (command_tx, command_rx) = mpsc::channel();
        registry
            .register(
                terminal_id,
                replay.clone(),
                command_tx,
                Arc::new(Mutex::new(None)),
            )
            .unwrap();

        // Simulate the worker side: an attach command is answered with the
        // ring snapshot and a live channel.
        let replay_for_worker = replay.clone();
        std::thread::spawn(move || match command_rx.recv().unwrap() {
            TerminalWorkerCommand::Attach { sender, reply } => {
                let replay_events = replay_for_worker.to_stream();
                let last_seq = replay_for_worker.last_seq();
                let _ = reply.send((replay_events, last_seq));
                let size = TerminalSize::new(80, 24);
                let bytes: Arc<[u8]> = Arc::from(b"print\n".to_vec());
                let seq = replay_for_worker.push_output(size, bytes.clone());
                let event = TerminalStreamEvent::Output { seq, size, bytes };
                let _ = sender.send(event.clone());
                let _ = sender.send(event);
            }
            other => panic!("unexpected command {other:?}"),
        });

        let attachment = registry.attach(terminal_id).unwrap();
        assert_eq!(attachment.last_seq, 1);
        let first = attachment
            .events
            .recv_timeout(Duration::from_secs(1))
            .unwrap();
        let second = attachment
            .events
            .recv_timeout(Duration::from_secs(1))
            .unwrap();
        assert_eq!(first.seq(), second.seq());
        assert_eq!(first.output_bytes(), b"print\n".len());
    }

    #[test]
    fn wait_process_exit_uses_lifecycle_state() {
        let registry = TerminalRegistry::new();
        let terminal_id = TerminalId::new(2);
        let (command_tx, _command_rx) = mpsc::channel();
        registry
            .register(
                terminal_id,
                test_ring(),
                command_tx,
                Arc::new(Mutex::new(None)),
            )
            .unwrap();

        let waiter_registry = registry.clone();
        let waiter = std::thread::spawn(move || {
            waiter_registry.wait_process_exit(terminal_id, Duration::from_secs(5))
        });
        registry.mark_exited(terminal_id, Some(3));
        let replay = waiter.join().unwrap().unwrap();
        assert!(matches!(
            replay.process,
            TerminalProcessState::Exited { code: Some(3) }
        ));
    }

    #[test]
    fn replay_snapshot_rebuilds_the_screen_from_raw_events() {
        let registry = TerminalRegistry::new();
        let terminal_id = TerminalId::new(3);
        let replay = test_ring();
        let (command_tx, _command_rx) = mpsc::channel();
        registry
            .register(
                terminal_id,
                replay.clone(),
                command_tx,
                Arc::new(Mutex::new(None)),
            )
            .unwrap();

        let size = TerminalSize::new(80, 24);
        replay.push_output(size, Arc::from(b"WATER_CAPTURE_ME\r\n".to_vec()));
        let capture = registry.replay(terminal_id).unwrap();
        let snapshot = crate::terminal::snapshot_from_replay(&capture, 2_000);
        assert!(snapshot.visible_text().contains("WATER_CAPTURE_ME"));

        // Resize history is honored by the replay.
        let wide = TerminalSize::new(100, 30);
        replay.push_resize(wide);
        let capture = registry.replay(terminal_id).unwrap();
        let snapshot = crate::terminal::snapshot_from_replay(&capture, 2_000);
        assert_eq!(snapshot.size.columns, 100);
    }

    #[test]
    fn contains_text_matches_the_raw_replay_history() {
        let registry = TerminalRegistry::new();
        let terminal_id = TerminalId::new(4);
        let (command_tx, _command_rx) = mpsc::channel();
        registry
            .register(
                terminal_id,
                test_ring(),
                command_tx,
                Arc::new(Mutex::new(None)),
            )
            .unwrap();
        registry.publish_output(terminal_id, b"needle-haystack-needle\n");
        let replay = registry
            .contains_text(terminal_id, "needle-haystack", Duration::from_secs(1))
            .unwrap();
        assert_eq!(replay.terminal_id, terminal_id);
    }
}
