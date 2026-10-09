package goui

import (
	"fmt"
	"strings"
	"sync/atomic"

	"gioui.org/io/key"
	"github.com/SurTeam/Water/internal/goconfig"
	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

type macMenuItem struct {
	menu, item objc.ID
	index      int
}
type macMenuState struct {
	title, key string
	modifiers  uintptr
	enabled    bool
}

type macNativePlatform struct {
	emit          func(string)
	canQuitServer bool
	mainQueue     uintptr
	dispatch      func(uintptr, objc.Block)
	controller    objc.ID
	// AppKit objects and the item map are owned exclusively by the main queue.
	items              map[string]macMenuItem
	configured         goconfig.AppConfig
	menuTitles         map[objc.ID]string
	menuState          map[string]macMenuState
	publishedMenu      map[string]any
	menuDirty          bool
	window             objc.ID
	layer              objc.ID
	radius             float64
	installed          bool
	pending            atomic.Bool
	pendingCalls       atomic.Int32
	state              atomic.Pointer[map[string]any]
	notificationStatus atomic.Pointer[string]
	filePanel          objc.ID // main queue only
	fileChoice         string  // explicit control API selection, main queue only
	filePickerOpen     atomic.Bool
}

func macSend(id objc.ID, selector string, args ...any) objc.ID {
	return id.Send(objc.RegisterName(selector), args...)
}
func macClass(name string) objc.ID { return objc.ID(objc.GetClass(name)) }
func macText(value string) objc.ID {
	return macSend(macClass("NSString"), "stringWithUTF8String:", value)
}
func macString(value objc.ID) string {
	if value == 0 {
		return ""
	}
	return objc.Send[string](value, objc.RegisterName("UTF8String"))
}
func macApplication() objc.ID { return macSend(macClass("NSApplication"), "sharedApplication") }

func newNativePlatform(emit func(string), canQuitServer bool) (nativePlatform, error) {
	p := &macNativePlatform{emit: emit, canQuitServer: canQuitServer, items: map[string]macMenuItem{}}
	lib, err := purego.Dlopen("/usr/lib/libSystem.B.dylib", purego.RTLD_LAZY|purego.RTLD_GLOBAL)
	if err != nil {
		return nil, fmt.Errorf("load main queue: %w", err)
	}
	// dispatch_get_main_queue is an SDK macro; the exported queue object is
	// the address of _dispatch_main_q, not a callable function symbol.
	p.mainQueue, err = purego.Dlsym(lib, "_dispatch_main_q")
	if err != nil {
		return nil, fmt.Errorf("resolve main queue: %w", err)
	}
	purego.RegisterLibFunc(&p.dispatch, lib, "dispatch_async")
	class, err := objc.RegisterClass("WaterApplicationMenu", objc.GetClass("NSObject"), nil, nil, []objc.MethodDef{{
		Cmd: objc.RegisterName("waterMenuAction:"),
		Fn: func(_ objc.ID, _ objc.SEL, sender objc.ID) {
			for action, ref := range p.items {
				if ref.item == sender {
					p.emit(action)
					return
				}
			}
		},
	}})
	if err != nil {
		return nil, fmt.Errorf("register Water menu: %w", err)
	}
	p.controller = macSend(objc.ID(class), "new")
	return p, nil
}

func (p *macNativePlatform) onMain(fn func()) bool {
	if p.pendingCalls.Add(1) > 32 {
		p.pendingCalls.Add(-1)
		return false
	}
	block := objc.NewBlock(func(_ objc.Block) {
		defer p.pendingCalls.Add(-1)
		pool := macSend(macClass("NSAutoreleasePool"), "new")
		defer macSend(pool, "release")
		fn()
	})
	p.dispatch(p.mainQueue, block)
	block.Release()
	return true
}

// Leave system menu actions clickable, but remove GLFW's fixed shortcuts so
// AppKit cannot steal bindings such as Cmd+H (pane focus) or Cmd+Q (ignore).
func (p *macNativePlatform) install() {
	main := macSend(macApplication(), "mainMenu")
	var windowMenu objc.ID
	for i := 0; i < int(macSend(main, "numberOfItems")); i++ {
		menu := macSend(macSend(main, "itemAtIndex:", i), "submenu")
		for j := 0; j < int(macSend(menu, "numberOfItems")); j++ {
			item := macSend(menu, "itemAtIndex:", j)
			macSend(item, "setKeyEquivalent:", macText(""))
			action := objc.SEL(macSend(item, "action"))
			name := ""
			switch action {
			case objc.RegisterName("hide:"):
				name = "hide-window"
			case objc.RegisterName("terminate:"):
				name = "quit-gui"
				macSend(item, "setTitle:", macText("Quit GUI"))
			case objc.RegisterName("performMiniaturize:"):
				name = "minimize-window"
				windowMenu = menu
			}
			if name != "" {
				p.bind(name, menu, item, j)
			}
		}
	}
	appMenu := macSend(macSend(main, "itemAtIndex:", 0), "submenu")
	macSend(appMenu, "setAutoenablesItems:", false)
	p.add("check-updates", "Software update", appMenu)
	p.add("quit-and-server", "Quit GUI and Local Server", appMenu)
	macSend(p.items["quit-and-server"].item, "setEnabled:", p.canQuitServer)
	if windowMenu != 0 {
		p.add("show-window", "Show Water", windowMenu)
	}
	p.installed = true
	p.menuTitles = map[objc.ID]string{}
	var collect func(objc.ID)
	collect = func(menu objc.ID) {
		for i := 0; i < int(macSend(menu, "numberOfItems")); i++ {
			item := macSend(menu, "itemAtIndex:", i)
			p.menuTitles[item] = macString(macSend(item, "title"))
			if submenu := macSend(item, "submenu"); submenu != 0 {
				collect(submenu)
			}
		}
	}
	collect(main)
}
func (p *macNativePlatform) bind(name string, menu, item objc.ID, index int) {
	macSend(item, "setTarget:", p.controller)
	macSend(item, "setAction:", objc.RegisterName("waterMenuAction:"))
	macSend(item, "setEnabled:", true)
	p.items[name] = macMenuItem{menu, item, index}
}
func (p *macNativePlatform) add(name, title string, menu objc.ID) {
	index := int(macSend(menu, "numberOfItems"))
	item := macSend(menu, "addItemWithTitle:action:keyEquivalent:", macText(title), objc.RegisterName("waterMenuAction:"), macText(""))
	p.bind(name, menu, item, index)
}

func macMenuEquivalent(binding string) (string, uintptr) {
	ev, ok := automationInputEvent(binding)
	pressed, isKey := ev.(key.Event)
	if !ok || !isKey || len(pressed.Name) != 1 {
		return "", 0
	}
	var mask uintptr
	if pressed.Modifiers.Contain(key.ModCommand) || pressed.Modifiers.Contain(key.ModSuper) {
		mask |= 1 << 20
	}
	if pressed.Modifiers.Contain(key.ModShift) {
		mask |= 1 << 17
	}
	if pressed.Modifiers.Contain(key.ModCtrl) {
		mask |= 1 << 18
	}
	if pressed.Modifiers.Contain(key.ModAlt) {
		mask |= 1 << 19
	}
	return strings.ToLower(string(pressed.Name)), mask
}

func (p *macNativePlatform) Update(cfg goconfig.AppConfig) {
	if !p.pending.CompareAndSwap(false, true) {
		return
	}
	if !p.onMain(func() {
		defer p.pending.Store(false)
		if end := traceNativeWork("platform.update"); end != nil {
			defer end()
		}
		if !p.installed {
			p.install()
		}
		if p.configured.Shortcuts != cfg.Shortcuts {
			p.menuDirty = true
			for action, binding := range map[string]string{"hide-window": cfg.Shortcuts.HideWindow, "minimize-window": cfg.Shortcuts.MinimizeWindow} {
				ref := p.items[action]
				name, mask := macMenuEquivalent(binding)
				macSend(ref.item, "setKeyEquivalent:", macText(name))
				macSend(ref.item, "setKeyEquivalentModifierMask:", mask)
			}
		}
		p.updateCorners(float64(cfg.UI.WindowCornerRadius))
		if p.configured.UI.Language != cfg.UI.Language {
			p.menuDirty = true
			for item, title := range p.menuTitles {
				macSend(item, "setTitle:", macText(ctext(cfg.UI.Language, title)))
			}
			for action, title := range map[string]string{"hide-window": "Hide Water", "minimize-window": "Minimize", "quit-gui": "Quit GUI", "quit-and-server": "Quit GUI and Local Server", "show-window": "Show Water"} {
				if ref, ok := p.items[action]; ok {
					macSend(ref.item, "setTitle:", macText(ctext(cfg.UI.Language, title)))
				}
			}
		}
		p.configured = cfg
		p.publish()
	}) {
		p.pending.Store(false)
	}
}
func (p *macNativePlatform) updateCorners(radius float64) {
	if p.window == 0 {
		p.window = macSend(macApplication(), "keyWindow")
		if p.window == 0 {
			return
		}
		view := macSend(p.window, "contentView")
		macSend(view, "setWantsLayer:", true)
		p.layer = macSend(view, "layer")
		macSend(p.window, "setOpaque:", false)
		macSend(p.window, "setBackgroundColor:", macSend(macClass("NSColor"), "clearColor"))
		macSend(p.window, "setHasShadow:", true)
		p.radius = -1
	}
	if p.radius == radius {
		return
	}
	macSend(p.layer, "setCornerRadius:", radius)
	macSend(p.layer, "setMasksToBounds:", true)
	p.radius = radius
	macSend(p.window, "invalidateShadow")
}

// Menu properties are owned by the main queue. Read configuration-owned
// properties only when dirty, but observe enabled state on every publication.
// New menu maps never mutate snapshots still held by the renderer or control API.
func (p *macNativePlatform) snapshotMenu(read func(macMenuItem, bool) macMenuState) (map[string]any, bool) {
	refresh := p.menuDirty || p.publishedMenu == nil
	if p.menuState == nil {
		p.menuState = make(map[string]macMenuState, len(p.items))
	}
	items, changed := p.publishedMenu, false
	for action, ref := range p.items {
		previous, exists := p.menuState[action]
		current := previous
		observed := read(ref, refresh)
		if refresh {
			current.title, current.key, current.modifiers = observed.title, observed.key, observed.modifiers
		}
		current.enabled = observed.enabled
		if !exists || current != previous {
			if !changed {
				items = make(map[string]any, len(p.items))
				for key, value := range p.publishedMenu {
					items[key] = value
				}
				changed = true
			}
			items[action] = map[string]any{"title": current.title, "key": current.key, "modifiers": current.modifiers, "enabled": current.enabled}
			p.menuState[action] = current
		}
	}
	p.menuDirty = false
	p.publishedMenu = items
	return items, changed
}

func (p *macNativePlatform) publish() {
	if end := traceNativeWork("platform.publish"); end != nil {
		defer end()
	}
	items, menuChanged := p.snapshotMenu(func(ref macMenuItem, full bool) macMenuState {
		value := macMenuState{enabled: macSend(ref.item, "isEnabled") != 0}
		if full {
			value.title = macString(macSend(ref.item, "title"))
			value.key = macString(macSend(ref.item, "keyEquivalent"))
			value.modifiers = uintptr(macSend(ref.item, "keyEquivalentModifierMask"))
		}
		return value
	})
	hidden := macSend(macApplication(), "isHidden") != 0
	var number uintptr
	var occluded, masks, opaque bool
	var radius float64
	if p.layer != 0 {
		number = uintptr(macSend(p.window, "windowNumber"))
		occluded = macSend(p.window, "occlusionState")&(1<<1) == 0
		radius = objc.Send[float64](p.layer, objc.RegisterName("cornerRadius"))
		masks = macSend(p.layer, "masksToBounds") != 0
		opaque = macSend(p.window, "isOpaque") != 0
	}
	if previous := p.state.Load(); previous != nil && !menuChanged && (*previous)["ready"] == p.installed && (*previous)["hidden"] == hidden {
		if p.layer == 0 || ((*previous)["window_number"] == number && (*previous)["occluded"] == occluded && (*previous)["window_corner_radius"] == radius && (*previous)["window_masks_to_bounds"] == masks && (*previous)["window_opaque"] == opaque) {
			return
		}
	}
	snapshot := map[string]any{"ready": p.installed, "hidden": hidden, "items": items}
	if p.layer != 0 {
		snapshot["window_number"] = number
		// NSWindowOcclusionStateVisible is bit 1: an absent bit means the
		// entire window is covered, hidden, minimized, or on another Space.
		snapshot["occluded"] = occluded
		snapshot["window_corner_radius"] = radius
		snapshot["window_masks_to_bounds"] = masks
		snapshot["window_opaque"] = opaque
	}
	p.state.Store(&snapshot)
}
func (p *macNativePlatform) Snapshot() map[string]any {
	if snapshot := p.state.Load(); snapshot != nil {
		copy := make(map[string]any, len(*snapshot)+1)
		for key, value := range *snapshot {
			copy[key] = value
		}
		if status := p.notificationStatus.Load(); status != nil {
			copy["notification_status"] = *status
		}
		copy["file_picker_open"] = p.filePickerOpen.Load()
		return copy
	}
	return map[string]any{"ready": false, "hidden": false}
}
func (p *macNativePlatform) Hide() {
	p.onMain(func() { macSend(macApplication(), "hide:", objc.ID(0)); p.publish() })
}
func (p *macNativePlatform) Show() {
	p.onMain(func() {
		macSend(macApplication(), "unhide:", objc.ID(0))
		macSend(macApplication(), "activateIgnoringOtherApps:", true)
		if p.window != 0 {
			macSend(p.window, "makeKeyAndOrderFront:", objc.ID(0))
		}
		p.publish()
	})
}
func (p *macNativePlatform) Invoke(action string) error {
	if action == "file-picker-cancel" || strings.HasPrefix(action, "file-picker-select:") {
		if !p.filePickerOpen.Load() {
			return fmt.Errorf("no file picker is open")
		}
		if !p.onMain(func() {
			if p.filePanel == 0 {
				return
			}
			if strings.HasPrefix(action, "file-picker-select:") {
				p.fileChoice = strings.TrimPrefix(action, "file-picker-select:")
			}
			macSend(p.filePanel, "cancel:", objc.ID(0))
		}) {
			return fmt.Errorf("native queue is busy")
		}
		return nil
	}
	snapshot := p.Snapshot()
	items, ok := snapshot["items"].(map[string]any)
	if !ok {
		return fmt.Errorf("native menu is not ready")
	}
	item, ok := items[action].(map[string]any)
	if !ok || item["enabled"] != true {
		return fmt.Errorf("menu action %q is unavailable", action)
	}
	if !p.onMain(func() { ref := p.items[action]; macSend(ref.menu, "performActionForItemAtIndex:", ref.index) }) {
		return fmt.Errorf("native menu queue is busy")
	}
	return nil
}
