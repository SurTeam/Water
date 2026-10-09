//go:build darwin

package goui

import (
	"errors"

	"github.com/ebitengine/purego/objc"
)

// AppKit owns the panel and callback on its main queue; the GUI frame loop
// never waits for a modal dialog or performs a file read.
func (p *macNativePlatform) ChooseFile(title string) (string, error) {
	result := make(chan string, 1)
	if !p.onMain(func() {
		panel := macSend(macClass("NSOpenPanel"), "openPanel")
		p.filePanel = panel
		p.fileChoice = ""
		macSend(panel, "setTitle:", macText(title))
		macSend(panel, "setCanChooseFiles:", true)
		macSend(panel, "setCanChooseDirectories:", false)
		macSend(panel, "setAllowsMultipleSelection:", false)
		callback := objc.NewBlock(func(_ objc.Block, response int64) {
			path := ""
			if response == 1 {
				path = macString(macSend(macSend(panel, "URL"), "path"))
			}
			if p.fileChoice != "" {
				path = p.fileChoice
			}
			p.filePanel = 0
			p.fileChoice = ""
			p.filePickerOpen.Store(false)
			result <- path
		})
		macSend(panel, "beginWithCompletionHandler:", callback)
		p.filePickerOpen.Store(true)
		callback.Release()
	}) {
		return "", errors.New("file picker unavailable; try again")
	}
	return <-result, nil
}
