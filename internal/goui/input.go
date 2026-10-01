package goui

import (
	"image"
	"strings"
	"unicode"
	"unicode/utf8"

	"gioui.org/gesture"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op/clip"

	"github.com/SurTeam/Water/internal/govt"
)

type TerminalInput struct {
	tag   struct{}
	click gesture.Click

	OnInput func([]byte)

	composing bool
	pendingComposition string
}

func (i *TerminalInput) Process(gtx layout.Context, snap govt.Snapshot) {
	for {
		ev, ok := i.click.Update(gtx.Source)
		if !ok { break }
		if ev.Kind == gesture.KindPress {
			gtx.Execute(key.FocusCmd{Tag:&i.tag})
			gtx.Execute(key.SoftKeyboardCmd{Show:true})
		}
	}

	allMods := key.ModCtrl | key.ModAlt | key.ModShift | key.ModCommand | key.ModSuper
	filters := []event.Filter{
		key.FocusFilter{Target:&i.tag},
		key.Filter{Focus:&i.tag, Name:key.NameReturn, Optional:allMods},
		key.Filter{Focus:&i.tag, Name:key.NameEnter, Optional:allMods},
		key.Filter{Focus:&i.tag, Name:key.NameEscape, Optional:allMods},
		key.Filter{Focus:&i.tag, Name:key.NameTab, Optional:allMods},
		key.Filter{Focus:&i.tag, Name:key.NameDeleteBackward, Optional:allMods},
		key.Filter{Focus:&i.tag, Name:key.NameDeleteForward, Optional:allMods},
		key.Filter{Focus:&i.tag, Name:key.NameLeftArrow, Optional:allMods},
		key.Filter{Focus:&i.tag, Name:key.NameRightArrow, Optional:allMods},
		key.Filter{Focus:&i.tag, Name:key.NameUpArrow, Optional:allMods},
		key.Filter{Focus:&i.tag, Name:key.NameDownArrow, Optional:allMods},
		key.Filter{Focus:&i.tag, Name:key.NameHome, Optional:allMods},
		key.Filter{Focus:&i.tag, Name:key.NameEnd, Optional:allMods},
		key.Filter{Focus:&i.tag, Name:key.NamePageUp, Optional:allMods},
		key.Filter{Focus:&i.tag, Name:key.NamePageDown, Optional:allMods},
		key.Filter{Focus:&i.tag, Name:key.NameF1, Optional:allMods},
		key.Filter{Focus:&i.tag, Name:key.NameF2, Optional:allMods},
		key.Filter{Focus:&i.tag, Name:key.NameF3, Optional:allMods},
		key.Filter{Focus:&i.tag, Name:key.NameF4, Optional:allMods},
		key.Filter{Focus:&i.tag, Name:key.NameF5, Optional:allMods},
		key.Filter{Focus:&i.tag, Name:key.NameF6, Optional:allMods},
		key.Filter{Focus:&i.tag, Name:key.NameF7, Optional:allMods},
		key.Filter{Focus:&i.tag, Name:key.NameF8, Optional:allMods},
		key.Filter{Focus:&i.tag, Name:key.NameF9, Optional:allMods},
		key.Filter{Focus:&i.tag, Name:key.NameF10, Optional:allMods},
		key.Filter{Focus:&i.tag, Name:key.NameF11, Optional:allMods},
		key.Filter{Focus:&i.tag, Name:key.NameF12, Optional:allMods},
		key.Filter{Focus:&i.tag, Optional:allMods},
	}

	for {
		ev, ok := gtx.Event(filters...)
		if !ok { break }
		switch ev := ev.(type) {
		case key.FocusEvent:
			if !ev.Focus {
				i.composing=false
				i.pendingComposition=""
			}
		case key.CompositionEvent:
			// Gio exposes the IME composition range separately from edit events.
			// While composition is active, keep replacement text local. Send only
			// the final edit once composition collapses.
			wasComposing:=i.composing
			i.composing = ev.Start != ev.End
			if wasComposing && !i.composing && i.pendingComposition!="" {
				i.emit([]byte(i.pendingComposition))
				i.pendingComposition=""
			}
		case key.EditEvent:
			if ev.Text=="" { continue }
			if i.composing {
				i.pendingComposition=ev.Text
			} else {
				i.emit([]byte(ev.Text))
			}
		case key.Event:
			if ev.State!=key.Press || ev.Modifiers.Contain(key.ModCommand) || ev.Modifiers.Contain(key.ModSuper) {
				continue
			}
			if seq:=EncodeKey(ev,snap.ApplicationCursor);len(seq)>0 {
				i.emit(seq)
			}
		}
	}
}

func (i *TerminalInput) Add(gtx layout.Context, size image.Point) {
	stack:=clip.Rect{Max:size}.Push(gtx.Ops)
	event.Op(gtx.Ops,&i.tag)
	key.InputHintOp{Tag:&i.tag,Hint:key.HintText}.Add(gtx.Ops)
	i.click.Add(gtx.Ops)
	stack.Pop()
}

func (i *TerminalInput) Focus(gtx layout.Context) {
	gtx.Execute(key.FocusCmd{Tag:&i.tag})
}

func (i *TerminalInput) emit(data []byte) {
	if len(data)==0 || i.OnInput==nil { return }
	i.OnInput(data)
}

func EncodeKey(ev key.Event, applicationCursor bool) []byte {
	mods:=ev.Modifiers
	modParam:=modifierParameter(mods)
	modified:=mods&(key.ModShift|key.ModAlt|key.ModCtrl)!=0

	final:=byte(0)
	switch ev.Name {
	case key.NameUpArrow: final='A'
	case key.NameDownArrow: final='B'
	case key.NameRightArrow: final='C'
	case key.NameLeftArrow: final='D'
	case key.NameHome: final='H'
	case key.NameEnd: final='F'
	}
	if final!=0 {
		if modified {
			return []byte("\x1b[1;"+itoaSmall(modParam)+string(final))
		}
		if applicationCursor {
			return []byte{'\x1b','O',final}
		}
		return []byte{'\x1b','[',final}
	}

	switch ev.Name {
	case key.NameReturn,key.NameEnter:
		if mods.Contain(key.ModAlt) { return []byte("\x1b\r") }
		return []byte("\r")
	case key.NameEscape:
		return []byte{0x1b}
	case key.NameTab:
		if mods.Contain(key.ModShift) { return []byte("\x1b[Z") }
		return []byte("\t")
	case key.NameDeleteBackward:
		if mods.Contain(key.ModAlt) { return []byte{0x1b,0x7f} }
		return []byte{0x7f}
	case key.NameDeleteForward:
		if modified { return []byte("\x1b[3;"+itoaSmall(modParam)+"~") }
		return []byte("\x1b[3~")
	case key.NamePageUp:
		if modified { return []byte("\x1b[5;"+itoaSmall(modParam)+"~") }
		return []byte("\x1b[5~")
	case key.NamePageDown:
		if modified { return []byte("\x1b[6;"+itoaSmall(modParam)+"~") }
		return []byte("\x1b[6~")
	}

	if fn:=functionKeyNumber(ev.Name);fn!=0 {
		if fn<=4 {
			finals:="PQRS"
			final:=finals[fn-1]
			if modified { return []byte("\x1b[1;"+itoaSmall(modParam)+string(final)) }
			return []byte{0x1b,'O',final}
		}
		codes:=[]string{"15","17","18","19","20","21","23","24"}
		code:=codes[fn-5]
		if modified { return []byte("\x1b["+code+";"+itoaSmall(modParam)+"~") }
		return []byte("\x1b["+code+"~")
	}

	name:=string(ev.Name)
	r,count:=utf8.DecodeRuneInString(name)
	if r==utf8.RuneError || count!=len(name) { return nil }
	if mods.Contain(key.ModCtrl) {
		if b,ok:=controlByte(r);ok {
			data:=[]byte{b}
			if mods.Contain(key.ModAlt) { data=append([]byte{0x1b},data...) }
			return data
		}
	}
	if mods.Contain(key.ModAlt) && !mods.Contain(key.ModCtrl) {
		return append([]byte{0x1b},[]byte(name)...)
	}
	return nil
}

func modifierParameter(mods key.Modifiers) int {
	value:=1
	if mods.Contain(key.ModShift){value+=1}
	if mods.Contain(key.ModAlt){value+=2}
	if mods.Contain(key.ModCtrl){value+=4}
	return value
}

func controlByte(r rune)(byte,bool){
	r=unicode.ToUpper(r)
	switch {
	case r>='@'&&r<='_':
		return byte(r-'@'),true
	case r=='?':
		return 0x7f,true
	case r==' ':
		return 0,true
	}
	return 0,false
}

func functionKeyNumber(name key.Name)int{
	names:=[]key.Name{key.NameF1,key.NameF2,key.NameF3,key.NameF4,key.NameF5,key.NameF6,key.NameF7,key.NameF8,key.NameF9,key.NameF10,key.NameF11,key.NameF12}
	for idx,candidate:=range names{if name==candidate{return idx+1}}
	return 0
}

func itoaSmall(v int)string{
	if v<10{return string(rune('0'+v))}
	var b strings.Builder
	b.WriteByte(byte('0'+v/10));b.WriteByte(byte('0'+v%10))
	return b.String()
}
