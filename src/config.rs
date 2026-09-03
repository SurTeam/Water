use std::collections::BTreeMap;
use std::fs;
use std::io;
use std::path::{Path, PathBuf};

use serde::{Deserialize, Serialize};
use thiserror::Error;

use crate::agent::AgentKind;
use crate::terminal::{
    DEFAULT_COLUMNS, DEFAULT_INACTIVE_SCROLLBACK_LINES, DEFAULT_LINES,
    DEFAULT_MAX_TOTAL_SCROLLBACK_BYTES, DEFAULT_SCROLLBACK_LINES, MAX_COLUMNS, MAX_LINES,
    MAX_SCROLLBACK_LINES, MAX_TOTAL_SCROLLBACK_BYTES, MIN_MAX_TOTAL_SCROLLBACK_BYTES,
    default_shell_args, default_shell_program,
};

const DEFAULT_FONT_FAMILY: &str = "Sarasa Term SC Nerd Font";
const DEFAULT_FONT_SIZE: f32 = 16.0;
const DEFAULT_LINE_HEIGHT: f32 = 18.0;

pub const DEFAULT_WINDOW_WIDTH: f32 = 1100.0;
pub const DEFAULT_WINDOW_HEIGHT: f32 = 760.0;
pub const DEFAULT_WINDOW_MIN_WIDTH: f32 = 400.0;
pub const DEFAULT_WINDOW_MIN_HEIGHT: f32 = 260.0;
pub const DEFAULT_SIDEBAR_WIDTH: f32 = 170.0;
pub const DEFAULT_SIDEBAR_MIN_WIDTH: f32 = 170.0;
pub const DEFAULT_SIDEBAR_MAX_WIDTH: f32 = 420.0;
pub const DEFAULT_SIDEBAR_RESIZE_HANDLE_WIDTH: f32 = 6.0;
pub const DEFAULT_TITLEBAR_HEIGHT: f32 = 36.0;
pub const DEFAULT_TAB_HEIGHT: f32 = 28.0;
pub const DEFAULT_SIDEBAR_HEADER_HEIGHT: f32 = 24.0;
pub const DEFAULT_PANE_MARGIN: f32 = 4.0;
pub const DEFAULT_PANE_PADDING: f32 = 8.0;
pub const DEFAULT_UI_FONT_SIZE: f32 = 14.0;
pub const DEFAULT_TERMINAL_LINE_HEIGHT: f32 = DEFAULT_LINE_HEIGHT;

/// The effective configuration used by the running application.
///
/// Files are decoded into [`AppConfigOverrides`] and merged onto
/// `AppConfig::default()`. Keeping the effective values in this type means
/// the rest of the application never has to deal with a partially specified
/// configuration.
#[derive(Debug, Clone, Default, PartialEq, Serialize)]
pub struct AppConfig {
    pub startup: StartupConfig,
    pub shell: ShellConfig,
    pub features: FeatureConfig,
    pub theme: ThemeConfig,
    pub terminal: TerminalConfig,
    pub ui: UiConfig,
    pub shortcuts: ShortcutConfig,
}

impl AppConfig {
    /// Returns the primary per-user config path for the current platform.
    ///
    /// macOS keeps using Application Support as the native default. The XDG
    /// path is also supported through [`Self::standard_path`] and is selected
    /// automatically when it is the existing user config, which makes the
    /// standard `~/.config/water` layout convenient on every platform.
    pub fn default_path() -> PathBuf {
        if cfg!(target_os = "macos")
            && let Some(home) = std::env::var_os("HOME")
        {
            return PathBuf::from(home)
                .join("Library")
                .join("Application Support")
                .join("water")
                .join("config.json");
        }

        Self::standard_path()
    }

    /// Returns the conventional XDG-style Water config path.
    pub fn standard_path() -> PathBuf {
        if let Some(config_home) = std::env::var_os("XDG_CONFIG_HOME") {
            return PathBuf::from(config_home).join("water").join("config.json");
        }
        if let Some(home) = std::env::var_os("HOME") {
            return PathBuf::from(home)
                .join(".config")
                .join("water")
                .join("config.json");
        }
        PathBuf::from("water-config.json")
    }

    /// Chooses an existing user config before falling back to the native
    /// default path. This keeps old macOS installs working while allowing a
    /// user-created `~/.config/water/config.json` to be the active override.
    pub fn default_load_path() -> PathBuf {
        let native = Self::default_path();
        if native.is_file() {
            return native;
        }
        let standard = Self::standard_path();
        if standard.is_file() {
            return standard;
        }
        native
    }

    /// Loads a user config file. A missing file means "use application
    /// defaults"; the file itself is an override layer, so omitted fields keep
    /// their built-in defaults.
    pub fn load_from_path(path: impl AsRef<Path>) -> Result<Self, ConfigError> {
        let path = path.as_ref().to_path_buf();
        let contents = match fs::read_to_string(&path) {
            Ok(contents) => contents,
            Err(error) if error.kind() == io::ErrorKind::NotFound => return Ok(Self::default()),
            Err(source) => return Err(ConfigError::Read { path, source }),
        };
        let document: ConfigDocument =
            serde_json::from_str(&contents).map_err(|source| ConfigError::Parse {
                path: path.clone(),
                source,
            })?;
        Ok(document.into_overrides().into_config())
    }

    /// Loads the default per-user config path, honoring `WATER_CONFIG` when
    /// present. The selected path is returned for diagnostics and saving.
    pub fn load_default() -> Result<(Self, PathBuf), ConfigError> {
        let path = std::env::var_os("WATER_CONFIG")
            .map(PathBuf::from)
            .unwrap_or_else(Self::default_load_path);
        Ok((Self::load_from_path(&path)?, path))
    }

    /// Writes this effective configuration to the selected per-user config
    /// path and returns that path. The serialized document is complete, so it
    /// is easy to inspect and edit by hand; loading still treats it as an
    /// override layer.
    pub fn save_default(&self) -> Result<PathBuf, ConfigError> {
        let path = std::env::var_os("WATER_CONFIG")
            .map(PathBuf::from)
            .unwrap_or_else(Self::default_load_path);
        self.save_to_path(&path)?;
        Ok(path)
    }

    /// Writes a complete, pretty-printed config file.
    pub fn save_to_path(&self, path: impl AsRef<Path>) -> Result<(), ConfigError> {
        let path = path.as_ref().to_path_buf();
        if let Some(parent) = path.parent()
            && !parent.as_os_str().is_empty()
        {
            fs::create_dir_all(parent).map_err(|source| ConfigError::CreateDirectory {
                path: parent.to_path_buf(),
                source,
            })?;
        }
        let contents = serde_json::to_string_pretty(&self.clone().normalized())
            .map_err(ConfigError::Serialize)?;
        fs::write(&path, format!("{contents}\n"))
            .map_err(|source| ConfigError::Write { path, source })
    }

    /// Returns a path suitable for launching a terminal, expanding a leading
    /// `~` without requiring the config file to contain a machine-specific
    /// absolute path.
    pub fn default_cwd_path(&self) -> Option<PathBuf> {
        let value = self.startup.default_cwd.as_deref()?.trim();
        if value.is_empty() {
            return None;
        }
        if value == "~" {
            return std::env::var_os("HOME").map(PathBuf::from);
        }
        if let Some(relative) = value.strip_prefix("~/") {
            return std::env::var_os("HOME").map(|home| PathBuf::from(home).join(relative));
        }
        Some(PathBuf::from(value))
    }

    /// Whether changing from this effective config requires a process restart
    /// before the model and terminal workers can use the new values.
    pub fn restart_required_for(&self, next: &Self) -> bool {
        self.startup.default_cwd != next.startup.default_cwd
            || self.startup.control_socket != next.startup.control_socket
            || self.startup.initial_workspace != next.startup.initial_workspace
            || self.startup.initial_terminal != next.startup.initial_terminal
            || self.shell != next.shell
            || self.terminal.scrollback_lines != next.terminal.scrollback_lines
            || self.terminal.inactive_scrollback_lines != next.terminal.inactive_scrollback_lines
            || self.terminal.max_total_scrollback_bytes != next.terminal.max_total_scrollback_bytes
            || self.terminal.default_columns != next.terminal.default_columns
            || self.terminal.default_lines != next.terminal.default_lines
    }

    pub fn normalized(mut self) -> Self {
        self.startup = self.startup.normalized();
        self.shell = self.shell.normalized();
        self.terminal = self.terminal.normalized();
        self.ui = self.ui.normalized();
        self
    }
}

impl<'de> Deserialize<'de> for AppConfig {
    fn deserialize<D>(deserializer: D) -> Result<Self, D::Error>
    where
        D: serde::Deserializer<'de>,
    {
        Ok(ConfigDocument::deserialize(deserializer)?
            .into_overrides()
            .into_config())
    }
}

#[derive(Debug, Error)]
pub enum ConfigError {
    #[error("could not read config {path}: {source}")]
    Read { path: PathBuf, source: io::Error },
    #[error("could not parse config {path}: {source}")]
    Parse {
        path: PathBuf,
        source: serde_json::Error,
    },
    #[error("could not create config directory {path}: {source}")]
    CreateDirectory { path: PathBuf, source: io::Error },
    #[error("could not write config {path}: {source}")]
    Write { path: PathBuf, source: io::Error },
    #[error("could not serialize config: {0}")]
    Serialize(#[source] serde_json::Error),
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct StartupConfig {
    /// Initial directory for terminals when there is no active terminal to
    /// inherit from. `null` uses Water's process working directory.
    pub default_cwd: Option<String>,
    /// Optional Unix control socket path. CLI and WATER_CONTROL_SOCKET take
    /// precedence when they are present.
    pub control_socket: Option<String>,
    pub initial_workspace: bool,
    pub initial_terminal: bool,
    pub window_width: f32,
    pub window_height: f32,
    /// Smallest logical-pixel width a Water window can be dragged down to.
    pub window_min_width: f32,
    /// Smallest logical-pixel height a Water window can be dragged down to.
    pub window_min_height: f32,
}

impl Default for StartupConfig {
    fn default() -> Self {
        Self {
            default_cwd: Some("~".to_owned()),
            control_socket: None,
            initial_workspace: true,
            initial_terminal: true,
            window_width: DEFAULT_WINDOW_WIDTH,
            window_height: DEFAULT_WINDOW_HEIGHT,
            window_min_width: DEFAULT_WINDOW_MIN_WIDTH,
            window_min_height: DEFAULT_WINDOW_MIN_HEIGHT,
        }
    }
}

impl StartupConfig {
    fn normalized(mut self) -> Self {
        self.default_cwd = self
            .default_cwd
            .take()
            .map(|cwd| cwd.trim().to_owned())
            .filter(|cwd| !cwd.is_empty());
        self.control_socket = self
            .control_socket
            .take()
            .map(|path| path.trim().to_owned())
            .filter(|path| !path.is_empty());
        self.window_min_width = self.window_min_width.clamp(200.0, 4096.0);
        self.window_min_height = self.window_min_height.clamp(120.0, 4096.0);
        self.window_width = self
            .window_width
            .clamp(480.0, 4096.0)
            .max(self.window_min_width);
        self.window_height = self
            .window_height
            .clamp(320.0, 4096.0)
            .max(self.window_min_height);
        self
    }
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct ShellConfig {
    pub program: String,
    /// Arguments passed to every default terminal. This is an ordered list,
    /// not a shell command string.
    pub args: Vec<String>,
}

impl Default for ShellConfig {
    fn default() -> Self {
        let program = default_shell_program();
        let args = default_shell_args(&program);
        Self { program, args }
    }
}

impl ShellConfig {
    fn normalized(mut self) -> Self {
        self.program = self.program.trim().to_owned();
        if self.program.is_empty() {
            self.program = default_shell_program();
        }
        self
    }
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct FeatureConfig {
    /// Allow the terminal surface to own mouse input instead of starting a
    /// local text selection when the terminal requests mouse reporting.
    pub mouse_reporting: bool,
    /// Wrap Cmd-V input in bracketed-paste markers when the terminal requests
    /// bracketed paste mode.
    pub bracketed_paste: bool,
    /// Enable drag selection and Cmd-C clipboard copying.
    pub selection: bool,
}

impl Default for FeatureConfig {
    fn default() -> Self {
        Self {
            mouse_reporting: true,
            bracketed_paste: true,
            selection: true,
        }
    }
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct TerminalConfig {
    /// Initial terminal dimensions before GPUI reports the actual pane size.
    pub default_columns: usize,
    pub default_lines: usize,
    /// Maximum number of scrollback rows retained by the focused terminal.
    pub scrollback_lines: usize,
    /// Maximum number of scrollback rows retained by a terminal once it has
    /// lost focus. Background tabs keep only this tail until refocused.
    pub inactive_scrollback_lines: usize,
    /// Aggregate byte budget shared by the scrollback grids of all
    /// terminals. Bytes (not rows) because a row's cost scales with width.
    pub max_total_scrollback_bytes: usize,
    /// Font family used by terminal rows.
    pub font_family: String,
    /// Terminal font size in logical pixels.
    pub font_size: f32,
    /// Terminal row height in logical pixels.
    pub line_height: f32,
}

impl Default for TerminalConfig {
    fn default() -> Self {
        Self {
            default_columns: DEFAULT_COLUMNS,
            default_lines: DEFAULT_LINES,
            scrollback_lines: DEFAULT_SCROLLBACK_LINES,
            inactive_scrollback_lines: DEFAULT_INACTIVE_SCROLLBACK_LINES,
            max_total_scrollback_bytes: DEFAULT_MAX_TOTAL_SCROLLBACK_BYTES,
            font_family: DEFAULT_FONT_FAMILY.to_owned(),
            font_size: DEFAULT_FONT_SIZE,
            line_height: DEFAULT_LINE_HEIGHT,
        }
    }
}

impl TerminalConfig {
    fn normalized(mut self) -> Self {
        self.default_columns = self.default_columns.clamp(2, MAX_COLUMNS);
        self.default_lines = self.default_lines.clamp(1, MAX_LINES);
        self.scrollback_lines = self.scrollback_lines.clamp(1, MAX_SCROLLBACK_LINES);
        self.inactive_scrollback_lines = self
            .inactive_scrollback_lines
            .clamp(1, MAX_SCROLLBACK_LINES)
            .min(self.scrollback_lines);
        self.max_total_scrollback_bytes = self
            .max_total_scrollback_bytes
            .clamp(MIN_MAX_TOTAL_SCROLLBACK_BYTES, MAX_TOTAL_SCROLLBACK_BYTES);
        if self.font_family.trim().is_empty() {
            self.font_family = DEFAULT_FONT_FAMILY.to_owned();
        } else {
            self.font_family = self.font_family.trim().to_owned();
        }
        self.font_size = self.font_size.clamp(8.0, 32.0);
        self.line_height = self.line_height.clamp(8.0, 64.0);
        self
    }
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct UiConfig {
    /// Base font size for Water chrome, workspace labels, and settings text.
    pub font_size: f32,
    pub sidebar_visible: bool,
    /// Whether workspace rows show a count of currently running agents.
    pub sidebar_show_agent_count: bool,
    /// Whether a vertical mouse wheel over the tab strip scrolls it
    /// horizontally. Trackpad horizontal deltas always scroll the strip.
    pub tab_bar_vertical_wheel_scroll: bool,
    pub sidebar_width: f32,
    pub sidebar_min_width: f32,
    pub sidebar_max_width: f32,
    pub sidebar_resize_handle_width: f32,
    pub titlebar_height: f32,
    pub tab_height: f32,
    /// Height of a workspace group row in the sidebar. These rows are the
    /// sidebar's section headers since agents nest under their workspace.
    pub sidebar_header_height: f32,
    pub pane_margin: f32,
    pub pane_padding: f32,
}

impl Default for UiConfig {
    fn default() -> Self {
        Self {
            font_size: DEFAULT_UI_FONT_SIZE,
            sidebar_visible: true,
            sidebar_show_agent_count: true,
            tab_bar_vertical_wheel_scroll: false,
            sidebar_width: DEFAULT_SIDEBAR_WIDTH,
            sidebar_min_width: DEFAULT_SIDEBAR_MIN_WIDTH,
            sidebar_max_width: DEFAULT_SIDEBAR_MAX_WIDTH,
            sidebar_resize_handle_width: DEFAULT_SIDEBAR_RESIZE_HANDLE_WIDTH,
            titlebar_height: DEFAULT_TITLEBAR_HEIGHT,
            tab_height: DEFAULT_TAB_HEIGHT,
            sidebar_header_height: DEFAULT_SIDEBAR_HEADER_HEIGHT,
            pane_margin: DEFAULT_PANE_MARGIN,
            pane_padding: DEFAULT_PANE_PADDING,
        }
    }
}

impl UiConfig {
    fn normalized(mut self) -> Self {
        self.font_size = self.font_size.clamp(8.0, 32.0);
        self.sidebar_min_width = self.sidebar_min_width.clamp(100.0, 800.0);
        self.sidebar_max_width = self.sidebar_max_width.clamp(self.sidebar_min_width, 1200.0);
        self.sidebar_width = self
            .sidebar_width
            .clamp(self.sidebar_min_width, self.sidebar_max_width);
        self.sidebar_resize_handle_width = self.sidebar_resize_handle_width.clamp(1.0, 40.0);
        self.titlebar_height = self.titlebar_height.clamp(24.0, 96.0);
        self.tab_height = self.tab_height.clamp(20.0, 80.0);
        self.sidebar_header_height = self.sidebar_header_height.clamp(20.0, 96.0);
        self.pane_margin = self.pane_margin.clamp(0.0, 32.0);
        self.pane_padding = self.pane_padding.clamp(0.0, 48.0);
        self
    }
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct ShortcutConfig {
    pub open_settings: String,
    pub new_window: String,
    pub hide_window: String,
    pub minimize_window: String,
    pub ignore_quit: String,
    pub new_terminal_tab: String,
    pub new_workspace: String,
    pub toggle_sidebar: String,
    /// Template for tab-index bindings; # is replaced by 1..9 and 0.
    pub switch_tab: String,
    pub next_tab: String,
    pub previous_tab: String,
    pub next_workspace: String,
    pub previous_workspace: String,
    pub rename_workspace: String,
    pub rename_tab: String,
    pub split_right: String,
    pub split_down: String,
    pub close_pane: String,
    pub focus_left: String,
    pub focus_right: String,
    pub focus_up: String,
    pub focus_down: String,
    pub paste: String,
    pub copy_or_interrupt: String,
    pub eof: String,
    pub scroll_page_up: String,
    pub scroll_page_down: String,
}

impl Default for ShortcutConfig {
    fn default() -> Self {
        Self {
            open_settings: "cmd-,".to_owned(),
            new_window: "cmd-n".to_owned(),
            hide_window: "cmd-w".to_owned(),
            minimize_window: "cmd-m".to_owned(),
            ignore_quit: "cmd-q".to_owned(),
            new_terminal_tab: "cmd-t".to_owned(),
            new_workspace: "cmd-shift-n".to_owned(),
            toggle_sidebar: "cmd-e".to_owned(),
            switch_tab: "cmd-#".to_owned(),
            next_tab: "cmd-]".to_owned(),
            previous_tab: "cmd-[".to_owned(),
            next_workspace: "ctrl-tab".to_owned(),
            previous_workspace: "ctrl-shift-tab".to_owned(),
            rename_workspace: "cmd-shift-e".to_owned(),
            rename_tab: "cmd-shift-t".to_owned(),
            split_right: "cmd-\\".to_owned(),
            split_down: "cmd--".to_owned(),
            close_pane: "cmd-shift-w".to_owned(),
            focus_left: "cmd-h".to_owned(),
            focus_right: "cmd-l".to_owned(),
            focus_up: "cmd-k".to_owned(),
            focus_down: "cmd-j".to_owned(),
            paste: "cmd-v".to_owned(),
            copy_or_interrupt: "cmd-c".to_owned(),
            eof: "cmd-d".to_owned(),
            scroll_page_up: "shift-pageup".to_owned(),
            scroll_page_down: "shift-pagedown".to_owned(),
        }
    }
}

/// Returns the concrete key binding for a tab index in the configurable
/// `switch_tab` template. Indexes 0..=8 map to 1..=9 and index 9 maps to 0.
/// Invalid or missing templates use the built-in `cmd-#` template. Indexes
/// outside the first ten tabs are returned unchanged for callers that need to
/// report or handle them explicitly.
pub fn switch_tab_binding(switch_tab_source: &str, tab_index: usize) -> String {
    if tab_index >= 10 {
        return switch_tab_source.to_owned();
    }
    let source = if is_valid_switch_tab_source(switch_tab_source) {
        switch_tab_source.trim()
    } else {
        "cmd-#"
    };
    let digit = if tab_index == 9 {
        '0'
    } else {
        char::from(b'1' + tab_index as u8)
    };
    source.replace('#', &digit.to_string())
}

/// Validates the `ShortcutConfig::switch_tab` template. It must contain one
/// `#` placeholder and become a valid GPUI keystroke when that placeholder is
/// replaced with a digit.
pub fn is_valid_switch_tab_source(source: &str) -> bool {
    let source = source.trim();
    source.matches('#').count() == 1
        && source.split_whitespace().count() == 1
        && gpui::Keystroke::parse(&source.replace('#', "1")).is_ok()
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct ThemeConfig {
    pub terminal_background: String,
    pub terminal_foreground: String,
    pub selection_background: String,
    pub cursor_foreground: String,
    pub cursor_background: String,
    pub inactive_cursor: String,
    pub inverse_foreground: String,
    pub inverse_background: String,
    pub pane_background: String,
    /// Accent color used for active pane borders and active tabs.
    pub active_pane_border: String,
    pub inactive_pane_border: String,
    pub chrome_background: String,
    pub tab_active_background: String,
    pub tab_inactive_background: String,
    pub tab_add_background: String,
    pub ui_foreground: String,
    /// Background of the sidebar behind workspace and agent rows.
    pub sidebar_background: String,
    /// Background of an unselected workspace row in the sidebar.
    pub sidebar_workspace_background: String,
    /// Background of an unselected agent row in the sidebar.
    pub sidebar_agent_background: String,
    /// Background of the active workspace row in the sidebar.
    pub sidebar_workspace_active_background: String,
    /// Background of the focused agent row in the sidebar.
    pub sidebar_agent_active_background: String,
    /// Color of the insertion line shown while dragging sidebar items.
    pub sidebar_drag_indicator_color: String,
    /// Per-agent accent colors keyed by [`AgentKind::config_key`].
    pub agent_colors: BTreeMap<String, String>,
}

/// Water owns these built-in defaults; they mirror the dark Kitty palette
/// without reading Kitty configuration at runtime.
impl Default for ThemeConfig {
    fn default() -> Self {
        Self {
            terminal_background: "#2c2c2c".to_owned(),
            terminal_foreground: "#e4e4e4".to_owned(),
            selection_background: "#555555".to_owned(),
            cursor_foreground: "#2c2c2c".to_owned(),
            cursor_background: "#e4e4e4".to_owned(),
            inactive_cursor: "#555555".to_owned(),
            inverse_foreground: "#2c2c2c".to_owned(),
            inverse_background: "#e4e4e4".to_owned(),
            pane_background: "#2c2c2c".to_owned(),
            active_pane_border: "#339966".to_owned(),
            inactive_pane_border: "#555555".to_owned(),
            chrome_background: "#000000".to_owned(),
            tab_active_background: "#339966".to_owned(),
            tab_inactive_background: "#000000".to_owned(),
            tab_add_background: "#555555".to_owned(),
            ui_foreground: "#e4e4e4".to_owned(),
            sidebar_background: "#000000".to_owned(),
            sidebar_workspace_background: "#000000".to_owned(),
            sidebar_agent_background: "#000000".to_owned(),
            sidebar_workspace_active_background: "#339966".to_owned(),
            sidebar_agent_active_background: "#339966".to_owned(),
            sidebar_drag_indicator_color: "#339966".to_owned(),
            agent_colors: default_agent_colors(),
        }
    }
}

fn default_agent_colors() -> BTreeMap<String, String> {
    AgentKind::all()
        .into_iter()
        .map(|kind| {
            (
                kind.config_key().to_owned(),
                format!("#{:06x}", kind.default_color()),
            )
        })
        .collect()
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct ThemeColors {
    pub terminal_background: u32,
    pub terminal_foreground: u32,
    pub selection_background: u32,
    pub cursor_foreground: u32,
    pub cursor_background: u32,
    pub inactive_cursor: u32,
    pub inverse_foreground: u32,
    pub inverse_background: u32,
    pub pane_background: u32,
    pub active_pane_border: u32,
    pub inactive_pane_border: u32,
    pub chrome_background: u32,
    pub tab_active_background: u32,
    pub tab_inactive_background: u32,
    pub tab_add_background: u32,
    pub ui_foreground: u32,
    pub sidebar_background: u32,
    pub sidebar_workspace_background: u32,
    pub sidebar_agent_background: u32,
    pub sidebar_workspace_active_background: u32,
    pub sidebar_agent_active_background: u32,
    pub sidebar_drag_indicator: u32,
    pub agent_colors: [u32; 13],
}

impl ThemeConfig {
    pub fn colors(&self) -> ThemeColors {
        let mut agent_colors = [0; 13];
        for (index, kind) in AgentKind::all().into_iter().enumerate() {
            agent_colors[index] = self
                .agent_colors
                .get(kind.config_key())
                .map(|value| parse_color(value, kind.default_color()))
                .unwrap_or_else(|| kind.default_color());
        }
        ThemeColors {
            terminal_background: parse_color(&self.terminal_background, 0x2c2c2c),
            terminal_foreground: parse_color(&self.terminal_foreground, 0xe4e4e4),
            selection_background: parse_color(&self.selection_background, 0x555555),
            cursor_foreground: parse_color(&self.cursor_foreground, 0x2c2c2c),
            cursor_background: parse_color(&self.cursor_background, 0xe4e4e4),
            inactive_cursor: parse_color(&self.inactive_cursor, 0x555555),
            inverse_foreground: parse_color(&self.inverse_foreground, 0x2c2c2c),
            inverse_background: parse_color(&self.inverse_background, 0xe4e4e4),
            pane_background: parse_color(&self.pane_background, 0x2c2c2c),
            active_pane_border: parse_color(&self.active_pane_border, 0x339966),
            inactive_pane_border: parse_color(&self.inactive_pane_border, 0x555555),
            chrome_background: parse_color(&self.chrome_background, 0x000000),
            tab_active_background: parse_color(&self.tab_active_background, 0x339966),
            tab_inactive_background: parse_color(&self.tab_inactive_background, 0x000000),
            tab_add_background: parse_color(&self.tab_add_background, 0x555555),
            ui_foreground: parse_color(&self.ui_foreground, 0xe4e4e4),
            sidebar_background: parse_color(&self.sidebar_background, 0x000000),
            sidebar_workspace_background: parse_color(&self.sidebar_workspace_background, 0x000000),
            sidebar_agent_background: parse_color(&self.sidebar_agent_background, 0x000000),
            sidebar_workspace_active_background: parse_color(
                &self.sidebar_workspace_active_background,
                0x339966,
            ),
            sidebar_agent_active_background: parse_color(
                &self.sidebar_agent_active_background,
                0x339966,
            ),
            sidebar_drag_indicator: parse_color(&self.sidebar_drag_indicator_color, 0x339966),
            agent_colors,
        }
    }

    pub fn is_valid_color(value: &str) -> bool {
        parse_color_option(value).is_some()
    }
}

impl ThemeColors {
    pub fn agent_color(&self, kind: AgentKind) -> u32 {
        AgentKind::all()
            .into_iter()
            .position(|candidate| candidate == kind)
            .map(|index| self.agent_colors[index])
            .unwrap_or_else(|| kind.default_color())
    }
}

fn parse_color(value: &str, fallback: u32) -> u32 {
    parse_color_option(value).unwrap_or(fallback)
}

fn parse_color_option(value: &str) -> Option<u32> {
    let value = value.trim();
    let value = value
        .strip_prefix('#')
        .or_else(|| value.strip_prefix("0x"))
        .or_else(|| value.strip_prefix("0X"))
        .unwrap_or(value);
    if value.len() == 3 {
        let mut expanded = String::with_capacity(6);
        for character in value.chars() {
            expanded.push(character);
            expanded.push(character);
        }
        return u32::from_str_radix(&expanded, 16).ok();
    }
    if value.len() == 6 {
        return u32::from_str_radix(value, 16).ok();
    }
    None
}

#[derive(Debug, Deserialize)]
#[serde(untagged)]
enum ConfigDocument {
    Wrapped { overrides: AppConfigOverrides },
    Flat(AppConfigOverrides),
}

impl ConfigDocument {
    fn into_overrides(self) -> AppConfigOverrides {
        match self {
            Self::Wrapped { overrides } | Self::Flat(overrides) => overrides,
        }
    }
}

/// Optional values read from disk. Nested options are deliberate: a missing
/// field keeps the built-in default instead of replacing an entire section.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct AppConfigOverrides {
    pub startup: Option<StartupConfigOverrides>,
    pub shell: Option<ShellConfigOverrides>,
    pub features: Option<FeatureConfigOverrides>,
    pub theme: Option<ThemeConfigOverrides>,
    pub terminal: Option<TerminalConfigOverrides>,
    pub ui: Option<UiConfigOverrides>,
    pub shortcuts: Option<ShortcutConfigOverrides>,
}

pub type ConfigOverrides = AppConfigOverrides;

impl AppConfigOverrides {
    pub fn into_config(self) -> AppConfig {
        let mut config = AppConfig::default();
        self.apply_to(&mut config);
        config.normalized()
    }

    pub fn apply_to(&self, config: &mut AppConfig) {
        if let Some(startup) = &self.startup {
            if let Some(default_cwd) = &startup.default_cwd {
                config.startup.default_cwd = default_cwd.clone();
            }
            if let Some(control_socket) = &startup.control_socket {
                config.startup.control_socket = control_socket.clone();
            }
            if let Some(value) = startup.initial_workspace {
                config.startup.initial_workspace = value;
            }
            if let Some(value) = startup.initial_terminal {
                config.startup.initial_terminal = value;
            }
            if let Some(value) = startup.window_width {
                config.startup.window_width = value;
            }
            if let Some(value) = startup.window_height {
                config.startup.window_height = value;
            }
            if let Some(value) = startup.window_min_width {
                config.startup.window_min_width = value;
            }
            if let Some(value) = startup.window_min_height {
                config.startup.window_min_height = value;
            }
        }
        if let Some(shell) = &self.shell {
            let program_changed = shell.program.is_some();
            if let Some(program) = &shell.program {
                config.shell.program = program.clone();
            }
            if let Some(args) = &shell.args {
                config.shell.args = args.clone();
            } else if program_changed {
                config.shell.args = default_shell_args(&config.shell.program);
            }
        }
        if let Some(features) = &self.features {
            if let Some(value) = features.mouse_reporting {
                config.features.mouse_reporting = value;
            }
            if let Some(value) = features.bracketed_paste {
                config.features.bracketed_paste = value;
            }
            if let Some(value) = features.selection {
                config.features.selection = value;
            }
        }
        if let Some(theme) = &self.theme {
            theme.apply_to(&mut config.theme);
        }
        if let Some(terminal) = &self.terminal {
            if let Some(value) = terminal.default_columns {
                config.terminal.default_columns = value;
            }
            if let Some(value) = terminal.default_lines {
                config.terminal.default_lines = value;
            }
            if let Some(value) = terminal.scrollback_lines {
                config.terminal.scrollback_lines = value;
            }
            if let Some(value) = terminal.inactive_scrollback_lines {
                config.terminal.inactive_scrollback_lines = value;
            }
            if let Some(value) = terminal.max_total_scrollback_bytes {
                config.terminal.max_total_scrollback_bytes = value;
            }
            if let Some(value) = &terminal.font_family {
                config.terminal.font_family = value.clone();
            }
            if let Some(value) = terminal.font_size {
                config.terminal.font_size = value;
            }
            if let Some(value) = terminal.line_height {
                config.terminal.line_height = value;
            }
        }
        if let Some(ui) = &self.ui {
            if let Some(value) = ui.font_size {
                config.ui.font_size = value;
            }
            if let Some(value) = ui.sidebar_visible {
                config.ui.sidebar_visible = value;
            }
            if let Some(value) = ui.sidebar_show_agent_count {
                config.ui.sidebar_show_agent_count = value;
            }
            if let Some(value) = ui.tab_bar_vertical_wheel_scroll {
                config.ui.tab_bar_vertical_wheel_scroll = value;
            }
            if let Some(value) = ui.sidebar_width {
                config.ui.sidebar_width = value;
            }
            if let Some(value) = ui.sidebar_min_width {
                config.ui.sidebar_min_width = value;
            }
            if let Some(value) = ui.sidebar_max_width {
                config.ui.sidebar_max_width = value;
            }
            if let Some(value) = ui.sidebar_resize_handle_width {
                config.ui.sidebar_resize_handle_width = value;
            }
            if let Some(value) = ui.titlebar_height {
                config.ui.titlebar_height = value;
            }
            if let Some(value) = ui.tab_height {
                config.ui.tab_height = value;
            }
            if let Some(value) = ui.sidebar_header_height {
                config.ui.sidebar_header_height = value;
            }
            if let Some(value) = ui.pane_margin {
                config.ui.pane_margin = value;
            }
            if let Some(value) = ui.pane_padding {
                config.ui.pane_padding = value;
            }
        }
        if let Some(shortcuts) = &self.shortcuts {
            shortcuts.apply_to(&mut config.shortcuts);
        }
    }
}

#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct StartupConfigOverrides {
    pub default_cwd: Option<Option<String>>,
    pub control_socket: Option<Option<String>>,
    pub initial_workspace: Option<bool>,
    pub initial_terminal: Option<bool>,
    pub window_width: Option<f32>,
    pub window_height: Option<f32>,
    pub window_min_width: Option<f32>,
    pub window_min_height: Option<f32>,
}

#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct ShellConfigOverrides {
    pub program: Option<String>,
    pub args: Option<Vec<String>>,
}

#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct FeatureConfigOverrides {
    pub mouse_reporting: Option<bool>,
    pub bracketed_paste: Option<bool>,
    pub selection: Option<bool>,
}

#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct ThemeConfigOverrides {
    pub terminal_background: Option<String>,
    pub terminal_foreground: Option<String>,
    pub selection_background: Option<String>,
    pub cursor_foreground: Option<String>,
    pub cursor_background: Option<String>,
    pub inactive_cursor: Option<String>,
    pub inverse_foreground: Option<String>,
    pub inverse_background: Option<String>,
    pub pane_background: Option<String>,
    pub active_pane_border: Option<String>,
    pub inactive_pane_border: Option<String>,
    pub chrome_background: Option<String>,
    pub tab_active_background: Option<String>,
    pub tab_inactive_background: Option<String>,
    pub tab_add_background: Option<String>,
    pub ui_foreground: Option<String>,
    pub sidebar_background: Option<String>,
    pub sidebar_workspace_background: Option<String>,
    pub sidebar_agent_background: Option<String>,
    pub sidebar_workspace_active_background: Option<String>,
    pub sidebar_agent_active_background: Option<String>,
    pub sidebar_drag_indicator_color: Option<String>,
    pub agent_colors: Option<BTreeMap<String, String>>,
}

impl ThemeConfigOverrides {
    fn apply_to(&self, theme: &mut ThemeConfig) {
        macro_rules! apply {
            ($field:ident) => {
                if let Some(value) = &self.$field {
                    theme.$field = value.clone();
                }
            };
        }
        apply!(terminal_background);
        apply!(terminal_foreground);
        apply!(selection_background);
        apply!(cursor_foreground);
        apply!(cursor_background);
        apply!(inactive_cursor);
        apply!(inverse_foreground);
        apply!(inverse_background);
        apply!(pane_background);
        apply!(active_pane_border);
        apply!(inactive_pane_border);
        apply!(chrome_background);
        apply!(tab_active_background);
        apply!(tab_inactive_background);
        apply!(tab_add_background);
        apply!(ui_foreground);
        apply!(sidebar_background);
        apply!(sidebar_workspace_background);
        apply!(sidebar_agent_background);
        apply!(sidebar_workspace_active_background);
        apply!(sidebar_agent_active_background);
        apply!(sidebar_drag_indicator_color);
        if let Some(agent_colors) = &self.agent_colors {
            for (key, value) in agent_colors {
                if AgentKind::from_config_key(key).is_some() {
                    theme.agent_colors.insert(key.clone(), value.clone());
                }
            }
        }
    }
}

#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct TerminalConfigOverrides {
    pub default_columns: Option<usize>,
    pub default_lines: Option<usize>,
    pub scrollback_lines: Option<usize>,
    pub inactive_scrollback_lines: Option<usize>,
    pub max_total_scrollback_bytes: Option<usize>,
    pub font_family: Option<String>,
    pub font_size: Option<f32>,
    pub line_height: Option<f32>,
}

#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct UiConfigOverrides {
    pub font_size: Option<f32>,
    pub sidebar_visible: Option<bool>,
    pub sidebar_show_agent_count: Option<bool>,
    pub tab_bar_vertical_wheel_scroll: Option<bool>,
    pub sidebar_width: Option<f32>,
    pub sidebar_min_width: Option<f32>,
    pub sidebar_max_width: Option<f32>,
    pub sidebar_resize_handle_width: Option<f32>,
    pub titlebar_height: Option<f32>,
    pub tab_height: Option<f32>,
    pub sidebar_header_height: Option<f32>,
    pub pane_margin: Option<f32>,
    pub pane_padding: Option<f32>,
}

#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct ShortcutConfigOverrides {
    pub open_settings: Option<String>,
    pub new_window: Option<String>,
    pub hide_window: Option<String>,
    pub minimize_window: Option<String>,
    pub ignore_quit: Option<String>,
    pub new_terminal_tab: Option<String>,
    pub new_workspace: Option<String>,
    pub toggle_sidebar: Option<String>,
    pub switch_tab: Option<String>,
    pub next_tab: Option<String>,
    pub previous_tab: Option<String>,
    pub next_workspace: Option<String>,
    pub previous_workspace: Option<String>,
    pub rename_workspace: Option<String>,
    pub rename_tab: Option<String>,
    pub split_right: Option<String>,
    pub split_down: Option<String>,
    pub close_pane: Option<String>,
    pub focus_left: Option<String>,
    pub focus_right: Option<String>,
    pub focus_up: Option<String>,
    pub focus_down: Option<String>,
    pub paste: Option<String>,
    pub copy_or_interrupt: Option<String>,
    pub eof: Option<String>,
    pub scroll_page_up: Option<String>,
    pub scroll_page_down: Option<String>,
}

impl ShortcutConfigOverrides {
    fn apply_to(&self, shortcuts: &mut ShortcutConfig) {
        macro_rules! apply {
            ($field:ident) => {
                if let Some(value) = &self.$field {
                    shortcuts.$field = value.clone();
                }
            };
        }
        apply!(open_settings);
        apply!(new_window);
        apply!(hide_window);
        apply!(minimize_window);
        apply!(ignore_quit);
        apply!(new_terminal_tab);
        apply!(new_workspace);
        apply!(toggle_sidebar);
        apply!(switch_tab);
        apply!(next_tab);
        apply!(previous_tab);
        apply!(next_workspace);
        apply!(previous_workspace);
        apply!(rename_workspace);
        apply!(rename_tab);
        apply!(split_right);
        apply!(split_down);
        apply!(close_pane);
        apply!(focus_left);
        apply!(focus_right);
        apply!(focus_up);
        apply!(focus_down);
        apply!(paste);
        apply!(copy_or_interrupt);
        apply!(eof);
        apply!(scroll_page_up);
        apply!(scroll_page_down);
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn missing_config_uses_defaults_and_invalid_colors_fall_back() {
        let config = AppConfig::default();
        assert_eq!(config.terminal.scrollback_lines, DEFAULT_SCROLLBACK_LINES);
        assert_eq!(
            config.terminal.inactive_scrollback_lines,
            DEFAULT_INACTIVE_SCROLLBACK_LINES
        );
        assert_eq!(
            config.terminal.max_total_scrollback_bytes,
            DEFAULT_MAX_TOTAL_SCROLLBACK_BYTES
        );
        assert_eq!(config.theme.colors().terminal_background, 0x2c2c2c);
        assert_eq!(config.theme.colors().terminal_foreground, 0xe4e4e4);
        assert_eq!(config.theme.colors().active_pane_border, 0x339966);
        assert_eq!(config.theme.colors().tab_active_background, 0x339966);
        assert_eq!(config.theme.colors().sidebar_background, 0x000000);
        assert_eq!(config.theme.colors().sidebar_workspace_background, 0x000000);
        assert_eq!(config.theme.colors().sidebar_agent_background, 0x000000);
        assert_eq!(
            config.theme.colors().sidebar_workspace_active_background,
            0x339966
        );
        assert_eq!(
            config.theme.colors().sidebar_agent_active_background,
            0x339966
        );
        assert_eq!(config.theme.colors().sidebar_drag_indicator, 0x339966);
        assert_eq!(
            config.theme.colors().agent_color(AgentKind::ClaudeCode),
            0xd97757
        );
        assert!(config.ui.sidebar_show_agent_count);
        assert_eq!(config.shortcuts.switch_tab, "cmd-#");
        assert_eq!(config.shortcuts.next_tab, "cmd-]");
        assert_eq!(config.shortcuts.previous_tab, "cmd-[");
        assert_eq!(config.shortcuts.next_workspace, "ctrl-tab");
        assert_eq!(config.shortcuts.previous_workspace, "ctrl-shift-tab");
        assert_eq!(parse_color("not-a-color", 0x123456), 0x123456);
        assert!(!ThemeConfig::is_valid_color("not-a-color"));
    }

    #[test]
    fn config_round_trips_through_a_separate_defaults_file() {
        let path =
            std::env::temp_dir().join(format!("water-config-test-{}.json", std::process::id()));
        let config = AppConfig::default();
        config.save_to_path(&path).unwrap();
        let loaded = AppConfig::load_from_path(&path).unwrap();
        std::fs::remove_file(path).unwrap();
        assert_eq!(loaded, config);
    }

    #[test]
    fn partial_config_is_merged_onto_the_default_override_layer() {
        let path = write_temp_config(r#"{"terminal":{"scrollback_lines":321}}"#);
        let config: AppConfig = AppConfig::load_from_path(&path).unwrap();
        std::fs::remove_file(path).unwrap();
        assert_eq!(config.terminal.scrollback_lines, 321);
        // Normalization keeps the inactive tail within the per-terminal cap.
        assert_eq!(config.terminal.inactive_scrollback_lines, 321);
        assert_eq!(
            config.terminal.max_total_scrollback_bytes,
            DEFAULT_MAX_TOTAL_SCROLLBACK_BYTES
        );
        assert_eq!(config.terminal.default_columns, DEFAULT_COLUMNS);
        assert!(config.features.selection);
        assert_eq!(config.theme.terminal_background, "#2c2c2c");
        assert_eq!(config.shell.args, default_shell_args(&config.shell.program));
    }

    #[test]
    fn agent_theme_overrides_merge_known_keys_and_ignore_unknown_keys() {
        let path = write_temp_config(
            r##"{
                "theme": {
                    "agent_colors": {
                        "codex": "#abcdef",
                        "not_an_agent": "#123456"
                    }
                }
            }"##,
        );
        let config = AppConfig::load_from_path(&path).unwrap();
        std::fs::remove_file(path).unwrap();
        assert_eq!(
            config.theme.agent_colors.get("codex"),
            Some(&"#abcdef".to_owned())
        );
        assert!(!config.theme.agent_colors.contains_key("not_an_agent"));
        assert_eq!(
            config.theme.colors().agent_color(AgentKind::Codex),
            0xabcdef
        );
        assert_eq!(
            config.theme.colors().agent_color(AgentKind::ClaudeCode),
            AgentKind::ClaudeCode.default_color()
        );
    }

    #[test]
    fn sidebar_drag_indicator_theme_override_round_trips() {
        let path = write_temp_config(
            r##"{
                "theme": {
                    "sidebar_drag_indicator_color": "#abcdef"
                }
            }"##,
        );
        let config = AppConfig::load_from_path(&path).unwrap();
        std::fs::remove_file(path).unwrap();
        let round_trip_path = std::env::temp_dir().join(format!(
            "water-config-drag-indicator-round-trip-{}.json",
            std::process::id()
        ));
        config.save_to_path(&round_trip_path).unwrap();
        let loaded = AppConfig::load_from_path(&round_trip_path).unwrap();
        std::fs::remove_file(round_trip_path).unwrap();
        assert_eq!(loaded.theme.sidebar_drag_indicator_color, "#abcdef");
        assert_eq!(loaded.theme.colors().sidebar_drag_indicator, 0xabcdef);
    }

    #[test]
    fn switch_tab_binding_substitutes_digits_and_falls_back() {
        assert_eq!(switch_tab_binding("cmd-#", 0), "cmd-1");
        assert_eq!(switch_tab_binding("cmd-#", 8), "cmd-9");
        assert_eq!(switch_tab_binding("cmd-#", 9), "cmd-0");
        assert_eq!(switch_tab_binding("cmd-shift-#", 1), "cmd-shift-2");
        assert_eq!(switch_tab_binding("", 0), "cmd-1");
        assert_eq!(switch_tab_binding("cmd-x-#", 0), "cmd-1");
        assert_eq!(switch_tab_binding("cmd-#", 10), "cmd-#");
        assert!(is_valid_switch_tab_source("cmd-#"));
        assert!(is_valid_switch_tab_source("cmd-shift-#"));
        assert!(!is_valid_switch_tab_source("cmd-x-#"));
        assert!(!is_valid_switch_tab_source("cmd-##"));
    }

    #[test]
    fn explicit_empty_shell_args_are_preserved() {
        let config: AppConfig =
            serde_json::from_str(r#"{"shell":{"program":"/bin/zsh","args":[]}}"#).unwrap();
        assert_eq!(config.shell.program, "/bin/zsh");
        assert!(config.shell.args.is_empty());
    }

    #[test]
    fn wrapped_override_documents_are_supported() {
        let path = write_temp_config(
            r#"{"overrides":{"terminal":{"default_columns":64},"ui":{"sidebar_visible":false}}}"#,
        );
        let config = AppConfig::load_from_path(&path).unwrap();
        std::fs::remove_file(path).unwrap();
        assert_eq!(config.terminal.default_columns, 64);
        assert!(!config.ui.sidebar_visible);
        assert_eq!(config.terminal.default_lines, DEFAULT_LINES);
    }

    #[test]
    fn all_new_sections_keep_the_previous_defaults() {
        let config = AppConfig::default();
        assert_eq!(config.startup.default_cwd.as_deref(), Some("~"));
        assert_eq!(config.startup.window_width, DEFAULT_WINDOW_WIDTH);
        assert_eq!(config.startup.window_height, DEFAULT_WINDOW_HEIGHT);
        assert_eq!(config.startup.window_min_width, DEFAULT_WINDOW_MIN_WIDTH);
        assert_eq!(config.startup.window_min_height, DEFAULT_WINDOW_MIN_HEIGHT);
        assert_eq!(config.ui.sidebar_width, DEFAULT_SIDEBAR_WIDTH);
        assert_eq!(config.ui.font_size, DEFAULT_UI_FONT_SIZE);
        assert_eq!(config.shortcuts.open_settings, "cmd-,");
        assert_eq!(config.terminal.default_columns, DEFAULT_COLUMNS);
        assert_eq!(config.terminal.default_lines, DEFAULT_LINES);
    }

    #[test]
    fn override_layer_merges_shell_cwd_theme_and_shortcuts_independently() {
        let path = write_temp_config(
            r##"{
                "startup": {"default_cwd": "~/Projects", "window_width": 1500},
                "shell": {"program": "/bin/sh"},
                "theme": {"active_pane_border": "#abcdef"},
                "ui": {"sidebar_show_agent_count": false},
                "shortcuts": {
                    "open_settings": "cmd-shift-,",
                    "switch_tab": "cmd-alt-#",
                    "next_tab": "cmd-shift-]"
                }
            }"##,
        );
        let config = AppConfig::load_from_path(&path).unwrap();
        std::fs::remove_file(path).unwrap();
        assert_eq!(config.startup.default_cwd.as_deref(), Some("~/Projects"));
        assert_eq!(config.startup.window_width, 1500.0);
        assert_eq!(config.shell.program, "/bin/sh");
        assert!(config.shell.args.is_empty());
        assert_eq!(config.theme.active_pane_border, "#abcdef");
        assert_eq!(config.shortcuts.open_settings, "cmd-shift-,");
        assert_eq!(config.shortcuts.switch_tab, "cmd-alt-#");
        assert_eq!(config.shortcuts.next_tab, "cmd-shift-]");
        assert_eq!(config.shortcuts.new_window, "cmd-n");
        assert!(!config.ui.sidebar_show_agent_count);
    }

    #[test]
    fn restart_classification_only_covers_worker_and_startup_settings() {
        let base = AppConfig::default();
        let mut immediate = base.clone();
        immediate.theme.ui_foreground = "#ffffff".to_owned();
        immediate.ui.sidebar_width += 10.0;
        immediate.terminal.font_size += 1.0;
        assert!(!base.restart_required_for(&immediate));

        let mut restart = base;
        restart.shell.args = vec!["-f".to_owned()];
        assert!(AppConfig::default().restart_required_for(&restart));
    }

    #[test]
    fn config_normalizes_terminal_and_ui_limits() {
        let config = AppConfig {
            terminal: TerminalConfig {
                default_columns: usize::MAX,
                default_lines: usize::MAX,
                scrollback_lines: usize::MAX,
                inactive_scrollback_lines: usize::MAX,
                max_total_scrollback_bytes: usize::MAX,
                font_family: "   ".to_owned(),
                font_size: 1.0,
                line_height: 1000.0,
            },
            ui: UiConfig {
                font_size: 100.0,
                sidebar_width: 9999.0,
                sidebar_min_width: 500.0,
                sidebar_max_width: 100.0,
                ..UiConfig::default()
            },
            ..AppConfig::default()
        }
        .normalized();
        assert_eq!(config.terminal.default_columns, MAX_COLUMNS);
        assert_eq!(config.terminal.default_lines, MAX_LINES);
        assert_eq!(config.terminal.scrollback_lines, MAX_SCROLLBACK_LINES);
        assert_eq!(
            config.terminal.inactive_scrollback_lines,
            MAX_SCROLLBACK_LINES
        );
        assert_eq!(
            config.terminal.max_total_scrollback_bytes,
            MAX_TOTAL_SCROLLBACK_BYTES
        );
        assert_eq!(config.terminal.font_family, DEFAULT_FONT_FAMILY);
        assert_eq!(config.terminal.font_size, 8.0);
        assert_eq!(config.terminal.line_height, 64.0);
        assert_eq!(config.ui.font_size, 32.0);
        assert_eq!(config.ui.sidebar_min_width, 500.0);
        assert_eq!(config.ui.sidebar_max_width, 500.0);
        assert_eq!(config.ui.sidebar_width, 500.0);
    }

    fn write_temp_config(contents: &str) -> PathBuf {
        let path = std::env::temp_dir().join(format!(
            "water-config-override-test-{}-{}.json",
            std::process::id(),
            contents.len()
        ));
        std::fs::write(&path, contents).unwrap();
        path
    }
}
