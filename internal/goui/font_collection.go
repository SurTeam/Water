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
func loadNativeFontSource(path string, index int, bitmapPixels ...int) (*text.GoTextFaceSource, error) {
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
	start := int64(0)
	if string(initial[:4]) != "ttcf" {
		if index != 0 {
			return nil, fmt.Errorf("font face index %d outside single font", index)
		}
	} else {
		count := binary.BigEndian.Uint32(initial[8:])
		if index < 0 || uint64(index) >= uint64(count) {
			return nil, fmt.Errorf("font face index %d outside collection of %d", index, count)
		}
		var offset [4]byte
		if _, err := file.ReadAt(offset[:], 12+int64(index)*4); err != nil {
			return nil, err
		}
		start = int64(binary.BigEndian.Uint32(offset[:]))
	}
	if _, err := file.ReadAt(initial[:], start); err != nil {
		return nil, err
	}
	header := make([]byte, 12+16*int(binary.BigEndian.Uint16(initial[4:])))
	if _, err := file.ReadAt(header, start); err != nil {
		return nil, err
	}
	// Water renders the default variable-font instance and exposes no axis
	// controls. The default outlines/metrics are already in glyf/hmtx; parsing
	// unused per-axis deltas (notably SFNS gvar) retains tens of MB.
	compact := append([]byte(nil), header[:12]...)
	for entry := 12; entry < len(header); entry += 16 {
		switch string(header[entry : entry+4]) {
		case "gvar":
			continue
		}
		compact = append(compact, header[entry:entry+16]...)
	}
	header = compact
	tables := (len(header) - 12) / 16
	binary.BigEndian.PutUint16(header[4:], uint16(tables))
	if tables > 0 {
		power, selector := 1, 0
		for power*2 <= tables {
			power *= 2
			selector++
		}
		binary.BigEndian.PutUint16(header[6:], uint16(power*16))
		binary.BigEndian.PutUint16(header[8:], uint16(selector))
		binary.BigEndian.PutUint16(header[10:], uint16(tables*16-power*16))
	}
	view := nativeFontFileView{header: header, file: file}
	pixels := 32
	if len(bitmapPixels) > 0 && bitmapPixels[0] > 0 {
		pixels = bitmapPixels[0]
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
		if string(table[:4]) == "sbix" {
			strike, err := selectNativeBitmapStrike(file, int64(offset), int64(length), pixels)
			if err != nil {
				return nil, err
			}
			view.bitmap = strike
			if uint64(len(header))+uint64(info.Size())+12+uint64(strike.length) > math.MaxUint32 {
				return nil, fmt.Errorf("virtual sbix table too large")
			}
			binary.BigEndian.PutUint32(table[8:], uint32(uint64(len(header))+uint64(info.Size())))
			binary.BigEndian.PutUint32(table[12:], uint32(12+strike.length))
		}
	}
	view.bitmapStart = int64(len(header)) + info.Size()
	return text.NewGoTextFaceSource(&nativeFontResource{
		SectionReader: io.NewSectionReader(&view, 0, view.bitmapStart+12+view.bitmap.length),
		file:          file, headerSize: int64(len(header)), fileSize: info.Size(),
	})
}

type nativeFontResource struct {
	*io.SectionReader
	file                 *os.File
	headerSize, fileSize int64
}

func (r *nativeFontResource) View(offset int64, length int) ([]byte, func(), error) {
	offset -= r.headerSize
	if offset < 0 || length < 0 || offset > r.fileSize || int64(length) > r.fileSize-offset {
		return nil, nil, fmt.Errorf("font view outside original file")
	}
	return mapNativeFontTable(r.file, offset, length)
}

// sbix contains a complete set of bitmap glyphs at every strike size. Keep the
// smallest strike at least as large as the rendered pixels (or the largest
// available); loading every strike retains hundreds of MB of unused PNGs.
// The selected strike still contains every glyph, including emoji sequences.
func selectNativeBitmapStrike(file io.ReaderAt, offset, length int64, pixels int) (nativeBitmapStrike, error) {
	var header [8]byte
	if length < 8 {
		return nativeBitmapStrike{}, fmt.Errorf("short sbix header")
	}
	if _, err := file.ReadAt(header[:], offset); err != nil {
		return nativeBitmapStrike{}, err
	}
	count := int64(binary.BigEndian.Uint32(header[4:]))
	if count == 0 || count > (length-8)/4 {
		return nativeBitmapStrike{}, fmt.Errorf("invalid sbix strike count")
	}
	var best nativeBitmapStrike
	bestSize := 0
	for i := int64(0); i < count; i++ {
		var offsets [8]byte
		n := 4
		if i+1 < count {
			n = 8
		}
		if _, err := file.ReadAt(offsets[:n], offset+8+i*4); err != nil {
			return best, err
		}
		start, end := int64(binary.BigEndian.Uint32(offsets[:4])), length
		if n == 8 {
			end = int64(binary.BigEndian.Uint32(offsets[4:]))
		}
		if start < 8+count*4 || end < start+4 || end > length {
			return best, fmt.Errorf("invalid sbix strike offsets")
		}
		var dimensions [4]byte
		if _, err := file.ReadAt(dimensions[:], offset+start); err != nil {
			return best, err
		}
		size := int(binary.BigEndian.Uint16(dimensions[:2]))
		if size == 0 {
			return best, fmt.Errorf("invalid sbix strike size")
		}
		if bestSize == 0 || bestSize < pixels && size > bestSize || size >= pixels && bestSize >= pixels && size < bestSize {
			bestSize = size
			best = nativeBitmapStrike{offset: offset + start, length: end - start}
			copy(best.header[:8], header[:])
			binary.BigEndian.PutUint32(best.header[4:8], 1)
			binary.BigEndian.PutUint32(best.header[8:], 12)
		}
	}
	return best, nil
}

type nativeBitmapStrike struct {
	header         [12]byte
	offset, length int64
}

type nativeFontFileView struct {
	header      []byte
	file        io.ReaderAt
	bitmap      nativeBitmapStrike
	bitmapStart int64
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
	if v.bitmap.length > 0 && off >= v.bitmapStart {
		rel := off - v.bitmapStart
		if rel < 12 {
			m := copy(p, v.bitmap.header[rel:])
			n += m
			p, rel = p[m:], rel+int64(m)
		}
		if len(p) == 0 {
			return n, nil
		}
		if rel-12 >= v.bitmap.length {
			return n, io.EOF
		}
		remaining := v.bitmap.length - (rel - 12)
		short := int64(len(p)) > remaining
		if short {
			p = p[:remaining]
		}
		m, err := v.file.ReadAt(p, v.bitmap.offset+rel-12)
		if short && err == nil {
			err = io.EOF
		}
		return n + m, err
	}
	if v.bitmap.length > 0 && off < v.bitmapStart && int64(len(p)) > v.bitmapStart-off {
		before := int(v.bitmapStart - off)
		m, err := v.file.ReadAt(p[:before], off-int64(len(v.header)))
		n += m
		if err != nil {
			return n, err
		}
		m, err = v.ReadAt(p[before:], v.bitmapStart)
		return n + m, err
	}
	m, err := v.file.ReadAt(p, off-int64(len(v.header)))
	return n + m, err
}
