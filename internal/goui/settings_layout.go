package goui

import (
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
	"image"
	"reflect"
)

func (c *WorkspaceClient) renderSettings(gtx layout.Context, th *material.Theme, groups []string) layout.Dimensions {
	s := &c.settings
	focusRequested := s.requestFocus
	for {
		ev, ok := gtx.Event(key.Filter{Name: key.NameEscape})
		if !ok {
			break
		}
		if e, ok := ev.(key.Event); ok && e.State == key.Press && !s.saving {
			s.cancelChanges()
			if c.invalidate != nil {
				c.invalidate()
			}
			return layout.Dimensions{}
		}
	}
	width := min(gtx.Constraints.Max.X, gtx.Dp(720))
	origin := image.Pt((gtx.Constraints.Max.X-width)/2+gtx.Dp(16), gtx.Dp(16))
	headerHeight, tabsHeight, footerHeight, messageHeight := gtx.Dp(76), gtx.Dp(44), gtx.Dp(48), gtx.Dp(28)
	rowHeight := gtx.Dp(44)
	listTop := origin.Y + headerHeight + tabsHeight
	track := func(label string, rect image.Rectangle) {
		c.hitRegions = append(c.hitRegions, automationHit{Rect: rect, Kind: hitSettingsControl, Label: label})
	}
	return layout.Stack{Alignment: layout.Center}.Layout(gtx,
		layout.Expanded(func(gtx layout.Context) layout.Dimensions {
			return s.scrim.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Dimensions{Size: gtx.Constraints.Max}
			})
		}),
		layout.Stacked(func(gtx layout.Context) layout.Dimensions {
			if s.saving {
				gtx = gtx.Disabled()
			}
			gtx.Constraints.Min.X = width
			gtx.Constraints.Max.X = width
			gtx.Constraints.Min.Y = gtx.Constraints.Max.Y
			card := image.Rectangle{Max: image.Pt(width, gtx.Constraints.Max.Y)}
			paint.FillShape(gtx.Ops, mixColor(th.Palette.Bg, th.Palette.Fg, .14), clip.UniformRRect(card, gtx.Dp(10)).Op(gtx.Ops))
			paint.FillShape(gtx.Ops, th.Palette.Bg, clip.UniformRRect(card.Inset(1), gtx.Dp(10)).Op(gtx.Ops))
			return layout.UniformInset(unit.Dp(16)).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				height := gtx.Constraints.Max.Y
				contentWidth := gtx.Constraints.Max.X
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						gtx.Constraints = layout.Exact(image.Pt(contentWidth, headerHeight))
						return layout.Flex{Axis: layout.Vertical}.Layout(gtx, layout.Rigid(material.H6(th, c.tr("Settings")).Layout), layout.Rigid(material.Caption(th, c.tr("Appearance and shortcuts apply now. Other settings require restart.")).Layout))
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						gtx.Constraints = layout.Exact(image.Pt(contentWidth, tabsHeight))
						x := origin.X
						children := []layout.FlexChild{}
						for i, name := range groups {
							i, name := i, name
							if i > 0 {
								children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
									x += gtx.Dp(8)
									return layout.Dimensions{Size: image.Pt(gtx.Dp(8), 0)}
								}))
							}
							children = append(children, layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
								button := chromeButton(th, &s.tabs[i], c.tr(name))
								button.Background = configColor(c.currentConfig().Theme.SidebarWorkspaceActiveBackground, 0x29332f)
								button.Selected = s.group == i
								if s.group != i {
									button.Background = configColor(c.currentConfig().Theme.SidebarBackground, 0x171a1c)
									button.Color = th.Palette.Fg
								}
								dims := button.Layout(gtx)
								track("category:"+name, image.Rect(x, origin.Y+headerHeight, x+dims.Size.X, origin.Y+headerHeight+dims.Size.Y))
								x += dims.Size.X
								return dims
							}))
						}
						return layout.Flex{}.Layout(gtx, children...)
					}),
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
						listBounds := image.Rect(origin.X, listTop, origin.X+contentWidth, listTop+gtx.Constraints.Max.Y)
						rows := settingsRows(s)
						return material.List(th, &s.list).Layout(gtx, len(rows), func(gtx layout.Context, index int) layout.Dimensions {
							gtx.Constraints.Min.Y = rowHeight
							gtx.Constraints.Max.Y = rowHeight
							if rows[index].field < 0 {
								title := material.Subtitle2(th, c.tr(rows[index].section))
								title.Color = configColor(c.currentConfig().Theme.Accent, 0x72d6ab)
								return layout.Center.Layout(gtx, title.Layout)
							}
							fieldIndex := rows[index].field
							f := &s.fields[fieldIndex]
							labelWidth := min(gtx.Dp(260), contentWidth/2)
							y := listTop + (index-s.list.Position.First)*rowHeight - s.list.Position.Offset
							if rect := image.Rect(origin.X+labelWidth, y, origin.X+contentWidth, y+rowHeight).Intersect(listBounds); !rect.Empty() {
								track("field:"+f.group+"."+f.name, rect)
							}
							return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
								layout.Rigid(func(gtx layout.Context) layout.Dimensions {
									gtx.Constraints.Max.X = labelWidth
									gtx.Constraints.Min.X = labelWidth
									return material.Body2(th, localizedField(c.language(), *f)).Layout(gtx)
								}),
								layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
									var dims layout.Dimensions
									if f.kind == reflect.Bool {
										dims = material.CheckBox(th, &f.toggle, "").Layout(gtx)
									} else if f.group == "Theme" {
										dims = layout.Flex{Alignment: layout.Middle}.Layout(gtx, layout.Flexed(1, chromeEditor(th, &f.editor, "")), layout.Rigid(func(gtx layout.Context) layout.Dimensions {
											size := image.Pt(gtx.Dp(28), gtx.Dp(28))
											paint.FillShape(gtx.Ops, configColor(f.editor.Text(), 0x555555), clip.UniformRRect(image.Rectangle{Max: size}, gtx.Dp(4)).Op(gtx.Ops))
											return layout.Dimensions{Size: size}
										}))
									} else {
										dims = chromeEditor(th, &f.editor, "")(gtx)
									}
									if s.requestFocus && s.focus == fieldIndex {
										gtx.Execute(key.FocusCmd{Tag: &f.editor})
										s.requestFocus = false
									}
									if !focusRequested && gtx.Focused(&f.editor) {
										s.focus = fieldIndex
									}
									pass := pointer.PassOp{}.Push(gtx.Ops)
									area := clip.Rect{Max: gtx.Constraints.Max}.Push(gtx.Ops)
									event.Op(gtx.Ops, f)
									area.Pop()
									pass.Pop()
									return dims
								}),
							)
						})
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						gtx.Constraints = layout.Exact(image.Pt(contentWidth, messageHeight))
						return material.Caption(th, c.tr(s.message)).Layout(gtx)
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						gtx.Constraints = layout.Exact(image.Pt(contentWidth, footerHeight))
						label := "Save"
						if s.saving {
							label = "Saving…"
						}
						x := origin.X
						children := []layout.FlexChild{}
						for _, control := range []struct {
							button      *widget.Clickable
							label, name string
						}{{&s.save, label, "save"}, {&s.cancel, "Cancel", "cancel"}, {&s.defaults, "Defaults", "defaults"}} {
							control := control
							if len(children) > 0 {
								children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
									x += gtx.Dp(12)
									return layout.Dimensions{Size: image.Pt(gtx.Dp(12), 0)}
								}))
							}
							if control.name == "cancel" && s.confirmDiscard {
								control.label = "Discard changes"
							}
							children = append(children, layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
								button := chromeButton(th, control.button, c.tr(control.label))
								if control.name == "save" {
									button.Background = th.Palette.ContrastBg
									button.Color = th.Palette.ContrastFg
								}
								dims := button.Layout(gtx)
								y := origin.Y + height - footerHeight
								track(control.name, image.Rect(x, y, x+dims.Size.X, y+dims.Size.Y))
								x += dims.Size.X
								return dims
							}))
						}
						return layout.Flex{}.Layout(gtx, children...)
					}),
				)
			})
		}),
	)
}
