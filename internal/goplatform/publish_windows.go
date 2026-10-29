//go:build windows

package goplatform

import "golang.org/x/sys/windows"

// Windows does not allow fsync on a directory handle opened by os.Open. Use a
// write-through replacement after the caller has flushed and closed the file.
func PublishFile(source, target string) error {
	from, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}
