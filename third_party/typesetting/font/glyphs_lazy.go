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
	if len(offsets) == 0 {
		return nil, nil
	}
	if uint64(offsets[len(offsets)-1]) > uint64(len(raw)) {
		return nil, nil
	}
	headers := make(tables.Glyf, len(offsets)-1)
	for i := range headers {
		start, end := offsets[i], offsets[i+1]
		if end < start || uint64(end) > uint64(len(raw)) {
			return nil, nil
		}
		if start == end {
			continue
		}
		if end-start < 10 {
			return nil, nil
		}
		h := raw[start:end]
		headers[i] = tables.Glyph{XMin: int16(binary.BigEndian.Uint16(h[2:])), YMin: int16(binary.BigEndian.Uint16(h[4:])), XMax: int16(binary.BigEndian.Uint16(h[6:])), YMax: int16(binary.BigEndian.Uint16(h[8:]))}
	}
	return headers, &lazyGlyphTable{raw: raw, offsets: offsets, cache: make(map[tables.GlyphID]tables.Glyph)}
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
