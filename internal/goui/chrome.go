package goui

import (
	"image"
	"image/color"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
)

// Chrome controls keep the existing Clickable event path, but use compact,
// centered labels instead of Material's raised action buttons.
type chromeButtonStyle struct {
	theme             *material.Theme
	click             *widget.Clickable
	label             string
	Background, Color color.NRGBA
	Selected          bool
	Compact           bool
	FillWidth         bool
	TextSize          unit.Sp
}

func chromeButton(th *material.Theme, click *widget.Clickable, label string) chromeButtonStyle {
	return chromeButtonStyle{theme: th, click: click, label: label, Color: th.Palette.Fg}
}

func (b chromeButtonStyle) Layout(gtx layout.Context) layout.Dimensions {
	return b.click.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Stack{}.Layout(gtx,
			layout.Expanded(func(gtx layout.Context) layout.Dimensions {
				bg := b.Background
				if b.click.Hovered() || b.click.Pressed() || gtx.Focused(b.click) {
					if bg.A == 0 {
						bg = b.theme.Palette.Bg
					}
					bg = mixColor(bg, b.theme.Palette.Fg, 0.08)
				}
				paint.FillShape(gtx.Ops, bg, clip.UniformRRect(image.Rectangle{Max: gtx.Constraints.Min}, gtx.Dp(6)).Op(gtx.Ops))
				if b.Selected {
					paint.FillShape(gtx.Ops, b.theme.Palette.ContrastBg, clip.Rect{Min: image.Pt(0, gtx.Dp(9)), Max: image.Pt(gtx.Dp(2), gtx.Constraints.Min.Y-gtx.Dp(9))}.Op())
				}
				return layout.Dimensions{Size: gtx.Constraints.Min}
			}),
			layout.Stacked(func(gtx layout.Context) layout.Dimensions {
				if b.FillWidth {
					gtx.Constraints.Min.X = gtx.Constraints.Max.X
				}
				padding := unit.Dp(12)
				if b.Compact {
					padding = 10
				}
				return layout.Inset{Left: padding, Right: padding, Top: 8, Bottom: 8}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					size := b.TextSize
					if size == 0 {
						size = b.theme.TextSize
					}
					label := material.Label(b.theme, size, b.label)
					label.Color = b.Color
					label.MaxLines = 1
					return layout.Center.Layout(gtx, label.Layout)
				})
			}),
		)
	})
}

func mixColor(a, b color.NRGBA, amount float32) color.NRGBA {
	return color.NRGBA{R: uint8(float32(a.R)*(1-amount) + float32(b.R)*amount), G: uint8(float32(a.G)*(1-amount) + float32(b.G)*amount), B: uint8(float32(a.B)*(1-amount) + float32(b.B)*amount), A: 255}
}

func sectionLabel(gtx layout.Context, th *material.Theme, text string) layout.Dimensions {
	return layout.Inset{Left: 20, Right: 12, Top: 20, Bottom: 8}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		label := material.Label(th, unit.Sp(10), text)
		label.Color = mixColor(th.Palette.Bg, th.Palette.Fg, .5)
		label.Font.Weight = 600
		return label.Layout(gtx)
	})
}

func chromeEditor(th *material.Theme, editor *widget.Editor, hint string) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		width := gtx.Constraints.Max.X
		return layout.Stack{}.Layout(gtx,
			layout.Expanded(func(gtx layout.Context) layout.Dimensions {
				size := gtx.Constraints.Min
				border := mixColor(th.Palette.Bg, th.Palette.Fg, .15)
				if gtx.Focused(editor) {
					border = th.Palette.ContrastBg
				}
				paint.FillShape(gtx.Ops, border, clip.UniformRRect(image.Rectangle{Max: size}, gtx.Dp(5)).Op(gtx.Ops))
				paint.FillShape(gtx.Ops, mixColor(th.Palette.Bg, th.Palette.Fg, .025), clip.UniformRRect(image.Rectangle{Max: size}.Inset(1), gtx.Dp(4)).Op(gtx.Ops))
				return layout.Dimensions{Size: size}
			}),
			layout.Stacked(func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min.Y = 0
				gtx.Constraints.Min.X = width
				return layout.Inset{Left: 10, Right: 10, Top: 7, Bottom: 7}.Layout(gtx, material.Editor(th, editor, hint).Layout)
			}),
		)
	}
}

func chromeRule(gtx layout.Context, th *material.Theme) {
	paint.FillShape(gtx.Ops, mixColor(th.Palette.Bg, th.Palette.Fg, .08), clip.Rect{Max: image.Pt(gtx.Constraints.Max.X, 1)}.Op())
}

// Keep decoration offsets local to avoid changing control API hit coordinates.
func chromeOffset(gtx layout.Context, at image.Point, draw func()) {
	defer op.Offset(at).Push(gtx.Ops).Pop()
	draw()
}
