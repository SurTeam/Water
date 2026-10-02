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
	Startup   StartupConfig   `json:"startup"`
	Server    ServerConfig    `json:"server"`
	Shell     ShellConfig     `json:"shell"`
	Features  FeatureConfig   `json:"features"`
	Terminal  TerminalConfig  `json:"terminal"`
	UI        UIConfig        `json:"ui"`
	Shortcuts ShortcutConfig  `json:"shortcuts"`
	Theme     ThemeConfig     `json:"theme"`
}

type StartupConfig struct {
	DefaultCWD       *string `json:"default_cwd"`
	ControlSocket    *string `json:"control_socket"`
	InitialWorkspace bool    `json:"initial_workspace"`
	InitialTerminal  bool    `json:"initial_terminal"`
	WindowWidth      float32 `json:"window_width"`
	WindowHeight     float32 `json:"window_height"`
	WindowMinWidth   float32 `json:"window_min_width"`
	WindowMinHeight  float32 `json:"window_min_height"`
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
	DefaultColumns             int    `json:"default_columns"`
	DefaultLines               int    `json:"default_lines"`
	ScrollbackLines            int    `json:"scrollback_lines"`
	InactiveScrollbackLines    int    `json:"inactive_scrollback_lines"`
	MaxTotalScrollbackBytes    int    `json:"max_total_scrollback_bytes"`
	ReplayHistoryBytes         int    `json:"replay_history_bytes"`
	FontFamily                 string `json:"font_family"`
	FontSize                   float32 `json:"font_size"`
	LineHeight                 float32 `json:"line_height"`
	Ligatures                  bool   `json:"ligatures"`
	Hyperlinks                 bool   `json:"hyperlinks"`
	HyperlinkCommandClick      bool   `json:"hyperlink_command_click"`
	RemoteHyperlinkAutoDownload bool  `json:"remote_hyperlink_auto_download"`
	HyperlinkDownloadDirectory string `json:"hyperlink_download_directory"`
}

type UIConfig struct {
	SidebarWidth  float32 `json:"sidebar_width"`
	PanePadding   float32 `json:"pane_padding"`
	PaneMargin    float32 `json:"pane_margin"`
	PaneCornerRadius float32 `json:"pane_corner_radius"`
	PaneDividerWidth float32 `json:"pane_divider_width"`
	UIFontSize    float32 `json:"ui_font_size"`
}

type ShortcutConfig struct {
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
	TerminalBackground string `json:"terminal_background"`
	TerminalForeground string `json:"terminal_foreground"`
	CursorBackground   string `json:"cursor_background"`
	ChromeBackground   string `json:"chrome_background"`
	Accent             string `json:"accent"`
	UIForeground       string `json:"ui_foreground"`
	SidebarBackground  string `json:"sidebar_background"`
}

func Default() AppConfig {
	cwd := "~"
	return AppConfig{
		Startup: StartupConfig{
			DefaultCWD: &cwd,
			InitialWorkspace: true,
			InitialTerminal: true,
			WindowWidth: 1100,
			WindowHeight: 760,
			WindowMinWidth: 400,
			WindowMinHeight: 260,
		},
		Server: ServerConfig{Detached:true, AutoStart:true, DetachOnQuit:true},
		Shell: ShellConfig{Program:DefaultShellProgram()},
		Features: FeatureConfig{MouseReporting:true, BracketedPaste:true, Selection:true},
		Terminal: TerminalConfig{
			DefaultColumns:DefaultColumns,
			DefaultLines:DefaultLines,
			ScrollbackLines:DefaultScrollbackLines,
			InactiveScrollbackLines:DefaultInactiveScrollbackLines,
			MaxTotalScrollbackBytes:DefaultMaxTotalScrollbackBytes,
			ReplayHistoryBytes:DefaultReplayHistoryBytes,
			FontFamily:"Sarasa Term SC",
			FontSize:16,
			LineHeight:18,
			Ligatures:true,
			Hyperlinks:true,
			HyperlinkDownloadDirectory:"~/Downloads/Water",
		},
		UI: UIConfig{
			SidebarWidth:200,
			PanePadding:10,
			PaneMargin:5,
			PaneCornerRadius:14,
			PaneDividerWidth:2,
			UIFontSize:14,
		},
		Shortcuts: ShortcutConfig{
			NewTerminalTab:"cmd-t",
			NewWorkspace:"cmd-shift-n",
			ClosePane:"cmd-shift-w",
			PromotePaneToTab:"cmd-shift-enter",
			FocusLeft:"cmd-h",
			FocusRight:"cmd-l",
			FocusUp:"cmd-k",
			FocusDown:"cmd-j",
			Paste:"cmd-v",
			CopyOrInterrupt:"cmd-c",
			EOF:"cmd-d",
			ScrollPageUp:"shift-pageup",
			ScrollPageDown:"shift-pagedown",
		},
		Theme: ThemeConfig{
			TerminalBackground:"#2c2c2c",
			TerminalForeground:"#e4e4e4",
			CursorBackground:"#e4e4e4",
			ChromeBackground:"#121416",
			Accent:"#72d6ab",
			UIForeground:"#e6eaea",
			SidebarBackground:"#171a1c",
		},
	}.Normalized()
}

func DefaultShellProgram() string {
	if p:=strings.TrimSpace(os.Getenv("WATER_SHELL"));p!="" {
		return p
	}
	for _,candidate:=range []string{"/opt/homebrew/bin/zsh","/usr/local/bin/zsh","/bin/zsh"} {
		if info,err:=os.Stat(candidate);err==nil && !info.IsDir() {
			return candidate
		}
	}
	return "/bin/sh"
}

func DefaultShellArgs(program string) []string {
	if filepath.Base(program)=="zsh" {
		return []string{"-l"}
	}
	return nil
}

func (c AppConfig) Normalized() AppConfig {
	c.Shell.Program=strings.TrimSpace(c.Shell.Program)
	if c.Shell.Program=="" {
		c.Shell.Program=DefaultShellProgram()
	}
	if c.Shell.Args==nil {
		c.Shell.Args=DefaultShellArgs(c.Shell.Program)
	}
	c.Terminal.DefaultColumns=clamp(c.Terminal.DefaultColumns,2,512)
	c.Terminal.DefaultLines=clamp(c.Terminal.DefaultLines,1,256)
	c.Terminal.ScrollbackLines=clamp(c.Terminal.ScrollbackLines,1,MaxScrollbackLines)
	c.Terminal.InactiveScrollbackLines=clamp(c.Terminal.InactiveScrollbackLines,1,MaxScrollbackLines)
	if c.Terminal.InactiveScrollbackLines>c.Terminal.ScrollbackLines {
		c.Terminal.InactiveScrollbackLines=c.Terminal.ScrollbackLines
	}
	c.Terminal.ReplayHistoryBytes=clamp(c.Terminal.ReplayHistoryBytes,MinReplayHistoryBytes,MaxReplayHistoryBytes)
	if strings.TrimSpace(c.Terminal.FontFamily)=="" {
		c.Terminal.FontFamily="Sarasa Term SC"
	}
	c.Terminal.FontSize=clampFloat(c.Terminal.FontSize,8,48)
	c.Terminal.LineHeight=clampFloat(c.Terminal.LineHeight,8,64)
	c.Startup.WindowMinWidth=clampFloat(c.Startup.WindowMinWidth,200,4096)
	c.Startup.WindowMinHeight=clampFloat(c.Startup.WindowMinHeight,120,4096)
	c.Startup.WindowWidth=clampFloat(c.Startup.WindowWidth,480,4096)
	if c.Startup.WindowWidth<c.Startup.WindowMinWidth{c.Startup.WindowWidth=c.Startup.WindowMinWidth}
	c.Startup.WindowHeight=clampFloat(c.Startup.WindowHeight,320,4096)
	if c.Startup.WindowHeight<c.Startup.WindowMinHeight{c.Startup.WindowHeight=c.Startup.WindowMinHeight}
	return c
}

func Load(path string)(AppConfig,error){
	cfg:=Default()
	data,err:=os.ReadFile(path)
	if errors.Is(err,os.ErrNotExist){return cfg,nil}
	if err!=nil{return AppConfig{},err}

	var envelope struct{Overrides json.RawMessage `json:"overrides"`}
	if err:=json.Unmarshal(data,&envelope);err!=nil{return AppConfig{},err}
	target:=data
	if len(envelope.Overrides)>0 && string(envelope.Overrides)!="null" {
		target=envelope.Overrides
	}
	if err:=json.Unmarshal(target,&cfg);err!=nil{return AppConfig{},err}
	return cfg.Normalized(),nil
}

func LoadDefault(buildVariant string)(AppConfig,string,error){
	path:=os.Getenv("WATER_CONFIG")
	if path=="" {
		path=DefaultLoadPath(buildVariant)
	}
	cfg,err:=Load(path)
	return cfg,path,err
}

func DefaultLoadPath(buildVariant string)string{
	native:=DefaultPath(buildVariant)
	if info,err:=os.Stat(native);err==nil&&!info.IsDir(){return native}
	standard:=StandardPath(buildVariant)
	if info,err:=os.Stat(standard);err==nil&&!info.IsDir(){return standard}
	return native
}

func DefaultPath(buildVariant string)string{
	dir:=configDirName(buildVariant)
	home,_:=os.UserHomeDir()
	if runtime.GOOS=="darwin" && home!="" {
		return filepath.Join(home,"Library","Application Support",dir,"config.json")
	}
	return StandardPath(buildVariant)
}

func StandardPath(buildVariant string)string{
	dir:=configDirName(buildVariant)
	if base:=os.Getenv("XDG_CONFIG_HOME");base!="" {
		return filepath.Join(base,dir,"config.json")
	}
	home,_:=os.UserHomeDir()
	if home!="" {
		return filepath.Join(home,".config",dir,"config.json")
	}
	return "water-config.json"
}

func (c AppConfig) DefaultCWD() string {
	if c.Startup.DefaultCWD==nil{return ""}
	value:=strings.TrimSpace(*c.Startup.DefaultCWD)
	if value=="~" {
		home,_:=os.UserHomeDir();return home
	}
	if strings.HasPrefix(value,"~/") {
		home,_:=os.UserHomeDir();return filepath.Join(home,strings.TrimPrefix(value,"~/"))
	}
	return value
}

func configDirName(buildVariant string)string{
	if buildVariant=="release"{return "water"}
	return "water-dev"
}

func clamp(v,lo,hi int)int{if v<lo{return lo};if v>hi{return hi};return v}
func clampFloat(v,lo,hi float32)float32{if v<lo{return lo};if v>hi{return hi};return v}


func ParseColor(value string,fallback uint32)uint32{
	value=strings.TrimSpace(value)
	value=strings.TrimPrefix(value,"#")
	value=strings.TrimPrefix(value,"0x")
	value=strings.TrimPrefix(value,"0X")
	if len(value)==3{
		value=string([]byte{value[0],value[0],value[1],value[1],value[2],value[2]})
	}
	if len(value)!=6{return fallback}
	parsed,err:=strconv.ParseUint(value,16,32)
	if err!=nil{return fallback}
	return uint32(parsed)
}
