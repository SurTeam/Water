//go:build !darwin

package goui

func noteLiveWindow(*EbitengineWindow) {}

func ensureWindowDragHook() {}

func titlebarDragSuppressed() bool { return true }

func fitWindowHeightToScreen() {}
