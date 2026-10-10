//go:build !darwin

package goui

import "image"

func ensureTextCommandHook() {}

func applySystemKeyRepeat(*nativeKeyRepeater) {}

func repositionTextInput(image.Rectangle, float64) {}
