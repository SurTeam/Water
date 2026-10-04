//go:build darwin || linux

package goui

import (
	"os"

	"golang.org/x/sys/unix"
)

// Each view owns its mapping, independently of the font file descriptor.
// Shared read-only pages replace a private heap copy of the entire glyf table.
func mapNativeFontTable(file *os.File, offset int64, length int) ([]byte, func(), error) {
	page := int64(os.Getpagesize())
	start := offset / page * page
	delta := int(offset - start)
	data, err := unix.Mmap(int(file.Fd()), start, delta+length, unix.PROT_READ, unix.MAP_SHARED)
	if err != nil {
		return nil, nil, err
	}
	return data[delta : delta+length], func() { _ = unix.Munmap(data) }, nil
}
