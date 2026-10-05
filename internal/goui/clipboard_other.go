//go:build !darwin

package goui

import "golang.design/x/clipboard"

func clipboardHasImage() bool { return len(clipboard.Read(clipboard.FmtImage)) > 0 }
