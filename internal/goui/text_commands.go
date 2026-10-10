package goui

import (
	"image"
	"sync"
	"sync/atomic"
)

// imeComposing is set while marked text is on screen. Editing keys then stay
// with the input method instead of the terminal.
var imeComposing atomic.Bool

// imeCaretFrame converts a framebuffer caret into AppKit points, top-left
// origin. macOS text views are in points; the framebuffer is already
// points times the display scale, so the scale must be divided out once.
func imeCaretFrame(bounds image.Rectangle, scale float64) (x, bottom, width, height float64, ok bool) {
	if bounds.Empty() {
		return 0, 0, 0, 0, false
	}
	if scale < 1 {
		scale = 1
	}
	return float64(bounds.Min.X)/scale + 6, float64(bounds.Max.Y) / scale, float64(max(bounds.Dx(), 1)) / scale, float64(max(bounds.Dy(), 1)) / scale, true
}

// Text commands are delivered by the platform input method, one OS event at a
// time. They are not sampled from a key-repeat timer.
var textCommandQueue struct {
	mu    sync.Mutex
	specs []string
}

// macVirtualKeySpec distinguishes Backspace (left) from Forward Delete (right).
// macOS otherwise delivers both as the DEL character, which the tty erases backward.
func macVirtualKeySpec(code uint16) (string, bool) {
	switch code {
	case 0x33:
		return "Backspace", true
	case 0x75:
		return "Delete", true
	case 0x7B:
		return "Left", true
	case 0x7C:
		return "Right", true
	case 0x7E:
		return "Up", true
	case 0x7D:
		return "Down", true
	case 0x24, 0x4C:
		return "Return", true
	case 0x30:
		return "Tab", true
	case 0x73:
		return "Home", true
	case 0x77:
		return "End", true
	case 0x74:
		return "PageUp", true
	case 0x79:
		return "PageDown", true
	case 0x35:
		return "Escape", true
	default:
		return "", false
	}
}

func textCommandSpec(selector string) (string, bool) {
	switch selector {
	case "deleteBackward:":
		return "Backspace", true
	case "deleteForward:":
		return "Delete", true
	case "deleteWordBackward:":
		return "alt-Backspace", true
	case "deleteWordForward:":
		return "alt-Delete", true
	case "insertNewline:", "insertLineBreak:":
		return "Return", true
	case "insertTab:":
		return "Tab", true
	case "moveLeft:":
		return "Left", true
	case "moveRight:":
		return "Right", true
	case "moveUp:":
		return "Up", true
	case "moveDown:":
		return "Down", true
	case "moveToBeginningOfLine:", "moveToLeftEndOfLine:":
		return "Home", true
	case "moveToEndOfLine:", "moveToRightEndOfLine:":
		return "End", true
	case "scrollPageUp:", "pageUp:":
		return "PageUp", true
	case "scrollPageDown:", "pageDown:":
		return "PageDown", true
	case "cancelOperation:":
		return "Escape", true
	default:
		return "", false
	}
}

func noteTextCommand(selector string) {
	spec, ok := textCommandSpec(selector)
	if !ok {
		return
	}
	noteTextCommandSpec(spec)
}

func noteTextCommandSpec(spec string) {
	if spec == "" {
		return
	}
	textCommandQueue.mu.Lock()
	textCommandQueue.specs = append(textCommandQueue.specs, spec)
	textCommandQueue.mu.Unlock()
}

func takeTextCommands() []string {
	textCommandQueue.mu.Lock()
	defer textCommandQueue.mu.Unlock()
	if len(textCommandQueue.specs) == 0 {
		return nil
	}
	specs := textCommandQueue.specs
	textCommandQueue.specs = nil
	return specs
}
