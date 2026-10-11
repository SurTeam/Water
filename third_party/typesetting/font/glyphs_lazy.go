// SPDX-License-Identifier: Unlicense OR BSD-3-Clause

package font

import (
	"encoding/binary"
	"runtime"
	"sync"

	"github.com/go-text/typesetting/font/opentype/tables"
)

// Water keeps compact glyph headers for metrics, then decodes contour points
// only for outlines actually rendered. The cache is bounded even when output
// cycles through the entire CJK repertoire. Returned glyphs remain immutable
// and valid when their cache entry is evicted, preserving concurrent readers.
const lazyGlyphCacheLimit = 1024

type lazyGlyphTable struct {
	mu      sync.Mutex
	raw     []byte
	offsets []uint32
	cache   map[tables.GlyphID]tables.Glyph
	ring    [lazyGlyphCacheLimit]tables.GlyphID
	next    int
	release func()
}

func newLazyGlyphView(raw []byte, offsets []uint32, release func()) (tables.Glyf, *lazyGlyphTable) {
	headers, lazy := newLazyGlyphTable(raw, offsets)
	if release != nil {
		if lazy == nil {
			release()
		} else {
			lazy.release = release
			runtime.SetFinalizer(lazy, func(table *lazyGlyphTable) { table.release() })
		}
	}
	return headers, lazy
}

func newLazyGlyphTable(raw []byte, offsets []uint32) (tables.Glyf, *lazyGlyphTable) {
	if !validLazyGlyphOffsets(raw, offsets) {
		return nil, nil
	}
	// Glyph bytes stay unread until a metrics or outline request. The returned
	// slice is nil on purpose: glyph count comes from the loca offsets.
	return nil, &lazyGlyphTable{raw: raw, offsets: offsets, cache: make(map[tables.GlyphID]tables.Glyph)}
}

func validLazyGlyphOffsets(raw []byte, offsets []uint32) bool {
	if len(offsets) == 0 {
		return false
	}
	if uint64(offsets[len(offsets)-1]) > uint64(len(raw)) {
		return false
	}
	for i := 0; i < len(offsets)-1; i++ {
		start, end := offsets[i], offsets[i+1]
		if end < start || uint64(end) > uint64(len(raw)) {
			return false
		}
		if start != end && end-start < 10 {
			return false
		}
	}
	return true
}

// header reads the 10-byte glyf header for one glyph. An empty loca span has
// no bytes and returns a zero glyph without touching the following record.
func (l *lazyGlyphTable) header(gid tables.GlyphID) (tables.Glyph, bool) {
	if l == nil || int(gid)+1 >= len(l.offsets) {
		return tables.Glyph{}, false
	}
	start, end := l.offsets[gid], l.offsets[int(gid)+1]
	if start == end {
		return tables.Glyph{}, true
	}
	if end < start || int(end) > len(l.raw) || end-start < 10 {
		return tables.Glyph{}, false
	}
	h := l.raw[start : start+10]
	return tables.Glyph{
		XMin: int16(binary.BigEndian.Uint16(h[2:])),
		YMin: int16(binary.BigEndian.Uint16(h[4:])),
		XMax: int16(binary.BigEndian.Uint16(h[6:])),
		YMax: int16(binary.BigEndian.Uint16(h[8:])),
	}, true
}

func (f *Font) glyfCount() int {
	if f == nil {
		return 0
	}
	if f.lazyGlyf != nil {
		if n := len(f.lazyGlyf.offsets); n > 0 {
			return n - 1
		}
		return 0
	}
	return len(f.glyf)
}

func (f *Font) hasGlyf() bool {
	return f != nil && (f.lazyGlyf != nil || len(f.glyf) > 0)
}

func (f *Font) glyphOutline(gid tables.GlyphID) tables.Glyph {
	if f.lazyGlyf == nil {
		return f.glyf[gid]
	}
	l := f.lazyGlyf
	defer runtime.KeepAlive(l)
	l.mu.Lock()
	defer l.mu.Unlock()
	if glyph, ok := l.cache[gid]; ok {
		return glyph
	}
	start, end := l.offsets[gid], l.offsets[int(gid)+1]
	var glyph tables.Glyph
	if start != end {
		var err error
		data := l.raw[start:end]
		if l.release != nil {
			// Parsed instructions may reference their input. A returned glyph
			// must remain valid after the font and its mapping are collected.
			data = append([]byte(nil), data...)
		}
		glyph, _, err = tables.ParseGlyph(data)
		if err != nil {
			glyph = tables.Glyph{}
		}
	}
	if len(l.cache) == lazyGlyphCacheLimit {
		delete(l.cache, l.ring[l.next])
	}
	l.ring[l.next] = gid
	l.next = (l.next + 1) % lazyGlyphCacheLimit
	l.cache[gid] = glyph
	return glyph
}
