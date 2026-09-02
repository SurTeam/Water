use std::fs;
use std::io;
use std::path::{Path, PathBuf};

use serde::{Deserialize, Serialize};
use thiserror::Error;

use crate::terminal::{MAX_SCROLLBACK_LINES, MAX_TOTAL_SCROLLBACK_LINES};

const DEFAULT_FONT_FAMILY: &str = "Sarasa Term SC Nerd Font";
const DEFAULT_FONT_SIZE: f32 = 14.0;
const DEFAULT_LINE_HEIGHT: f32 = 18.0;

#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct AppConfig {
    pub features: FeatureConfig,
    pub theme: ThemeConfig,
    pub terminal: TerminalConfig,
}

impl AppConfig {
    /// Returns the per-user defaults file path for the current platform.
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

    /// Loads a user config file. A missing file means "use application
    /// defaults"; malformed files are reported so configuration mistakes are
    /// visible instead of silently changing behavior.
    pub fn load_from_path(path: impl AsRef<Path>) -> Result<Self, ConfigError> {
        let path = path.as_ref().to_path_buf();
        let contents = match fs::read_to_string(&path) {
            Ok(contents) => contents,
            Err(error) if error.kind() == io::ErrorKind::NotFound => return Ok(Self::default()),
            Err(source) => return Err(ConfigError::Read { path, source }),
        };
        let config: Self =
            serde_json::from_str(&contents).map_err(|source| ConfigError::Parse {
                path: path.clone(),
                source,
            })?;
        Ok(config.normalized())
    }

    /// Loads the default per-user config path, honoring `WATER_CONFIG` when
    /// present. The selected path is returned for diagnostics.
    pub fn load_default() -> Result<(Self, PathBuf), ConfigError> {
        let path = std::env::var_os("WATER_CONFIG")
            .map(PathBuf::from)
            .unwrap_or_else(Self::default_path);
        Ok((Self::load_from_path(&path)?, path))
    }

    /// Writes this configuration to the selected per-user defaults path and
    /// returns that path.
    pub fn save_default(&self) -> Result<PathBuf, ConfigError> {
        let path = std::env::var_os("WATER_CONFIG")
            .map(PathBuf::from)
            .unwrap_or_else(Self::default_path);
        self.save_to_path(&path)?;
        Ok(path)
    }

    /// Writes a complete, pretty-printed defaults file. The application does
    /// not create one automatically, so a fresh install stays clean until the
    /// user chooses to persist settings.
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

    pub fn normalized(mut self) -> Self {
        self.terminal.scrollback_lines = self
            .terminal
            .scrollback_lines
            .clamp(1, MAX_SCROLLBACK_LINES);
        self.terminal.max_total_scrollback_lines = self
            .terminal
            .max_total_scrollback_lines
            .clamp(1, MAX_TOTAL_SCROLLBACK_LINES);
        if self.terminal.font_family.trim().is_empty() {
            self.terminal.font_family = DEFAULT_FONT_FAMILY.to_owned();
        }
        self.terminal.font_size = self.terminal.font_size.clamp(8.0, 32.0);
        self.terminal.line_height = self.terminal.line_height.clamp(8.0, 64.0);
        self
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
    /// Maximum number of normal scrollback rows retained by each terminal.
    pub scrollback_lines: usize,
    /// Maximum number of scrollback rows retained by all terminals together,
    /// including temporary rows kept while a terminal is pinned away from live
    /// output.
    pub max_total_scrollback_lines: usize,
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
            scrollback_lines: MAX_SCROLLBACK_LINES,
            max_total_scrollback_lines: MAX_TOTAL_SCROLLBACK_LINES,
            font_family: DEFAULT_FONT_FAMILY.to_owned(),
            font_size: DEFAULT_FONT_SIZE,
            line_height: DEFAULT_LINE_HEIGHT,
        }
    }
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
        }
    }
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
}

impl ThemeConfig {
    pub fn colors(&self) -> ThemeColors {
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
        }
    }
}

fn parse_color(value: &str, fallback: u32) -> u32 {
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
        return u32::from_str_radix(&expanded, 16).unwrap_or(fallback);
    }
    if value.len() == 6 {
        return u32::from_str_radix(value, 16).unwrap_or(fallback);
    }
    fallback
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn missing_config_uses_defaults_and_invalid_colors_fall_back() {
        let config = AppConfig::default();
        assert_eq!(config.terminal.scrollback_lines, MAX_SCROLLBACK_LINES);
        assert_eq!(
            config.terminal.max_total_scrollback_lines,
            MAX_TOTAL_SCROLLBACK_LINES
        );
        assert_eq!(config.theme.colors().terminal_background, 0x2c2c2c);
        assert_eq!(config.theme.colors().terminal_foreground, 0xe4e4e4);
        assert_eq!(config.theme.colors().active_pane_border, 0x339966);
        assert_eq!(config.theme.colors().tab_active_background, 0x339966);
        assert_eq!(parse_color("not-a-color", 0x123456), 0x123456);
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
    fn partial_config_keeps_defaults_for_omitted_sections() {
        let config: AppConfig =
            serde_json::from_str(r#"{"terminal":{"scrollback_lines":321}}"#).unwrap();
        assert_eq!(config.terminal.scrollback_lines, 321);
        assert_eq!(
            config.terminal.max_total_scrollback_lines,
            MAX_TOTAL_SCROLLBACK_LINES
        );
        assert!(config.features.selection);
        assert_eq!(config.theme.terminal_background, "#2c2c2c");
        assert_eq!(config.theme.terminal_foreground, "#e4e4e4");
        assert_eq!(config.theme.active_pane_border, "#339966");
    }

    #[test]
    fn config_normalizes_terminal_limits() {
        let config = AppConfig {
            terminal: TerminalConfig {
                scrollback_lines: usize::MAX,
                max_total_scrollback_lines: usize::MAX,
                font_family: "   ".to_owned(),
                font_size: 1.0,
                line_height: 1000.0,
            },
            ..AppConfig::default()
        }
        .normalized();
        assert_eq!(config.terminal.scrollback_lines, MAX_SCROLLBACK_LINES);
        assert_eq!(
            config.terminal.max_total_scrollback_lines,
            MAX_TOTAL_SCROLLBACK_LINES
        );
        assert_eq!(config.terminal.font_family, DEFAULT_FONT_FAMILY);
        assert_eq!(config.terminal.font_size, 8.0);
        assert_eq!(config.terminal.line_height, 64.0);
    }
}
