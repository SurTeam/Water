package goui

import (
	"fmt"
	"image"

	"gioui.org/io/event"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget/material"
	"github.com/SurTeam/Water/internal/gomodel"
)

type splitDivider struct {
	frame    uint64
	dragging bool
	ratio    float32
}

func (c *WorkspaceClient) layoutSplit(gtx layout.Context, th *material.Theme, node *paneTree, tab gomodel.TabDump, origin image.Point, path []bool) layout.Dimensions {
	if c.dividers == nil {
		c.dividers = map[string]*splitDivider{}
	}
	name := fmt.Sprintf("%s:%v", tab.ID, path)
	divider := c.dividers[name]
	if divider == nil {
		divider = &splitDivider{}
		c.dividers[name] = divider
	}
	divider.frame = c.layoutFrame
	ratio := node.Ratio
	if ratio < .05 || ratio > .95 {
		ratio = .5
	}
	if divider.dragging {
		ratio = divider.ratio
	}
	size := gtx.Constraints.Max
	vertical := node.Axis == "vertical"
	span := size.X
	if vertical {
		span = size.Y
	}
	gap := gtx.Dp(unit.Dp(c.currentConfig().UI.PaneDividerWidth))
	if gap < 6 {
		gap = 6
	}
	if gap > span {
		gap = span
	}
	usable := span - gap
	first := int(float32(usable) * ratio)
	second := usable - first
	firstSize, secondSize, dividerSize := image.Pt(first, size.Y), image.Pt(second, size.Y), image.Pt(gap, size.Y)
	dividerOffset, secondOffset := image.Pt(first, 0), image.Pt(first+gap, 0)
	if vertical {
		firstSize = image.Pt(size.X, first)
		secondSize = image.Pt(size.X, second)
		dividerSize = image.Pt(size.X, gap)
		dividerOffset = image.Pt(0, first)
		secondOffset = image.Pt(0, first+gap)
	}
	// Drag preview stays in the client. Only the committed ratio changes the model.
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: divider, Kinds: pointer.Press | pointer.Drag | pointer.Release | pointer.Cancel})
		if !ok {
			break
		}
		pe, ok := ev.(pointer.Event)
		if !ok {
			continue
		}
		switch pe.Kind {
		case pointer.Press:
			if pe.Buttons.Contain(pointer.ButtonPrimary) {
				divider.dragging = true
				divider.ratio = ratio
				gtx.Execute(pointer.GrabCmd{Tag: divider, ID: pe.PointerID})
			}
		case pointer.Drag, pointer.Release:
			if !divider.dragging {
				continue
			}
			position := pe.Position.X
			if vertical {
				position = pe.Position.Y
			}
			if usable > 0 {
				divider.ratio = (float32(first) + position - float32(gap)/2) / float32(usable)
				if divider.ratio < .05 {
					divider.ratio = .05
				}
				if divider.ratio > .95 {
					divider.ratio = .95
				}
			}
			if pe.Kind == pointer.Release {
				divider.dragging = false
				_ = c.session.DispatchAsync(map[string]any{"type": "pane.resize_split", "tab_id": tab.ID, "path": append([]bool{}, path...), "ratio": divider.ratio})
			}
		case pointer.Cancel:
			divider.dragging = false
		}
	}
	firstPath := append(append([]bool{}, path...), false)
	secondPath := append(append([]bool{}, path...), true)
	c.layoutPaneAt(exact(gtx, firstSize), th, node.First, tab, origin, firstPath)
	offset := op.Offset(secondOffset).Push(gtx.Ops)
	c.layoutPaneAt(exact(gtx, secondSize), th, node.Second, tab, origin.Add(secondOffset), secondPath)
	offset.Pop()
	offset = op.Offset(dividerOffset).Push(gtx.Ops)
	area := clip.Rect{Max: dividerSize}.Push(gtx.Ops)
	paint.Fill(gtx.Ops, configColor(c.currentConfig().Theme.ChromeBackground, 0x121416))
	if divider.dragging {
		paint.Fill(gtx.Ops, configColor(c.currentConfig().Theme.Accent, 0x72d6ab))
	}
	if vertical {
		pointer.CursorRowResize.Add(gtx.Ops)
	} else {
		pointer.CursorColResize.Add(gtx.Ops)
	}
	event.Op(gtx.Ops, divider)
	area.Pop()
	offset.Pop()
	c.hitRegions = append(c.hitRegions, automationHit{Rect: image.Rectangle{Min: origin.Add(dividerOffset), Max: origin.Add(dividerOffset).Add(dividerSize)}, Kind: hitDivider, ID: tab.ID, Label: name})
	return layout.Dimensions{Size: size}
}
