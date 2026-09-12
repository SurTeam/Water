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

const CLEAR_SCROLLBACK_SEQUENCE: &[u8] = b"\x1b[3J";

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
    scrollback_lines: usize,
    last_seq: TerminalSeq,
    process: TerminalProcessState,
    /// Local visual state changed since the GUI last consumed it.
    dirty: bool,
    /// Viewport pinned for browsing scrollback. Alacritty's grid keeps a
    /// non-bottom display anchored when output arrives by advancing its
    /// physical `display_offset`; this flag tells us to keep a separate user
    /// coordinate so that output-driven movement is not mistaken for a user
    /// scroll.
    viewport_pinned: bool,
    /// User-driven viewport coordinate. Unlike the grid's physical
    /// `display_offset`, this value does not change when new output is
    /// appended while the viewport is pinned.
    pinned_viewport: i64,
    /// Pi's regular TUI uses CSI 3 J to clear the host scrollback on a full
    /// redraw. Keep that sequence local to the alternate-screen-like agent
    /// session so the terminal's existing history remains available after it
    /// exits. The filter is stateful because PTY chunks can split an escape
    /// sequence at any byte boundary.
    scrollback_protected: bool,
    pending_scrollback_clear: Vec<u8>,
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
        Self::with_theme_and_scrollback_protection(
            terminal_id,
            size,
            scrollback_lines,
            theme,
            false,
        )
    }

    pub fn with_theme_and_scrollback_protection(
        terminal_id: TerminalId,
        size: TerminalSize,
        scrollback_lines: usize,
        theme: TerminalTheme,
        scrollback_protected: bool,
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
            scrollback_lines,
            last_seq: 0,
            process: TerminalProcessState::Running,
            dirty: false,
            viewport_pinned: false,
            pinned_viewport: 0,
            scrollback_protected,
            pending_scrollback_clear: Vec::new(),
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
    /// local term. The grid itself preserves a non-bottom display while it
    /// grows; do not restore `display_offset` here because doing so changes
    /// the rows that are actually visible.
    pub fn advance(&mut self, bytes: &[u8]) {
        if !bytes.is_empty() {
            self.dirty = true;
        }
        metrics::inc(metrics::processor_advances());
        metrics::add(metrics::terminal_bytes_advanced(), bytes.len());
        if self.viewport_pinned {
            // Preserve the rows behind a browsing viewport even after the
            // configured history limit is reached. The excess is deliberately
            // temporary: the first downward scroll trims it back to the
            // configured limit, so an idle live stream cannot permanently
            // change the terminal's scrollback size.
            self.term.grid_mut().update_history(usize::MAX);
        }
        if self.scrollback_protected {
            let filtered = self.filter_scrollback_clear(bytes);
            self.advance_unfiltered(&filtered);
        } else {
            if !self.pending_scrollback_clear.is_empty() {
                let pending = std::mem::take(&mut self.pending_scrollback_clear);
                self.advance_unfiltered(&pending);
            }
            self.advance_unfiltered(bytes);
        }
    }

    fn advance_unfiltered(&mut self, bytes: &[u8]) {
        if !bytes.is_empty() {
            self.processor.advance(&mut self.term, bytes);
        }
    }

    fn filter_scrollback_clear(&mut self, bytes: &[u8]) -> Vec<u8> {
        let mut input = Vec::with_capacity(self.pending_scrollback_clear.len() + bytes.len());
        input.append(&mut self.pending_scrollback_clear);
        input.extend_from_slice(bytes);

        let mut filtered = Vec::with_capacity(input.len());
        let mut index = 0;
        while index < input.len() {
            let remaining = &input[index..];
            if remaining.len() < CLEAR_SCROLLBACK_SEQUENCE.len()
                && CLEAR_SCROLLBACK_SEQUENCE.starts_with(remaining)
            {
                self.pending_scrollback_clear.extend_from_slice(remaining);
                break;
            }
            if remaining.starts_with(CLEAR_SCROLLBACK_SEQUENCE) {
                index += CLEAR_SCROLLBACK_SEQUENCE.len();
            } else {
                filtered.push(input[index]);
                index += 1;
            }
        }
        filtered
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

    /// Current viewport position in the user-facing coordinate: 0 = bottom,
    /// positive = rows up into history. While browsing, this is deliberately
    /// independent from the grid's physical `display_offset`, which may grow
    /// as output is appended behind the pinned viewport.
    pub fn viewport_position(&self) -> i64 {
        if self.viewport_pinned {
            self.pinned_viewport
        } else {
            self.term.grid().display_offset() as i64
        }
    }

    /// Debug: grid state for diagnosing scroll/overscan issues.
    pub fn grid_debug(&self) -> (usize, usize, usize, i32, i32) {
        let grid = self.term.grid();
        (
            grid.display_offset(),
            grid.total_lines(),
            grid.screen_lines(),
            grid.topmost_line().0,
            grid.bottommost_line().0,
        )
    }

    /// Rows of history currently retained above the viewport. The grid's
    /// This is the number of retained rows in the grid, independent of the
    /// user-facing viewport coordinate while output is growing behind a pin.
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
        if self.viewport_pinned && delta < 0 {
            self.trim_scrollback_to_configured_limit();
            self.pinned_viewport = self.term.grid().display_offset() as i64;
        }
        let previous_display_offset = self.term.grid().display_offset() as i64;
        let clamped = delta.clamp(i32::MIN as i64, i32::MAX as i64) as i32;
        self.term.scroll_display(Scroll::Delta(clamped));
        let display_offset = self.term.grid().display_offset() as i64;
        let actual_delta = display_offset.saturating_sub(previous_display_offset);
        if self.viewport_pinned {
            self.pinned_viewport = self
                .pinned_viewport
                .saturating_add(actual_delta)
                .clamp(0, self.history_len());
        } else {
            self.pinned_viewport = display_offset;
            if display_offset > 0 {
                self.viewport_pinned = true;
            }
        }
        if self.viewport_pinned && self.pinned_viewport == 0 {
            // A relative scroll can reach the semantic bottom while the
            // physical grid is still offset by output that arrived during the
            // browse. Jump to the real live bottom in that case.
            self.scroll_to_bottom();
            return;
        }
        self.dirty = true;
    }

    /// Absolute scroll target in the viewport coordinate (0 = bottom,
    /// positive = up). Clamped to the available history.
    pub fn scroll_to(&mut self, target: i64) {
        let target = target.max(0).min(self.history_len());
        if target == 0 {
            self.scroll_to_bottom();
            return;
        }
        if !self.viewport_pinned {
            self.viewport_pinned = true;
            self.pinned_viewport = self.term.grid().display_offset() as i64;
        }
        let delta = target - self.viewport_position();
        if delta != 0 {
            self.scroll_by(delta);
        }
    }

    pub fn scroll_to_bottom(&mut self) {
        self.trim_scrollback_to_configured_limit();
        self.viewport_pinned = false;
        self.pinned_viewport = 0;
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

    /// Sets the viewport pin. While pinned, output may advance the grid's
    /// physical `display_offset`, but the semantic user position remains
    /// unchanged until the user scrolls again.
    pub fn set_viewport_pinned(&mut self, pinned: bool) {
        if pinned {
            if !self.viewport_pinned {
                self.pinned_viewport = self.term.grid().display_offset() as i64;
            }
            self.viewport_pinned = true;
            self.term.grid_mut().update_history(usize::MAX);
        } else {
            self.trim_scrollback_to_configured_limit();
            self.viewport_pinned = false;
            self.pinned_viewport = 0;
        }
    }

    fn trim_scrollback_to_configured_limit(&mut self) {
        self.term.grid_mut().update_history(self.scrollback_lines);
    }

    pub fn set_scrollback_protected(&mut self, protected: bool) {
        if self.scrollback_protected == protected {
            return;
        }
        self.scrollback_protected = protected;
        if !protected && !self.pending_scrollback_clear.is_empty() {
            let pending = std::mem::take(&mut self.pending_scrollback_clear);
            self.advance_unfiltered(&pending);
        }
        self.dirty = true;
    }
}

/// Rebuilds a renderable screen in the caller from a server-provided raw
/// replay. This is used by `water ctl` and other inspection clients; it never
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
        let visible_before = emulator.snapshot(None).visible_text();
        // Explicitly pin the viewport for browsing.
        emulator.set_viewport_pinned(true);
        // More output while scrolled up: the viewport stays pinned.
        for i in 30..60u32 {
            emulator.apply(&output(
                i as TerminalSeq + 1,
                &format!("row {i}\r\n").into_bytes(),
                size,
            ));
        }
        assert_eq!(
            emulator.viewport_position(),
            position_before,
            "pinned viewport must not move when new output arrives"
        );
        assert_eq!(
            emulator.snapshot(None).visible_text(),
            visible_before,
            "output behind a pinned viewport must not move the visible rows"
        );
        emulator.scroll_to_bottom();
        assert!(emulator.at_bottom());
        assert_eq!(emulator.viewport_position(), 0);
    }

    #[test]
    fn pinned_viewport_scrolling_down_follows_content() {
        let size = TerminalSize::new(10, 3);
        let mut emulator = TerminalEmulator::new(TerminalId::new(5), size, 100);
        for i in 0..30u32 {
            emulator.apply(&output(
                i as TerminalSeq + 1,
                &format!("row {i}\r\n").into_bytes(),
                size,
            ));
        }
        emulator.scroll_by(5);
        emulator.set_viewport_pinned(true);
        let pos = emulator.viewport_position();
        // Scroll down 2 rows (toward live): viewport should move down and
        // the pin position should update to the new offset.
        emulator.scroll_by(-2);
        assert_eq!(emulator.viewport_position(), pos - 2);
        // New output while still pinned at the new position: viewport stays.
        emulator.apply(&output(
            31,
            &b"new row 1\r\nnew row 2\r\n".to_vec()[..],
            size,
        ));
        assert_eq!(
            emulator.viewport_position(),
            pos - 2,
            "pinned viewport must stay after user scrolled down"
        );
        emulator.scroll_to_bottom();
        assert!(emulator.at_bottom());
    }

    #[test]
    fn pinned_viewport_scroll_to_unpins() {
        let size = TerminalSize::new(10, 3);
        let mut emulator = TerminalEmulator::new(TerminalId::new(6), size, 100);
        for i in 0..30u32 {
            emulator.apply(&output(
                i as TerminalSeq + 1,
                &format!("row {i}\r\n").into_bytes(),
                size,
            ));
        }
        emulator.scroll_by(3);
        emulator.set_viewport_pinned(true);
        let pos = emulator.viewport_position();
        // Scroll to a non-zero position: stays pinned.
        emulator.scroll_to(pos);
        // New output: viewport stays.
        emulator.apply(&output(31, b"a\r\nb\r\nc\r\n".to_vec().as_slice(), size));
        assert_eq!(emulator.viewport_position(), pos);
        // Scroll to bottom: unpins.
        emulator.scroll_to(0);
        assert!(emulator.at_bottom());
    }

    #[test]
    fn pinned_output_can_overflow_temporarily_then_restores_scrollback_limit() {
        let size = TerminalSize::new(12, 3);
        let configured_scrollback = 5;
        let mut emulator = TerminalEmulator::new(TerminalId::new(10), size, configured_scrollback);
        for i in 0..10u32 {
            emulator.apply(&output(
                i as TerminalSeq + 1,
                &format!("old {i}\r\n").into_bytes(),
                size,
            ));
        }
        emulator.scroll_by(2);
        let visible_before = emulator.snapshot(None).visible_text();
        emulator.set_viewport_pinned(true);
        for i in 10..30u32 {
            emulator.apply(&output(
                i as TerminalSeq + 1,
                &format!("new {i}\r\n").into_bytes(),
                size,
            ));
        }
        assert_eq!(emulator.snapshot(None).visible_text(), visible_before);
        assert!(emulator.history_len() > configured_scrollback as i64);

        // Moving toward the live tail is the point at which temporary rows
        // become disposable. The grid must return to the configured bound.
        emulator.scroll_by(-1);
        assert_eq!(emulator.history_len(), configured_scrollback as i64);
    }

    #[test]
    fn protected_scrollback_ignores_split_clear_sequence() {
        let size = TerminalSize::new(12, 3);
        let mut emulator = TerminalEmulator::with_theme_and_scrollback_protection(
            TerminalId::new(8),
            size,
            100,
            TerminalTheme::default(),
            true,
        );
        for i in 0..12u32 {
            emulator.apply(&output(
                i as TerminalSeq + 1,
                &format!("history {i}\r\n").into_bytes(),
                size,
            ));
        }
        emulator.scroll_by(2);
        let visible_before = emulator.snapshot(None).visible_text();
        emulator.advance(b"\x1b[3");
        emulator.advance(b"J");
        let snapshot = emulator.snapshot(None);
        assert_eq!(snapshot.visible_text(), visible_before);
        assert!(
            snapshot
                .rows_before
                .iter()
                .any(|row| row.iter().any(|cell| cell.character != ' ')),
            "protected CSI 3 J must not erase retained history"
        );
    }

    #[test]
    fn alternate_screen_restores_primary_scrollback() {
        let size = TerminalSize::new(12, 3);
        let mut emulator = TerminalEmulator::new(TerminalId::new(9), size, 100);
        emulator.apply(&output(1, b"primary one\r\nprimary two\r\n", size));
        emulator.apply(&output(2, b"\x1b[?1049h\x1b[2J\x1b[Hagent screen", size));
        assert!(emulator.alternate_screen());
        emulator.apply(&output(3, b"\x1b[?1049l", size));
        let snapshot = emulator.snapshot(None);
        assert!(!emulator.alternate_screen());
        assert!(snapshot.visible_text().contains("primary two"));
        assert!(!snapshot.visible_text().contains("agent screen"));
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
