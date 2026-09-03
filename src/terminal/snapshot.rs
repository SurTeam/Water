use alacritty_terminal::event::EventListener;
use alacritty_terminal::grid::Dimensions;
use alacritty_terminal::index::{Column, Line};
use alacritty_terminal::term::{
    Term, TermMode,
    cell::{Cell, Flags},
};
use alacritty_terminal::vte::ansi::{Color, Rgb};
use serde::{Deserialize, Serialize};

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

#[derive(Debug, Clone, Copy, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct TerminalCellFlags {
    pub inverse: bool,
    pub bold: bool,
    pub italic: bool,
    pub underline: bool,
    pub strike: bool,
    pub wide: bool,
    pub wide_spacer: bool,
    pub leading_wide_spacer: bool,
    pub wrapline: bool,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct TerminalCell {
    pub character: char,
    pub fg: TerminalColor,
    pub bg: TerminalColor,
    pub flags: TerminalCellFlags,
    pub zerowidth: Vec<char>,
}

impl Default for TerminalCell {
    fn default() -> Self {
        Self {
            character: ' ',
            fg: TerminalColor::Named { value: 256 },
            bg: TerminalColor::Named { value: 257 },
            flags: TerminalCellFlags::default(),
            zerowidth: Vec::new(),
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
/// tabs that are not currently displayed. Presentations fetch full grids
/// for visible panes only (tmux/herdr keep history behind one surface).
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct TerminalSummary {
    pub terminal_id: TerminalId,
    pub size: TerminalSize,
    pub revision: u64,
    pub process: TerminalProcessState,
    pub display_offset: usize,
    pub viewport_position: i64,
    pub cursor: TerminalCursor,
    pub process_name: String,
    pub cwd: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct TerminalSnapshot {
    pub terminal_id: TerminalId,
    pub size: TerminalSize,
    /// Current foreground process name, refreshed by the PTY worker.
    #[serde(default)]
    pub process_name: String,
    /// Current foreground process working directory, refreshed by the PTY
    /// worker and used as the inheritance source for new shells.
    #[serde(default)]
    pub cwd: String,
    pub display_offset: usize,
    /// Cumulative viewport movement caused by commands or resize.
    ///
    /// Output can increase `display_offset` while a pinned viewport remains
    /// visually fixed. This independent coordinate lets consumers distinguish
    /// a user viewport move from output-driven history growth.
    #[serde(default)]
    pub viewport_position: i64,
    pub cursor: TerminalCursor,
    #[serde(default)]
    pub modes: TerminalModes,
    pub process: TerminalProcessState,
    pub revision: u64,
    pub cells: Vec<TerminalCell>,
    /// The nearest available grid row above the visible viewport, if any.
    #[serde(default)]
    pub rows_before: Vec<Vec<TerminalCell>>,
    /// The nearest available grid row below the visible viewport, if any.
    #[serde(default)]
    pub rows_after: Vec<Vec<TerminalCell>>,
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
            cells: vec![TerminalCell::default(); size.columns * size.lines],
            rows_before: Vec::new(),
            rows_after: Vec::new(),
        }
    }

    pub fn cell(&self, row: usize, column: usize) -> Option<&TerminalCell> {
        if row >= self.size.lines || column >= self.size.columns {
            return None;
        }
        self.cells.get(row * self.size.columns + column)
    }

    pub fn visible_text(&self) -> String {
        let mut text = String::with_capacity(self.size.columns * self.size.lines + self.size.lines);
        for row in 0..self.size.lines {
            if row > 0 {
                text.push('\n');
            }
            for column in 0..self.size.columns {
                if let Some(cell) = self.cell(row, column)
                    && !cell.flags.wide_spacer
                    && !cell.flags.leading_wide_spacer
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
            revision: self.revision,
            process: self.process,
            display_offset: self.display_offset,
            viewport_position: self.viewport_position,
            cursor: self.cursor,
            process_name: self.process_name.clone(),
            cwd: self.cwd.clone(),
        }
    }

    /// Rebuilds this snapshot keeping only its last `max_rows` viewport rows,
    /// dropping overscan context. Used when retiring a closed terminal so
    /// race-window waiters still see the visible tail without pinning the
    /// full grid in memory.
    pub fn compacted_tail(&self, max_rows: usize) -> Self {
        let columns = self.size.columns;
        let rows = self.size.lines;
        let keep = rows.min(max_rows.max(1)).max(1);
        let removed = rows.saturating_sub(keep);
        let start = removed.saturating_mul(columns).min(self.cells.len());
        let mut cells = self.cells[start..].to_vec();
        let want = keep.saturating_mul(columns);
        if cells.len() < want {
            cells.resize(want, TerminalCell::default());
        } else {
            cells.truncate(want);
        }
        Self {
            terminal_id: self.terminal_id,
            size: TerminalSize {
                columns,
                lines: keep,
            },
            process_name: self.process_name.clone(),
            cwd: self.cwd.clone(),
            // The retired history is gone, so any pinned viewport is moot.
            display_offset: 0,
            viewport_position: self.viewport_position,
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
            cells,
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
        let size = TerminalSize::new(term.columns(), term.screen_lines());
        let display_offset = term.grid().display_offset();
        let mut cells = Vec::with_capacity(size.columns * size.lines);
        for row in 0..size.lines {
            let line = Line(row as i32 - display_offset as i32);
            for column in 0..size.columns {
                let cell = &term.grid()[line][Column(column)];
                cells.push(Self::cell_from_alacritty(cell));
            }
        }

        let viewport_start = -(display_offset as i32);
        let viewport_end = viewport_start + size.lines as i32 - 1;
        let rows_before = if viewport_start > term.grid().topmost_line().0 {
            vec![Self::row_from_alacritty(
                term,
                Line(viewport_start - 1),
                size.columns,
            )]
        } else {
            Vec::new()
        };
        let rows_after = if viewport_end < term.grid().bottommost_line().0 {
            vec![Self::row_from_alacritty(
                term,
                Line(viewport_end + 1),
                size.columns,
            )]
        } else {
            Vec::new()
        };

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
            cells,
            rows_before,
            rows_after,
        }
    }

    fn row_from_alacritty<T: EventListener>(
        term: &Term<T>,
        line: Line,
        columns: usize,
    ) -> Vec<TerminalCell> {
        let mut row = Vec::with_capacity(columns);
        for column in 0..columns {
            row.push(Self::cell_from_alacritty(
                &term.grid()[line][Column(column)],
            ));
        }
        row
    }

    fn cell_from_alacritty(cell: &Cell) -> TerminalCell {
        TerminalCell {
            character: cell.c,
            fg: color_from_alacritty(cell.fg),
            bg: color_from_alacritty(cell.bg),
            flags: TerminalCellFlags {
                inverse: cell.flags.contains(Flags::INVERSE),
                bold: cell.flags.contains(Flags::BOLD),
                italic: cell.flags.contains(Flags::ITALIC),
                underline: cell.flags.intersects(Flags::ALL_UNDERLINES),
                strike: cell.flags.contains(Flags::STRIKEOUT),
                wide: cell.flags.contains(Flags::WIDE_CHAR),
                wide_spacer: cell.flags.contains(Flags::WIDE_CHAR_SPACER),
                leading_wide_spacer: cell.flags.contains(Flags::LEADING_WIDE_CHAR_SPACER),
                wrapline: cell.flags.contains(Flags::WRAPLINE),
            },
            zerowidth: cell.zerowidth().map(ToOwned::to_owned).unwrap_or_default(),
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
        assert_eq!(at_bottom.rows_before.len(), 1);
        assert!(at_bottom.rows_after.is_empty());

        term.scroll_display(Scroll::Delta(1));
        let one_row_up =
            TerminalSnapshot::from_term(terminal_id, &term, TerminalProcessState::Running, 2);
        assert_eq!(one_row_up.rows_before.len(), 1);
        assert_eq!(one_row_up.rows_after.len(), 1);
        assert_eq!(
            &one_row_up.cells[..size.columns],
            at_bottom.rows_before[0].as_slice()
        );
        assert_eq!(
            one_row_up.rows_after[0].as_slice(),
            &at_bottom.cells[size.columns..size.columns * size.lines]
        );

        term.scroll_display(Scroll::Top);
        let at_top =
            TerminalSnapshot::from_term(terminal_id, &term, TerminalProcessState::Running, 3);
        assert!(at_top.rows_before.is_empty());
        assert_eq!(at_top.rows_after.len(), 1);
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
}
