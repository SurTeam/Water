package goui

import (
	"fmt"
	"image"

	"github.com/google/uuid"
	"github.com/hajimehoshi/ebiten/v2"
)

func (w *EbitengineWindow) drawAgentRules(c *WorkspaceClient, dst *ebiten.Image) {
	s := &c.settings
	e := s.agents.rulesEditor
	r := w.overlayPanel(c, dst, w.dp(720), w.dp(680), true)
	left, right, y := r.Min.X+w.dp(24), r.Max.X-w.dp(24), r.Min.Y+w.dp(20)
	fg := configColor(c.currentConfig().Theme.UIForeground, 0xe6eaea)
	muted := mixColor(configColor(c.currentConfig().Theme.ChromeBackground, 0), fg, .65)
	label := func(text string, height float64, strong bool) {
		w.label(dst, c, image.Rect(left, y, right, y+w.dp(height)), c.tr(text), 10, fg, strong)
		y += w.dp(height)
	}
	button := func(x int, width float64, title, action string) {
		w.button(dst, c, image.Rect(x, y, x+w.dp(width), y+w.dp(28)), c.tr(title), hitSettingsControl, uuid.Nil, false)
		c.hitRegions[len(c.hitRegions)-1].Label = "agent-rules:" + action
	}
	button(right-w.dp(200), 200, "Fill from current pane", "sample")
	label("Edit recognition rules", 28, true)
	label("Match any rule below. Within a rule, match every condition.", 24, false)
	ruleNumber := 0
	if len(e.rules) > 0 {
		ruleNumber = e.rule + 1
	}
	label(fmt.Sprintf(c.tr("Rule %d of %d"), ruleNumber, len(e.rules)), 24, true)
	button(left, 84, "Previous", "previous-rule")
	button(left+w.dp(90), 84, "Next", "next-rule")
	button(left+w.dp(180), 112, "Add rule", "add-rule")
	button(left+w.dp(298), 112, "Remove rule", "remove-rule")
	y += w.dp(38)
	if len(e.rules) > 0 {
		condition := e.rules[e.rule].All[e.condition]
		label("All of these conditions must match (click to edit):", 24, true)
		all := e.rules[e.rule].All
		start, end := agentRuleConditionPage(int(float64(r.Dy())/w.scale), len(all), e.condition)
		// Page controls select the adjacent condition so keyboard and direct
		// condition selection always keep the edited row visible.
		button(left, 84, "Previous", "previous-condition")
		button(left+w.dp(90), 84, "Next", "next-condition")
		w.label(dst, c, image.Rect(left+w.dp(184), y, right, y+w.dp(28)), fmt.Sprintf("%d–%d / %d", start+1, end, len(all)), 10, fg, false)
		y += w.dp(32)
		for i := start; i < end; i++ {
			item := all[i]
			title := fmt.Sprintf("%d. %s %s %s", i+1, c.tr(agentRuleFieldLabel(item.Field)), c.tr(agentRuleOpLabel(item.Op)), item.Value)
			w.button(dst, c, image.Rect(left, y, right, y+w.dp(20)), title, hitSettingsControl, uuid.Nil, i == e.condition)
			c.hitRegions[len(c.hitRegions)-1].Label = fmt.Sprintf("agent-rules:condition:%d", i)
			y += w.dp(22)
		}
		button(left, 112, "Add condition", "add-condition")
		button(left+w.dp(120), 112, "Remove condition", "remove-condition")
		y += w.dp(34)
		button(left, 200, agentRuleFieldLabel(condition.Field), "field")
		button(left+w.dp(208), 200, agentRuleOpLabel(condition.Op), "op")
		y += w.dp(32)
		if condition.Op == "glob" {
			label("Wildcards: * and ? do not cross /. Name ignores letter case.", 22, false)
		} else {
			label("Name ignores letter case; other matches are case-sensitive.", 22, false)
		}
		label("Text to match", 24, true)
		for i := range s.fields {
			f := &s.fields[i]
			if f.group != "AgentRules" {
				continue
			}
			fr := image.Rect(left, y, right, y+w.dp(30))
			w.field(c, dst, fr, f.editor.Text(), s.focus == i, &f.editor)
			w.hit(c, fr, hitSettingsControl, uuid.Nil, "field:AgentRules.Value")
		}
		y += w.dp(36)
	} else {
		label("No rules: this profile will not match any process. Add a rule to start.", 30, false)
	}

	w.label(dst, c, image.Rect(left, y, right, y+w.dp(30)), c.tr(e.message), 10, muted, false)
	y = r.Max.Y - w.dp(48)
	button(right-w.dp(230), 140, "Apply rules", "apply")
	button(right-w.dp(84), 84, "Cancel", "cancel")
}
