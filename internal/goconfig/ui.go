package goconfig

// DefaultAgentColors mirrors the supported agent kinds; callers own the map.
func DefaultAgentColors() map[string]string {
	return map[string]string{"claude_code": "#d97757", "codex": "#2f9e63", "opencode": "#22a7f0", "gemini_cli": "#7b68ee", "aider": "#f2b134", "cursor_agent": "#e0e0e0", "amp": "#ff6b9d", "crush": "#00bcd4", "goose": "#9ccc65", "qwen_code": "#5c7cfa", "droid": "#b07c5a", "grok": "#d4d4d8", "pi": "#f97316"}
}

func (u UIConfig) normalized() UIConfig {
	u.SidebarMinWidth = clampFloat(u.SidebarMinWidth, 100, 800)
	u.SidebarMaxWidth = clampFloat(u.SidebarMaxWidth, u.SidebarMinWidth, 1200)
	u.SidebarWidth = clampFloat(u.SidebarWidth, u.SidebarMinWidth, u.SidebarMaxWidth)
	u.SidebarResizeHandleWidth = clampFloat(u.SidebarResizeHandleWidth, 1, 40)
	u.TitlebarHeight = clampFloat(u.TitlebarHeight, 24, 96)
	u.TabHeight = clampFloat(u.TabHeight, 20, 80)
	u.SidebarHeaderHeight = clampFloat(u.SidebarHeaderHeight, 20, 96)
	u.WindowPadding = clampFloat(u.WindowPadding, 0, 32)
	u.SidebarSurfaceMargin = clampFloat(u.SidebarSurfaceMargin, 0, 32)
	u.SidebarMargin = clampFloat(u.SidebarMargin, 0, 32)
	u.SidebarCardGap = clampFloat(u.SidebarCardGap, 0, 32)
	u.SidebarCardPadding = clampFloat(u.SidebarCardPadding, 0, 32)
	u.SidebarRowPadding = clampFloat(u.SidebarRowPadding, 0, 48)
	u.TitlebarPadding = clampFloat(u.TitlebarPadding, 0, 32)
	u.TitlebarGap = clampFloat(u.TitlebarGap, 0, 32)
	u.TabGap = clampFloat(u.TabGap, 0, 24)
	u.TabPadding = clampFloat(u.TabPadding, 0, 48)
	u.WindowCornerRadius = clampFloat(u.WindowCornerRadius, 0, 48)
	u.SidebarCardRadius = clampFloat(u.SidebarCardRadius, 0, 32)
	u.SidebarWorkspaceRadius = clampFloat(u.SidebarWorkspaceRadius, 0, 24)
	u.SidebarAgentRowGap = clampFloat(u.SidebarAgentRowGap, 0, 24)
	u.SidebarAgentPadding = clampFloat(u.SidebarAgentPadding, 0, 24)
	u.SidebarHostHeaderHeight = clampFloat(u.SidebarHostHeaderHeight, 16, 64)
	u.SidebarHostWorkspaceGap = clampFloat(u.SidebarHostWorkspaceGap, 0, 32)
	u.SidebarAgentRowWidth = clampFloat(u.SidebarAgentRowWidth, .2, 1)
	u.SidebarWorkspaceRowPadding = clampFloat(u.SidebarWorkspaceRowPadding, 0, 48)
	u.SidebarAgentRowPadding = clampFloat(u.SidebarAgentRowPadding, 0, 48)
	u.SidebarAgentRowHeight = clampFloat(u.SidebarAgentRowHeight, 16, 80)
	u.SidebarWorkspaceGap = clampFloat(u.SidebarWorkspaceGap, 0, 32)
	return u
}
