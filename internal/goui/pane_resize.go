package goui

import (
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
)

// nearestPaneSplit returns the deepest ancestor that changes this dimension.
// second records which child contains the pane, so growing it has the right sign.
func nearestPaneSplit(node *paneTree, pane uuid.UUID, vertical bool, path []bool) ([]bool, bool, bool) {
	if node == nil || node.Type != "split" {
		return nil, false, false
	}
	second := treeHasPane(node.Second, pane)
	child := node.First
	if second {
		child = node.Second
	}
	if !treeHasPane(child, pane) {
		return nil, false, false
	}
	childPath := append(append([]bool{}, path...), second)
	if foundPath, foundSecond, ok := nearestPaneSplit(child, pane, vertical, childPath); ok {
		return foundPath, foundSecond, true
	}
	if (node.Axis == "vertical") == vertical {
		return append([]bool{}, path...), second, true
	}
	return nil, false, false
}

func (w *EbitengineWindow) resizePaneStep(c *WorkspaceClient, direction string) {
	c.mu.RLock()
	state := c.state
	c.mu.RUnlock()
	ws := activeWorkspace(state)
	if ws == nil {
		return
	}
	tab := activeTab(*ws)
	if tab == nil {
		return
	}
	vertical := direction == "shorter" || direction == "taller"
	var tree paneTree
	if json.Unmarshal(tab.Tree, &tree) != nil {
		return
	}
	path, second, ok := nearestPaneSplit(&tree, tab.ActivePane, vertical, nil)
	if !ok {
		return
	}
	v := w.view(c)
	split, ok := v.splits[fmt.Sprintf("%s:%v", tab.ID, path)]
	if !ok {
		return
	}
	pane, ok := v.panes[tab.ActivePane]
	if !ok {
		return
	}
	span, cell := split.rect.Dx(), pane.cw
	if vertical {
		span, cell = split.rect.Dy(), pane.lh
	}
	gap := min(span, w.dp(float64(c.currentConfig().UI.PaneMargin)))
	if span <= gap || cell <= 0 {
		return
	}
	delta := float32(cell) / float32(span-gap)
	if direction == "narrower" || direction == "shorter" {
		delta = -delta
	}
	if second {
		delta = -delta
	}
	_ = c.session.DispatchAsync(map[string]any{
		"type": "pane.resize_split", "tab_id": tab.ID, "path": path, "delta": delta,
	})
}
