//go:build darwin

package goui

// clipboardFileURLs returns absolute file paths from the macOS general
// pasteboard when it contains file/folder references (public.file-url type).
// Returns nil if no file URLs are present.
func clipboardFileURLs() []string {
	pool := macSend(macClass("NSAutoreleasePool"), "new")
	defer macSend(pool, "release")
	pb := macSend(macClass("NSPasteboard"), "generalPasteboard")
	urlType := macText("public.file-url")
	data := macSend(pb, "dataForType:", urlType)
	if data == 0 {
		return nil
	}
	arr := macSend(macClass("NSArray"), "propertyListFromData:", data)
	if arr == 0 {
		return nil
	}
	count := int(macSend(arr, "count"))
	if count == 0 {
		return nil
	}
	paths := make([]string, 0, count)
	for i := 0; i < count; i++ {
		url := macSend(arr, "objectAtIndex:", i)
		if url == 0 {
			continue
		}
		path := macString(macSend(url, "path"))
		if path != "" {
			paths = append(paths, path)
		}
	}
	return paths
}

// RegisterFileDrop makes the key window's content view accept file drags.
// Dropped file paths are delivered to onDrop on the main thread.
func (p *macNativePlatform) RegisterFileDrop(onDrop func(paths []string)) {
	p.onDrop = onDrop
	p.onMain(func() {
		if p.window == 0 {
			p.window = macSend(macApplication(), "keyWindow")
		}
		if p.window == 0 {
			return
		}
		view := macSend(p.window, "contentView")
		if view == 0 {
			return
		}
		urlType := macText("public.file-url")
		types := macSend(macClass("NSArray"), "arrayWithObject:", urlType)
		macSend(view, "registerForDraggedTypes:", types)
		p.dropView = view
	})
}