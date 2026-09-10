use alacritty_terminal::event::EventListener;
use alacritty_terminal::grid::Dimensions;
use alacritty_terminal::index::{Column, Line};
use alacritty_terminal::term::{
    Term, TermMode,
    cell::{Cell, Flags},
};
use alacritty_terminal::vte::ansi::{Color, Rgb};
use serde::{Deserialize, Serialize};
use smallvec::SmallVec;
use std::cell::Cell as ThreadCell;
use std::sync::Arc;

/// Raw row reserve on each side of the viewport. This is deliberately wider
/// than the renderer's prepared-row lookahead: fast gestures can keep moving
/// from immutable local data while shaping remains bounded to a small band.
pub const VIEWPORT_OVERSCAN_ROWS: usize = 32;

use crate::ids::TerminalId;

pub const DEFAULT_COLUMNS: usize = 80;
pub const DEFAULT_LINES: usize = 24;
pub const MAX_COLUMNS: usize = 512;
pub const MAX_LINES: usize = 256;
/// Maximum normal scrollback configured for an individual terminal.
pub const MAX_SCROLLBACK_LINES: usize = 10_000;
/// Default per-terminal scrollback retained while a terminal is focused.
/// Matches the conservative tmux `history-limit` default.
pub const DEFAULT_SCROLLBACK_LINES: usize = 2_000;
/// Default per-terminal scrollback retained once a terminal loses focus.
/// Background tabs keep only their tail so many long tabs cannot exhaust
/// process memory.
pub const DEFAULT_INACTIVE_SCROLLBACK_LINES: usize = 500;
/// Default aggregate byte budget for all terminals' scrollback grids.
pub const DEFAULT_MAX_TOTAL_SCROLLBACK_BYTES: usize = 64 * 1024 * 1024;
pub const MIN_MAX_TOTAL_SCROLLBACK_BYTES: usize = 64 * 1024;
pub const MAX_TOTAL_SCROLLBACK_BYTES: usize = 1024 * 1024 * 1024;
pub const MAX_RECENT_OUTPUT_BYTES: usize = 64 * 1024;

/// Server-side raw replay history budget per terminal (bytes). The GUI
/// scrollback grid is a separate, row-based limit; the replay ring is the
/// resync source for re-attaching clients and headless captures.
pub const DEFAULT_REPLAY_HISTORY_BYTES: usize = 8 * 1024 * 1024;
pub const MIN_REPLAY_HISTORY_BYTES: usize = 1024 * 1024;
/// Approximate on-heap cost of one cell in the worker's alacritty grid.
pub const ANSI_SCROLLBACK_CELL_BYTES: usize = std::mem::size_of::<Cell>();
const SCROLLBACK_ROW_OVERHEAD_BYTES: usize = 64;

/// Estimated heap cost of one scrollback row at the given width.
///
/// Scrollback budgets must account in bytes, not rows: a 10,000-row history
/// at 512 columns costs an order of magnitude more than at 80 columns.
pub const fn scrollback_row_bytes(columns: usize) -> usize {
    let columns = if columns < 1 { 1 } else { columns };
    columns.saturating_mul(ANSI_SCROLLBACK_CELL_BYTES) + SCROLLBACK_ROW_OVERHEAD_BYTES
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
pub struct TerminalSize {
    pub columns: usize,
    pub lines: usize,
}

impl TerminalSize {
    pub fn new(columns: usize, lines: usize) -> Self {
        Self {
            columns: columns.clamp(2, MAX_COLUMNS),
            lines: lines.clamp(1, MAX_LINES),
        }
    }
}

impl Default for TerminalSize {
    fn default() -> Self {
        Self::new(DEFAULT_COLUMNS, DEFAULT_LINES)
    }
}

impl Dimensions for TerminalSize {
    fn total_lines(&self) -> usize {
        self.lines
    }

    fn screen_lines(&self) -> usize {
        self.lines
    }

    fn columns(&self) -> usize {
        self.columns
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum TerminalProcessState {
    Running,
    Exited { code: Option<i32> },
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case", tag = "type")]
pub enum TerminalColor {
    Named { value: u16 },
    Rgb { red: u8, green: u8, blue: u8 },
    Indexed { value: u8 },
}

impl Default for TerminalColor {
    fn default() -> Self {
        Self::Named { value: 256 }
    }
}

#[derive(Debug, Clone, Copy, Default, PartialEq, Eq)]
pub struct TerminalCellFlags(u16);

impl TerminalCellFlags {
    const INVERSE: u16 = 1 << 0;
    const BOLD: u16 = 1 << 1;
    const ITALIC: u16 = 1 << 2;
    const UNDERLINE: u16 = 1 << 3;
    const STRIKE: u16 = 1 << 4;
    const WIDE: u16 = 1 << 5;
    const WIDE_SPACER: u16 = 1 << 6;
    const LEADING_WIDE_SPACER: u16 = 1 << 7;
    const WRAPLINE: u16 = 1 << 8;
    const DIM: u16 = 1 << 9;

    fn set(&mut self, flag: u16, value: bool) {
        if value {
            self.0 |= flag;
        } else {
            self.0 &= !flag;
        }
    }

    pub fn inverse(self) -> bool {
        self.0 & Self::INVERSE != 0
    }

    pub fn bold(self) -> bool {
        self.0 & Self::BOLD != 0
    }

    pub fn italic(self) -> bool {
        self.0 & Self::ITALIC != 0
    }

    pub fn underline(self) -> bool {
        self.0 & Self::UNDERLINE != 0
    }

    pub fn strike(self) -> bool {
        self.0 & Self::STRIKE != 0
    }

    pub fn wide(self) -> bool {
        self.0 & Self::WIDE != 0
    }

    pub fn wide_spacer(self) -> bool {
        self.0 & Self::WIDE_SPACER != 0
    }

    pub fn leading_wide_spacer(self) -> bool {
        self.0 & Self::LEADING_WIDE_SPACER != 0
    }

    pub fn wrapline(self) -> bool {
        self.0 & Self::WRAPLINE != 0
    }

    pub fn dim(self) -> bool {
        self.0 & Self::DIM != 0
    }

    pub fn set_wide(&mut self, value: bool) {
        self.set(Self::WIDE, value);
    }

    pub fn set_wide_spacer(&mut self, value: bool) {
        self.set(Self::WIDE_SPACER, value);
    }

    pub fn set_leading_wide_spacer(&mut self, value: bool) {
        self.set(Self::LEADING_WIDE_SPACER, value);
    }

    fn from_alacritty(flags: Flags) -> Self {
        let mut compact = Self::default();
        compact.set(Self::INVERSE, flags.contains(Flags::INVERSE));
        compact.set(Self::BOLD, flags.contains(Flags::BOLD));
        compact.set(Self::ITALIC, flags.contains(Flags::ITALIC));
        compact.set(Self::UNDERLINE, flags.intersects(Flags::ALL_UNDERLINES));
        compact.set(Self::STRIKE, flags.contains(Flags::STRIKEOUT));
        compact.set(Self::DIM, flags.contains(Flags::DIM));
        compact.set(Self::WIDE, flags.contains(Flags::WIDE_CHAR));
        compact.set(Self::WIDE_SPACER, flags.contains(Flags::WIDE_CHAR_SPACER));
        compact.set(
            Self::LEADING_WIDE_SPACER,
            flags.contains(Flags::LEADING_WIDE_CHAR_SPACER),
        );
        compact.set(Self::WRAPLINE, flags.contains(Flags::WRAPLINE));
        compact
    }
}

#[derive(Serialize, Deserialize, Default)]
#[serde(default)]
struct TerminalCellFlagsWire {
    #[serde(skip_serializing_if = "is_false")]
    inverse: bool,
    #[serde(skip_serializing_if = "is_false")]
    bold: bool,
    #[serde(skip_serializing_if = "is_false")]
    italic: bool,
    #[serde(skip_serializing_if = "is_false")]
    underline: bool,
    #[serde(skip_serializing_if = "is_false")]
    strike: bool,
    #[serde(skip_serializing_if = "is_false")]
    dim: bool,
    #[serde(skip_serializing_if = "is_false")]
    wide: bool,
    #[serde(skip_serializing_if = "is_false")]
    wide_spacer: bool,
    #[serde(skip_serializing_if = "is_false")]
    leading_wide_spacer: bool,
    #[serde(skip_serializing_if = "is_false")]
    wrapline: bool,
}

fn is_false(value: &bool) -> bool {
    !*value
}

impl Serialize for TerminalCellFlags {
    fn serialize<S>(&self, serializer: S) -> Result<S::Ok, S::Error>
    where
        S: serde::Serializer,
    {
        TerminalCellFlagsWire {
            inverse: self.inverse(),
            bold: self.bold(),
            italic: self.italic(),
            underline: self.underline(),
            strike: self.strike(),
            dim: self.dim(),
            wide: self.wide(),
            wide_spacer: self.wide_spacer(),
            leading_wide_spacer: self.leading_wide_spacer(),
            wrapline: self.wrapline(),
        }
        .serialize(serializer)
    }
}

impl<'de> Deserialize<'de> for TerminalCellFlags {
    fn deserialize<D>(deserializer: D) -> Result<Self, D::Error>
    where
        D: serde::Deserializer<'de>,
    {
        let wire = TerminalCellFlagsWire::deserialize(deserializer)?;
        let mut flags = Self::default();
        flags.set(Self::INVERSE, wire.inverse);
        flags.set(Self::BOLD, wire.bold);
        flags.set(Self::ITALIC, wire.italic);
        flags.set(Self::UNDERLINE, wire.underline);
        flags.set(Self::STRIKE, wire.strike);
        flags.set(Self::DIM, wire.dim);
        flags.set(Self::WIDE, wire.wide);
        flags.set(Self::WIDE_SPACER, wire.wide_spacer);
        flags.set(Self::LEADING_WIDE_SPACER, wire.leading_wide_spacer);
        flags.set(Self::WRAPLINE, wire.wrapline);
        Ok(flags)
    }
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize)]
pub struct TerminalCell {
    #[serde(default = "default_cell_character")]
    pub character: char,
    #[serde(default = "default_cell_foreground")]
    pub fg: TerminalColor,
    #[serde(default = "default_cell_background")]
    pub bg: TerminalColor,
    #[serde(default)]
    pub flags: TerminalCellFlags,
    #[serde(default)]
    pub zerowidth: SmallVec<[char; 2]>,
}

thread_local! {
    static COMPACT_CELL_WIRE_DEPTH: ThreadCell<u32> = const { ThreadCell::new(0) };
}

/// Runs `serialize` with omission of default terminal-cell fields enabled on
/// this thread. The control protocol negotiates this for modern GUI sessions;
/// ordinary `state.dump` and legacy clients retain the original full shape.
#[allow(dead_code)]
pub(crate) fn with_compact_terminal_cell_wire<T>(serialize: impl FnOnce() -> T) -> T {
    struct Restore<'a> {
        depth: &'a ThreadCell<u32>,
        previous: u32,
    }

    impl Drop for Restore<'_> {
        fn drop(&mut self) {
            self.depth.set(self.previous);
        }
    }

    COMPACT_CELL_WIRE_DEPTH.with(|depth| {
        let previous = depth.get();
        depth.set(previous.saturating_add(1));
        let _restore = Restore { depth, previous };
        serialize()
    })
}

impl Serialize for TerminalCell {
    fn serialize<S>(&self, serializer: S) -> Result<S::Ok, S::Error>
    where
        S: serde::Serializer,
    {
        use serde::ser::SerializeStruct as _;

        let compact = COMPACT_CELL_WIRE_DEPTH.with(|depth| depth.get() != 0);
        let character_is_default = self.character == default_cell_character();
        let foreground_is_default = self.fg == default_cell_foreground();
        let background_is_default = self.bg == default_cell_background();
        let flags_are_default = self.flags == TerminalCellFlags::default();
        let zerowidth_is_default = self.zerowidth.is_empty();
        let field_count = if compact {
            usize::from(!character_is_default)
                + usize::from(!foreground_is_default)
                + usize::from(!background_is_default)
                + usize::from(!flags_are_default)
                + usize::from(!zerowidth_is_default)
        } else {
            5
        };
        let mut wire = serializer.serialize_struct("TerminalCell", field_count)?;
        if !compact || !character_is_default {
            wire.serialize_field("character", &self.character)?;
        }
        if !compact || !foreground_is_default {
            wire.serialize_field("fg", &self.fg)?;
        }
        if !compact || !background_is_default {
            wire.serialize_field("bg", &self.bg)?;
        }
        if !compact || !flags_are_default {
            wire.serialize_field("flags", &self.flags)?;
        }
        if !compact || !zerowidth_is_default {
            wire.serialize_field("zerowidth", &self.zerowidth)?;
        }
        wire.end()
    }
}

fn default_cell_character() -> char {
    ' '
}

fn default_cell_foreground() -> TerminalColor {
    TerminalColor::Named { value: 256 }
}

fn default_cell_background() -> TerminalColor {
    TerminalColor::Named { value: 257 }
}

impl Default for TerminalCell {
    fn default() -> Self {
        Self {
            character: default_cell_character(),
            fg: default_cell_foreground(),
            bg: default_cell_background(),
            flags: TerminalCellFlags::default(),
            zerowidth: SmallVec::new(),
        }
    }
}

#[derive(Debug, Clone, Copy, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct TerminalModes {
    pub application_cursor: bool,
    pub bracketed_paste: bool,
    pub mouse_reporting: bool,
    pub mouse_motion: bool,
    pub mouse_drag: bool,
    pub sgr_mouse: bool,
    pub utf8_mouse: bool,
    pub alternate_screen: bool,
    pub alternate_scroll: bool,
}

#[derive(Debug, Clone, Copy, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct TerminalCursor {
    pub row: usize,
    pub column: usize,
    pub visible: bool,
}

/// Cell-free projection of a terminal, safe to embed in state dumps for
/// tabs that are not currently displayed. Screen state (rows, cursor,
/// viewport) lives on the GUI's local emulator; the projection only carries
/// control-plane metadata (geometry, process, lifecycle).
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct TerminalSummary {
    pub terminal_id: TerminalId,
    pub size: TerminalSize,
    pub process: TerminalProcessState,
    pub process_name: String,
    pub cwd: String,
}

pub type TerminalRowSnapshot = Arc<[TerminalCell]>;

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct TerminalSnapshot {
    pub terminal_id: TerminalId,
    pub size: TerminalSize,
    /// Current foreground process name, refreshed by the PTY worker.
    pub process_name: String,
    /// Current foreground process working directory, refreshed by the PTY
    /// worker and used as the inheritance source for new shells.
    pub cwd: String,
    pub display_offset: usize,
    /// Cumulative viewport movement caused by commands or resize.
    ///
    /// Output can increase `display_offset` while a pinned viewport remains
    /// visually fixed. This independent coordinate lets consumers distinguish
    /// a user viewport move from output-driven history growth.
    pub viewport_position: i64,
    pub cursor: TerminalCursor,
    pub modes: TerminalModes,
    pub process: TerminalProcessState,
    pub revision: u64,
    /// Rows of scrollback history retained above the viewport (the grid's
    /// `total_lines - screen_lines`). The materialized `rows_before` only
    /// covers a bounded overscan window; this field is the authoritative
    /// scrollback limit the GUI may scroll to.
    pub history_len: i64,
    /// `viewport_position` of the oldest retained history row (the maximum
    /// reachable scroll target, 0 when there is no history).
    pub history_bottom: i64,
    /// Visible viewport rows. Rows are reference-counted so a viewport-only
    /// snapshot can reuse all unchanged rows from its predecessor.
    pub rows: Vec<TerminalRowSnapshot>,
    /// Available grid rows above the visible viewport, nearest first.
    pub rows_before: Vec<TerminalRowSnapshot>,
    /// Available grid rows below the visible viewport, nearest first.
    pub rows_after: Vec<TerminalRowSnapshot>,
}

#[derive(Serialize)]
struct TerminalSnapshotRef<'a> {
    terminal_id: TerminalId,
    size: TerminalSize,
    process_name: &'a str,
    cwd: &'a str,
    display_offset: usize,
    viewport_position: i64,
    cursor: TerminalCursor,
    modes: TerminalModes,
    process: TerminalProcessState,
    revision: u64,
    cells: Vec<&'a TerminalCell>,
    rows_before: Vec<&'a [TerminalCell]>,
    rows_after: Vec<&'a [TerminalCell]>,
}

#[derive(Deserialize)]
struct TerminalSnapshotOwned {
    terminal_id: TerminalId,
    size: TerminalSize,
    #[serde(default)]
    process_name: String,
    #[serde(default)]
    cwd: String,
    display_offset: usize,
    #[serde(default)]
    viewport_position: i64,
    cursor: TerminalCursor,
    #[serde(default)]
    modes: TerminalModes,
    process: TerminalProcessState,
    revision: u64,
    cells: Vec<TerminalCell>,
    #[serde(default)]
    rows_before: Vec<Vec<TerminalCell>>,
    #[serde(default)]
    rows_after: Vec<Vec<TerminalCell>>,
}

impl Serialize for TerminalSnapshot {
    fn serialize<S>(&self, serializer: S) -> Result<S::Ok, S::Error>
    where
        S: serde::Serializer,
    {
        TerminalSnapshotRef {
            terminal_id: self.terminal_id,
            size: self.size,
            process_name: &self.process_name,
            cwd: &self.cwd,
            display_offset: self.display_offset,
            viewport_position: self.viewport_position,
            cursor: self.cursor,
            modes: self.modes,
            process: self.process,
            revision: self.revision,
            cells: self.rows.iter().flat_map(|row| row.iter()).collect(),
            rows_before: self.rows_before.iter().map(AsRef::as_ref).collect(),
            rows_after: self.rows_after.iter().map(AsRef::as_ref).collect(),
        }
        .serialize(serializer)
    }
}

impl<'de> Deserialize<'de> for TerminalSnapshot {
    fn deserialize<D>(deserializer: D) -> Result<Self, D::Error>
    where
        D: serde::Deserializer<'de>,
    {
        let wire = TerminalSnapshotOwned::deserialize(deserializer)?;
        let rows = rows_from_flat_cells(wire.cells, wire.size);
        Ok(Self {
            terminal_id: wire.terminal_id,
            size: wire.size,
            process_name: wire.process_name,
            cwd: wire.cwd,
            display_offset: wire.display_offset,
            viewport_position: wire.viewport_position,
            cursor: wire.cursor,
            modes: wire.modes,
            process: wire.process,
            revision: wire.revision,
            // Deserialized wire snapshots carry no grid; the GUI local
            // emulator is the scrollback authority, so treat them as flat.
            history_len: 0,
            history_bottom: 0,
            rows,
            rows_before: wire
                .rows_before
                .into_iter()
                .map(|row| Arc::from(row.into_boxed_slice()))
                .collect(),
            rows_after: wire
                .rows_after
                .into_iter()
                .map(|row| Arc::from(row.into_boxed_slice()))
                .collect(),
        })
    }
}

fn rows_from_flat_cells(cells: Vec<TerminalCell>, size: TerminalSize) -> Vec<TerminalRowSnapshot> {
    let mut cells = cells.into_iter();
    (0..size.lines)
        .map(|_| {
            let row: Vec<_> = (0..size.columns)
                .map(|_| cells.next().unwrap_or_default())
                .collect();
            Arc::from(row.into_boxed_slice())
        })
        .collect()
}

impl TerminalSnapshot {
    pub fn empty(terminal_id: TerminalId, size: TerminalSize) -> Self {
        Self {
            terminal_id,
            size,
            process_name: String::new(),
            cwd: String::new(),
            display_offset: 0,
            viewport_position: 0,
            cursor: TerminalCursor::default(),
            modes: TerminalModes::default(),
            process: TerminalProcessState::Running,
            revision: 0,
            history_len: 0,
            history_bottom: 0,
            rows: (0..size.lines)
                .map(|_| Arc::from(vec![TerminalCell::default(); size.columns].into_boxed_slice()))
                .collect(),
            rows_before: Vec::new(),
            rows_after: Vec::new(),
        }
    }

    pub fn cell(&self, row: usize, column: usize) -> Option<&TerminalCell> {
        if row >= self.size.lines || column >= self.size.columns {
            return None;
        }
        self.rows.get(row)?.get(column)
    }

    pub fn cell_mut(&mut self, row: usize, column: usize) -> Option<&mut TerminalCell> {
        if row >= self.size.lines || column >= self.size.columns {
            return None;
        }
        Arc::make_mut(self.rows.get_mut(row)?).get_mut(column)
    }

    pub fn cell_count(&self) -> usize {
        self.rows.iter().map(|row| row.len()).sum()
    }

    /// Returns a row relative to the visible viewport. Negative rows address
    /// `rows_before`, visible rows address `cells`, and rows at or beyond the
    /// screen height address `rows_after`.
    pub fn relative_row(&self, row: i32) -> Option<&[TerminalCell]> {
        self.relative_row_snapshot(row).map(AsRef::as_ref)
    }

    pub fn relative_row_snapshot(&self, row: i32) -> Option<&TerminalRowSnapshot> {
        if row < 0 {
            let index = usize::try_from(row.saturating_neg().saturating_sub(1)).ok()?;
            return self.rows_before.get(index);
        }

        let row = usize::try_from(row).ok()?;
        if row < self.size.lines {
            return self.rows.get(row);
        }
        self.rows_after.get(row.saturating_sub(self.size.lines))
    }

    pub fn visible_text(&self) -> String {
        let mut text = String::with_capacity(self.size.columns * self.size.lines + self.size.lines);
        for row in 0..self.size.lines {
            if row > 0 {
                text.push('\n');
            }
            for column in 0..self.size.columns {
                if let Some(cell) = self.cell(row, column)
                    && !cell.flags.wide_spacer()
                    && !cell.flags.leading_wide_spacer()
                {
                    text.push(cell.character);
                    text.extend(cell.zerowidth.iter().copied());
                }
            }
        }
        text
    }

    pub fn contains_text(&self, needle: &str) -> bool {
        self.visible_text().contains(needle)
    }

    /// Cheap, cell-free projection of this snapshot's identity and metadata.
    pub fn summary(&self) -> TerminalSummary {
        TerminalSummary {
            terminal_id: self.terminal_id,
            size: self.size,
            process: self.process,
            process_name: self.process_name.clone(),
            cwd: self.cwd.clone(),
        }
    }

    /// Rebuilds this snapshot keeping only its last `max_rows` viewport rows,
    /// dropping overscan context. Used when retiring a closed terminal so
    /// race-window waiters still see the visible tail without pinning the
    /// full grid in memory.
    pub fn compacted_tail(&self, max_rows: usize) -> Self {
        let rows = self.size.lines;
        let keep = rows.min(max_rows.max(1)).max(1);
        let removed = rows.saturating_sub(keep);
        let mut visible_rows = self.rows[removed.min(self.rows.len())..].to_vec();
        visible_rows.resize_with(keep, || {
            Arc::from(vec![TerminalCell::default(); self.size.columns].into_boxed_slice())
        });
        visible_rows.truncate(keep);
        Self {
            terminal_id: self.terminal_id,
            size: TerminalSize {
                columns: self.size.columns,
                lines: keep,
            },
            process_name: self.process_name.clone(),
            cwd: self.cwd.clone(),
            // The retired history is gone, so any pinned viewport is moot.
            display_offset: 0,
            viewport_position: self.viewport_position,
            history_len: 0,
            history_bottom: self.viewport_position,
            cursor: TerminalCursor {
                row: self
                    .cursor
                    .row
                    .saturating_sub(removed)
                    .min(keep.saturating_sub(1)),
                column: self.cursor.column,
                visible: self.cursor.visible,
            },
            modes: self.modes,
            process: self.process,
            revision: self.revision,
            rows: visible_rows,
            rows_before: Vec::new(),
            rows_after: Vec::new(),
        }
    }

    pub fn from_term<T: EventListener>(
        terminal_id: TerminalId,
        term: &Term<T>,
        process: TerminalProcessState,
        revision: u64,
    ) -> Self {
        Self::from_term_with_viewport_position(terminal_id, term, process, revision, 0)
    }

    pub fn from_term_with_viewport_position<T: EventListener>(
        terminal_id: TerminalId,
        term: &Term<T>,
        process: TerminalProcessState,
        revision: u64,
        viewport_position: i64,
    ) -> Self {
        Self::from_term_with_previous(
            terminal_id,
            term,
            process,
            revision,
            viewport_position,
            None,
        )
    }

    pub fn from_term_with_previous<T: EventListener>(
        terminal_id: TerminalId,
        term: &Term<T>,
        process: TerminalProcessState,
        revision: u64,
        viewport_position: i64,
        previous: Option<&TerminalSnapshot>,
    ) -> Self {
        Self::from_term_with_previous_counted(
            terminal_id,
            term,
            process,
            revision,
            viewport_position,
            previous,
        )
        .0
    }

    pub(crate) fn from_term_with_previous_counted<T: EventListener>(
        terminal_id: TerminalId,
        term: &Term<T>,
        process: TerminalProcessState,
        revision: u64,
        viewport_position: i64,
        previous: Option<&TerminalSnapshot>,
    ) -> (Self, usize) {
        let size = TerminalSize::new(term.columns(), term.screen_lines());
        let display_offset = term.grid().display_offset();
        // Total retained history rows above the viewport: the authoritative
        // scrollback bound (rows_before is only a bounded overscan window).
        let history_len = term
            .grid()
            .total_lines()
            .saturating_sub(size.lines) as i64;
        let history_bottom = viewport_position.saturating_sub(history_len);
        let mut materialized_cells = 0;
        let rows = (0..size.lines)
            .map(|row| {
                Self::row_from_alacritty(
                    term,
                    Line(row as i32 - display_offset as i32),
                    size.columns,
                    previous,
                    (row as i64).saturating_sub(viewport_position),
                    &mut materialized_cells,
                )
            })
            .collect();

        let topmost = term.grid().topmost_line().0;
        let bottommost = term.grid().bottommost_line().0;
        let viewport_start = -(display_offset as i32);
        let viewport_end = viewport_start + size.lines as i32 - 1;
        let mut rows_before = Vec::new();
        for distance in 1..=VIEWPORT_OVERSCAN_ROWS {
            let line = viewport_start.saturating_sub(distance as i32);
            if line < topmost {
                break;
            }
            rows_before.push(
                Self::row_from_alacritty(
                    term,
                    Line(line),
                    size.columns,
                    previous,
                    (-(distance as i64)).saturating_sub(viewport_position),
                    &mut materialized_cells,
                ),
            );
        }
        let rows_after = (1..=VIEWPORT_OVERSCAN_ROWS)
            .map_while(|distance| {
                let line = viewport_end.saturating_add(distance as i32);
                (line <= bottommost).then(|| {
                    Self::row_from_alacritty(
                        term,
                        Line(line),
                        size.columns,
                        previous,
                        (size.lines as i64)
                            .saturating_add(distance as i64 - 1)
                            .saturating_sub(viewport_position),
                        &mut materialized_cells,
                    )
                })
            })
            .collect();

        let point = term.grid().cursor.point;
        let viewport_row = point.line.0 + display_offset as i32;
        let cursor = TerminalCursor {
            row: usize::try_from(viewport_row).unwrap_or(usize::MAX),
            column: point.column.0.min(size.columns.saturating_sub(1)),
            visible: term.mode().contains(TermMode::SHOW_CURSOR)
                && viewport_row >= 0
                && (viewport_row as usize) < size.lines,
        };

        let mode = term.mode();
        let modes = TerminalModes {
            application_cursor: mode.contains(TermMode::APP_CURSOR),
            bracketed_paste: mode.contains(TermMode::BRACKETED_PASTE),
            mouse_reporting: mode.intersects(TermMode::MOUSE_MODE),
            mouse_motion: mode.contains(TermMode::MOUSE_MOTION),
            mouse_drag: mode.contains(TermMode::MOUSE_DRAG),
            sgr_mouse: mode.contains(TermMode::SGR_MOUSE),
            utf8_mouse: mode.contains(TermMode::UTF8_MOUSE),
            alternate_screen: mode.contains(TermMode::ALT_SCREEN),
            alternate_scroll: mode.contains(TermMode::ALTERNATE_SCROLL),
        };

        (
            Self {
                terminal_id,
                size,
                process_name: String::new(),
                cwd: String::new(),
                display_offset,
                viewport_position,
                cursor,
                modes,
                process,
                revision,
                history_len,
                history_bottom,
                rows,
                rows_before,
                rows_after,
            },
            materialized_cells,
        )
    }

    fn row_from_alacritty<T: EventListener>(
        term: &Term<T>,
        line: Line,
        columns: usize,
        previous: Option<&TerminalSnapshot>,
        physical_row: i64,
        materialized_cells: &mut usize,
    ) -> TerminalRowSnapshot {
        // Reuse an unchanged row from the previous snapshot when the row is
        // actually present in that snapshot's storage. The previous snapshot
        // only stores rows within its own overscan window, so a row that was
        // scrolled out of that window must be materialized fresh.
        if let Some(previous) = previous {
            let relative = physical_row.saturating_add(previous.viewport_position);
            if let Ok(relative) = i32::try_from(relative)
                && previous.relative_row(relative).is_some()
                && let Some(row) = previous.relative_row(relative)
                && row.len() == columns
                && row.iter().enumerate().all(|(column, snapshot_cell)| {
                    Self::cell_matches_alacritty(snapshot_cell, &term.grid()[line][Column(column)])
                })
            {
                return if relative < 0 {
                    previous.rows_before[relative.unsigned_abs() as usize - 1].clone()
                } else if relative < previous.size.lines as i32 {
                    previous.rows[relative as usize].clone()
                } else {
                    previous.rows_after[relative as usize - previous.size.lines].clone()
                };
            }
        }

        *materialized_cells = materialized_cells.saturating_add(columns);
        let mut row = Vec::with_capacity(columns);
        for column in 0..columns {
            row.push(Self::cell_from_alacritty(
                &term.grid()[line][Column(column)],
            ));
        }
        Arc::from(row.into_boxed_slice())
    }

    fn cell_matches_alacritty(snapshot: &TerminalCell, cell: &Cell) -> bool {
        snapshot.character == cell.c
            && snapshot.fg == color_from_alacritty(cell.fg)
            && snapshot.bg == color_from_alacritty(cell.bg)
            && snapshot.flags == TerminalCellFlags::from_alacritty(cell.flags)
            && snapshot.zerowidth.as_slice() == cell.zerowidth().unwrap_or_default()
    }

    fn cell_from_alacritty(cell: &Cell) -> TerminalCell {
        TerminalCell {
            character: cell.c,
            fg: color_from_alacritty(cell.fg),
            bg: color_from_alacritty(cell.bg),
            flags: TerminalCellFlags::from_alacritty(cell.flags),
            zerowidth: cell.zerowidth().into_iter().flatten().copied().collect(),
        }
    }
}

fn color_from_alacritty(color: Color) -> TerminalColor {
    match color {
        Color::Named(named) => TerminalColor::Named {
            value: named as u16,
        },
        Color::Spec(Rgb { r, g, b }) => TerminalColor::Rgb {
            red: r,
            green: g,
            blue: b,
        },
        Color::Indexed(value) => TerminalColor::Indexed { value },
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use alacritty_terminal::event::VoidListener;
    use alacritty_terminal::grid::Scroll;
    use alacritty_terminal::term::Config;
    use alacritty_terminal::vte::ansi::Processor;

    fn test_term(size: TerminalSize, scrollback_lines: usize) -> Term<VoidListener> {
        Term::new(
            Config {
                scrolling_history: scrollback_lines,
                ..Config::default()
            },
            &size,
            VoidListener,
        )
    }

    #[test]
    fn extracts_nearest_overscan_rows_and_respects_grid_bounds() {
        let terminal_id = TerminalId::new(1);
        let size = TerminalSize::new(8, 2);
        let mut term = test_term(size, 8);
        let mut processor = Processor::<alacritty_terminal::vte::ansi::StdSyncHandler>::new();
        processor.advance(
            &mut term,
            b"zero\r\none\r\ntwo\r\nthree\r\nfour\r\nfive\r\n",
        );

        let at_bottom =
            TerminalSnapshot::from_term(terminal_id, &term, TerminalProcessState::Running, 1);
        assert!(at_bottom.rows_before.len() > 1);
        assert!(at_bottom.rows_before.len() <= VIEWPORT_OVERSCAN_ROWS);
        assert!(at_bottom.rows_after.is_empty());
        assert_eq!(
            at_bottom.relative_row(-1),
            Some(at_bottom.rows_before[0].as_ref())
        );
        assert_eq!(at_bottom.relative_row(0), Some(at_bottom.rows[0].as_ref()));

        term.scroll_display(Scroll::Delta(1));
        let (one_row_up, materialized_cells) = TerminalSnapshot::from_term_with_previous_counted(
            terminal_id,
            &term,
            TerminalProcessState::Running,
            2,
            1,
            Some(&at_bottom),
        );
        assert_eq!(materialized_cells, 0);
        assert_eq!(
            one_row_up.rows_before.len() + 1,
            at_bottom.rows_before.len()
        );
        assert_eq!(one_row_up.rows_after.len(), 1);
        assert_eq!(
            one_row_up.rows[0].as_ref(),
            at_bottom.rows_before[0].as_ref()
        );
        assert!(Arc::ptr_eq(&one_row_up.rows[0], &at_bottom.rows_before[0]));
        assert_eq!(
            one_row_up.rows_after[0].as_ref(),
            at_bottom.rows[1].as_ref()
        );
        assert!(Arc::ptr_eq(&one_row_up.rows_after[0], &at_bottom.rows[1]));

        term.scroll_display(Scroll::Top);
        let at_top =
            TerminalSnapshot::from_term(terminal_id, &term, TerminalProcessState::Running, 3);
        assert!(at_top.rows_before.is_empty());
        assert!(at_top.rows_after.len() > 1);
        assert!(at_top.rows_after.len() <= VIEWPORT_OVERSCAN_ROWS);
        assert_eq!(
            at_top.relative_row(size.lines as i32),
            Some(at_top.rows_after[0].as_ref())
        );
    }

    #[test]
    fn empty_and_legacy_snapshots_default_to_empty_overscan() {
        let terminal_id = TerminalId::new(1);
        let size = TerminalSize::new(8, 2);
        let empty = TerminalSnapshot::empty(terminal_id, size);
        assert!(empty.rows_before.is_empty());
        assert!(empty.rows_after.is_empty());

        let mut legacy = serde_json::to_value(&empty)
            .unwrap()
            .as_object()
            .cloned()
            .unwrap();
        legacy.remove("rows_before");
        legacy.remove("rows_after");
        let decoded: TerminalSnapshot = serde_json::from_value(legacy.into()).unwrap();
        assert!(decoded.rows_before.is_empty());
        assert!(decoded.rows_after.is_empty());
    }

    #[test]
    fn dim_cell_style_survives_snapshot_projection_and_wire_roundtrip() {
        assert_eq!(
            serde_json::to_value(TerminalCellFlags::default()).unwrap(),
            serde_json::json!({})
        );
        let flags = TerminalCellFlags::from_alacritty(Flags::DIM);
        assert!(flags.dim());

        let mut snapshot = TerminalSnapshot::empty(TerminalId::new(1), TerminalSize::new(2, 1));
        snapshot.cell_mut(0, 0).unwrap().flags = flags;
        let encoded = serde_json::to_vec(&snapshot).unwrap();
        let decoded: TerminalSnapshot = serde_json::from_slice(&encoded).unwrap();
        assert!(decoded.cell(0, 0).unwrap().flags.dim());
    }

    #[test]
    fn negotiated_compact_cell_wire_omits_defaults_and_restores_serializer_mode() {
        let default_cell = TerminalCell::default();
        let full = serde_json::to_value(&default_cell).unwrap();
        assert_eq!(full.as_object().unwrap().len(), 5);

        let compact =
            with_compact_terminal_cell_wire(|| serde_json::to_value(&default_cell).unwrap());
        assert_eq!(compact, serde_json::json!({}));
        assert_eq!(
            serde_json::from_value::<TerminalCell>(compact).unwrap(),
            default_cell
        );
        assert_eq!(
            serde_json::to_value(&default_cell)
                .unwrap()
                .as_object()
                .unwrap()
                .len(),
            5
        );

        let mut styled = TerminalCell {
            character: 'A',
            ..TerminalCell::default()
        };
        styled.flags = TerminalCellFlags::from_alacritty(Flags::DIM);
        let compact = with_compact_terminal_cell_wire(|| serde_json::to_value(&styled).unwrap());
        assert_eq!(
            compact,
            serde_json::json!({ "character": "A", "flags": { "dim": true } })
        );
        assert_eq!(
            serde_json::from_value::<TerminalCell>(compact).unwrap(),
            styled
        );
    }
}
