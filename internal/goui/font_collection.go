package goui

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"

	"github.com/hajimehoshi/ebiten/v2/text/v2"
)

// A TTC can contain dozens of large CJK faces. The collection constructor
// parses them all, even when the caller only wants one. Present the selected
// face as a standalone SFNT directory, backed by the original file's tables.
// Only directory bytes are copied; the font parser reads the selected tables.
func loadNativeFontSource(path string, index int) (*text.GoTextFaceSource, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	var initial [12]byte
	if _, err := file.ReadAt(initial[:], 0); err != nil {
		return nil, err
	}
	if string(initial[:4]) != "ttcf" {
		if index != 0 {
			return nil, fmt.Errorf("font face index %d outside single font", index)
		}
		return text.NewGoTextFaceSource(file)
	}
	count := binary.BigEndian.Uint32(initial[8:])
	if index < 0 || uint64(index) >= uint64(count) {
		return nil, fmt.Errorf("font face index %d outside collection of %d", index, count)
	}
	var offset [4]byte
	if _, err := file.ReadAt(offset[:], 12+int64(index)*4); err != nil {
		return nil, err
	}
	start := int64(binary.BigEndian.Uint32(offset[:]))
	if _, err := file.ReadAt(initial[:], start); err != nil {
		return nil, err
	}
	header := make([]byte, 12+16*int(binary.BigEndian.Uint16(initial[4:])))
	if _, err := file.ReadAt(header, start); err != nil {
		return nil, err
	}
	for entry := 12; entry < len(header); entry += 16 {
		table := header[entry : entry+16]
		offset, length := uint64(binary.BigEndian.Uint32(table[8:])), uint64(binary.BigEndian.Uint32(table[12:]))
		if offset+length > uint64(info.Size()) || offset+uint64(len(header)) > math.MaxUint32 {
			return nil, fmt.Errorf("font table %q outside source", table[:4])
		}
		// Shift all source offsets past the virtual directory, so even shared
		// tables before this face's original directory remain accessible.
		binary.BigEndian.PutUint32(table[8:], uint32(offset+uint64(len(header))))
	}
	view := nativeFontFileView{header: header, file: file}
	return text.NewGoTextFaceSource(io.NewSectionReader(&view, 0, int64(len(header))+info.Size()))
}

type nativeFontFileView struct {
	header []byte
	file   io.ReaderAt
}

func (v *nativeFontFileView) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, fmt.Errorf("negative font offset")
	}
	n := 0
	if off < int64(len(v.header)) {
		n = copy(p, v.header[off:])
		p, off = p[n:], off+int64(n)
	}
	if len(p) == 0 {
		return n, nil
	}
	m, err := v.file.ReadAt(p, off-int64(len(v.header)))
	return n + m, err
}
