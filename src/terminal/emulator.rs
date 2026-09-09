//! Local terminal emulator: the GUI-side Alacritty `Term` + `Processor`.
//!
//! The server never renders; it emits an ordered raw PTY stream. The GUI
//! replays the bounded history and then the live tail into this local
//! emulator, which owns the screen grid, cursor, modes, and scrollback.
//! Rendering reads [`TerminalEmulator::snapshot`]; keyboard/mouse input
//! goes to the PTY through the regular command path, and emulator query
//! responses come back out through [`TerminalEmulator::pty_writes`].

use std::sync::Arc;
use std::sync::atomic::{AtomicBool, Ordering};

use alacritty_terminal::event::{Event, EventListener};
use alacritty_terminal::grid::{Dimensions, Scroll};
use alacritty_terminal::term::{Config, Term, TermMode};
use alacritty_terminal::vte::ansi::{Processor, Rgb, StdSyncHandler};

use crate::ids::TerminalId;
use crate::metrics;

use super::TerminalTheme;
use super::model::TerminalReplay;
use super::snapshot::{TerminalProcessState, TerminalSize, TerminalSnapshot};
use super::stream::{TerminalSeq, TerminalStreamEvent};

/// How the emulator reacts to an applied event.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum EmulatorEffect {
    /// The emulator answered a terminal query (DA/DSR/OSC color/...); the
    /// bytes must be written to the PTY.
    PtyWrite(Vec<u8>),
    /// The PTY child exited.
    Exited { code: Option<i32> },
    /// A live stream skipped a sequence; the caller must discard this
    /// emulator and reattach from the server replay ring.
    SequenceGap {
        expected: TerminalSeq,
        actual: TerminalSeq,
    },
}

struct EmulatorProxy {
    /// Shared with the emulator: flips from replay (side effects dropped) to
    /// live (PtyWrite bytes forwarded to the PTY).
    live: Arc<AtomicBool>,
    pty_write_tx: std::sync::mpsc::Sender<Vec<u8>>,
    theme: TerminalTheme,
}

impl std::fmt::Debug for EmulatorProxy {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("EmulatorProxy")
            .field(
                "live",
                &self.live.load(std::sync::atomic::Ordering::Relaxed),
            )
            .finish()
    }
}

impl EmulatorProxy {
    fn forward(&self, bytes: Vec<u8>) {
        if self.live.load(Ordering::Acquire) {
            let _ = self.pty_write_tx.send(bytes);
        }
    }
}

impl EventListener for EmulatorProxy {
    fn send_event(&self, event: Event) {
        match event {
            Event::PtyWrite(text) => self.forward(text.into_bytes()),
            Event::ColorRequest(index, format) => {
                if let Some(color) = terminal_dynamic_color(self.theme, index) {
                    self.forward(format(color).into_bytes());
                }
            }
            // Side effects suppressed by design: title changes arrive
            // through the server's metadata scanner, the shell owns exit
            // state, and bells/clipboard are out of scope.
            Event::Title(_)
            | Event::ResetTitle
            | Event::Exit
            | Event::ChildExit(_)
            | Event::MouseCursorDirty
            | Event::ClipboardStore(_, _)
            | Event::ClipboardLoad(_, _)
            | Event::TextAreaSizeRequest(_)
            | Event::CursorBlinkingChange
            | Event::Wakeup
            | Event::Bell => {}
        }
    }
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

/// The GUI-owned terminal screen. One instance per (connection, terminal).
pub struct TerminalEmulator {
    term: Term<EmulatorProxy>,
    /// The vte parser. It must persist across output events so escape
    /// sequences split across frames are reassembled, not lost.
    processor: Processor<StdSyncHandler>,
    /// Shared replay/live flag (owned by the term's proxy as well).
    live: Arc<AtomicBool>,
    terminal_id: TerminalId,
    last_seq: TerminalSeq,
    process: TerminalProcessState,
    /// Local visual state changed since the GUI last consumed it.
    dirty: bool,
    pty_write_rx: std::sync::mpsc::Receiver<Vec<u8>>,
}

impl TerminalEmulator {
    pub fn new(terminal_id: TerminalId, size: TerminalSize, scrollback_lines: usize) -> Self {
        Self::with_theme(
            terminal_id,
            size,
            scrollback_lines,
            TerminalTheme::default(),
        )
    }

    pub fn with_theme(
        terminal_id: TerminalId,
        size: TerminalSize,
        scrollback_lines: usize,
        theme: TerminalTheme,
    ) -> Self {
        let (pty_write_tx, pty_write_rx) = std::sync::mpsc::channel();
        let live = Arc::new(AtomicBool::new(false));
        let proxy = EmulatorProxy {
            live: live.clone(),
            pty_write_tx,
            theme,
        };
        let term = Term::new(
            Config {
                scrolling_history: scrollback_lines,
                ..Config::default()
            },
            &size,
            proxy,
        );
        Self {
            term,
            processor: Processor::new(),
            live,
            terminal_id,
            last_seq: 0,
            process: TerminalProcessState::Running,
            dirty: false,
            pty_write_rx,
        }
    }

    /// Applies one ordered stream event. Out-of-order (duplicate or stale)
    /// events are skipped by sequence, which is what makes the
    /// replay/live handoff seamless.
    pub fn apply(&mut self, event: &TerminalStreamEvent) -> Vec<EmulatorEffect> {
        self.apply_batch(std::slice::from_ref(event))
    }

    /// Applies an ordered batch, concatenating adjacent output events so a
    /// PTY burst reaches Alacritty through one `Processor::advance` call.
    pub fn apply_batch(&mut self, events: &[TerminalStreamEvent]) -> Vec<EmulatorEffect> {
        let mut effects = Vec::new();
        let mut output = Vec::new();
        for event in events {
            if event.seq() <= self.last_seq {
                continue;
            }
            if !self.replaying()
                && self.last_seq != 0
                && event.seq() != self.last_seq.saturating_add(1)
            {
                effects.push(EmulatorEffect::SequenceGap {
                    expected: self.last_seq.saturating_add(1),
                    actual: event.seq(),
                });
                break;
            }
            self.last_seq = event.seq();
            self.dirty = true;
            match event {
                TerminalStreamEvent::Output { bytes, .. } => {
                    output.extend_from_slice(bytes);
                }
                TerminalStreamEvent::Resize { size, .. } => {
                    if !output.is_empty() {
                        self.advance(&output);
                        output.clear();
                    }
                    if *size != self.size() {
                        self.term.resize(*size);
                    }
                }
                TerminalStreamEvent::Exit { code, .. } => {
                    if !output.is_empty() {
                        self.advance(&output);
                        output.clear();
                    }
                    self.process = TerminalProcessState::Exited { code: *code };
                    effects.push(EmulatorEffect::Exited { code: *code });
                }
            }
        }
        if !output.is_empty() {
            self.advance(&output);
        }
        effects
    }

    /// Runs raw PTY bytes through the persistent vte parser against the
    /// local term.
    pub fn advance(&mut self, bytes: &[u8]) {
        if !bytes.is_empty() {
            self.dirty = true;
        }
        metrics::inc(metrics::processor_advances());
        metrics::add(metrics::terminal_bytes_advanced(), bytes.len());
        self.processor.advance(&mut self.term, bytes);
    }

    /// Switches from replay to live: from now on, emulator query responses
    /// (PtyWrite) are forwarded to the PTY.
    pub fn start_live(&mut self) {
        self.live.store(true, Ordering::Release);
    }

    pub fn replaying(&self) -> bool {
        !self.live.load(Ordering::Acquire)
    }

    /// Returns and clears the local repaint bit.
    pub fn take_dirty(&mut self) -> bool {
        std::mem::take(&mut self.dirty)
    }

    /// Drains pending emulator query responses that must be written to the
    /// PTY (DA/DSR/OSC 10/11/12 answers).
    pub fn pty_writes(&self) -> Vec<Vec<u8>> {
        self.pty_write_rx.try_iter().collect()
    }

    pub fn size(&self) -> TerminalSize {
        TerminalSize::new(self.term.grid().columns(), self.term.grid().screen_lines())
    }

    pub fn terminal_id(&self) -> TerminalId {
        self.terminal_id
    }

    pub fn last_seq(&self) -> TerminalSeq {
        self.last_seq
    }

    pub fn exited(&self) -> Option<Option<i32>> {
        match self.process {
            TerminalProcessState::Running => None,
            TerminalProcessState::Exited { code } => Some(code),
        }
    }

    /// Current viewport position in the cumulative coordinate the render
    /// code has always used: 0 = bottom, positive = rows up into history
    /// (the grid's `display_offset`, the single source of truth).
    pub fn viewport_position(&self) -> i64 {
        self.term.grid().display_offset() as i64
    }

    /// Rows of history currently retained above the viewport. The grid's
    /// `display_offset` is exactly the number of history rows above the
    /// viewport, so this equals [`TerminalEmulator::viewport_position`].
    pub fn history_len(&self) -> i64 {
        self.term
            .grid()
            .total_lines()
            .saturating_sub(self.term.grid().screen_lines()) as i64
    }

    /// Scrolls by `delta` rows (positive = up into history).
    pub fn scroll_by(&mut self, delta: i64) {
        if delta == 0 {
            return;
        }
        let clamped = delta.clamp(i32::MIN as i64, i32::MAX as i64) as i32;
        self.term.scroll_display(Scroll::Delta(clamped));
        self.dirty = true;
    }

    /// Absolute scroll target in the viewport coordinate (0 = bottom,
    /// positive = up). Clamped to the available history.
    pub fn scroll_to(&mut self, target: i64) {
        let target = target.max(0).min(self.history_len());
        let delta = target - self.viewport_position();
        if delta != 0 {
            self.scroll_by(delta);
        }
    }

    pub fn scroll_to_bottom(&mut self) {
        self.term.scroll_display(Scroll::Bottom);
        self.dirty = true;
    }

    pub fn cursor_visible(&self) -> bool {
        self.term.mode().contains(TermMode::SHOW_CURSOR)
    }

    pub fn mouse_reporting(&self) -> bool {
        self.term.mode().intersects(TermMode::MOUSE_MODE)
    }

    pub fn alternate_screen(&self) -> bool {
        self.term.mode().contains(TermMode::ALT_SCREEN)
    }

    /// Builds the renderable snapshot from the local grid. Row references
    /// are reused from `previous` when unchanged, so per-frame snapshots
    /// stay cheap during static content.
    pub fn snapshot(&self, previous: Option<&TerminalSnapshot>) -> TerminalSnapshot {
        let viewport_position = self.viewport_position();
        match previous {
            Some(previous)
                if previous.terminal_id == self.terminal_id && previous.size == self.size() =>
            {
                TerminalSnapshot::from_term_with_previous(
                    self.terminal_id,
                    &self.term,
                    self.process_state(),
                    self.last_seq,
                    viewport_position,
                    Some(previous),
                )
            }
            _ => TerminalSnapshot::from_term_with_viewport_position(
                self.terminal_id,
                &self.term,
                self.process_state(),
                self.last_seq,
                viewport_position,
            ),
        }
    }

    /// Process state for snapshots: the server's lifecycle is authoritative
    /// for metadata, but the emulator also knows when its child exited
    /// through the stream.
    pub fn process_state(&self) -> TerminalProcessState {
        self.process
    }

    /// True when the viewport is pinned to the bottom (output auto-scrolls).
    pub fn at_bottom(&self) -> bool {
        self.term.grid().display_offset() == 0
    }
}

/// Rebuilds a renderable screen in the caller from a server-provided raw
/// replay. This is used by `waterctl` and other inspection clients; it never
/// installs an emulator in the server or feeds replay-generated side effects
/// back to the PTY.
pub fn snapshot_from_replay(replay: &TerminalReplay, scrollback_lines: usize) -> TerminalSnapshot {
    let mut emulator = TerminalEmulator::new(replay.terminal_id, replay.size, scrollback_lines);
    let mut size = replay.size;
    for wire in &replay.events {
        let Some(event) = TerminalStreamEvent::from_wire(wire, size) else {
            continue;
        };
        if let TerminalStreamEvent::Resize { size: next, .. } = event {
            size = next;
        }
        emulator.apply(&event);
    }
    let mut snapshot = emulator.snapshot(None);
    snapshot.process = replay.process;
    snapshot
}

#[cfg(test)]
mod tests {
    use super::*;

    fn output(seq: TerminalSeq, bytes: &[u8], size: TerminalSize) -> TerminalStreamEvent {
        TerminalStreamEvent::Output {
            seq,
            size,
            bytes: Arc::from(bytes.to_vec()),
        }
    }

    #[test]
    fn replay_then_live_rebuilds_the_same_screen() {
        let size = TerminalSize::new(80, 24);
        let mut replay_emulator = TerminalEmulator::new(TerminalId::new(1), size, 500);
        let mut live_emulator = TerminalEmulator::new(TerminalId::new(1), size, 500);
        live_emulator.start_live();

        let history: [&[u8]; 3] = [b"line one\r\n", b"line two\r\n", b"line three\r\n"];
        for (index, chunk) in history.iter().enumerate() {
            let event = output(index as TerminalSeq + 1, chunk, size);
            replay_emulator.apply(&event);
            live_emulator.apply(&event);
        }
        assert_eq!(
            replay_emulator.snapshot(None).visible_text(),
            live_emulator.snapshot(None).visible_text()
        );
        assert!(
            live_emulator
                .snapshot(None)
                .visible_text()
                .contains("line three")
        );
    }

    #[test]
    fn duplicate_sequences_are_skipped() {
        let size = TerminalSize::new(80, 24);
        let mut emulator = TerminalEmulator::new(TerminalId::new(2), size, 100);
        let first = output(1, b"hello", size);
        emulator.apply(&first);
        // The live tail overlaps the replay tail by design.
        let effects = emulator.apply(&first);
        assert!(effects.is_empty());
        let second = output(2, b" world", size);
        emulator.apply(&second);
        assert_eq!(
            emulator
                .snapshot(None)
                .visible_text()
                .lines()
                .next()
                .unwrap_or("")
                .trim_end(),
            "hello world"
        );
    }

    #[test]
    fn scrollback_pins_the_viewport_while_scrolled_up() {
        let size = TerminalSize::new(10, 3);
        let mut emulator = TerminalEmulator::new(TerminalId::new(3), size, 100);
        for i in 0..30u32 {
            emulator.apply(&output(
                i as TerminalSeq + 1,
                &format!("row {i}\r\n").into_bytes(),
                size,
            ));
        }
        assert!(emulator.history_len() > 0);
        emulator.scroll_by(2);
        let position_before = emulator.viewport_position();
        assert!(position_before > 0);
        // More output while scrolled up: the viewport stays pinned.
        for i in 30..60u32 {
            emulator.apply(&output(
                i as TerminalSeq + 1,
                &format!("row {i}\r\n").into_bytes(),
                size,
            ));
        }
        assert!(emulator.viewport_position() >= position_before);
        emulator.scroll_to_bottom();
        assert!(emulator.at_bottom());
        assert_eq!(emulator.viewport_position(), 0);
    }

    #[test]
    fn resize_history_is_honored_in_order() {
        let size = TerminalSize::new(10, 3);
        let mut emulator = TerminalEmulator::new(TerminalId::new(4), size, 100);
        emulator.apply(&TerminalStreamEvent::Resize {
            seq: 1,
            size: TerminalSize::new(120, 40),
        });
        assert_eq!(emulator.size(), TerminalSize::new(120, 40));
        emulator.apply(&output(2, b"after resize", TerminalSize::new(120, 40)));
        assert!(
            emulator
                .snapshot(None)
                .visible_text()
                .contains("after resize")
        );
    }

    #[test]
    fn exit_event_records_the_code() {
        let size = TerminalSize::new(10, 3);
        let mut emulator = TerminalEmulator::new(TerminalId::new(5), size, 100);
        let effects = emulator.apply(&TerminalStreamEvent::Exit {
            seq: 1,
            code: Some(7),
        });
        assert_eq!(effects, vec![EmulatorEffect::Exited { code: Some(7) }]);
        assert_eq!(emulator.exited(), Some(Some(7)));
    }

    #[test]
    fn replay_suppresses_terminal_query_writes_and_live_restores_them() {
        let size = TerminalSize::new(80, 24);
        let mut emulator = TerminalEmulator::with_theme(
            TerminalId::new(6),
            size,
            100,
            TerminalTheme::new(0x112233, 0x445566, 0x778899),
        );
        let historical_queries = output(
            1,
            b"\x1b[6n\x1b[c\x1b]10;?\x07\x1b]11;?\x07\x1b]12;?\x07",
            size,
        );
        emulator.apply(&historical_queries);
        assert!(
            emulator.pty_writes().is_empty(),
            "historical DSR/DA/color queries must never write to the live PTY"
        );

        emulator.start_live();
        emulator.apply(&output(2, b"\x1b[6n\x1b[c\x1b]10;?\x07", size));
        assert!(
            !emulator.pty_writes().is_empty(),
            "live terminal query responses must use the shared PTY input path"
        );
    }

    #[test]
    fn live_sequence_gap_requests_a_replay_resync() {
        let size = TerminalSize::new(80, 24);
        let mut emulator = TerminalEmulator::new(TerminalId::new(7), size, 100);
        emulator.apply(&output(10, b"replay", size));
        emulator.start_live();
        let effects = emulator.apply(&output(12, b"gap", size));
        assert_eq!(
            effects,
            vec![EmulatorEffect::SequenceGap {
                expected: 11,
                actual: 12,
            }]
        );
    }
}
