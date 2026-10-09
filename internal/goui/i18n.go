package goui

import (
	"fmt"
	"strings"
)

// UI text is localized on the client. Model names and terminal output remain
// user content; automation keys and protocol identifiers never depend on locale.
var chineseText = map[string]string{
	"Upgrade this server to import local TLS files": "请升级当前服务端，以支持导入本机 TLS 文件",
	"Choose…": "选择文件…",
	"HTTPS needs both a PEM certificate chain (.pem/.crt) and its key.":                 "HTTPS 需同时提供 PEM 证书链（.pem/.crt）和匹配的私钥。",
	"Key: unencrypted PEM (.pem/.key), RSA / EC / PKCS#8; not .p12/.pfx.":               "私钥：未加密 PEM（.pem/.key），支持 RSA／EC／PKCS#8；不支持 .p12/.pfx。",
	"Choose both files locally; Apply imports them to this server.":                     "两份文件均从本机选择；点击应用后导入当前服务端（包括远程）。",
	"Choose PEM certificate / chain":                                                    "选择 PEM 证书／证书链",
	"Choose unencrypted PEM private key":                                                "选择未加密的 PEM 私钥",
	"Choose a file on this computer":                                                    "请从本机选择文件",
	"Select both files on this computer before applying":                                "请从本机选择证书和私钥两份文件后再应用",
	"File picker unavailable":                                                           "文件选择器不可用",
	"cannot open selected TLS file":                                                     "无法打开所选 TLS 文件",
	"select a regular PEM file":                                                         "请选择普通 PEM 文件",
	"TLS files must be at most 256 KiB each":                                            "每份 TLS 文件不能超过 256 KiB",
	"use PEM certificates and an unencrypted PEM private key":                           "请使用 PEM 证书和未加密的 PEM 私钥",
	"invalid TLS pair: use PEM certificates and a matching unencrypted PEM private key": "证书与私钥无效：请提供 PEM 证书和匹配的未加密 PEM 私钥",
	"HTTP service: enter a listen IP/hostname and a fixed port separately.":             "HTTP 服务：分别填写监听 IP／主机名和固定端口。",
	"Web service": "Web 服务", "On": "开启", "Off": "关闭",
	"Start Web with server": "随 server 启动 Web",
	"Manage browser access, pairing and devices in a separate window.": "在独立窗口中管理网页访问、扫码配对和设备。",
	"HTTP example: http://server-ip:8080. The saved port stays fixed.": "HTTP 示例：http://服务端IP:8080，保存后端口保持固定。",
	"Web server": "Web 服务", "Start Web": "启动 Web", "Stop Web": "停止 Web", "Web settings": "Web 设置", "Apply Web settings": "应用 Web 设置", "Pair device": "配对设备", "Cancel pairing": "取消配对", "Devices": "设备", "Paired browsers": "已配对浏览器", "No paired browsers": "没有已配对浏览器", "Revoke": "撤销", "Back": "返回", "Capabilities": "能力详情", "Capability · GUI / Server": "能力 · GUI / 服务端", "Expires at %s": "有效期至 %s", "Scan to connect directly to this server.": "扫码后直连此服务端。", "Pairing expired. Create a new invitation.": "配对已过期，请创建新邀请。", "Pairing invitation is no longer active": "配对邀请已使用或失效", "Web settings belong to this server. Stop Web before applying.": "这些设置属于当前服务端，应用前请先停止 Web。", "This server does not support Web access. Upgrade the server to enable it.": "当前服务端不支持 Web 接入，请升级服务端。", "Applies when Web service starts": "启动 Web 服务时生效",
	"Server": "服务", "Connection: %s": "连接：%s", "Server revision: %s": "服务构建：%s",
	"Capability": "能力", "Provided": "提供", "Required": "需要", "Unknown": "未知",
	"Refresh": "刷新", "Save layout": "保存布局", "Restart server": "重启服务", "Retry restore": "重试恢复",
	"Save layout and restart": "保存布局并重启", "Working…": "正在处理…",
	"GUI and server support each other":                                                        "GUI 与服务端能力相互兼容",
	"Server changed. Restart to apply the server update.":                                      "服务端有更新，重启后生效。",
	"Legacy server: capability and recovery support are unknown":                               "旧服务端的能力与恢复支持未知",
	"Legacy server cannot safely export current directories. Restart recovery is unavailable.": "旧服务端不能可靠导出当前目录，暂不支持自动重启恢复。",
	"Restart restores layout and directories. Running tasks will end.":                         "重启会恢复布局与目录，正在运行的任务将结束。",
	"Server operation completed":                                                               "服务操作已完成", "Server restarted and layout restored": "服务已重启，布局已恢复",
	"protocol version mismatch": "协议版本不匹配", "API signature mismatch": "API 接口不匹配", "dev/release variant mismatch": "开发版与正式版不匹配",
	"Software update": "软件更新", "Check for updates": "检查更新", "Checking for updates…": "正在检查更新…",
	"Current version: %s": "当前版本：%s", "Version %s is available": "发现新版本 %s", "Version %s is ready to install": "版本 %s 已准备好安装",
	"No compatible updates are available": "暂无适用于此平台的新版本", "Downloading and verifying…": "正在下载并校验…",
	"Download update": "下载更新", "Install and restart": "安装并重启", "Installing update…": "正在安装更新…",
	"Update failed. You can try again.": "更新失败，可以重试。", "Close": "关闭",
	"Updates are downloaded from Water's GitHub Releases.":                    "更新来自 Water 的 GitHub Releases。",
	"Restart keeps detached terminal servers running.":                        "重启后，独立运行的终端服务将继续运行。",
	"Restart requires a detached local server. Change Server settings first.": "安装需要独立终端服务，请先调整服务设置并重新启动应用。",
	"Settings saved":           "设置已保存",
	"Terminal needs attention": "终端需要你的注意",
	"Reconnecting":             "重连中",
	"Running":                  "运行中", "Exited": "已退出", "Idle": "空闲", "Paused": "已暂停 · 需要留意", "Completed": "已完成", "Error": "错误", "Sidebar buttons": "侧栏底部按钮",
	"Agent started: %s": "Agent 已开始运行：%s", "Agent completed: %s": "Agent 已完成：%s", "Agent paused; it may need your attention: %s": "Agent 已暂停，可能需要你留意：%s", "Agent reported an error: %s": "Agent 报告错误：%s",
	"Agent is still running: %s": "Agent 仍在运行：%s", "Long-running command: %s": "长时间运行的命令：%s",
	"Block": "块", "Bar": "竖线", "Underline": "下划线",
	"Under workspace": "工作区下方", "Separate agents": "集中展示", "Left": "居左", "Center": "居中", "Right": "居右", "Discard changes": "放弃修改",
	"Unsaved changes. Click Discard changes to cancel, or continue editing.": "有未保存的修改。点击“放弃修改”取消，或继续编辑。",
	"Font and text": "字体与文字", "History and memory": "历史与内存", "Links and downloads": "链接与下载", "Language and interface": "语言与界面", "Tabs": "标签页", "Terminal panes": "终端窗格", "Agents": "Agent", "Workspaces": "工作区", "Sidebar layout": "侧栏布局", "Window appearance": "窗口外观",
	"Connections and workspaces": "连接与工作区", "Panes": "窗格", "Windows and settings": "窗口与设置", "Terminal input": "终端输入", "Agent colors": "Agent 颜色", "ANSI palette": "ANSI 调色板", "Connection colors": "主机卡片颜色", "Workspace colors": "工作区颜色", "Pane colors": "窗格颜色", "Tab colors": "标签页颜色", "Terminal colors": "终端颜色", "Interface colors": "界面颜色", "Minimum terminal size": "最小终端尺寸", "Initial terminal size": "初始终端尺寸", "Startup behavior": "启动行为", "Shell": "命令解释器", "Local server": "本地服务", "Terminal behavior": "终端行为",
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
	"Enabled": "服务启动时启用 Web", "ListenAddress": "HTTP 监听地址", "ListenPort": "HTTP 监听端口", "TLS": "启用 HTTPS（TLS）", "PublicURL": "Web 地址", "TLSCertFile": "TLS 证书文件（仅 HTTPS）", "TLSKeyFile": "TLS 私钥文件（仅 HTTPS）",
	"SidebarRemoteButtonHeight": "连接远程按钮高度", "SidebarRemoteButtonFontSize": "连接远程按钮字号",
	"SidebarWorkspaceButtonHeight": "新建工作区按钮高度", "SidebarWorkspaceButtonFontSize": "新建工作区按钮字号",
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
	"SystemNotifications": "系统通知", "AgentLongRunNotificationSeconds": "Agent 长时间运行通知阈值（秒）", "ShellLongRunNotificationSeconds": "后台 Shell 命令通知阈值（秒）",
	"SidebarAgentMode": "Agent 展示模式", "SidebarHostFontSize": "主机字号", "SidebarWorkspaceFontSize": "工作区字号", "SidebarAgentFontSize": "Agent 字号",
	"SidebarHostAlignment": "主机文字对齐", "SidebarWorkspaceAlignment": "工作区文字对齐", "SidebarAgentAlignment": "Agent 文字对齐",
	"SidebarHostRowWidth": "主机行宽比例", "SidebarWorkspaceRowWidth": "工作区行宽比例",
	"SidebarAgentRowGap": "Agent 行间距", "SidebarAgentPadding": "Agent 列表内边距", "SidebarHostHeaderHeight": "主机标题高度", "SidebarHostWorkspaceGap": "主机与工作区间距", "SidebarAgentRowWidth": "Agent 行宽比例",
	"SidebarWorkspaceRowPadding": "工作区行内边距", "SidebarAgentRowPadding": "Agent 行内边距", "SidebarAgentRowHeight": "Agent 行高", "SidebarWorkspaceGap": "工作区间距",
	"SidebarMargin": "侧栏边距", "SidebarCardGap": "侧栏卡片间距", "SidebarCardPadding": "侧栏卡片内边距", "SidebarRowPadding": "主机行内边距",
	"TitlebarPadding": "标题栏内边距", "TitlebarGap": "标题栏控件间距", "TabGap": "标签页间距", "TabPadding": "标签页内边距", "WindowCornerRadius": "窗口圆角",
	"SidebarCardRadius": "侧栏卡片圆角", "SidebarWorkspaceRadius": "工作区圆角", "SidebarWidth": "侧栏宽度", "PanePadding": "窗格内边距", "PaneMargin": "窗格间距", "PaneCornerRadius": "窗格圆角", "PaneDividerWidth": "窗格分隔线宽度", "UIFontSize": "界面字号", "UIFontFamily": "界面字体",
	"NewWindow": "新建窗口", "ConnectRemote": "连接远程主机", "RenameWorkspace": "重命名工作区", "RenameTab": "重命名标签页", "HideWindow": "隐藏窗口", "MinimizeWindow": "最小化窗口", "IgnoreQuit": "忽略退出快捷键", "SwitchTab": "切换标签页",
	"NextWorkspace": "下一个工作区", "PreviousWorkspace": "上一个工作区", "OpenSettings": "打开设置", "SplitRight": "向右分屏", "SplitDown": "向下分屏", "NextTab": "下一个标签页", "PreviousTab": "上一个标签页", "ToggleSidebar": "切换侧栏", "NewTerminalTab": "新建终端标签页", "NewWorkspace": "新建工作区", "ClosePane": "关闭窗格", "PromotePaneToTab": "将窗格提升为标签页",
	"FocusLeft": "聚焦左侧窗格", "FocusRight": "聚焦右侧窗格", "FocusUp": "聚焦上方窗格", "FocusDown": "聚焦下方窗格", "Paste": "粘贴", "CopyOrInterrupt": "复制", "EOF": "输入结束符", "ScrollPageUp": "向上翻页", "ScrollPageDown": "向下翻页",
	"CursorStyle": "光标形状", "CursorBlink": "光标闪烁",
	"CursorForeground": "光标文字颜色", "SidebarConnectionActiveBorder": "活动连接边框颜色", "SidebarConnectionInactiveBorder": "非活动连接边框颜色", "SidebarConnectionOfflineColor": "离线连接文字颜色", "SidebarConnectionOfflineBorder": "离线连接边框颜色",
	"SidebarAgentBackground": "Agent 背景颜色", "SidebarAgentActiveBackground": "活动 Agent 背景颜色", "SidebarDragIndicatorColor": "侧栏拖动指示颜色", "SelectionBackground": "选中文字背景颜色", "InactiveCursor": "非活动光标颜色", "InverseForeground": "反色文字颜色", "InverseBackground": "反色背景颜色",
	"PaneBackground": "窗格背景颜色", "ActivePaneBorder": "活动窗格边框颜色", "InactivePaneBorder": "非活动窗格边框颜色", "AccentForeground": "强调文字颜色", "TabActiveBackground": "活动标签页背景颜色", "TabInactiveBackground": "非活动标签页背景颜色", "TabAddBackground": "新建标签按钮背景颜色",
	"SidebarConnectionBackground": "主机卡片背景颜色", "SidebarConnectionActiveBackground": "活动主机卡片背景颜色", "SidebarWorkspaceBackground": "工作区背景颜色", "SidebarWorkspaceActiveBackground": "活动工作区背景颜色",
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
