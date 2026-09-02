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
/// Hard safety cap for the combined temporary scrollback of all terminals.
pub const MAX_TOTAL_SCROLLBACK_LINES: usize = 100_000;
pub const MAX_RECENT_OUTPUT_BYTES: usize = 64 * 1024;

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
        }
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
