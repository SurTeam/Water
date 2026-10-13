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
	r := w.overlayPanel(c, dst, w.dp(720), w.dp(530), true)
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
	label("Edit recognition rules", 28, true)
	label("Rules: any rule may match. all: every condition in that rule must match.", 24, false)
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
		label(fmt.Sprintf(c.tr("Condition %d of %d (all)"), e.condition+1, len(e.rules[e.rule].All)), 24, true)
		button(left, 84, "Previous", "previous-condition")
		button(left+w.dp(90), 84, "Next", "next-condition")
		button(left+w.dp(180), 112, "Add condition", "add-condition")
		button(left+w.dp(298), 112, "Remove condition", "remove-condition")
		y += w.dp(38)
		button(left, 200, "field: "+condition.Field, "field")
		y += w.dp(32)
		descriptions := map[string]string{
			"executable": "executable: process executable basename, e.g. node or claude.",
			"name":       "name: normalized process/launcher name; ignores letter case.",
			"path":       "path: any process argument containing /, e.g. a script path.",
			"argument":   "argument: any individual process argument, including argv[0].",
		}
		label(descriptions[condition.Field], 26, false)
		button(left, 200, "op: "+condition.Op, "op")
		y += w.dp(32)
		descriptions = map[string]string{
			"exact":    "exact: the entire field must equal the value (case-sensitive except name).",
			"contains": "contains: the field must contain the value as a substring.",
			"glob":     "glob: filepath pattern; * and ? do not cross /. Use exact for literal symbols.",
		}
		label(descriptions[condition.Op], 26, false)
		label("value: enter the match text directly; JSON escaping is automatic.", 24, true)
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
	label("Values are match data; they are never executed as shell commands.", 24, false)
	w.label(dst, c, image.Rect(left, y, right, y+w.dp(30)), c.tr(e.message), 10, muted, false)
	y = r.Max.Y - w.dp(48)
	button(right-w.dp(230), 140, "Apply to JSON", "apply")
	button(right-w.dp(84), 84, "Cancel", "cancel")
}
