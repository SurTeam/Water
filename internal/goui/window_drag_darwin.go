//go:build darwin

package goui

import (
	"sync/atomic"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

var (
	liveWindow         atomic.Pointer[EbitengineWindow]
	windowDragHooked   atomic.Bool
	nativeDragActive   atomic.Bool
	originalMouseDown  uintptr
	windowDragCallback uintptr
)

func noteLiveWindow(w *EbitengineWindow) { liveWindow.Store(w) }

func ensureWindowDragHook() {
	if windowDragHooked.Load() {
		return
	}
	class := objc.GetClass("GLFWContentView")
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
	)
	purego.RegisterLibFunc(&getMethod, lib, "class_getInstanceMethod")
	purego.RegisterLibFunc(&setImpl, lib, "method_setImplementation")
	method := getMethod(uintptr(class), uintptr(objc.RegisterName("mouseDown:")))
	if method == 0 {
		return
	}
	windowDragCallback = purego.NewCallback(func(self, cmd, event uintptr) {
		if startNativeTitlebarDrag(objc.ID(self), objc.ID(event)) {
			return
		}
		if imp := originalMouseDown; imp != 0 {
			purego.SyscallN(imp, self, cmd, event)
		}
	})
	originalMouseDown = setImpl(method, windowDragCallback)
	windowDragHooked.Store(true)
}

// startNativeTitlebarDrag moves the window inside AppKit's mouse-down tracking
// loop. Doing it from the frame loop waits for vsync and a full redraw, so the
// window trails the cursor.
func startNativeTitlebarDrag(view, event objc.ID) bool {
	w := liveWindow.Load()
	if w == nil || view == 0 || event == 0 {
		return false
	}
	if !nativeDragActive.CompareAndSwap(false, true) {
		return true
	}
	defer nativeDragActive.Store(false)
	snapshot := w.dragTargets.Load()
	if snapshot == nil {
		return false
	}
	frame := objc.Send[macRect](view, objc.RegisterName("frame"))
	loc := objc.Send[macPoint](event, objc.RegisterName("locationInWindow"))
	kind := topDragTarget(snapshot.targets, loc.x, frame.size.height-loc.y)
	if kind != hitTitlebar {
		return false
	}
	if objc.Send[int64](event, objc.RegisterName("clickCount")) >= 2 {
		w.titlebarZoom.Store(true)
		return true
	}
	window := macSend(view, "window")
	if window == 0 || macSend(window, "isZoomed") != 0 {
		return false
	}
	sel := objc.RegisterName("performWindowDragWithEvent:")
	if macSend(window, "respondsToSelector:", sel) == 0 {
		return false
	}
	macSend(window, "setMovable:", true)
	macSend(window, "performWindowDragWithEvent:", event)
	return true
}
