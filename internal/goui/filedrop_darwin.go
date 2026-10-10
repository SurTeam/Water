//go:build darwin

package goui

import (
	"strings"
	"unsafe"

	"github.com/ebitengine/purego/objc"
)

// clipboardFileURLs returns absolute file paths from the macOS general
// pasteboard when it contains file/folder references. Finder stores one
// public.file-url per pasteboard item (a file URL, not a property list), so
// reading a single dataForType blob as an NSArray misses the drop.
func clipboardFileURLs() []string {
	pool := macSend(macClass("NSAutoreleasePool"), "new")
	defer macSend(pool, "release")
	pb := macSend(macClass("NSPasteboard"), "generalPasteboard")
	if pb == 0 {
		return nil
	}
	// Finder's public.file-url values are often file-reference URLs
	// (file:///.file/id=...), while this legacy array is already a POSIX path.
	if paths := pasteboardFilenameList(pb); len(paths) > 0 {
		return paths
	}
	if paths := pasteboardFileURLs(pb); len(paths) > 0 {
		return paths
	}
	return pasteboardItemFileURLs(pb)
}

func pasteboardFileURLs(pb objc.ID) []string {
	classes := macSend(macClass("NSArray"), "arrayWithObject:", macClass("NSURL"))
	yes := macSend(macClass("NSNumber"), "numberWithBool:", true)
	options := macSend(macClass("NSDictionary"), "dictionaryWithObject:forKey:", yes, macText("NSPasteboardURLReadingFileURLsOnlyKey"))
	urls := macSend(pb, "readObjectsForClasses:options:", classes, options)
	return fileURLPaths(urls)
}

func pasteboardItemFileURLs(pb objc.ID) []string {
	items := macSend(pb, "pasteboardItems")
	n := int(macSend(items, "count"))
	if n == 0 {
		return nil
	}
	urlType := macText("public.file-url")
	var paths []string
	for i := 0; i < n; i++ {
		item := macSend(items, "objectAtIndex:", i)
		raw := macString(macSend(item, "stringForType:", urlType))
		if raw == "" {
			continue
		}
		url := macSend(macClass("NSURL"), "URLWithString:", macText(raw))
		if p := fileSystemPath(url); p != "" {
			paths = append(paths, p)
		}
	}
	return paths
}

func pasteboardFilenameList(pb objc.ID) []string {
	// Finder still publishes this legacy absolute-path array alongside file URLs.
	arr := macSend(pb, "propertyListForType:", macText("NSFilenamesPboardType"))
	n := int(macSend(arr, "count"))
	if n == 0 {
		return nil
	}
	paths := make([]string, 0, n)
	for i := 0; i < n; i++ {
		if p := macString(macSend(arr, "objectAtIndex:", i)); p != "" {
			paths = append(paths, p)
		}
	}
	return paths
}

func fileURLPaths(urls objc.ID) []string {
	n := int(macSend(urls, "count"))
	if n == 0 {
		return nil
	}
	paths := make([]string, 0, n)
	for i := 0; i < n; i++ {
		if p := fileSystemPath(macSend(urls, "objectAtIndex:", i)); p != "" {
			paths = append(paths, p)
		}
	}
	return paths
}

func fileSystemPath(url objc.ID) string {
	if url == 0 {
		return ""
	}
	var resolved objc.ID
	macSend(url, "getResourceValue:forKey:error:", &resolved, macText("NSURLPathKey"), objc.ID(0))
	if p := usableFilePath(macString(resolved)); p != "" {
		return p
	}
	if p := usableFilePath(macCString(macSend(url, "fileSystemRepresentation"))); p != "" {
		return p
	}
	return usableFilePath(macString(macSend(url, "path")))
}

func usableFilePath(path string) string {
	if path == "" || strings.HasPrefix(path, "/.file/") {
		return ""
	}
	return path
}

func macCString(ptr objc.ID) string {
	if ptr == 0 {
		return ""
	}
	p := unsafe.Pointer(uintptr(ptr))
	var n int
	for *(*byte)(unsafe.Add(p, n)) != 0 {
		n++
	}
	return string(unsafe.Slice((*byte)(p), n))
}

// RegisterFileDrop records the callback for API compatibility. On macOS the
// GLFW content view already owns NSDraggingDestination, so registering types
// here never receives performDragOperation:. Drops are read from
// ebiten.DroppedFiles, which exposes absolute paths.
func (p *macNativePlatform) RegisterFileDrop(onDrop func(paths []string)) {
	p.onDrop = onDrop
}
