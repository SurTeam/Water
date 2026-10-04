package goconfig

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

const (
	DefaultColumns                 = 80
	DefaultLines                   = 24
	DefaultScrollbackLines         = 2000
	DefaultInactiveScrollbackLines = 500
	DefaultMaxTotalScrollbackBytes = 64 * 1024 * 1024
	DefaultReplayHistoryBytes      = 8 * 1024 * 1024
	MinReplayHistoryBytes          = 1 * 1024 * 1024
	MaxReplayHistoryBytes          = 8 * 1024 * 1024
	MaxScrollbackLines             = 10_000
)

type AppConfig struct {
	Startup   StartupConfig  `json:"startup"`
	Server    ServerConfig   `json:"server"`
	Shell     ShellConfig    `json:"shell"`
	Features  FeatureConfig  `json:"features"`
	Terminal  TerminalConfig `json:"terminal"`
	UI        UIConfig       `json:"ui"`
	Shortcuts ShortcutConfig `json:"shortcuts"`
	Theme     ThemeConfig    `json:"theme"`
}

type StartupConfig struct {
	DefaultCWD       *string `json:"default_cwd"`
	ControlSocket    *string `json:"control_socket"`
	InitialWorkspace bool    `json:"initial_workspace"`
	InitialTerminal  bool    `json:"initial_terminal"`
	WindowColumns    int     `json:"window_columns"`
	WindowRows       int     `json:"window_rows"`
	WindowMinColumns int     `json:"window_min_columns"`
	WindowMinRows    int     `json:"window_min_rows"`
	// Deprecated pixel fields are accepted from older configs and cleared on save.
	WindowWidth     float32 `json:"window_width,omitempty"`
	WindowHeight    float32 `json:"window_height,omitempty"`
	WindowMinWidth  float32 `json:"window_min_width,omitempty"`
	WindowMinHeight float32 `json:"window_min_height,omitempty"`
}

type ServerConfig struct {
	Detached     bool    `json:"detached"`
	AutoStart    bool    `json:"auto_start"`
	DetachOnQuit bool    `json:"detach_on_quit"`
	SocketPath   *string `json:"socket_path"`
}

type ShellConfig struct {
	Program string   `json:"program"`
	Args    []string `json:"args"`
}

type FeatureConfig struct {
	MouseReporting bool `json:"mouse_reporting"`
	BracketedPaste bool `json:"bracketed_paste"`
	Selection      bool `json:"selection"`
}

type TerminalConfig struct {
	CursorStyle string `json:"cursor_style"`
	CursorBlink bool   `json:"cursor_blink"`
	// Legacy terminal size aliases mirror the canonical startup grid.
	DefaultColumns              int     `json:"default_columns,omitempty"`
	DefaultLines                int     `json:"default_lines,omitempty"`
	ScrollbackLines             int     `json:"scrollback_lines"`
	InactiveScrollbackLines     int     `json:"inactive_scrollback_lines"`
	MaxTotalScrollbackBytes     int     `json:"max_total_scrollback_bytes"`
	ReplayHistoryBytes          int     `json:"replay_history_bytes"`
	FontFamily                  string  `json:"font_family"`
	FontSize                    float32 `json:"font_size"`
	LineHeight                  float32 `json:"line_height"`
	Ligatures                   bool    `json:"ligatures"`
	Hyperlinks                  bool    `json:"hyperlinks"`
	HyperlinkCommandClick       bool    `json:"hyperlink_command_click"`
	RemoteHyperlinkAutoDownload bool    `json:"remote_hyperlink_auto_download"`
	HyperlinkDownloadDirectory  string  `json:"hyperlink_download_directory"`
}

type UIConfig struct {
	SystemNotifications            bool    `json:"system_notifications"`
	SidebarAgentMode               string  `json:"sidebar_agent_mode"`
	SidebarHostFontSize            float32 `json:"sidebar_host_font_size"`
	SidebarWorkspaceFontSize       float32 `json:"sidebar_workspace_font_size"`
	SidebarAgentFontSize           float32 `json:"sidebar_agent_font_size"`
	SidebarRemoteButtonHeight      float32 `json:"sidebar_remote_button_height"`
	SidebarRemoteButtonFontSize    float32 `json:"sidebar_remote_button_font_size"`
	SidebarWorkspaceButtonHeight   float32 `json:"sidebar_workspace_button_height"`
	SidebarWorkspaceButtonFontSize float32 `json:"sidebar_workspace_button_font_size"`
	SidebarHostAlignment           string  `json:"sidebar_host_alignment"`
	SidebarWorkspaceAlignment      string  `json:"sidebar_workspace_alignment"`
	SidebarAgentAlignment          string  `json:"sidebar_agent_alignment"`
	SidebarHostRowWidth            float32 `json:"sidebar_host_row_width"`
	SidebarWorkspaceRowWidth       float32 `json:"sidebar_workspace_row_width"`
	Language                       string  `json:"language"`
	SidebarVisible                 bool    `json:"sidebar_visible"`
	SidebarShowAgentCount          bool    `json:"sidebar_show_agent_count"`
	DimInactivePanes               bool    `json:"dim_inactive_panes"`
	TabBarVerticalWheelScroll      bool    `json:"tab_bar_vertical_wheel_scroll"`
	WorkspaceNavigationAcrossHosts bool    `json:"workspace_navigation_across_hosts"`
	SidebarMinWidth                float32 `json:"sidebar_min_width"`
	SidebarMaxWidth                float32 `json:"sidebar_max_width"`
	SidebarResizeHandleWidth       float32 `json:"sidebar_resize_handle_width"`
	TitlebarHeight                 float32 `json:"titlebar_height"`
	TabHeight                      float32 `json:"tab_height"`
	SidebarHeaderHeight            float32 `json:"sidebar_header_height"`
	WindowPadding                  float32 `json:"window_padding"`
	SidebarSurfaceMargin           float32 `json:"sidebar_surface_margin"`
	SidebarAgentRowGap             float32 `json:"sidebar_agent_row_gap"`
	SidebarAgentPadding            float32 `json:"sidebar_agent_padding"`
	SidebarHostHeaderHeight        float32 `json:"sidebar_host_header_height"`
	SidebarHostWorkspaceGap        float32 `json:"sidebar_host_workspace_gap"`
	SidebarAgentRowWidth           float32 `json:"sidebar_agent_row_width"`
	SidebarWorkspaceRowPadding     float32 `json:"sidebar_workspace_row_padding"`
	SidebarAgentRowPadding         float32 `json:"sidebar_agent_row_padding"`
	SidebarAgentRowHeight          float32 `json:"sidebar_agent_row_height"`
	SidebarWorkspaceGap            float32 `json:"sidebar_workspace_gap"`
	SidebarMargin                  float32 `json:"sidebar_margin"`
	SidebarCardGap                 float32 `json:"sidebar_card_gap"`
	SidebarCardPadding             float32 `json:"sidebar_card_padding"`
	SidebarRowPadding              float32 `json:"sidebar_row_padding"`
	TitlebarPadding                float32 `json:"titlebar_padding"`
	TitlebarGap                    float32 `json:"titlebar_gap"`
	TabGap                         float32 `json:"tab_gap"`
	TabPadding                     float32 `json:"tab_padding"`
	TabFontSize                    float32 `json:"tab_font_size"`
	TabMaxTitleLength              int     `json:"tab_max_title_length"`
	WindowCornerRadius             float32 `json:"window_corner_radius"`
	SidebarCardRadius              float32 `json:"sidebar_card_radius"`
	SidebarWorkspaceRadius         float32 `json:"sidebar_workspace_radius"`
	SidebarWidth                   float32 `json:"sidebar_width"`
	PanePadding                    float32 `json:"pane_padding"`
	PaneMargin                     float32 `json:"pane_margin"`
	PaneCornerRadius               float32 `json:"pane_corner_radius"`
	PaneDividerWidth               float32 `json:"pane_divider_width"`
	UIFontSize                     float32 `json:"font_size"`
	UIFontFamily                   string  `json:"font_family"`
}

func (u *UIConfig) UnmarshalJSON(data []byte) error {
	type plainUI UIConfig
	value := plainUI(*u)
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	var aliases struct {
		Legacy    *float32 `json:"ui_font_size"`
		Canonical *float32 `json:"font_size"`
	}
	if err := json.Unmarshal(data, &aliases); err != nil {
		return err
	}
	if aliases.Canonical == nil && aliases.Legacy != nil {
		value.UIFontSize = *aliases.Legacy
	}
	*u = UIConfig(value)
	return nil
}

type ShortcutConfig struct {
	NewWindow         string `json:"new_window"`
	ConnectRemote     string `json:"connect_remote"`
	RenameWorkspace   string `json:"rename_workspace"`
	RenameTab         string `json:"rename_tab"`
	HideWindow        string `json:"hide_window"`
	MinimizeWindow    string `json:"minimize_window"`
	IgnoreQuit        string `json:"ignore_quit"`
	SwitchTab         string `json:"switch_tab"`
	NextWorkspace     string `json:"next_workspace"`
	PreviousWorkspace string `json:"previous_workspace"`
	OpenSettings      string `json:"open_settings"`
	SplitRight        string `json:"split_right"`
	SplitDown         string `json:"split_down"`
	NextTab           string `json:"next_tab"`
	PreviousTab       string `json:"previous_tab"`
	ToggleSidebar     string `json:"toggle_sidebar"`
	NewTerminalTab    string `json:"new_terminal_tab"`
	NewWorkspace      string `json:"new_workspace"`
	ClosePane         string `json:"close_pane"`
	PromotePaneToTab  string `json:"promote_pane_to_tab"`
	FocusLeft         string `json:"focus_left"`
	FocusRight        string `json:"focus_right"`
	FocusUp           string `json:"focus_up"`
	FocusDown         string `json:"focus_down"`
	Paste             string `json:"paste"`
	CopyOrInterrupt   string `json:"copy_or_interrupt"`
	EOF               string `json:"eof"`
	ScrollPageUp      string `json:"scroll_page_up"`
	ScrollPageDown    string `json:"scroll_page_down"`
}

type ThemeConfig struct {
	CursorForeground                  string            `json:"cursor_foreground"`
	SidebarConnectionActiveBorder     string            `json:"sidebar_connection_active_border"`
	SidebarConnectionInactiveBorder   string            `json:"sidebar_connection_inactive_border"`
	SidebarConnectionOfflineColor     string            `json:"sidebar_connection_offline_color"`
	SidebarConnectionOfflineBorder    string            `json:"sidebar_connection_offline_border"`
	SidebarAgentBackground            string            `json:"sidebar_agent_background"`
	SidebarAgentActiveBackground      string            `json:"sidebar_agent_active_background"`
	SidebarDragIndicatorColor         string            `json:"sidebar_drag_indicator_color"`
	AgentColors                       map[string]string `json:"agent_colors"`
	SelectionBackground               string            `json:"selection_background"`
	InactiveCursor                    string            `json:"inactive_cursor"`
	InverseForeground                 string            `json:"inverse_foreground"`
	InverseBackground                 string            `json:"inverse_background"`
	PaneBackground                    string            `json:"pane_background"`
	ActivePaneBorder                  string            `json:"active_pane_border"`
	InactivePaneBorder                string            `json:"inactive_pane_border"`
	AccentForeground                  string            `json:"accent_foreground"`
	TabActiveBackground               string            `json:"tab_active_background"`
	TabInactiveBackground             string            `json:"tab_inactive_background"`
	TabAddBackground                  string            `json:"tab_add_background"`
	SidebarConnectionBackground       string            `json:"sidebar_connection_background"`
	SidebarConnectionActiveBackground string            `json:"sidebar_connection_active_background"`
	SidebarWorkspaceBackground        string            `json:"sidebar_workspace_background"`
	SidebarWorkspaceActiveBackground  string            `json:"sidebar_workspace_active_background"`
	ANSIColors                        []string          `json:"ansi_colors,omitempty"`
	TerminalBackground                string            `json:"terminal_background"`
	TerminalForeground                string            `json:"terminal_foreground"`
	CursorBackground                  string            `json:"cursor_background"`
	ChromeBackground                  string            `json:"chrome_background"`
	Accent                            string            `json:"accent"`
	UIForeground                      string            `json:"ui_foreground"`
	SidebarBackground                 string            `json:"sidebar_background"`
}

func Default() AppConfig {
	cwd := "~"
	return AppConfig{
		Startup: StartupConfig{
			DefaultCWD:       &cwd,
			InitialWorkspace: true,
			InitialTerminal:  true,
			WindowColumns:    80,
			WindowRows:       24,
			WindowMinColumns: 40,
			WindowMinRows:    10,
		},
		Server:   ServerConfig{Detached: true, AutoStart: true, DetachOnQuit: true},
		Shell:    ShellConfig{Program: DefaultShellProgram()},
		Features: FeatureConfig{MouseReporting: true, BracketedPaste: true, Selection: true},
		Terminal: TerminalConfig{
			CursorStyle:                "block",
			CursorBlink:                true,
			DefaultColumns:             DefaultColumns,
			DefaultLines:               DefaultLines,
			ScrollbackLines:            DefaultScrollbackLines,
			InactiveScrollbackLines:    DefaultInactiveScrollbackLines,
			MaxTotalScrollbackBytes:    DefaultMaxTotalScrollbackBytes,
			ReplayHistoryBytes:         DefaultReplayHistoryBytes,
			FontFamily:                 "Sarasa Term SC",
			FontSize:                   16,
			LineHeight:                 18,
			Ligatures:                  true,
			Hyperlinks:                 true,
			HyperlinkDownloadDirectory: "~/Downloads/Water",
		},
		UI: UIConfig{
			SidebarAgentMode: "workspace", SidebarHostFontSize: 12, SidebarWorkspaceFontSize: 12, SidebarAgentFontSize: 12,
			SidebarRemoteButtonHeight: 30, SidebarRemoteButtonFontSize: 12, SidebarWorkspaceButtonHeight: 30, SidebarWorkspaceButtonFontSize: 12,
			SidebarHostAlignment: "center", SidebarWorkspaceAlignment: "center", SidebarAgentAlignment: "center",
			SidebarHostRowWidth: 1, SidebarWorkspaceRowWidth: 1,
			Language:       "en",
			SidebarVisible: true, SidebarShowAgentCount: true, DimInactivePanes: true,
			SidebarMinWidth: 170, SidebarMaxWidth: 420, SidebarResizeHandleWidth: 6,
			TitlebarHeight: 36, TabHeight: 24, SidebarHeaderHeight: 24,
			WindowPadding: 10, SidebarMargin: 6, SidebarCardGap: 8, SidebarCardPadding: 8,
			SidebarRowPadding: 12, TitlebarPadding: 10, TitlebarGap: 8, TabGap: 5, TabPadding: 12,
			WindowCornerRadius: 16, SidebarCardRadius: 14, SidebarWorkspaceRadius: 8,
			SidebarAgentRowGap: 3, SidebarAgentPadding: 6, SidebarHostHeaderHeight: 28,
			SidebarHostWorkspaceGap: 8, SidebarAgentRowWidth: .8,
			SidebarWorkspaceRowPadding: 10, SidebarAgentRowPadding: 10, SidebarAgentRowHeight: 28,
			SidebarWorkspaceGap: 4,
			SidebarWidth:        200,
			PanePadding:         2,
			PaneMargin:          5,
			PaneCornerRadius:    6,
			PaneDividerWidth:    2,
			UIFontSize:          12,
			TabFontSize:         12,
			TabMaxTitleLength:   32,
		},
		Shortcuts: ShortcutConfig{
			NewWindow: "cmd-n", ConnectRemote: "cmd-shift-k", RenameWorkspace: "cmd-shift-e", RenameTab: "cmd-shift-t",
			HideWindow: "cmd-w", MinimizeWindow: "cmd-m", IgnoreQuit: "cmd-q",
			SwitchTab: "cmd-#", NextWorkspace: "ctrl-tab", PreviousWorkspace: "ctrl-shift-tab",
			OpenSettings: "cmd-,", SplitRight: "cmd-\\", SplitDown: "cmd--",
			NextTab: "cmd-]", PreviousTab: "cmd-[", ToggleSidebar: "cmd-e",
			NewTerminalTab:   "cmd-t",
			NewWorkspace:     "cmd-shift-n",
			ClosePane:        "cmd-shift-w",
			PromotePaneToTab: "cmd-shift-enter",
			FocusLeft:        "cmd-h",
			FocusRight:       "cmd-l",
			FocusUp:          "cmd-k",
			FocusDown:        "cmd-j",
			Paste:            "cmd-v",
			CopyOrInterrupt:  "cmd-c",
			EOF:              "ctrl-d",
			ScrollPageUp:     "shift-pageup",
			ScrollPageDown:   "shift-pagedown",
		},
		Theme: ThemeConfig{
			CursorForeground: "#1e1e20", SidebarConnectionActiveBorder: "#45454a",
			SidebarConnectionInactiveBorder: "#303034", SidebarConnectionOfflineColor: "#ef7d83",
			SidebarConnectionOfflineBorder: "#70444a", SidebarAgentBackground: "#242426",
			SidebarAgentActiveBackground: "#353539", SidebarDragIndicatorColor: "#8eaeed",
			AgentColors:         DefaultAgentColors(),
			SelectionBackground: "#555555", InactiveCursor: "#555555",
			InverseForeground: "#2c2c2c", InverseBackground: "#e4e4e4",
			PaneBackground: "#1e1e20", ActivePaneBorder: "#45454a", InactivePaneBorder: "#303034",
			AccentForeground: "#ffffff", TabActiveBackground: "#414145", TabInactiveBackground: "#28282b", TabAddBackground: "#28282b",
			SidebarConnectionBackground: "#252528", SidebarConnectionActiveBackground: "#353539",
			SidebarWorkspaceBackground: "#252528", SidebarWorkspaceActiveBackground: "#353539",
			TerminalBackground: "#1e1e20",
			TerminalForeground: "#e4e4e4",
			CursorBackground:   "#e4e4e4",
			ChromeBackground:   "#28282b",
			Accent:             "#8eaeed",
			UIForeground:       "#e6eaea",
			SidebarBackground:  "#252528",
		},
	}.Normalized()
}

func DefaultShellProgram() string {
	if p := strings.TrimSpace(os.Getenv("WATER_SHELL")); p != "" {
		return p
	}
	for _, candidate := range []string{"/opt/homebrew/bin/zsh", "/usr/local/bin/zsh", "/bin/zsh"} {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return "/bin/sh"
}

func DefaultShellArgs(program string) []string {
	if filepath.Base(program) == "zsh" {
		return []string{"-l"}
	}
	return nil
}

func (c AppConfig) Normalized() AppConfig {
	agentColors := DefaultAgentColors()
	for kind, value := range c.Theme.AgentColors {
		agentColors[kind] = value
	}
	c.Theme.AgentColors = agentColors
	c.Shell.Program = strings.TrimSpace(c.Shell.Program)
	if c.Shell.Program == "" {
		c.Shell.Program = DefaultShellProgram()
	}
	if c.Shell.Args == nil {
		c.Shell.Args = DefaultShellArgs(c.Shell.Program)
	}
	c.Terminal.DefaultColumns = clamp(c.Terminal.DefaultColumns, 2, 512)
	c.Terminal.DefaultLines = clamp(c.Terminal.DefaultLines, 1, 256)
	c.Terminal.ScrollbackLines = clamp(c.Terminal.ScrollbackLines, 1, MaxScrollbackLines)
	c.Terminal.InactiveScrollbackLines = clamp(c.Terminal.InactiveScrollbackLines, 1, MaxScrollbackLines)
	if c.Terminal.InactiveScrollbackLines > c.Terminal.ScrollbackLines {
		c.Terminal.InactiveScrollbackLines = c.Terminal.ScrollbackLines
	}
	c.Terminal.ReplayHistoryBytes = clamp(c.Terminal.ReplayHistoryBytes, MinReplayHistoryBytes, MaxReplayHistoryBytes)
	c.Terminal.CursorStyle = strings.ToLower(strings.TrimSpace(c.Terminal.CursorStyle))
	switch c.Terminal.CursorStyle {
	case "block", "bar", "underline":
	default:
		c.Terminal.CursorStyle = "block"
	}
	if strings.TrimSpace(c.Terminal.FontFamily) == "" {
		c.Terminal.FontFamily = "Sarasa Term SC"
	}
	c.Terminal.FontSize = clampFloat(c.Terminal.FontSize, 8, 48)
	c.UI = c.UI.normalized()
	c.UI.PanePadding = clampFloat(c.UI.PanePadding, 0, 48)
	c.UI.PaneMargin = clampFloat(c.UI.PaneMargin, 0, 32)
	c.UI.PaneCornerRadius = clampFloat(c.UI.PaneCornerRadius, 0, 48)
	c.UI.PaneDividerWidth = clampFloat(c.UI.PaneDividerWidth, 1, 16)
	c.UI.UIFontSize = clampFloat(c.UI.UIFontSize, 6, 32)
	c.Terminal.LineHeight = clampFloat(c.Terminal.LineHeight, 8, 64)
	c.Startup.WindowMinColumns = min(512, max(2, c.Startup.WindowMinColumns))
	c.Startup.WindowMinRows = min(256, max(1, c.Startup.WindowMinRows))
	c.Startup.WindowColumns = min(512, max(c.Startup.WindowMinColumns, c.Startup.WindowColumns))
	c.Startup.WindowRows = min(256, max(c.Startup.WindowMinRows, c.Startup.WindowRows))
	c.Terminal.DefaultColumns = c.Startup.WindowColumns
	c.Terminal.DefaultLines = c.Startup.WindowRows
	c.Startup.WindowWidth, c.Startup.WindowHeight = 0, 0
	c.Startup.WindowMinWidth, c.Startup.WindowMinHeight = 0, 0
	switch strings.ToLower(strings.TrimSpace(c.UI.Language)) {
	case "zh", "zh-cn", "zh-hans":
		c.UI.Language = "zh-Hans"
	default:
		c.UI.Language = "en"
	}
	return c
}

func Load(path string) (AppConfig, error) {
	cfg := Default()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return AppConfig{}, err
	}

	var envelope struct {
		Overrides json.RawMessage `json:"overrides"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return AppConfig{}, err
	}
	target := data
	if len(envelope.Overrides) > 0 && string(envelope.Overrides) != "null" {
		target = envelope.Overrides
	}
	var shellOverride struct {
		Shell *struct {
			Program *string         `json:"program"`
			Args    json.RawMessage `json:"args"`
		} `json:"shell"`
	}
	if err := json.Unmarshal(target, &shellOverride); err != nil {
		return AppConfig{}, err
	}
	if err := json.Unmarshal(target, &cfg); err != nil {
		return AppConfig{}, err
	}
	var sizes struct {
		Startup struct {
			Columns *int `json:"window_columns"`
			Rows    *int `json:"window_rows"`
		} `json:"startup"`
		Terminal struct {
			Columns *int `json:"default_columns"`
			Rows    *int `json:"default_lines"`
		} `json:"terminal"`
	}
	if err := json.Unmarshal(target, &sizes); err != nil {
		return AppConfig{}, err
	}
	if sizes.Startup.Columns == nil && sizes.Terminal.Columns != nil {
		cfg.Startup.WindowColumns = *sizes.Terminal.Columns
	}
	if sizes.Startup.Rows == nil && sizes.Terminal.Rows != nil {
		cfg.Startup.WindowRows = *sizes.Terminal.Rows
	}
	if shellOverride.Shell != nil && shellOverride.Shell.Program != nil &&
		(len(shellOverride.Shell.Args) == 0 || string(shellOverride.Shell.Args) == "null") {
		cfg.Shell.Args = DefaultShellArgs(cfg.Shell.Program)
	}
	return cfg.Normalized(), nil
}

func LoadDefault(buildVariant string) (AppConfig, string, error) {
	path := os.Getenv("WATER_CONFIG")
	if path == "" {
		path = DefaultLoadPath(buildVariant)
	}
	cfg, err := Load(path)
	return cfg, path, err
}

func DefaultLoadPath(buildVariant string) string {
	native := DefaultPath(buildVariant)
	if info, err := os.Stat(native); err == nil && !info.IsDir() {
		return native
	}
	standard := StandardPath(buildVariant)
	if info, err := os.Stat(standard); err == nil && !info.IsDir() {
		return standard
	}
	return native
}

func DefaultPath(buildVariant string) string {
	dir := configDirName(buildVariant)
	home, _ := os.UserHomeDir()
	if runtime.GOOS == "darwin" && home != "" {
		return filepath.Join(home, "Library", "Application Support", dir, "config.json")
	}
	return StandardPath(buildVariant)
}

func StandardPath(buildVariant string) string {
	dir := configDirName(buildVariant)
	if base := os.Getenv("XDG_CONFIG_HOME"); base != "" {
		return filepath.Join(base, dir, "config.json")
	}
	home, _ := os.UserHomeDir()
	if home != "" {
		return filepath.Join(home, ".config", dir, "config.json")
	}
	return "water-config.json"
}

func (c AppConfig) DefaultCWD() string {
	if c.Startup.DefaultCWD == nil {
		return ""
	}
	value := strings.TrimSpace(*c.Startup.DefaultCWD)
	if value == "~" {
		home, _ := os.UserHomeDir()
		return home
	}
	if strings.HasPrefix(value, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, strings.TrimPrefix(value, "~/"))
	}
	return value
}

func configDirName(buildVariant string) string {
	if buildVariant == "release" {
		return "water"
	}
	return "water-dev"
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
func clampFloat(v, lo, hi float32) float32 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func ParseColor(value string, fallback uint32) uint32 {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "#")
	value = strings.TrimPrefix(value, "0x")
	value = strings.TrimPrefix(value, "0X")
	if len(value) == 3 {
		value = string([]byte{value[0], value[0], value[1], value[1], value[2], value[2]})
	}
	if len(value) != 6 {
		return fallback
	}
	parsed, err := strconv.ParseUint(value, 16, 32)
	if err != nil {
		return fallback
	}
	return uint32(parsed)
}
