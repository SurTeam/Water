//go:build darwin

package goui

import (
	"image"
	"structs"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

var textCommandHooked atomic.Bool

func ensureTextCommandHook() {
	if textCommandHooked.Load() {
		return
	}
	class := objc.GetClass("EbitengineTextInputClient")
	if class == 0 {
		return
	}
	lib, err := purego.Dlopen("/usr/lib/libobjc.A.dylib", purego.RTLD_LAZY|purego.RTLD_GLOBAL)
	if err != nil {
		return
	}
	var (
		getMethod func(uintptr, uintptr) uintptr
		setImpl   func(uintptr, uintptr) uintptr
		addMethod func(uintptr, uintptr, uintptr, uintptr) bool
		selName   func(uintptr) unsafe.Pointer
	)
	purego.RegisterLibFunc(&getMethod, lib, "class_getInstanceMethod")
	purego.RegisterLibFunc(&setImpl, lib, "method_setImplementation")
	purego.RegisterLibFunc(&addMethod, lib, "class_addMethod")
	purego.RegisterLibFunc(&selName, lib, "sel_getName")
	method := getMethod(uintptr(class), uintptr(objc.RegisterName("doCommandBySelector:")))
	if method == 0 {
		return
	}
	callback := purego.NewCallback(func(_, _ uintptr, command uintptr) {
		if spec, ok := editingSpecFromCurrentEvent(); ok {
			noteTextCommandSpec(spec)
			return
		}
		noteTextCommand(readCString(selName(command)))
	})
	setImpl(method, callback)
	addMethod(uintptr(class), uintptr(objc.RegisterName("keyDown:")), purego.NewCallback(func(self, cmd, event uintptr) {
		if spec, ok := editingSpecFromEvent(objc.ID(event)); ok {
			noteTextCommandSpec(spec)
			return
		}
		objc.SendSuper[uintptr](objc.ID(self), objc.SEL(cmd), objc.ID(event))
	}), uintptr(unsafe.Pointer(&keyDownTypeEncoding[0])))
	textCommandHooked.Store(true)
}

// v@:@ is void keyDown:(id)event. Kept alive for the ObjC runtime.
var keyDownTypeEncoding = []byte{'v', '@', ':', '@', 0}

const (
	nsEventModifierShift   = 1 << 17
	nsEventModifierControl = 1 << 18
	nsEventModifierOption  = 1 << 19
	nsEventModifierCommand = 1 << 20
)

func editingSpecFromCurrentEvent() (string, bool) {
	event := macApplication().Send(objc.RegisterName("currentEvent"))
	if event == 0 {
		return "", false
	}
	return editingSpecFromEvent(event)
}

func editingSpecFromEvent(event objc.ID) (string, bool) {
	if imeComposing.Load() || event == 0 {
		return "", false
	}
	flags := objc.Send[uint64](event, objc.RegisterName("modifierFlags"))
	if flags&(nsEventModifierControl|nsEventModifierOption|nsEventModifierCommand) != 0 {
		return "", false
	}
	spec, ok := macVirtualKeySpec(objc.Send[uint16](event, objc.RegisterName("keyCode")))
	if !ok {
		return "", false
	}
	if flags&nsEventModifierShift != 0 {
		spec = "shift-" + spec
	}
	return spec, true
}

func applySystemKeyRepeat(r *nativeKeyRepeater) {
	nsEvent := objc.ID(objc.GetClass("NSEvent"))
	delay := objc.Send[float64](nsEvent, objc.RegisterName("keyRepeatDelay"))
	every := objc.Send[float64](nsEvent, objc.RegisterName("keyRepeatInterval"))
	r.useSystemRepeat(time.Duration(delay*float64(time.Second)), time.Duration(every*float64(time.Second)))
}

func readCString(ptr unsafe.Pointer) string {
	if ptr == nil {
		return ""
	}
	raw := unsafe.Slice((*byte)(ptr), 128)
	n := 0
	for n < len(raw) && raw[n] != 0 {
		n++
	}
	return string(raw[:n])
}

type macPoint struct {
	_    structs.HostLayout
	x, y float64
}
type macSize struct {
	_             structs.HostLayout
	width, height float64
}
type macRect struct {
	_      structs.HostLayout
	origin macPoint
	size   macSize
}

// Move the live text-input view. Restarting the session would clear keystrokes
// that arrived after the caret moved.
//
// On macOS, GLFW and the text-input view are already in AppKit points.
// Multiplying by the backing scale places the candidate window about one
// full screen away from the caret.
func repositionTextInput(bounds image.Rectangle, scale float64) {
	x, y, width, height, ok := imeCaretFrame(bounds, scale)
	if !ok {
		return
	}
	window := macApplication().Send(objc.RegisterName("mainWindow"))
	if window == 0 {
		return
	}
	content := window.Send(objc.RegisterName("contentView"))
	if content == 0 {
		return
	}
	frame := objc.Send[macRect](content, objc.RegisterName("frame"))
	view := textInputView(content)
	if view == 0 {
		return
	}
	view.Send(objc.RegisterName("setFrame:"), macRect{
		origin: macPoint{x: x, y: frame.size.height - y},
		size:   macSize{width: width, height: height},
	})
}

func textInputView(content objc.ID) objc.ID {
	class := objc.ID(objc.GetClass("EbitengineTextInputClient"))
	subs := content.Send(objc.RegisterName("subviews"))
	if subs == 0 || class == 0 {
		return 0
	}
	count := objc.Send[uintptr](subs, objc.RegisterName("count"))
	for i := uintptr(0); i < count; i++ {
		view := subs.Send(objc.RegisterName("objectAtIndex:"), i)
		if view.Send(objc.RegisterName("isKindOfClass:"), class) != 0 {
			return view
		}
	}
	return 0
}
