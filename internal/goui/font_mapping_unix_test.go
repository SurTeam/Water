//go:build darwin || linux

package goui

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestMappedFontTableSurvivesFileCloseAndChecksVirtualBounds(t *testing.T) {
	data := bytes.Repeat([]byte("immutable-font-table"), 1000)
	path := filepath.Join(t.TempDir(), "font")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	resource := nativeFontResource{file: file, headerSize: 128, fileSize: int64(len(data))}
	view, release, err := resource.View(128+37, 9000)
	if err != nil {
		file.Close()
		t.Fatal(err)
	}
	defer release()
	file.Close()
	if !bytes.Equal(view, data[37:9037]) {
		t.Fatal("unaligned mapped table differs after file close")
	}
	for _, offset := range []int64{0, 127, 128 + int64(len(data)) - 1} {
		if _, _, err := resource.View(offset, 100); err == nil {
			t.Fatal("accepted invalid virtual offset", offset)
		}
	}
}
