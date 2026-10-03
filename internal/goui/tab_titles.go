package goui

import (
	"encoding/json"
	"strings"

	"github.com/SurTeam/Water/internal/gomodel"
)

func tabCommandTitle(tab gomodel.TabDump) string {
	if tab.TitleOverride != nil {
		return *tab.TitleOverride
	}
	var root paneTree
	if json.Unmarshal(tab.Tree, &root) != nil {
		return tab.Title
	}
	var find func(*paneTree) string
	find = func(node *paneTree) string {
		if node == nil {
			return ""
		}
		if node.PaneID == tab.ActivePane && node.Terminal != nil {
			return node.Terminal.Summary.ProcessName
		}
		if name := find(node.First); name != "" {
			return name
		}
		return find(node.Second)
	}
	if name := strings.TrimSpace(find(&root)); name != "" {
		return name
	}
	return tab.Title
}

func boundedTabTitle(title string, limit int) string {
	runes := []rune(title)
	if len(runes) <= limit {
		return title
	}
	return string(runes[:max(0, limit-1)]) + "…"
}
