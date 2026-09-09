use gpui::{
    AnyElement, App, Context, FocusHandle, Focusable, KeyDownEvent, MouseButton, MouseDownEvent,
    MouseMoveEvent, MouseUpEvent, SharedString, Window, div, font, prelude::*, px, rgb,
};

use crate::agent::AgentKind;
use crate::config::{AppConfig, ThemeConfig, is_valid_switch_tab_source, switch_tab_binding};

use super::application::{HideWindow, IgnoreQuit, MinimizeWindow, WaterApplication};

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum ApplyKind {
    Immediate,
    NewWindow,
    Restart,
}

impl ApplyKind {
    fn label(self) -> &'static str {
        match self {
            Self::Immediate => "立即生效",
            Self::NewWindow => "新窗口生效",
            Self::Restart => "重启后生效",
        }
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum SettingField {
    DefaultCwd,
    ControlSocket,
    InitialWorkspace,
    InitialTerminal,
    WindowWidth,
    WindowHeight,
    WindowMinWidth,
    WindowMinHeight,
    ShellProgram,
    ShellArgs,
    DefaultColumns,
    DefaultLines,
    ScrollbackLines,
    InactiveScrollbackLines,
    MaxTotalScrollbackBytes,
    FontFamily,
    FontSize,
    LineHeight,
    MouseReporting,
    BracketedPaste,
    Selection,
    UiFontSize,
    SidebarVisible,
    SidebarShowAgentCount,
    TabBarVerticalWheelScroll,
    SidebarWidth,
    SidebarMinWidth,
    SidebarMaxWidth,
    SidebarResizeHandleWidth,
    TitlebarHeight,
    TabHeight,
    SidebarHeaderHeight,
    PaneMargin,
    PanePadding,
    ThemeTerminalBackground,
    ThemeTerminalForeground,
    ThemeSelectionBackground,
    ThemeCursorForeground,
    ThemeCursorBackground,
    ThemeInactiveCursor,
    ThemeInverseForeground,
    ThemeInverseBackground,
    ThemePaneBackground,
    ThemeActivePaneBorder,
    ThemeInactivePaneBorder,
    ThemeChromeBackground,
    ThemeTabActiveBackground,
    ThemeTabInactiveBackground,
    ThemeTabAddBackground,
    ThemeUiForeground,
    ThemeSidebarBackground,
    ThemeSidebarConnectionBackground,
    ThemeSidebarConnectionActiveBackground,
    ThemeSidebarWorkspaceBackground,
    ThemeSidebarAgentBackground,
    ThemeSidebarWorkspaceActiveBackground,
    ThemeSidebarAgentActiveBackground,
    ThemeSidebarDragIndicator,
    AgentColor(AgentKind),
    ShortcutOpenSettings,
    ShortcutNewWindow,
    ShortcutHideWindow,
    ShortcutMinimizeWindow,
    ShortcutIgnoreQuit,
    ShortcutNewTerminalTab,
    ShortcutNewWorkspace,
    ShortcutToggleSidebar,
    ShortcutSwitchTab,
    ShortcutNextTab,
    ShortcutPreviousTab,
    ShortcutNextWorkspace,
    ShortcutPreviousWorkspace,
    ShortcutRenameWorkspace,
    ShortcutRenameTab,
    ShortcutSplitRight,
    ShortcutSplitDown,
    ShortcutClosePane,
    ShortcutFocusLeft,
    ShortcutFocusRight,
    ShortcutFocusUp,
    ShortcutFocusDown,
    ShortcutPaste,
    ShortcutCopyOrInterrupt,
    ShortcutEof,
    ShortcutScrollPageUp,
    ShortcutScrollPageDown,
}

impl SettingField {
    fn id(self) -> String {
        let id = match self {
            Self::DefaultCwd => "default-cwd",
            Self::ControlSocket => "control-socket",
            Self::InitialWorkspace => "initial-workspace",
            Self::InitialTerminal => "initial-terminal",
            Self::WindowWidth => "window-width",
            Self::WindowHeight => "window-height",
            Self::WindowMinWidth => "window-min-width",
            Self::WindowMinHeight => "window-min-height",
            Self::ShellProgram => "shell-program",
            Self::ShellArgs => "shell-args",
            Self::DefaultColumns => "default-columns",
            Self::DefaultLines => "default-lines",
            Self::ScrollbackLines => "scrollback-lines",
            Self::InactiveScrollbackLines => "inactive-scrollback-lines",
            Self::MaxTotalScrollbackBytes => "max-total-scrollback-bytes",
            Self::FontFamily => "font-family",
            Self::FontSize => "font-size",
            Self::LineHeight => "line-height",
            Self::MouseReporting => "mouse-reporting",
            Self::BracketedPaste => "bracketed-paste",
            Self::Selection => "selection",
            Self::UiFontSize => "ui-font-size",
            Self::SidebarVisible => "sidebar-visible",
            Self::SidebarShowAgentCount => "sidebar-show-agent-count",
            Self::TabBarVerticalWheelScroll => "tab-bar-vertical-wheel-scroll",
            Self::SidebarWidth => "sidebar-width",
            Self::SidebarMinWidth => "sidebar-min-width",
            Self::SidebarMaxWidth => "sidebar-max-width",
            Self::SidebarResizeHandleWidth => "sidebar-resize-handle-width",
            Self::TitlebarHeight => "titlebar-height",
            Self::TabHeight => "tab-height",
            Self::SidebarHeaderHeight => "sidebar-header-height",
            Self::PaneMargin => "pane-margin",
            Self::PanePadding => "pane-padding",
            Self::ThemeTerminalBackground => "theme-terminal-background",
            Self::ThemeTerminalForeground => "theme-terminal-foreground",
            Self::ThemeSelectionBackground => "theme-selection-background",
            Self::ThemeCursorForeground => "theme-cursor-foreground",
            Self::ThemeCursorBackground => "theme-cursor-background",
            Self::ThemeInactiveCursor => "theme-inactive-cursor",
            Self::ThemeInverseForeground => "theme-inverse-foreground",
            Self::ThemeInverseBackground => "theme-inverse-background",
            Self::ThemePaneBackground => "theme-pane-background",
            Self::ThemeActivePaneBorder => "theme-active-pane-border",
            Self::ThemeInactivePaneBorder => "theme-inactive-pane-border",
            Self::ThemeChromeBackground => "theme-chrome-background",
            Self::ThemeTabActiveBackground => "theme-tab-active-background",
            Self::ThemeTabInactiveBackground => "theme-tab-inactive-background",
            Self::ThemeTabAddBackground => "theme-tab-add-background",
            Self::ThemeUiForeground => "theme-ui-foreground",
            Self::ThemeSidebarBackground => "theme-sidebar-background",
            Self::ThemeSidebarConnectionBackground => "theme-sidebar-connection-background",
            Self::ThemeSidebarConnectionActiveBackground => {
                "theme-sidebar-connection-active-background"
            }
            Self::ThemeSidebarWorkspaceBackground => "theme-sidebar-workspace-background",
            Self::ThemeSidebarAgentBackground => "theme-sidebar-agent-background",
            Self::ThemeSidebarWorkspaceActiveBackground => {
                "theme-sidebar-workspace-active-background"
            }
            Self::ThemeSidebarAgentActiveBackground => "theme-sidebar-agent-active-background",
            Self::ThemeSidebarDragIndicator => "theme-sidebar-drag-indicator",
            Self::AgentColor(kind) => return format!("agent-color-{}", kind.config_key()),
            Self::ShortcutOpenSettings => "shortcut-open-settings",
            Self::ShortcutNewWindow => "shortcut-new-window",
            Self::ShortcutHideWindow => "shortcut-hide-window",
            Self::ShortcutMinimizeWindow => "shortcut-minimize-window",
            Self::ShortcutIgnoreQuit => "shortcut-ignore-quit",
            Self::ShortcutNewTerminalTab => "shortcut-new-terminal-tab",
            Self::ShortcutNewWorkspace => "shortcut-new-workspace",
            Self::ShortcutToggleSidebar => "shortcut-toggle-sidebar",
            Self::ShortcutSwitchTab => "shortcut-switch-tab",
            Self::ShortcutNextTab => "shortcut-next-tab",
            Self::ShortcutPreviousTab => "shortcut-previous-tab",
            Self::ShortcutNextWorkspace => "shortcut-next-workspace",
            Self::ShortcutPreviousWorkspace => "shortcut-previous-workspace",
            Self::ShortcutRenameWorkspace => "shortcut-rename-workspace",
            Self::ShortcutRenameTab => "shortcut-rename-tab",
            Self::ShortcutSplitRight => "shortcut-split-right",
            Self::ShortcutSplitDown => "shortcut-split-down",
            Self::ShortcutClosePane => "shortcut-close-pane",
            Self::ShortcutFocusLeft => "shortcut-focus-left",
            Self::ShortcutFocusRight => "shortcut-focus-right",
            Self::ShortcutFocusUp => "shortcut-focus-up",
            Self::ShortcutFocusDown => "shortcut-focus-down",
            Self::ShortcutPaste => "shortcut-paste",
            Self::ShortcutCopyOrInterrupt => "shortcut-copy-or-interrupt",
            Self::ShortcutEof => "shortcut-eof",
            Self::ShortcutScrollPageUp => "shortcut-scroll-page-up",
            Self::ShortcutScrollPageDown => "shortcut-scroll-page-down",
        };
        id.to_owned()
    }

    fn is_boolean(self) -> bool {
        matches!(
            self,
            Self::InitialWorkspace
                | Self::InitialTerminal
                | Self::MouseReporting
                | Self::BracketedPaste
                | Self::Selection
                | Self::SidebarVisible
                | Self::SidebarShowAgentCount
                | Self::TabBarVerticalWheelScroll
        )
    }

    fn is_color(self) -> bool {
        matches!(
            self,
            Self::ThemeTerminalBackground
                | Self::ThemeTerminalForeground
                | Self::ThemeSelectionBackground
                | Self::ThemeCursorForeground
                | Self::ThemeCursorBackground
                | Self::ThemeInactiveCursor
                | Self::ThemeInverseForeground
                | Self::ThemeInverseBackground
                | Self::ThemePaneBackground
                | Self::ThemeActivePaneBorder
                | Self::ThemeInactivePaneBorder
                | Self::ThemeChromeBackground
                | Self::ThemeTabActiveBackground
                | Self::ThemeTabInactiveBackground
                | Self::ThemeTabAddBackground
                | Self::ThemeUiForeground
                | Self::ThemeSidebarBackground
                | Self::ThemeSidebarConnectionBackground
                | Self::ThemeSidebarConnectionActiveBackground
                | Self::ThemeSidebarWorkspaceBackground
                | Self::ThemeSidebarAgentBackground
                | Self::ThemeSidebarWorkspaceActiveBackground
                | Self::ThemeSidebarAgentActiveBackground
                | Self::ThemeSidebarDragIndicator
                | Self::AgentColor(_)
        )
    }
}

/// A standalone, model-independent configuration editor. It keeps a working
/// copy until Save succeeds, then WaterApplication fans the immediate part of
/// the effective configuration out to every workspace window.
pub struct SettingsView {
    application: WaterApplication,
    config: AppConfig,
    focus_handle: FocusHandle,
    titlebar_dragging: bool,
    editing: Option<SettingField>,
    edit_value: String,
    dirty: bool,
    saving: bool,
    restart_after_save: bool,
    pending_restart: bool,
    status: Option<String>,
    status_is_error: bool,
}

impl SettingsView {
    pub(crate) fn new(application: WaterApplication, focus_handle: FocusHandle) -> Self {
        Self {
            config: application.config(),
            application,
            focus_handle,
            titlebar_dragging: false,
            editing: None,
            edit_value: String::new(),
            dirty: false,
            saving: false,
            restart_after_save: false,
            pending_restart: false,
            status: None,
            status_is_error: false,
        }
    }

    fn begin_edit(&mut self, field: SettingField, window: &mut Window, cx: &mut Context<Self>) {
        if field.is_boolean() || self.saving {
            return;
        }
        self.editing = Some(field);
        self.edit_value = self.raw_value(field);
        self.status = None;
        self.status_is_error = false;
        self.focus_handle.focus(window, cx);
        cx.notify();
    }

    fn cancel_edit(&mut self, cx: &mut Context<Self>) {
        if self.editing.take().is_some() {
            self.edit_value.clear();
            self.status = None;
            self.status_is_error = false;
            cx.notify();
        }
    }

    fn commit_edit(&mut self, cx: &mut Context<Self>) {
        let Some(field) = self.editing else {
            return;
        };
        let value = self.edit_value.clone();
        match self.set_field(field, value) {
            Ok(()) => {
                self.config = self.config.clone().normalized();
                self.editing = None;
                self.edit_value.clear();
                self.dirty = true;
                self.status = Some("有未保存的修改".to_owned());
                self.status_is_error = false;
                cx.notify();
            }
            Err(error) => {
                self.status = Some(error);
                self.status_is_error = true;
                cx.notify();
            }
        }
    }

    fn toggle_field(&mut self, field: SettingField, cx: &mut Context<Self>) {
        if self.saving {
            return;
        }
        match field {
            SettingField::InitialWorkspace => {
                self.config.startup.initial_workspace = !self.config.startup.initial_workspace
            }
            SettingField::InitialTerminal => {
                self.config.startup.initial_terminal = !self.config.startup.initial_terminal
            }
            SettingField::MouseReporting => {
                self.config.features.mouse_reporting = !self.config.features.mouse_reporting
            }
            SettingField::BracketedPaste => {
                self.config.features.bracketed_paste = !self.config.features.bracketed_paste
            }
            SettingField::Selection => {
                self.config.features.selection = !self.config.features.selection
            }
            SettingField::SidebarVisible => {
                self.config.ui.sidebar_visible = !self.config.ui.sidebar_visible
            }
            SettingField::SidebarShowAgentCount => {
                self.config.ui.sidebar_show_agent_count = !self.config.ui.sidebar_show_agent_count
            }
            SettingField::TabBarVerticalWheelScroll => {
                self.config.ui.tab_bar_vertical_wheel_scroll =
                    !self.config.ui.tab_bar_vertical_wheel_scroll
            }
            _ => return,
        }
        self.config = self.config.clone().normalized();
        self.dirty = true;
        self.status = Some("有未保存的修改".to_owned());
        self.status_is_error = false;
        cx.notify();
    }

    fn reset_to_defaults(&mut self, cx: &mut Context<Self>) {
        if self.saving {
            return;
        }
        self.config = AppConfig::default();
        self.editing = None;
        self.edit_value.clear();
        self.dirty = true;
        self.restart_after_save = false;
        self.pending_restart = false;
        self.status = Some("已恢复内置默认值，点击保存后生效".to_owned());
        self.status_is_error = false;
        cx.notify();
    }

    fn save(&mut self, cx: &mut Context<Self>) {
        if self.saving {
            return;
        }
        if self.editing.is_some() {
            self.commit_edit(cx);
            if self.editing.is_some() {
                return;
            }
        }
        let config = self.config.clone().normalized();
        if let Err(error) = validate_shortcuts(&config.shortcuts) {
            self.status = Some(error);
            self.status_is_error = true;
            self.restart_after_save = false;
            cx.notify();
            return;
        }
        let old_config = self.application.config();
        let requires_restart = old_config.restart_required_for(&config);
        let path = self.application.config_path();
        self.saving = true;
        self.status = Some(if self.restart_after_save {
            "正在保存配置，保存成功后将重启 Water…".to_owned()
        } else {
            "正在保存配置…".to_owned()
        });
        self.status_is_error = false;
        cx.spawn(async move |entity, cx| {
            let result = cx
                .background_executor()
                .spawn(async move {
                    config
                        .save_to_path(&path)
                        .map_err(|error| error.to_string())
                        .map(|_| (config, requires_restart, path))
                })
                .await;
            let _ = entity.update(cx, |view, cx| {
                view.saving = false;
                let restart_after_save = view.restart_after_save;
                view.restart_after_save = false;
                match result {
                    Ok((config, requires_restart, path)) => {
                        view.config = config.clone();
                        view.application.apply_config(config, cx);
                        view.dirty = false;
                        view.pending_restart |= requires_restart;
                        view.status = Some(if view.pending_restart {
                            format!("已保存到 {}；部分设置将在重启 Water 后生效", path.display())
                        } else {
                            format!("已保存到 {}", path.display())
                        });
                        view.status_is_error = false;
                        if restart_after_save {
                            cx.restart();
                            return;
                        }
                    }
                    Err(error) => {
                        view.status = Some(format!("保存失败：{error}"));
                        view.status_is_error = true;
                    }
                }
                cx.notify();
            });
        })
        .detach();
        cx.notify();
    }

    fn restart(&mut self, cx: &mut Context<Self>) {
        if self.saving {
            self.status = Some("配置正在保存，请稍候再重启".to_owned());
            self.status_is_error = true;
            cx.notify();
            return;
        }
        if self.editing.is_some() {
            self.commit_edit(cx);
            if self.editing.is_some() {
                return;
            }
        }
        if self.dirty {
            self.restart_after_save = true;
            self.save(cx);
            return;
        }
        if !self.pending_restart {
            self.status = Some("当前没有需要重启后生效的配置".to_owned());
            self.status_is_error = false;
            cx.notify();
            return;
        }
        cx.restart();
    }

    fn handle_key_down(
        &mut self,
        event: &KeyDownEvent,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) -> bool {
        if let Some(field) = self.editing {
            if event.keystroke.modifiers.platform && event.keystroke.key == "v" {
                let clipboard = cx.read_from_clipboard_async();
                cx.spawn(async move |entity, cx| {
                    let Ok(Some(item)) = clipboard.await else {
                        return;
                    };
                    let Some(text) = clipboard_text(item) else {
                        return;
                    };
                    let _ = entity.update(cx, |view, cx| {
                        if view.editing == Some(field) {
                            view.edit_value.push_str(&text);
                            cx.notify();
                        }
                    });
                })
                .detach();
                cx.stop_propagation();
                return true;
            }
            match event.keystroke.key.as_str() {
                "enter" | "return" => self.commit_edit(cx),
                "escape" => self.cancel_edit(cx),
                "backspace" | "delete" => {
                    self.edit_value.pop();
                    cx.notify();
                }
                _ if !event.keystroke.modifiers.control
                    && !event.keystroke.modifiers.alt
                    && !event.keystroke.modifiers.platform
                    && !event.keystroke.modifiers.function
                    && event
                        .keystroke
                        .key_char
                        .as_deref()
                        .is_some_and(|character| {
                            !character.is_empty() && character.chars().all(|c| !c.is_control())
                        }) =>
                {
                    if let Some(character) = event.keystroke.key_char.as_deref() {
                        self.edit_value.push_str(character);
                        cx.notify();
                    }
                }
                _ => {}
            }
            let _ = field;
            cx.stop_propagation();
            return true;
        }

        if event.keystroke.key == "escape" {
            window.remove_window();
            cx.stop_propagation();
            return true;
        }
        false
    }

    fn raw_value(&self, field: SettingField) -> String {
        match field {
            SettingField::DefaultCwd => self.config.startup.default_cwd.clone().unwrap_or_default(),
            SettingField::ControlSocket => self
                .config
                .startup
                .control_socket
                .clone()
                .unwrap_or_default(),
            SettingField::InitialWorkspace => self.config.startup.initial_workspace.to_string(),
            SettingField::InitialTerminal => self.config.startup.initial_terminal.to_string(),
            SettingField::WindowWidth => format_float(self.config.startup.window_width),
            SettingField::WindowHeight => format_float(self.config.startup.window_height),
            SettingField::WindowMinWidth => format_float(self.config.startup.window_min_width),
            SettingField::WindowMinHeight => format_float(self.config.startup.window_min_height),
            SettingField::ShellProgram => self.config.shell.program.clone(),
            SettingField::ShellArgs => {
                serde_json::to_string(&self.config.shell.args).unwrap_or_else(|_| "[]".to_owned())
            }
            SettingField::DefaultColumns => self.config.terminal.default_columns.to_string(),
            SettingField::DefaultLines => self.config.terminal.default_lines.to_string(),
            SettingField::ScrollbackLines => self.config.terminal.scrollback_lines.to_string(),
            SettingField::InactiveScrollbackLines => {
                self.config.terminal.inactive_scrollback_lines.to_string()
            }
            SettingField::MaxTotalScrollbackBytes => {
                self.config.terminal.max_total_scrollback_bytes.to_string()
            }
            SettingField::FontFamily => self.config.terminal.font_family.clone(),
            SettingField::FontSize => format_float(self.config.terminal.font_size),
            SettingField::LineHeight => format_float(self.config.terminal.line_height),
            SettingField::MouseReporting => self.config.features.mouse_reporting.to_string(),
            SettingField::BracketedPaste => self.config.features.bracketed_paste.to_string(),
            SettingField::Selection => self.config.features.selection.to_string(),
            SettingField::UiFontSize => format_float(self.config.ui.font_size),
            SettingField::SidebarVisible => self.config.ui.sidebar_visible.to_string(),
            SettingField::SidebarShowAgentCount => {
                self.config.ui.sidebar_show_agent_count.to_string()
            }
            SettingField::TabBarVerticalWheelScroll => {
                self.config.ui.tab_bar_vertical_wheel_scroll.to_string()
            }
            SettingField::SidebarWidth => format_float(self.config.ui.sidebar_width),
            SettingField::SidebarMinWidth => format_float(self.config.ui.sidebar_min_width),
            SettingField::SidebarMaxWidth => format_float(self.config.ui.sidebar_max_width),
            SettingField::SidebarResizeHandleWidth => {
                format_float(self.config.ui.sidebar_resize_handle_width)
            }
            SettingField::TitlebarHeight => format_float(self.config.ui.titlebar_height),
            SettingField::TabHeight => format_float(self.config.ui.tab_height),
            SettingField::SidebarHeaderHeight => format_float(self.config.ui.sidebar_header_height),
            SettingField::PaneMargin => format_float(self.config.ui.pane_margin),
            SettingField::PanePadding => format_float(self.config.ui.pane_padding),
            SettingField::ThemeTerminalBackground => self.config.theme.terminal_background.clone(),
            SettingField::ThemeTerminalForeground => self.config.theme.terminal_foreground.clone(),
            SettingField::ThemeSelectionBackground => {
                self.config.theme.selection_background.clone()
            }
            SettingField::ThemeCursorForeground => self.config.theme.cursor_foreground.clone(),
            SettingField::ThemeCursorBackground => self.config.theme.cursor_background.clone(),
            SettingField::ThemeInactiveCursor => self.config.theme.inactive_cursor.clone(),
            SettingField::ThemeInverseForeground => self.config.theme.inverse_foreground.clone(),
            SettingField::ThemeInverseBackground => self.config.theme.inverse_background.clone(),
            SettingField::ThemePaneBackground => self.config.theme.pane_background.clone(),
            SettingField::ThemeActivePaneBorder => self.config.theme.active_pane_border.clone(),
            SettingField::ThemeInactivePaneBorder => self.config.theme.inactive_pane_border.clone(),
            SettingField::ThemeChromeBackground => self.config.theme.chrome_background.clone(),
            SettingField::ThemeTabActiveBackground => {
                self.config.theme.tab_active_background.clone()
            }
            SettingField::ThemeTabInactiveBackground => {
                self.config.theme.tab_inactive_background.clone()
            }
            SettingField::ThemeTabAddBackground => self.config.theme.tab_add_background.clone(),
            SettingField::ThemeUiForeground => self.config.theme.ui_foreground.clone(),
            SettingField::ThemeSidebarBackground => self.config.theme.sidebar_background.clone(),
            SettingField::ThemeSidebarConnectionBackground => {
                self.config.theme.sidebar_connection_background.clone()
            }
            SettingField::ThemeSidebarConnectionActiveBackground => self
                .config
                .theme
                .sidebar_connection_active_background
                .clone(),
            SettingField::ThemeSidebarWorkspaceBackground => {
                self.config.theme.sidebar_workspace_background.clone()
            }
            SettingField::ThemeSidebarAgentBackground => {
                self.config.theme.sidebar_agent_background.clone()
            }
            SettingField::ThemeSidebarWorkspaceActiveBackground => self
                .config
                .theme
                .sidebar_workspace_active_background
                .clone(),
            SettingField::ThemeSidebarAgentActiveBackground => {
                self.config.theme.sidebar_agent_active_background.clone()
            }
            SettingField::ThemeSidebarDragIndicator => {
                self.config.theme.sidebar_drag_indicator_color.clone()
            }
            SettingField::AgentColor(kind) => self
                .config
                .theme
                .agent_colors
                .get(kind.config_key())
                .cloned()
                .unwrap_or_else(|| format!("#{:06x}", kind.default_color())),
            SettingField::ShortcutOpenSettings => self.config.shortcuts.open_settings.clone(),
            SettingField::ShortcutNewWindow => self.config.shortcuts.new_window.clone(),
            SettingField::ShortcutHideWindow => self.config.shortcuts.hide_window.clone(),
            SettingField::ShortcutMinimizeWindow => self.config.shortcuts.minimize_window.clone(),
            SettingField::ShortcutIgnoreQuit => self.config.shortcuts.ignore_quit.clone(),
            SettingField::ShortcutNewTerminalTab => self.config.shortcuts.new_terminal_tab.clone(),
            SettingField::ShortcutNewWorkspace => self.config.shortcuts.new_workspace.clone(),
            SettingField::ShortcutToggleSidebar => self.config.shortcuts.toggle_sidebar.clone(),
            SettingField::ShortcutSwitchTab => self.config.shortcuts.switch_tab.clone(),
            SettingField::ShortcutNextTab => self.config.shortcuts.next_tab.clone(),
            SettingField::ShortcutPreviousTab => self.config.shortcuts.previous_tab.clone(),
            SettingField::ShortcutNextWorkspace => self.config.shortcuts.next_workspace.clone(),
            SettingField::ShortcutPreviousWorkspace => {
                self.config.shortcuts.previous_workspace.clone()
            }
            SettingField::ShortcutRenameWorkspace => self.config.shortcuts.rename_workspace.clone(),
            SettingField::ShortcutRenameTab => self.config.shortcuts.rename_tab.clone(),
            SettingField::ShortcutSplitRight => self.config.shortcuts.split_right.clone(),
            SettingField::ShortcutSplitDown => self.config.shortcuts.split_down.clone(),
            SettingField::ShortcutClosePane => self.config.shortcuts.close_pane.clone(),
            SettingField::ShortcutFocusLeft => self.config.shortcuts.focus_left.clone(),
            SettingField::ShortcutFocusRight => self.config.shortcuts.focus_right.clone(),
            SettingField::ShortcutFocusUp => self.config.shortcuts.focus_up.clone(),
            SettingField::ShortcutFocusDown => self.config.shortcuts.focus_down.clone(),
            SettingField::ShortcutPaste => self.config.shortcuts.paste.clone(),
            SettingField::ShortcutCopyOrInterrupt => {
                self.config.shortcuts.copy_or_interrupt.clone()
            }
            SettingField::ShortcutEof => self.config.shortcuts.eof.clone(),
            SettingField::ShortcutScrollPageUp => self.config.shortcuts.scroll_page_up.clone(),
            SettingField::ShortcutScrollPageDown => self.config.shortcuts.scroll_page_down.clone(),
        }
    }

    fn set_field(&mut self, field: SettingField, value: String) -> Result<(), String> {
        let value = value.trim().to_owned();
        match field {
            SettingField::DefaultCwd => {
                self.config.startup.default_cwd = (!value.is_empty()).then_some(value)
            }
            SettingField::ControlSocket => {
                self.config.startup.control_socket = (!value.is_empty()).then_some(value)
            }
            SettingField::WindowWidth => {
                self.config.startup.window_width = parse_float(&value, "窗口宽度")?
            }
            SettingField::WindowHeight => {
                self.config.startup.window_height = parse_float(&value, "窗口高度")?
            }
            SettingField::WindowMinWidth => {
                self.config.startup.window_min_width = parse_float(&value, "窗口最小宽度")?
            }
            SettingField::WindowMinHeight => {
                self.config.startup.window_min_height = parse_float(&value, "窗口最小高度")?
            }
            SettingField::ShellProgram => {
                if value.is_empty() {
                    return Err("shell 命令不能为空".to_owned());
                }
                self.config.shell.program = value;
            }
            SettingField::ShellArgs => {
                self.config.shell.args = serde_json::from_str::<Vec<String>>(&value)
                    .map_err(|_| "shell 参数必须是 JSON 字符串数组，例如 [\"-l\"]".to_owned())?;
            }
            SettingField::DefaultColumns => {
                self.config.terminal.default_columns = parse_usize(&value, "默认列数")?
            }
            SettingField::DefaultLines => {
                self.config.terminal.default_lines = parse_usize(&value, "默认行数")?
            }
            SettingField::ScrollbackLines => {
                self.config.terminal.scrollback_lines = parse_usize(&value, "终端滚动历史")?
            }
            SettingField::InactiveScrollbackLines => {
                self.config.terminal.inactive_scrollback_lines =
                    parse_usize(&value, "非活跃终端滚动历史")?
            }
            SettingField::MaxTotalScrollbackBytes => {
                self.config.terminal.max_total_scrollback_bytes =
                    parse_usize(&value, "滚动历史总字节预算")?
            }
            SettingField::FontFamily => {
                if value.is_empty() {
                    return Err("字体不能为空".to_owned());
                }
                self.config.terminal.font_family = value;
            }
            SettingField::FontSize => {
                self.config.terminal.font_size = parse_float(&value, "字体大小")?
            }
            SettingField::LineHeight => {
                self.config.terminal.line_height = parse_float(&value, "行高")?
            }
            SettingField::UiFontSize => {
                self.config.ui.font_size = parse_float(&value, "界面字体大小")?
            }
            SettingField::SidebarWidth => {
                self.config.ui.sidebar_width = parse_float(&value, "侧边栏宽度")?
            }
            SettingField::SidebarMinWidth => {
                self.config.ui.sidebar_min_width = parse_float(&value, "侧边栏最小宽度")?
            }
            SettingField::SidebarMaxWidth => {
                self.config.ui.sidebar_max_width = parse_float(&value, "侧边栏最大宽度")?
            }
            SettingField::SidebarResizeHandleWidth => {
                self.config.ui.sidebar_resize_handle_width =
                    parse_float(&value, "侧边栏拖拽手柄宽度")?
            }
            SettingField::TitlebarHeight => {
                self.config.ui.titlebar_height = parse_float(&value, "标题栏高度")?
            }
            SettingField::TabHeight => {
                self.config.ui.tab_height = parse_float(&value, "标签栏高度")?
            }
            SettingField::SidebarHeaderHeight => {
                self.config.ui.sidebar_header_height = parse_float(&value, "侧边栏标题高度")?
            }
            SettingField::PaneMargin => {
                self.config.ui.pane_margin = parse_float(&value, "面板外边距")?
            }
            SettingField::PanePadding => {
                self.config.ui.pane_padding = parse_float(&value, "面板内边距")?
            }
            field if field.is_color() => {
                if !ThemeConfig::is_valid_color(&value) {
                    return Err("颜色必须是 3 位或 6 位十六进制值，例如 #339966".to_owned());
                }
                set_theme_field(&mut self.config.theme, field, value)?;
            }
            field if is_shortcut(field) => {
                if !shortcut_is_valid_for(field, &value) {
                    return Err(
                        "快捷键格式无效，请使用 cmd-k、ctrl-k、shift-pageup，或用 # 表示标签序号"
                            .to_owned(),
                    );
                }
                set_shortcut_field(&mut self.config.shortcuts, field, value)?;
            }
            SettingField::InitialWorkspace
            | SettingField::InitialTerminal
            | SettingField::MouseReporting
            | SettingField::BracketedPaste
            | SettingField::Selection
            | SettingField::SidebarVisible
            | SettingField::SidebarShowAgentCount
            | SettingField::TabBarVerticalWheelScroll => {
                return Err("布尔项请直接点击开关".to_owned());
            }
            _ => return Err("暂不支持编辑此设置".to_owned()),
        }
        Ok(())
    }

    fn value(&self, field: SettingField) -> String {
        if self.editing == Some(field) {
            return format!("{}▌", self.edit_value);
        }
        match field {
            SettingField::InitialWorkspace => on_off(self.config.startup.initial_workspace),
            SettingField::InitialTerminal => on_off(self.config.startup.initial_terminal),
            SettingField::MouseReporting => on_off(self.config.features.mouse_reporting),
            SettingField::BracketedPaste => on_off(self.config.features.bracketed_paste),
            SettingField::Selection => on_off(self.config.features.selection),
            SettingField::SidebarVisible => on_off(self.config.ui.sidebar_visible),
            SettingField::SidebarShowAgentCount => on_off(self.config.ui.sidebar_show_agent_count),
            SettingField::TabBarVerticalWheelScroll => {
                on_off(self.config.ui.tab_bar_vertical_wheel_scroll)
            }
            SettingField::DefaultCwd => self
                .config
                .startup
                .default_cwd
                .clone()
                .unwrap_or_else(|| "(当前进程目录)".to_owned()),
            SettingField::ControlSocket => self
                .config
                .startup
                .control_socket
                .clone()
                .unwrap_or_else(|| "(环境变量或 /tmp/water.sock)".to_owned()),
            _ => self.raw_value(field),
        }
    }

    fn render_setting(
        &self,
        field: SettingField,
        label: &'static str,
        description: &'static str,
        apply: ApplyKind,
        theme: crate::config::ThemeColors,
        cx: &mut Context<Self>,
    ) -> AnyElement {
        let field_id = field.id();
        let editing = self.editing == Some(field);
        let value = self.value(field);
        let mut value_box = div()
            .id(format!("setting-value-{field_id}"))
            .min_w(px(260.))
            .max_w(px(460.))
            .px(px(10.))
            .py(px(7.))
            .flex()
            .items_center()
            .justify_end()
            .cursor_pointer()
            .border_1()
            .border_color(rgb(if editing {
                theme.active_pane_border
            } else {
                theme.inactive_pane_border
            }))
            .bg(rgb(theme.pane_background))
            .text_color(rgb(theme.terminal_foreground));
        if field.is_color()
            && let Some(color) = color_value(&self.config.theme, field)
        {
            value_box = value_box.child(
                div()
                    .size(px(14.))
                    .mr(px(8.))
                    .bg(rgb(color))
                    .border_1()
                    .border_color(rgb(theme.inactive_pane_border)),
            );
        }
        if field.is_boolean() {
            value_box = value_box
                .on_mouse_down(
                    MouseButton::Left,
                    cx.listener(move |this, _event: &MouseDownEvent, _window, cx| {
                        this.toggle_field(field, cx);
                        cx.stop_propagation();
                    }),
                )
                .child(SharedString::from(value));
        } else {
            value_box = value_box
                .on_mouse_down(
                    MouseButton::Left,
                    cx.listener(move |this, _event: &MouseDownEvent, window, cx| {
                        this.begin_edit(field, window, cx);
                        cx.stop_propagation();
                    }),
                )
                .child(SharedString::from(value));
        }
        div()
            .id(format!("setting-row-{field_id}"))
            .w_full()
            .p(px(10.))
            .gap(px(10.))
            .flex()
            .items_center()
            .hover(|style| style.bg(rgb(theme.chrome_background)))
            .child(
                div()
                    .flex_1()
                    .min_w(px(0.))
                    .flex()
                    .flex_col()
                    .gap(px(3.))
                    .child(
                        div()
                            .text_size(px(self.config.ui.font_size * 0.875))
                            .text_color(rgb(theme.ui_foreground))
                            .child(label),
                    )
                    .child(
                        div()
                            .text_size(px(self.config.ui.font_size * 0.75))
                            .text_color(rgb(theme.inactive_pane_border))
                            .child(description),
                    ),
            )
            .child(
                div()
                    .w(px(90.))
                    .flex_none()
                    .items_center()
                    .justify_end()
                    .text_size(px(self.config.ui.font_size * 0.75))
                    .text_color(rgb(theme.active_pane_border))
                    .child(apply.label()),
            )
            .child(value_box)
            .into_any_element()
    }

    fn render_section(
        &self,
        title: &'static str,
        rows: Vec<AnyElement>,
        theme: crate::config::ThemeColors,
    ) -> AnyElement {
        let mut section = div()
            .w_full()
            .mb(px(16.))
            .border_1()
            .border_color(rgb(theme.inactive_pane_border))
            .bg(rgb(theme.chrome_background))
            .flex()
            .flex_col()
            .child(
                div()
                    .w_full()
                    .px(px(12.))
                    .py(px(10.))
                    .text_size(px(self.config.ui.font_size * 1.125))
                    .text_color(rgb(theme.ui_foreground))
                    .child(title),
            );
        for row in rows {
            section = section.child(row);
        }
        section.into_any_element()
    }

    fn render_header(
        &self,
        theme: crate::config::ThemeColors,
        cx: &mut Context<Self>,
    ) -> AnyElement {
        let save_label = if self.saving {
            "保存中…"
        } else if self.dirty {
            "保存"
        } else {
            "已保存"
        };
        let restart_label = if self.saving {
            "重启中…"
        } else if self.pending_restart || self.dirty {
            "重启 Water"
        } else {
            "重启"
        };
        let restart_available = self.pending_restart || self.dirty;
        div()
            .h(px(self.config.ui.titlebar_height))
            .w_full()
            .px(px(16.))
            .gap(px(12.))
            .items_center()
            .flex()
            .bg(rgb(theme.chrome_background))
            .text_color(rgb(theme.ui_foreground))
            .on_mouse_down_out(cx.listener(|this, _event, _window, _cx| {
                this.titlebar_dragging = false;
            }))
            .on_mouse_down(
                MouseButton::Left,
                cx.listener(|this, _event: &MouseDownEvent, _window, _cx| {
                    this.titlebar_dragging = true;
                }),
            )
            .on_mouse_up(
                MouseButton::Left,
                cx.listener(|this, _event: &MouseUpEvent, _window, _cx| {
                    this.titlebar_dragging = false;
                }),
            )
            .on_mouse_move(cx.listener(|this, _event: &MouseMoveEvent, window, _cx| {
                if this.titlebar_dragging {
                    this.titlebar_dragging = false;
                    window.start_window_move();
                }
            }))
            .child(
                div()
                    .flex_1()
                    .text_size(px(self.config.ui.font_size * 1.125))
                    .child("Water 设置"),
            )
            .child(
                div()
                    .px(px(10.))
                    .py(px(6.))
                    .cursor_pointer()
                    .text_color(rgb(theme.ui_foreground))
                    .border_1()
                    .border_color(rgb(theme.inactive_pane_border))
                    .on_mouse_down(
                        MouseButton::Left,
                        cx.listener(|this, _event: &MouseDownEvent, _window, cx| {
                            this.reset_to_defaults(cx);
                            cx.stop_propagation();
                        }),
                    )
                    .child("恢复默认"),
            )
            .child(
                div()
                    .px(px(12.))
                    .py(px(6.))
                    .cursor_pointer()
                    .bg(rgb(if self.dirty {
                        theme.active_pane_border
                    } else {
                        theme.inactive_pane_border
                    }))
                    .text_color(rgb(theme.terminal_background))
                    .on_mouse_down(
                        MouseButton::Left,
                        cx.listener(|this, _event: &MouseDownEvent, _window, cx| {
                            this.save(cx);
                            cx.stop_propagation();
                        }),
                    )
                    .child(save_label),
            )
            .child(
                div()
                    .px(px(12.))
                    .py(px(6.))
                    .cursor_pointer()
                    .bg(rgb(if restart_available {
                        theme.active_pane_border
                    } else {
                        theme.inactive_pane_border
                    }))
                    .text_color(rgb(theme.terminal_background))
                    .on_mouse_down(
                        MouseButton::Left,
                        cx.listener(|this, _event: &MouseDownEvent, _window, cx| {
                            this.restart(cx);
                            cx.stop_propagation();
                        }),
                    )
                    .child(restart_label),
            )
            .child(
                div()
                    .px(px(10.))
                    .py(px(6.))
                    .cursor_pointer()
                    .text_color(rgb(theme.ui_foreground))
                    .border_1()
                    .border_color(rgb(theme.inactive_pane_border))
                    .on_mouse_down(MouseButton::Left, |_event: &MouseDownEvent, window, cx| {
                        window.remove_window();
                        cx.stop_propagation();
                    })
                    .child("关闭"),
            )
            .into_any_element()
    }

    fn render_status(&self, theme: crate::config::ThemeColors) -> Option<AnyElement> {
        let status = self.status.as_deref()?;
        Some(
            div()
                .w_full()
                .px(px(16.))
                .py(px(8.))
                .text_size(px(self.config.ui.font_size * 0.875))
                .text_color(rgb(if self.status_is_error {
                    0xff6b6b
                } else {
                    theme.ui_foreground
                }))
                .child(SharedString::from(status.to_owned()))
                .into_any_element(),
        )
    }
}

impl Focusable for SettingsView {
    fn focus_handle(&self, _cx: &App) -> FocusHandle {
        self.focus_handle.clone()
    }
}

impl Render for SettingsView {
    fn render(&mut self, _window: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        let theme = self.config.theme.colors();
        let mut content = div()
            .id("settings-scroll")
            .flex_1()
            .min_w(px(0.))
            .min_h(px(0.))
            .p(px(16.))
            .overflow_y_scroll()
            .flex()
            .flex_col()
            .bg(rgb(theme.terminal_background));

        let startup_rows = vec![
            self.render_setting(
                SettingField::DefaultCwd,
                "默认 cwd",
                "没有可继承终端时使用；支持 ~，留空表示当前进程目录",
                ApplyKind::Restart,
                theme,
                cx,
            ),
            self.render_setting(
                SettingField::ControlSocket,
                "控制 socket",
                "Unix 控制接口路径；CLI 和 WATER_CONTROL_SOCKET 优先",
                ApplyKind::Restart,
                theme,
                cx,
            ),
            self.render_setting(
                SettingField::InitialWorkspace,
                "启动时创建工作区",
                "应用启动时是否自动创建第一个工作区",
                ApplyKind::Restart,
                theme,
                cx,
            ),
            self.render_setting(
                SettingField::InitialTerminal,
                "启动时创建终端",
                "应用启动时是否在工作区中打开默认终端",
                ApplyKind::Restart,
                theme,
                cx,
            ),
            self.render_setting(
                SettingField::WindowWidth,
                "默认窗口宽度",
                "新建 Water 窗口的初始逻辑像素宽度",
                ApplyKind::NewWindow,
                theme,
                cx,
            ),
            self.render_setting(
                SettingField::WindowHeight,
                "默认窗口高度",
                "新建 Water 窗口的初始逻辑像素高度",
                ApplyKind::NewWindow,
                theme,
                cx,
            ),
            self.render_setting(
                SettingField::WindowMinWidth,
                "窗口最小宽度",
                "窗口可自由缩小到的最小逻辑像素宽度",
                ApplyKind::NewWindow,
                theme,
                cx,
            ),
            self.render_setting(
                SettingField::WindowMinHeight,
                "窗口最小高度",
                "窗口可自由缩小到的最小逻辑像素高度",
                ApplyKind::NewWindow,
                theme,
                cx,
            ),
        ];
        content = content.child(self.render_section("启动与窗口", startup_rows, theme));

        let shell_rows = vec![
            self.render_setting(
                SettingField::ShellProgram,
                "Shell 命令",
                "默认终端启动的可执行文件，例如 /opt/homebrew/bin/zsh",
                ApplyKind::Restart,
                theme,
                cx,
            ),
            self.render_setting(
                SettingField::ShellArgs,
                "Shell 参数",
                "JSON 字符串数组，例如 [\"-l\"]",
                ApplyKind::Restart,
                theme,
                cx,
            ),
        ];
        content = content.child(self.render_section("Shell", shell_rows, theme));

        let terminal_rows = vec![
            self.render_setting(
                SettingField::DefaultColumns,
                "默认列数",
                "新终端第一次布局前使用的列数",
                ApplyKind::Restart,
                theme,
                cx,
            ),
            self.render_setting(
                SettingField::DefaultLines,
                "默认行数",
                "新终端第一次布局前使用的行数",
                ApplyKind::Restart,
                theme,
                cx,
            ),
            self.render_setting(
                SettingField::ScrollbackLines,
                "每个终端滚动历史",
                "焦点终端保留的滚动行数，最大 10000",
                ApplyKind::Restart,
                theme,
                cx,
            ),
            self.render_setting(
                SettingField::InactiveScrollbackLines,
                "非活跃终端滚动历史",
                "失去焦点后每个后台标签仅保留的行尾滚动行数",
                ApplyKind::Restart,
                theme,
                cx,
            ),
            self.render_setting(
                SettingField::MaxTotalScrollbackBytes,
                "滚动历史总字节预算",
                "所有 PTY 滚动网格共享的堆字节预算（按宽度计价，而非行数）",
                ApplyKind::Restart,
                theme,
                cx,
            ),
            self.render_setting(
                SettingField::FontFamily,
                "终端字体",
                "终端使用的等宽字体族",
                ApplyKind::Immediate,
                theme,
                cx,
            ),
            self.render_setting(
                SettingField::FontSize,
                "终端字体大小",
                "逻辑像素，范围 8–32",
                ApplyKind::Immediate,
                theme,
                cx,
            ),
            self.render_setting(
                SettingField::LineHeight,
                "终端行高",
                "逻辑像素，范围 8–64",
                ApplyKind::Immediate,
                theme,
                cx,
            ),
        ];
        content = content.child(self.render_section("终端", terminal_rows, theme));

        let feature_rows = vec![
            self.render_setting(
                SettingField::MouseReporting,
                "终端鼠标报告",
                "允许 TUI 程序接管鼠标点击和滚轮",
                ApplyKind::Immediate,
                theme,
                cx,
            ),
            self.render_setting(
                SettingField::BracketedPaste,
                "Bracketed paste",
                "终端请求时为 Cmd-V 包裹 bracketed-paste 标记",
                ApplyKind::Immediate,
                theme,
                cx,
            ),
            self.render_setting(
                SettingField::Selection,
                "终端文本选择",
                "允许拖拽选择和 Cmd-C 复制",
                ApplyKind::Immediate,
                theme,
                cx,
            ),
        ];
        content = content.child(self.render_section("终端交互功能", feature_rows, theme));

        let ui_rows = vec![
            self.render_setting(
                SettingField::UiFontSize,
                "界面字体大小",
                "Water 标题栏、标签、侧边栏、设置页面等 UI 文本的基础字号，范围 8–32",
                ApplyKind::Immediate,
                theme,
                cx,
            ),
            self.render_setting(
                SettingField::SidebarVisible,
                "默认显示侧边栏",
                "打开新窗口时侧边栏是否可见",
                ApplyKind::Immediate,
                theme,
                cx,
            ),
            self.render_setting(
                SettingField::SidebarShowAgentCount,
                "显示运行中 Agent 数量",
                "在每个工作区右侧显示当前运行中的 Agent 数量",
                ApplyKind::Immediate,
                theme,
                cx,
            ),
            self.render_setting(
                SettingField::TabBarVerticalWheelScroll,
                "纵向滚轮滚动标签栏",
                "默认关闭；开启后在标签栏上滚动鼠标滚轮可横向滚动标签（触控板横向滑动始终可用）",
                ApplyKind::Immediate,
                theme,
                cx,
            ),
            self.render_setting(
                SettingField::SidebarWidth,
                "侧边栏宽度",
                "逻辑像素，拖拽范围由最小/最大宽度限制",
                ApplyKind::Immediate,
                theme,
                cx,
            ),
            self.render_setting(
                SettingField::SidebarMinWidth,
                "侧边栏最小宽度",
                "逻辑像素",
                ApplyKind::Immediate,
                theme,
                cx,
            ),
            self.render_setting(
                SettingField::SidebarMaxWidth,
                "侧边栏最大宽度",
                "逻辑像素",
                ApplyKind::Immediate,
                theme,
                cx,
            ),
            self.render_setting(
                SettingField::SidebarResizeHandleWidth,
                "侧边栏拖拽手柄宽度",
                "逻辑像素",
                ApplyKind::Immediate,
                theme,
                cx,
            ),
            self.render_setting(
                SettingField::TitlebarHeight,
                "标题栏高度",
                "逻辑像素",
                ApplyKind::Immediate,
                theme,
                cx,
            ),
            self.render_setting(
                SettingField::TabHeight,
                "标签栏高度",
                "逻辑像素",
                ApplyKind::Immediate,
                theme,
                cx,
            ),
            self.render_setting(
                SettingField::SidebarHeaderHeight,
                "侧边栏标题高度",
                "逻辑像素",
                ApplyKind::Immediate,
                theme,
                cx,
            ),
            self.render_setting(
                SettingField::PaneMargin,
                "面板外边距",
                "终端面板之间的外边距，逻辑像素",
                ApplyKind::Immediate,
                theme,
                cx,
            ),
            self.render_setting(
                SettingField::PanePadding,
                "面板内边距",
                "终端面板内容的内边距，逻辑像素",
                ApplyKind::Immediate,
                theme,
                cx,
            ),
        ];
        content = content.child(self.render_section("界面布局", ui_rows, theme));

        let theme_fields = [
            (SettingField::ThemeTerminalBackground, "终端背景色"),
            (SettingField::ThemeTerminalForeground, "终端前景色"),
            (SettingField::ThemeSelectionBackground, "选择背景色"),
            (SettingField::ThemeCursorForeground, "光标前景色"),
            (SettingField::ThemeCursorBackground, "光标背景色"),
            (SettingField::ThemeInactiveCursor, "非焦点光标色"),
            (SettingField::ThemeInverseForeground, "反色前景色"),
            (SettingField::ThemeInverseBackground, "反色背景色"),
            (SettingField::ThemePaneBackground, "面板背景色"),
            (SettingField::ThemeActivePaneBorder, "活动面板边框色"),
            (SettingField::ThemeInactivePaneBorder, "非活动面板边框色"),
            (SettingField::ThemeChromeBackground, "窗口 chrome 背景色"),
            (SettingField::ThemeTabActiveBackground, "活动标签背景色"),
            (SettingField::ThemeTabInactiveBackground, "非活动标签背景色"),
            (SettingField::ThemeTabAddBackground, "标签悬停/添加背景色"),
            (SettingField::ThemeUiForeground, "界面前景色"),
            (SettingField::ThemeSidebarBackground, "侧边栏背景色"),
            (
                SettingField::ThemeSidebarConnectionBackground,
                "未选中主机背景色",
            ),
            (
                SettingField::ThemeSidebarConnectionActiveBackground,
                "选中主机背景色",
            ),
            (
                SettingField::ThemeSidebarWorkspaceBackground,
                "未选中工作区背景色",
            ),
            (
                SettingField::ThemeSidebarAgentBackground,
                "未选中 Agent 背景色",
            ),
            (
                SettingField::ThemeSidebarWorkspaceActiveBackground,
                "选中工作区背景色",
            ),
            (
                SettingField::ThemeSidebarAgentActiveBackground,
                "选中 Agent 背景色",
            ),
            (
                SettingField::ThemeSidebarDragIndicator,
                "侧边栏拖拽指示线颜色",
            ),
        ];
        let theme_rows = theme_fields
            .into_iter()
            .map(|(field, label)| {
                self.render_setting(
                    field,
                    label,
                    "支持 #rgb、#rrggbb、0xrrggbb",
                    ApplyKind::Immediate,
                    theme,
                    cx,
                )
            })
            .collect();
        content = content.child(self.render_section("配色", theme_rows, theme));

        let agent_color_rows = AgentKind::all()
            .into_iter()
            .map(|kind| {
                self.render_setting(
                    SettingField::AgentColor(kind),
                    kind.label(),
                    "Agent 代表色；支持 #rgb、#rrggbb、0xrrggbb",
                    ApplyKind::Immediate,
                    theme,
                    cx,
                )
            })
            .collect();
        content = content.child(self.render_section("Agent Colors", agent_color_rows, theme));

        let shortcut_fields = [
            (SettingField::ShortcutOpenSettings, "打开设置"),
            (SettingField::ShortcutNewWindow, "新建窗口"),
            (SettingField::ShortcutHideWindow, "隐藏窗口"),
            (SettingField::ShortcutMinimizeWindow, "最小化窗口"),
            (SettingField::ShortcutIgnoreQuit, "忽略退出"),
            (SettingField::ShortcutNewTerminalTab, "新建终端标签"),
            (SettingField::ShortcutNewWorkspace, "新建工作区"),
            (SettingField::ShortcutToggleSidebar, "切换侧边栏"),
            (SettingField::ShortcutSwitchTab, "切换到指定标签"),
            (SettingField::ShortcutNextTab, "下一个标签"),
            (SettingField::ShortcutPreviousTab, "上一个标签"),
            (SettingField::ShortcutNextWorkspace, "下一个工作区"),
            (SettingField::ShortcutPreviousWorkspace, "上一个工作区"),
            (SettingField::ShortcutRenameWorkspace, "重命名工作区"),
            (SettingField::ShortcutRenameTab, "重命名标签"),
            (SettingField::ShortcutSplitRight, "右侧分屏"),
            (SettingField::ShortcutSplitDown, "向下分屏"),
            (SettingField::ShortcutClosePane, "关闭面板"),
            (SettingField::ShortcutFocusLeft, "聚焦左侧面板"),
            (SettingField::ShortcutFocusRight, "聚焦右侧面板"),
            (SettingField::ShortcutFocusUp, "聚焦上方面板"),
            (SettingField::ShortcutFocusDown, "聚焦下方面板"),
            (SettingField::ShortcutPaste, "粘贴到终端"),
            (SettingField::ShortcutCopyOrInterrupt, "复制或发送 Ctrl-C"),
            (SettingField::ShortcutEof, "发送 EOF"),
            (SettingField::ShortcutScrollPageUp, "向上滚动一页"),
            (SettingField::ShortcutScrollPageDown, "向下滚动一页"),
        ];
        let shortcut_rows = shortcut_fields
            .into_iter()
            .map(|(field, label)| {
                self.render_setting(
                    field,
                    label,
                    if field == SettingField::ShortcutSwitchTab {
                        "GPUI keystroke 格式，例如 cmd-#（# 会替换为标签序号）"
                    } else {
                        "GPUI keystroke 格式，例如 cmd-shift-n"
                    },
                    ApplyKind::Immediate,
                    theme,
                    cx,
                )
            })
            .collect();
        content = content.child(self.render_section("快捷键", shortcut_rows, theme));

        let mut root = div()
            .size_full()
            .flex()
            .flex_col()
            .track_focus(&self.focus_handle)
            .on_key_down(cx.listener(|this, event: &KeyDownEvent, window, cx| {
                this.handle_key_down(event, window, cx);
            }))
            .on_action(|_: &HideWindow, window, _cx| {
                window.remove_window();
            })
            .on_action(|_: &MinimizeWindow, window, _cx| {
                window.minimize_window();
            })
            .on_action(|_: &IgnoreQuit, _window, _cx| {})
            .bg(rgb(theme.terminal_background))
            .font(font(self.config.terminal.font_family.clone()))
            .text_size(px(self.config.ui.font_size))
            .text_color(rgb(theme.ui_foreground))
            .child(self.render_header(theme, cx))
            .child(content);
        if let Some(status) = self.render_status(theme) {
            root = root.child(status);
        }
        if self.pending_restart {
            root = root.child(
                div()
                    .w_full()
                    .px(px(16.))
                    .py(px(10.))
                    .bg(rgb(theme.tab_add_background))
                    .text_color(rgb(theme.ui_foreground))
                    .child("部分启动、Shell 和 PTY 历史设置需要重启 Water 后生效。"),
            );
        }
        root
    }
}

fn format_float(value: f32) -> String {
    if value.fract() == 0.0 {
        format!("{value:.0}")
    } else {
        format!("{value:.2}")
    }
}

fn parse_float(value: &str, label: &str) -> Result<f32, String> {
    let value = value
        .parse::<f32>()
        .map_err(|_| format!("{label}必须是数字"))?;
    value
        .is_finite()
        .then_some(value)
        .ok_or_else(|| format!("{label}必须是有限数字"))
}

fn parse_usize(value: &str, label: &str) -> Result<usize, String> {
    value
        .parse::<usize>()
        .map_err(|_| format!("{label}必须是非负整数"))
}

fn on_off(value: bool) -> String {
    if value { "开启" } else { "关闭" }.to_owned()
}

fn clipboard_text(item: gpui::ClipboardItem) -> Option<String> {
    item.entries.into_iter().find_map(|entry| match entry {
        gpui::ClipboardEntry::String(text) => Some(text.into_text()),
        gpui::ClipboardEntry::Image(_) | gpui::ClipboardEntry::ExternalPaths(_) => None,
    })
}

fn shortcut_is_valid(value: &str) -> bool {
    let value = value.trim();
    !value.is_empty()
        && !value.contains('#')
        && value
            .split_whitespace()
            .all(|keystroke| gpui::Keystroke::parse(keystroke).is_ok())
}

fn shortcut_is_valid_for(field: SettingField, value: &str) -> bool {
    if field == SettingField::ShortcutSwitchTab {
        is_valid_switch_tab_source(value)
    } else {
        shortcut_is_valid(value)
    }
}

/// Concrete keystroke(s) a shortcut value can produce at runtime. The
/// tab-template (`cmd-#`) expands to its ten digit bindings; every other
/// shortcut contributes exactly itself.
fn shortcut_expansions(label: &str, value: &str) -> Vec<String> {
    if label == "切换标签" {
        (0..10)
            .map(|index| switch_tab_binding(value, index))
            .collect()
    } else {
        vec![value.trim().to_owned()]
    }
}

fn validate_shortcuts(shortcuts: &crate::config::ShortcutConfig) -> Result<(), String> {
    let values = [
        ("打开设置", &shortcuts.open_settings),
        ("新建窗口", &shortcuts.new_window),
        ("隐藏窗口", &shortcuts.hide_window),
        ("最小化窗口", &shortcuts.minimize_window),
        ("忽略退出", &shortcuts.ignore_quit),
        ("新建终端标签", &shortcuts.new_terminal_tab),
        ("新建工作区", &shortcuts.new_workspace),
        ("切换侧边栏", &shortcuts.toggle_sidebar),
        ("切换标签", &shortcuts.switch_tab),
        ("下一个标签", &shortcuts.next_tab),
        ("上一个标签", &shortcuts.previous_tab),
        ("下一个工作区", &shortcuts.next_workspace),
        ("上一个工作区", &shortcuts.previous_workspace),
        ("重命名工作区", &shortcuts.rename_workspace),
        ("重命名标签", &shortcuts.rename_tab),
        ("右侧分屏", &shortcuts.split_right),
        ("向下分屏", &shortcuts.split_down),
        ("关闭面板", &shortcuts.close_pane),
        ("聚焦左侧面板", &shortcuts.focus_left),
        ("聚焦右侧面板", &shortcuts.focus_right),
        ("聚焦上方面板", &shortcuts.focus_up),
        ("聚焦下方面板", &shortcuts.focus_down),
        ("粘贴到终端", &shortcuts.paste),
        ("复制或发送 Ctrl-C", &shortcuts.copy_or_interrupt),
        ("发送 EOF", &shortcuts.eof),
        ("向上滚动一页", &shortcuts.scroll_page_up),
        ("向下滚动一页", &shortcuts.scroll_page_down),
    ];
    for (index, (label, value)) in values.iter().enumerate() {
        let valid = if *label == "切换标签" {
            is_valid_switch_tab_source(value)
        } else {
            shortcut_is_valid(value)
        };
        if !valid {
            return Err(format!("{label}的快捷键格式无效"));
        }
        let expansions = shortcut_expansions(label, value);
        for (other_label, other_value) in values.iter().skip(index + 1) {
            let others = shortcut_expansions(other_label, other_value);
            if let Some(hit) = expansions.iter().find(|e| others.contains(e)) {
                return Err(format!("快捷键冲突：{label}和{other_label}都使用 {hit}"));
            }
        }
    }
    Ok(())
}

fn is_shortcut(field: SettingField) -> bool {
    matches!(
        field,
        SettingField::ShortcutOpenSettings
            | SettingField::ShortcutNewWindow
            | SettingField::ShortcutHideWindow
            | SettingField::ShortcutMinimizeWindow
            | SettingField::ShortcutIgnoreQuit
            | SettingField::ShortcutNewTerminalTab
            | SettingField::ShortcutNewWorkspace
            | SettingField::ShortcutToggleSidebar
            | SettingField::ShortcutSwitchTab
            | SettingField::ShortcutNextTab
            | SettingField::ShortcutPreviousTab
            | SettingField::ShortcutNextWorkspace
            | SettingField::ShortcutPreviousWorkspace
            | SettingField::ShortcutRenameWorkspace
            | SettingField::ShortcutRenameTab
            | SettingField::ShortcutSplitRight
            | SettingField::ShortcutSplitDown
            | SettingField::ShortcutClosePane
            | SettingField::ShortcutFocusLeft
            | SettingField::ShortcutFocusRight
            | SettingField::ShortcutFocusUp
            | SettingField::ShortcutFocusDown
            | SettingField::ShortcutPaste
            | SettingField::ShortcutCopyOrInterrupt
            | SettingField::ShortcutEof
            | SettingField::ShortcutScrollPageUp
            | SettingField::ShortcutScrollPageDown
    )
}

fn set_theme_field(
    theme: &mut ThemeConfig,
    field: SettingField,
    value: String,
) -> Result<(), String> {
    match field {
        SettingField::ThemeTerminalBackground => theme.terminal_background = value,
        SettingField::ThemeTerminalForeground => theme.terminal_foreground = value,
        SettingField::ThemeSelectionBackground => theme.selection_background = value,
        SettingField::ThemeCursorForeground => theme.cursor_foreground = value,
        SettingField::ThemeCursorBackground => theme.cursor_background = value,
        SettingField::ThemeInactiveCursor => theme.inactive_cursor = value,
        SettingField::ThemeInverseForeground => theme.inverse_foreground = value,
        SettingField::ThemeInverseBackground => theme.inverse_background = value,
        SettingField::ThemePaneBackground => theme.pane_background = value,
        SettingField::ThemeActivePaneBorder => theme.active_pane_border = value,
        SettingField::ThemeInactivePaneBorder => theme.inactive_pane_border = value,
        SettingField::ThemeChromeBackground => theme.chrome_background = value,
        SettingField::ThemeTabActiveBackground => theme.tab_active_background = value,
        SettingField::ThemeTabInactiveBackground => theme.tab_inactive_background = value,
        SettingField::ThemeTabAddBackground => theme.tab_add_background = value,
        SettingField::ThemeUiForeground => theme.ui_foreground = value,
        SettingField::ThemeSidebarBackground => theme.sidebar_background = value,
        SettingField::ThemeSidebarConnectionBackground => {
            theme.sidebar_connection_background = value
        }
        SettingField::ThemeSidebarConnectionActiveBackground => {
            theme.sidebar_connection_active_background = value
        }
        SettingField::ThemeSidebarWorkspaceBackground => theme.sidebar_workspace_background = value,
        SettingField::ThemeSidebarAgentBackground => theme.sidebar_agent_background = value,
        SettingField::ThemeSidebarWorkspaceActiveBackground => {
            theme.sidebar_workspace_active_background = value
        }
        SettingField::ThemeSidebarAgentActiveBackground => {
            theme.sidebar_agent_active_background = value
        }
        SettingField::ThemeSidebarDragIndicator => theme.sidebar_drag_indicator_color = value,
        SettingField::AgentColor(kind) => {
            theme
                .agent_colors
                .insert(kind.config_key().to_owned(), value);
        }
        _ => return Err("不是颜色设置".to_owned()),
    }
    Ok(())
}

fn set_shortcut_field(
    shortcuts: &mut crate::config::ShortcutConfig,
    field: SettingField,
    value: String,
) -> Result<(), String> {
    match field {
        SettingField::ShortcutOpenSettings => shortcuts.open_settings = value,
        SettingField::ShortcutNewWindow => shortcuts.new_window = value,
        SettingField::ShortcutHideWindow => shortcuts.hide_window = value,
        SettingField::ShortcutMinimizeWindow => shortcuts.minimize_window = value,
        SettingField::ShortcutIgnoreQuit => shortcuts.ignore_quit = value,
        SettingField::ShortcutNewTerminalTab => shortcuts.new_terminal_tab = value,
        SettingField::ShortcutNewWorkspace => shortcuts.new_workspace = value,
        SettingField::ShortcutToggleSidebar => shortcuts.toggle_sidebar = value,
        SettingField::ShortcutSwitchTab => shortcuts.switch_tab = value,
        SettingField::ShortcutNextTab => shortcuts.next_tab = value,
        SettingField::ShortcutPreviousTab => shortcuts.previous_tab = value,
        SettingField::ShortcutNextWorkspace => shortcuts.next_workspace = value,
        SettingField::ShortcutPreviousWorkspace => shortcuts.previous_workspace = value,
        SettingField::ShortcutRenameWorkspace => shortcuts.rename_workspace = value,
        SettingField::ShortcutRenameTab => shortcuts.rename_tab = value,
        SettingField::ShortcutSplitRight => shortcuts.split_right = value,
        SettingField::ShortcutSplitDown => shortcuts.split_down = value,
        SettingField::ShortcutClosePane => shortcuts.close_pane = value,
        SettingField::ShortcutFocusLeft => shortcuts.focus_left = value,
        SettingField::ShortcutFocusRight => shortcuts.focus_right = value,
        SettingField::ShortcutFocusUp => shortcuts.focus_up = value,
        SettingField::ShortcutFocusDown => shortcuts.focus_down = value,
        SettingField::ShortcutPaste => shortcuts.paste = value,
        SettingField::ShortcutCopyOrInterrupt => shortcuts.copy_or_interrupt = value,
        SettingField::ShortcutEof => shortcuts.eof = value,
        SettingField::ShortcutScrollPageUp => shortcuts.scroll_page_up = value,
        SettingField::ShortcutScrollPageDown => shortcuts.scroll_page_down = value,
        _ => return Err("不是快捷键设置".to_owned()),
    }
    Ok(())
}

fn color_value(theme: &ThemeConfig, field: SettingField) -> Option<u32> {
    if !field.is_color() {
        return None;
    }
    let colors = theme.colors();
    Some(match field {
        SettingField::ThemeTerminalBackground => colors.terminal_background,
        SettingField::ThemeTerminalForeground => colors.terminal_foreground,
        SettingField::ThemeSelectionBackground => colors.selection_background,
        SettingField::ThemeCursorForeground => colors.cursor_foreground,
        SettingField::ThemeCursorBackground => colors.cursor_background,
        SettingField::ThemeInactiveCursor => colors.inactive_cursor,
        SettingField::ThemeInverseForeground => colors.inverse_foreground,
        SettingField::ThemeInverseBackground => colors.inverse_background,
        SettingField::ThemePaneBackground => colors.pane_background,
        SettingField::ThemeActivePaneBorder => colors.active_pane_border,
        SettingField::ThemeInactivePaneBorder => colors.inactive_pane_border,
        SettingField::ThemeChromeBackground => colors.chrome_background,
        SettingField::ThemeTabActiveBackground => colors.tab_active_background,
        SettingField::ThemeTabInactiveBackground => colors.tab_inactive_background,
        SettingField::ThemeTabAddBackground => colors.tab_add_background,
        SettingField::ThemeUiForeground => colors.ui_foreground,
        SettingField::ThemeSidebarBackground => colors.sidebar_background,
        SettingField::ThemeSidebarConnectionBackground => colors.sidebar_connection_background,
        SettingField::ThemeSidebarConnectionActiveBackground => {
            colors.sidebar_connection_active_background
        }
        SettingField::ThemeSidebarWorkspaceBackground => colors.sidebar_workspace_background,
        SettingField::ThemeSidebarAgentBackground => colors.sidebar_agent_background,
        SettingField::ThemeSidebarWorkspaceActiveBackground => {
            colors.sidebar_workspace_active_background
        }
        SettingField::ThemeSidebarAgentActiveBackground => colors.sidebar_agent_active_background,
        SettingField::ThemeSidebarDragIndicator => colors.sidebar_drag_indicator,
        SettingField::AgentColor(kind) => colors.agent_color(kind),
        _ => return None,
    })
}
