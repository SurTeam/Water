package goui

import (
	"bytes"
	"encoding/binary"
	"io"
	"os"
	"reflect"
	"testing"

	"github.com/SurTeam/Water/internal/goconfig"
	"github.com/hajimehoshi/ebiten/v2/text/v2"
)

func TestNativeBitmapStrikeSelectionAndVirtualReads(t *testing.T) {
	// Deliberately unsorted strikes, with distinguishable payloads.
	data := make([]byte, 20)
	binary.BigEndian.PutUint16(data, 1)
	binary.BigEndian.PutUint32(data[4:], 3)
	for i, size := range []int{64, 20, 32} {
		binary.BigEndian.PutUint32(data[8+i*4:], uint32(len(data)))
		strike := make([]byte, 12)
		binary.BigEndian.PutUint16(strike, uint16(size))
		binary.BigEndian.PutUint16(strike[2:], 72)
		copy(strike[4:], bytes.Repeat([]byte{byte(size)}, 8))
		data = append(data, strike...)
	}
	for _, tc := range []struct{ pixels, want int }{{16, 20}, {20, 20}, {21, 32}, {32, 32}, {33, 64}, {160, 64}} {
		strike, err := selectNativeBitmapStrike(bytes.NewReader(data), 0, int64(len(data)), tc.pixels)
		if err != nil {
			t.Fatal(err)
		}
		view := nativeFontFileView{file: bytes.NewReader(data), bitmap: strike, bitmapStart: int64(len(data))}
		got, err := io.ReadAll(io.NewSectionReader(&view, view.bitmapStart, 12+strike.length))
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 24 || binary.BigEndian.Uint32(got[4:]) != 1 || binary.BigEndian.Uint32(got[8:]) != 12 || int(binary.BigEndian.Uint16(got[12:])) != tc.want {
			t.Fatalf("pixels %d: invalid virtual strike %v", tc.pixels, got)
		}
		cross := make([]byte, 8)
		if n, err := view.ReadAt(cross, view.bitmapStart-4); n != 8 || err != nil || !bytes.Equal(cross[:4], data[len(data)-4:]) || !bytes.Equal(cross[4:], got[:4]) {
			t.Fatalf("cross-boundary read: %v, %d, %v", cross, n, err)
		}
		if n, err := view.ReadAt(make([]byte, 4), view.bitmapStart+12+strike.length-2); n != 2 || err != io.EOF {
			t.Fatalf("end read: %d, %v", n, err)
		}
	}
	for _, corrupt := range []func([]byte){
		func(b []byte) { binary.BigEndian.PutUint32(b[4:], 0xffffffff) },
		func(b []byte) { binary.BigEndian.PutUint32(b[8:], 0) },
		func(b []byte) { binary.BigEndian.PutUint32(b[12:], uint32(len(b)+1)) },
	} {
		bad := bytes.Clone(data)
		corrupt(bad)
		if _, err := selectNativeBitmapStrike(bytes.NewReader(bad), 0, int64(len(bad)), 32); err == nil {
			t.Fatal("invalid strike accepted")
		}
	}
}

func TestNativeCompactFontsPreserveDefaultRendering(t *testing.T) {
	for _, path := range []string{"/System/Library/Fonts/SFNS.ttf", "/System/Library/Fonts/Apple Color Emoji.ttc"} {
		t.Run(path, func(t *testing.T) {
			file, err := os.Open(path)
			if err != nil {
				t.Skip("system font unavailable")
			}
			defer file.Close()
			original, err := text.NewGoTextFaceSource(file)
			if path == "/System/Library/Fonts/Apple Color Emoji.ttc" {
				var collection []*text.GoTextFaceSource
				collection, err = text.NewGoTextFaceSourcesFromCollection(file)
				if err == nil {
					original = collection[0]
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, size := range []float64{20, 32, 64, 160} {
				compact, err := loadNativeFontSource(path, 0, int(size))
				if err != nil {
					t.Fatal(err)
				}
				a, b := &text.GoTextFace{Source: original, Size: size}, &text.GoTextFace{Source: compact, Size: size}
				if !reflect.DeepEqual(a.Metrics(), b.Metrics()) {
					t.Fatalf("%g: default metrics changed: %v != %v", size, a.Metrics(), b.Metrics())
				}
				value := "Water ffi Aé 中文 🍺👩‍💻🇨🇳"
				left, right := text.AppendLazyGlyphs(nil, value, a, nil), text.AppendLazyGlyphs(nil, value, b, nil)
				if len(left) != len(right) {
					t.Fatal("glyph count changed")
				}
				for i := range left {
					l, r := left[i], right[i]
					if l.GID != r.GID || l.OriginX != r.OriginX || l.OriginY != r.OriginY || l.AdvanceX != r.AdvanceX || l.AdvanceY != r.AdvanceY || l.ImageBounds != r.ImageBounds || l.Colored() != r.Colored() {
						t.Fatalf("%g: glyph %d changed: %+v != %+v", size, i, l, r)
					}
				}
			}
		})
	}
}

func TestNativeFontsReuseConfiguredSarasaFallback(t *testing.T) {
	cfg := goconfig.Default()
	f := newNativeFonts(cfg)
	defer f.close()
	primary := f.loaded[fontKey{family: cfg.Terminal.FontFamily}]
	if primary == nil {
		t.Skip("configured Sarasa font unavailable")
	}
	if f.fallback != primary {
		t.Fatal("configured SC font was duplicated for CJK fallback")
	}
	if _, loaded := f.loaded[fontKey{family: "Sarasa UI SC"}]; loaded {
		t.Fatal("unused SC backup loaded")
	}
	for _, tc := range []struct {
		size float64
		want int
	}{{16, 0}, {32, 0}, {33, 64}, {64, 64}, {65, 128}, {256, 256}} {
		if got := nativeBitmapPixels(tc.size); got != tc.want {
			t.Fatalf("%g: %d != %d", tc.size, got, tc.want)
		}
	}
}
