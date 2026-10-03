package goui

import "strings"

func settingsSection(f settingsField) string {
	switch f.group {
	case "Terminal":
		if strings.Contains(f.name, "Hyperlink") {
			return "Links and downloads"
		}
		if strings.Contains(f.name, "Scrollback") || f.name == "ReplayHistoryBytes" {
			return "History and memory"
		}
		return "Font and text"
	case "UI":
		if strings.HasPrefix(f.name, "Tab") {
			return "Tabs"
		}
		if strings.HasPrefix(f.name, "Pane") || f.name == "DimInactivePanes" || f.name == "WindowPadding" {
			return "Terminal panes"
		}
		if strings.Contains(f.name, "Agent") {
			return "Agents"
		}
		if strings.Contains(f.name, "Workspace") || f.name == "SidebarHeaderHeight" {
			return "Workspaces"
		}
		if strings.HasPrefix(f.name, "Sidebar") {
			return "Sidebar layout"
		}
		if strings.HasPrefix(f.name, "Titlebar") || f.name == "WindowCornerRadius" {
			return "Window appearance"
		}
		return "Language and interface"
	case "Shortcuts":
		if strings.Contains(f.name, "Workspace") || f.name == "ConnectRemote" {
			return "Connections and workspaces"
		}
		if strings.Contains(f.name, "Tab") {
			return "Tabs"
		}
		if strings.Contains(f.name, "Pane") || strings.HasPrefix(f.name, "Focus") || strings.HasPrefix(f.name, "Split") {
			return "Panes"
		}
		if strings.Contains(f.name, "Window") || f.name == "OpenSettings" || f.name == "IgnoreQuit" || f.name == "ToggleSidebar" {
			return "Windows and settings"
		}
		return "Terminal input"
	case "Theme":
		if strings.HasPrefix(f.name, "AgentColors.") || strings.HasPrefix(f.name, "SidebarAgent") {
			return "Agent colors"
		}
		if strings.HasPrefix(f.label, "ANSI color ") {
			return "ANSI palette"
		}
		if strings.HasPrefix(f.name, "SidebarConnection") {
			return "Connection colors"
		}
		if strings.HasPrefix(f.name, "SidebarWorkspace") {
			return "Workspace colors"
		}
		if strings.Contains(f.name, "Pane") {
			return "Pane colors"
		}
		if strings.HasPrefix(f.name, "Tab") {
			return "Tab colors"
		}
		if strings.HasPrefix(f.name, "Terminal") || strings.Contains(f.name, "Cursor") || strings.HasPrefix(f.name, "Inverse") || f.name == "SelectionBackground" {
			return "Terminal colors"
		}
		return "Interface colors"
	case "Startup":
		if strings.HasPrefix(f.name, "WindowMin") {
			return "Minimum terminal size"
		}
		if strings.HasPrefix(f.name, "Window") {
			return "Initial terminal size"
		}
		return "Startup behavior"
	case "Shell":
		return "Shell"
	case "Server":
		return "Local server"
	case "Features":
		return "Terminal behavior"
	}
	return f.group
}

type settingsRow struct {
	field   int
	section string
}

func settingsRows(s *settingsPanel) []settingsRow {
	groups := []string{"Terminal", "UI", "Shortcuts", "Theme", "Startup"}
	bySection := map[string][]int{}
	for i, f := range s.fields {
		if f.group != groups[s.group] && !(s.group == 4 && (f.group == "Shell" || f.group == "Server" || f.group == "Features")) {
			continue
		}
		section := settingsSection(f)
		bySection[section] = append(bySection[section], i)
	}
	var rows []settingsRow
	order := [][]string{
		{"Font and text", "History and memory", "Links and downloads"},
		{"Language and interface", "Tabs", "Window appearance", "Terminal panes", "Sidebar layout", "Workspaces", "Agents"},
		{"Windows and settings", "Connections and workspaces", "Tabs", "Panes", "Terminal input"},
		{"Terminal colors", "ANSI palette", "Interface colors", "Tab colors", "Pane colors", "Connection colors", "Workspace colors", "Agent colors"},
		{"Initial terminal size", "Minimum terminal size", "Startup behavior", "Shell", "Local server", "Terminal behavior"},
	}
	for _, section := range order[s.group] {
		if len(bySection[section]) == 0 {
			continue
		}
		rows = append(rows, settingsRow{field: -1, section: section})
		for _, i := range bySection[section] {
			rows = append(rows, settingsRow{field: i, section: section})
		}
	}
	return rows
}
