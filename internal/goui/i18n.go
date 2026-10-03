package goui

import (
	"fmt"
	"strings"
)

// UI text is localized on the client. Model names and terminal output remain
// user content; automation keys and protocol identifiers never depend on locale.
var chineseText = map[string]string{
	"Font and text": "字体与文字", "History and memory": "历史与内存", "Links and downloads": "链接与下载", "Language and interface": "语言与界面", "Tabs": "标签页", "Terminal panes": "终端窗格", "Agents": "Agent", "Workspaces": "工作区", "Sidebar layout": "侧栏布局", "Window appearance": "窗口外观",
	"Connections and workspaces": "连接与工作区", "Panes": "窗格", "Windows and settings": "窗口与设置", "Terminal input": "终端输入", "Agent colors": "Agent 颜色", "ANSI palette": "ANSI 调色板", "Connection colors": "连接颜色", "Workspace colors": "工作区颜色", "Pane colors": "窗格颜色", "Tab colors": "标签页颜色", "Terminal colors": "终端颜色", "Interface colors": "界面颜色", "Minimum terminal size": "最小终端尺寸", "Initial terminal size": "初始终端尺寸", "Startup behavior": "启动行为", "Shell": "命令解释器", "Local server": "本地服务", "Terminal behavior": "终端行为",
	"Shell arguments: unfinished quote or escape": "Shell 参数：引号或转义未完成", "settings must be a JSON object": "设置必须是 JSON 对象", "settings overrides must be an object": "设置覆盖项必须是 JSON 对象",
	"Enter an SSH destination": "请输入 SSH 目标",
	"Saving…":                  "正在保存…", "Defaults": "默认值", "About Water": "关于 Water", "About Water Dev": "关于 Water Dev", "Hide Water Dev": "隐藏 Water Dev", "Services": "服务", "Enter Full Screen": "进入全屏", "Exit Full Screen": "退出全屏", "Bring All to Front": "全部置于最前",
	"Settings": "设置", "Terminal": "终端", "UI": "界面", "Shortcuts": "快捷键", "Theme": "主题", "Startup": "启动",
	"Save": "保存", "Cancel": "取消", "Reset defaults": "恢复默认", "Connect": "连接", "Download": "下载", "Rename": "重命名",
	"Connect to a remote": "连接远程主机", "SSH destination": "SSH 目标", "Connecting…": "正在连接…", "Download this file?": "下载此文件？",
	"+  Connect remote": "+  连接远程主机", "+  New workspace": "+  新建工作区", "+ Connect remote": "+ 连接远程主机", "+ New workspace": "+ 新建工作区",
	"Local": "本地", "Offline": "离线", "disconnected": "已断开", "connected": "已连接", "connecting": "正在连接", "running": "运行中", "idle": "空闲",
	"Creating workspace…": "正在创建工作区…", "Empty pane": "空窗格", "Attaching terminal…": "正在连接终端…",
	"New windows": "新窗口生效", "Restart": "重启后生效", "Applies immediately": "立即生效",
	"Enter a name": "请输入名称", "Rename workspace": "重命名工作区", "Rename tab": "重命名标签页",
	"Hide Water": "隐藏 Water", "Hide Others": "隐藏其他应用", "Show All": "显示全部", "Window": "窗口", "Minimize": "最小化", "Zoom": "缩放",
	"Quit GUI": "退出界面", "Quit GUI and Local Server": "退出界面与本地服务", "Show Water": "显示 Water",
	"Settings path is unavailable": "设置文件路径不可用", "Another connection is saving settings": "另一个连接正在保存设置",
	"Settings changed in another connection. Cancel and reopen to reload them.": "设置已在另一个连接中更改。请取消并重新打开设置。",
	"Choose English or Simplified Chinese":                                      "请选择英文或简体中文",
	"Connection closed: %s":                                                     "连接已关闭：%s", "Server shutdown failed: %s": "服务关闭失败：%s", "New window failed: %s": "新窗口创建失败：%s",
	"Font unavailable: %s. Using Go Mono.": "字体 %s 不可用，正在使用 Go Mono。",
	"%s: use #RRGGBB":                      "%s：请使用 #RRGGBB 格式", "%s: invalid color": "%s：颜色无效",
	"%s: invalid shortcut": "%s：快捷键无效", "%s: enter a whole number": "%s：请输入整数", "%s: enter a positive number": "%s：请输入正数",
	"Switch Tab: use a modifier followed by #, such as cmd-#":             "切换标签页：请使用修饰键加 #，例如 cmd-#",
	"Shortcut conflict: %s and %s":                                        "快捷键冲突：%s 与 %s",
	"Appearance and shortcuts apply now. Other settings require restart.": "外观与快捷键立即生效，其他设置需重启。",
}

var chineseFields = map[string]string{
	"TabFontSize": "标签页字号", "TabMaxTitleLength": "标签名称最大字符数",
	"Language": "界面语言", "DefaultCWD": "默认工作目录", "ControlSocket": "控制套接字", "InitialWorkspace": "启动时创建工作区", "InitialTerminal": "启动时创建终端",
	"WindowColumns": "启动窗口终端列数", "WindowRows": "启动窗口终端行数", "WindowMinColumns": "窗口最小终端列数", "WindowMinRows": "窗口最小终端行数",
	"Detached": "后台服务", "AutoStart": "自动启动服务", "DetachOnQuit": "退出后保留服务", "SocketPath": "服务套接字路径", "Program": "Shell 程序", "Args": "Shell 参数",
	"MouseReporting": "鼠标事件上报", "BracketedPaste": "括号粘贴模式", "Selection": "文本选择",
	"DefaultColumns": "新终端列数", "DefaultLines": "新终端行数", "ScrollbackLines": "滚动历史行数", "InactiveScrollbackLines": "非活动终端历史行数",
	"MaxTotalScrollbackBytes": "历史内存上限（字节）", "ReplayHistoryBytes": "重连历史容量（字节）", "FontFamily": "字体", "FontSize": "字号", "LineHeight": "行高",
	"Ligatures": "字体连字", "Hyperlinks": "超链接", "HyperlinkCommandClick": "按 Command 点击超链接", "RemoteHyperlinkAutoDownload": "自动下载远程链接文件", "HyperlinkDownloadDirectory": "链接文件下载目录",
	"SidebarVisible": "显示侧栏", "SidebarShowAgentCount": "显示 Agent 数量", "DimInactivePanes": "调暗非活动窗格", "TabBarVerticalWheelScroll": "标签栏支持纵向滚轮",
	"WorkspaceNavigationAcrossHosts": "跨主机切换工作区", "SidebarMinWidth": "侧栏最小宽度", "SidebarMaxWidth": "侧栏最大宽度", "SidebarResizeHandleWidth": "侧栏缩放手柄宽度",
	"TitlebarHeight": "标题栏高度", "TabHeight": "标签页高度", "SidebarHeaderHeight": "工作区行高", "WindowPadding": "窗口内边距", "SidebarSurfaceMargin": "侧栏外边距",
	"SidebarAgentRowGap": "Agent 行间距", "SidebarAgentPadding": "Agent 列表内边距", "SidebarHostHeaderHeight": "主机标题高度", "SidebarHostWorkspaceGap": "主机与工作区间距", "SidebarAgentRowWidth": "Agent 行宽比例",
	"SidebarWorkspaceRowPadding": "工作区行内边距", "SidebarAgentRowPadding": "Agent 行内边距", "SidebarAgentRowHeight": "Agent 行高", "SidebarWorkspaceGap": "工作区间距",
	"SidebarMargin": "侧栏边距", "SidebarCardGap": "侧栏卡片间距", "SidebarCardPadding": "侧栏卡片内边距", "SidebarRowPadding": "主机行内边距",
	"TitlebarPadding": "标题栏内边距", "TitlebarGap": "标题栏控件间距", "TabGap": "标签页间距", "TabPadding": "标签页内边距", "WindowCornerRadius": "窗口圆角",
	"SidebarCardRadius": "侧栏卡片圆角", "SidebarWorkspaceRadius": "工作区圆角", "SidebarWidth": "侧栏宽度", "PanePadding": "窗格内边距", "PaneMargin": "窗格间距", "PaneCornerRadius": "窗格圆角", "PaneDividerWidth": "窗格分隔线宽度", "UIFontSize": "界面字号", "UIFontFamily": "界面字体",
	"NewWindow": "新建窗口", "ConnectRemote": "连接远程主机", "RenameWorkspace": "重命名工作区", "RenameTab": "重命名标签页", "HideWindow": "隐藏窗口", "MinimizeWindow": "最小化窗口", "IgnoreQuit": "忽略退出快捷键", "SwitchTab": "切换标签页",
	"NextWorkspace": "下一个工作区", "PreviousWorkspace": "上一个工作区", "OpenSettings": "打开设置", "SplitRight": "向右分屏", "SplitDown": "向下分屏", "NextTab": "下一个标签页", "PreviousTab": "上一个标签页", "ToggleSidebar": "切换侧栏", "NewTerminalTab": "新建终端标签页", "NewWorkspace": "新建工作区", "ClosePane": "关闭窗格", "PromotePaneToTab": "将窗格提升为标签页",
	"FocusLeft": "聚焦左侧窗格", "FocusRight": "聚焦右侧窗格", "FocusUp": "聚焦上方窗格", "FocusDown": "聚焦下方窗格", "Paste": "粘贴", "CopyOrInterrupt": "复制或中断", "EOF": "输入结束符", "ScrollPageUp": "向上翻页", "ScrollPageDown": "向下翻页",
	"CursorForeground": "光标文字颜色", "SidebarConnectionActiveBorder": "活动连接边框颜色", "SidebarConnectionInactiveBorder": "非活动连接边框颜色", "SidebarConnectionOfflineColor": "离线连接文字颜色", "SidebarConnectionOfflineBorder": "离线连接边框颜色",
	"SidebarAgentBackground": "Agent 背景颜色", "SidebarAgentActiveBackground": "活动 Agent 背景颜色", "SidebarDragIndicatorColor": "侧栏拖动指示颜色", "SelectionBackground": "选中文字背景颜色", "InactiveCursor": "非活动光标颜色", "InverseForeground": "反色文字颜色", "InverseBackground": "反色背景颜色",
	"PaneBackground": "窗格背景颜色", "ActivePaneBorder": "活动窗格边框颜色", "InactivePaneBorder": "非活动窗格边框颜色", "AccentForeground": "强调文字颜色", "TabActiveBackground": "活动标签页背景颜色", "TabInactiveBackground": "非活动标签页背景颜色", "TabAddBackground": "新建标签按钮背景颜色",
	"SidebarConnectionBackground": "连接背景颜色", "SidebarConnectionActiveBackground": "活动连接背景颜色", "SidebarWorkspaceBackground": "工作区背景颜色", "SidebarWorkspaceActiveBackground": "活动工作区背景颜色",
	"TerminalBackground": "终端背景颜色", "TerminalForeground": "终端文字颜色", "CursorBackground": "光标背景颜色", "ChromeBackground": "界面背景颜色", "Accent": "强调颜色", "UIForeground": "界面文字颜色", "SidebarBackground": "侧栏背景颜色",
}

func ctext(language, value string) string {
	if language == "zh-Hans" {
		if translated, ok := chineseText[value]; ok {
			return translated
		}
	}
	return value
}
func (c *WorkspaceClient) language() string {
	if c.settings.visible {
		return c.settings.draft.UI.Language
	}
	return c.currentConfig().UI.Language
}
func (c *WorkspaceClient) tr(value string) string { return ctext(c.language(), value) }
func (c *WorkspaceClient) trf(value string, args ...any) string {
	return fmt.Sprintf(c.tr(value), args...)
}
func legacyPixelField(name string) bool {
	return name == "WindowWidth" || name == "WindowHeight" || name == "WindowMinWidth" || name == "WindowMinHeight"
}
func localizedField(language string, f settingsField) string {
	if language != "zh-Hans" {
		return f.label
	}
	if value, ok := chineseFields[f.name]; ok {
		return value
	}
	if strings.HasPrefix(f.name, "AgentColors.") {
		return strings.TrimSuffix(f.label, " color") + " 颜色"
	}
	if strings.HasPrefix(f.label, "ANSI color ") {
		return "ANSI 颜色 " + strings.TrimPrefix(f.label, "ANSI color ")
	}
	return f.label
}
