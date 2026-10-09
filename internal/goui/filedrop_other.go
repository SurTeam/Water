//go:build !darwin

package goui

// clipboardFileURLs returns absolute file paths from the clipboard when it
// contains file/folder references. On non-macOS platforms this is a no-op.
func clipboardFileURLs() []string { return nil }