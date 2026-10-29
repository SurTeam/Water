//go:build !windows

package goplatform

import (
	"os"
	"path/filepath"
)

// PublishFile publishes an already synced temporary file and persists its name.
func PublishFile(source, target string) error {
	if err := os.Rename(source, target); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(target))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
