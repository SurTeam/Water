// SPDX-License-Identifier: Unlicense OR BSD-3-Clause

package font

import (
	"bytes"
	"reflect"
	"runtime"
	"sync"
	"testing"

	ot "github.com/go-text/typesetting/font/opentype"
	"github.com/go-text/typesetting/font/opentype/tables"
	"golang.org/x/image/font/gofont/gomono"
	"golang.org/x/image/font/gofont/gomonobold"
	"golang.org/x/image/font/gofont/goregular"
)

func TestLazyGlyphsMatchEagerOutlinesAndMetrics(t *testing.T) {
	for _, data := range [][]byte{gomono.TTF, gomonobold.TTF, goregular.TTF} {
		loader, err := ot.NewLoader(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		font, err := NewFont(loader)
		if err != nil {
			t.Fatal(err)
		}
		if font.lazyGlyf == nil {
			t.Fatal("static font was eager")
		}
		eager, err := tables.ParseGlyf(font.lazyGlyf.raw, font.lazyGlyf.offsets)
		if err != nil {
			t.Fatal(err)
		}
		eagerFont := *font
		eagerFont.glyf, eagerFont.lazyGlyf = eager, nil
		lazyFace, eagerFace := NewFace(font), NewFace(&eagerFont)
		for i := range eager {
			gid := tables.GlyphID(i)
			left, lok := lazyFace.getExtentsFromGlyf(gid)
			right, rok := eagerFace.getExtentsFromGlyf(gid)
			if left != right || lok != rok {
				t.Fatalf("glyph %d metrics changed", i)
			}
		}
		if len(font.lazyGlyf.cache) != 0 {
			t.Fatal("static metrics decoded outlines")
		}
		for i := range eager {
			gid := tables.GlyphID(i)
			if !reflect.DeepEqual(font.glyphOutline(gid), eager[i]) {
				t.Fatalf("glyph %d decoding changed", i)
			}
			left, lerr := lazyFace.glyphDataFromGlyf(gid)
			right, rerr := eagerFace.glyphDataFromGlyf(gid)
			if !reflect.DeepEqual(left, right) || (lerr == nil) != (rerr == nil) {
				t.Fatalf("glyph %d composite outline changed", i)
			}
		}
	}
}

func TestLazyGlyphCacheIsBoundedAndConcurrent(t *testing.T) {
	// Empty glyphs exercise more IDs than the cache can retain, including
	// repeated eviction and concurrent lookups on the shared Font.
	const count = lazyGlyphCacheLimit * 3
	headers, lazy := newLazyGlyphTable(nil, make([]uint32, count+1))
	font := &Font{glyf: headers, lazyGlyf: lazy}
	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func(start int) {
			defer wg.Done()
			for i := 0; i < count*2; i++ {
				font.glyphOutline(tables.GlyphID((start + i) % count))
			}
		}(worker * count / 8)
	}
	wg.Wait()
	if len(lazy.cache) != lazyGlyphCacheLimit {
		t.Fatalf("cache retained %d entries", len(lazy.cache))
	}
}

func TestLazyGlyphOffsetsRejectInvalidBounds(t *testing.T) {
	for _, offsets := range [][]uint32{nil, {0, 11}, {8, 4}, {0, 9}} {
		if headers, lazy := newLazyGlyphTable(make([]byte, 10), offsets); headers != nil || lazy != nil {
			t.Fatalf("accepted invalid offsets %v", offsets)
		}
	}
}

func TestBorrowedGlyphOwnsInstructionsAfterViewRelease(t *testing.T) {
	loader, err := ot.NewLoader(bytes.NewReader(gomono.TTF))
	if err != nil {
		t.Fatal(err)
	}
	font, err := NewFont(loader)
	if err != nil {
		t.Fatal(err)
	}
	raw := append([]byte(nil), font.lazyGlyf.raw...)
	eager, err := tables.ParseGlyf(raw, font.lazyGlyf.offsets)
	if err != nil {
		t.Fatal(err)
	}
	released := false
	headers, lazy := newLazyGlyphView(raw, font.lazyGlyf.offsets, func() {
		for i := range raw {
			raw[i] = 0
		}
		released = true
	})
	mapped := &Font{glyf: headers, lazyGlyf: lazy}
	glyphs := make(tables.Glyf, len(eager))
	for i := range glyphs {
		glyphs[i] = mapped.glyphOutline(tables.GlyphID(i))
	}
	// Save a fully independent expected result before poisoning mapped bytes.
	ownedRaw := append([]byte(nil), raw...)
	want, err := tables.ParseGlyf(ownedRaw, lazy.offsets)
	if err != nil {
		t.Fatal(err)
	}
	runtime.SetFinalizer(lazy, nil)
	lazy.release()
	if !released || !reflect.DeepEqual(glyphs, want) {
		t.Fatal("returned glyph retained borrowed input")
	}
}

func TestInvalidBorrowedGlyphViewIsReleased(t *testing.T) {
	released := false
	_, lazy := newLazyGlyphView(make([]byte, 10), []uint32{0, 11}, func() { released = true })
	if lazy != nil || !released {
		t.Fatal("invalid glyph view leaked")
	}
}
