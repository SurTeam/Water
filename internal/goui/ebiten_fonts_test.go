package goui

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/hajimehoshi/ebiten/v2/text/v2"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/gomono"
	"golang.org/x/image/font/gofont/goregular"
)

func TestNativeFontFamiliesPreserveFallbackOrder(t *testing.T) {
	got := nativeFontFamilies(" Go Mono , Go, , go mono, Apple Color Emoji ")
	want := []string{"Go Mono", "Go", "Apple Color Emoji"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("font chain = %v, want %v", got, want)
	}
	f := testNativeFonts(nil)
	f.loaded[fontKey{family: "Go Mono"}] = fontSource(gomono.TTF)
	f.loaded[fontKey{family: "Go"}] = fontSource(goregular.TTF)
	f.loaded[fontKey{family: "Missing"}] = nil
	for _, names := range []string{"Go Mono, Go", "Go, Go Mono", "Missing, Go Mono, Go"} {
		face := f.face(names, 24, true, false, false)
		first := "Go Mono"
		if names == "Go, Go Mono" {
			first = "Go"
		}
		width, _ := text.Measure("iiiMMM", face, 0)
		primaryWidth, _ := text.Measure("iiiMMM", &text.GoTextFace{Source: f.loaded[fontKey{family: first}], Size: 24}, 0)
		if width != primaryWidth {
			t.Fatalf("%s: first available font did not win: %g != %g", names, width, primaryWidth)
		}
	}
}

func TestNativeFontChainFallsBackPerGlyph(t *testing.T) {
	path := "/System/Library/Fonts/Apple Color Emoji.ttc"
	if _, err := os.Stat(path); err != nil {
		t.Skip("system emoji font unavailable")
	}
	f := testNativeFonts(readNativeFontRecords(path))
	f.loaded[fontKey{family: "Go Mono"}] = fontSource(gomono.TTF)
	f.loaded[fontKey{family: "Apple Color Emoji"}] = f.load(fontKey{family: "Apple Color Emoji"})
	if f.loaded[fontKey{family: "Apple Color Emoji"}] == nil {
		t.Fatal("emoji collection did not load")
	}
	face := f.face("Go Mono, Apple Color Emoji", 24, true, false, false)
	glyphs := text.AppendLazyGlyphs(nil, "A🍺B", face, nil)
	if len(glyphs) != 3 || !glyphs[1].Colored() || glyphs[1].GID == 0 || glyphs[0].Colored() || glyphs[2].Colored() {
		t.Fatalf("fallback did not select emoji only for the missing glyph: %+v", glyphs)
	}
}

func testNativeFonts(records []nativeFontRecord) *nativeFonts {
	return &nativeFonts{records: records, loaded: map[fontKey]*text.GoTextFaceSource{},
		resolved: map[fontKey]nativeFontRecord{}, fileSources: map[string]*text.GoTextFaceSource{}}
}

func TestNativeFontUsesInternalFamilyAndStyle(t *testing.T) {
	dir := t.TempDir()
	var records []nativeFontRecord
	for i, data := range [][]byte{goregular.TTF, gobold.TTF} {
		// Deliberately unrelated filenames: matching must use name-table data.
		path := filepath.Join(dir, []string{"unrelated-a.ttf", "unrelated-b.ttf"}[i])
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		records = append(records, readNativeFontRecords(path)...)
	}
	f := testNativeFonts(records)
	for _, bold := range []bool{false, true} {
		k := fontKey{family: "Go", bold: bold}
		if source := f.load(k); source == nil {
			t.Fatal("metadata lookup returned fallback")
		} else {
			f.loaded[k] = source
		}
		r := f.resolution(k)
		want := "unrelated-a.ttf"
		if bold {
			want = "unrelated-b.ttf"
		}
		if filepath.Base(r["path"].(string)) != want || r["status"] != "ready" {
			t.Fatalf("incorrect face: %v", r)
		}
	}
	if f.load(fontKey{family: "Go Unknown"}) != nil {
		t.Fatal("unavailable family matched unrelated font")
	}
	missing := fontKey{family: "Missing"}
	f.loaded[missing] = nil
	if r := f.resolution(missing); r["status"] != "fallback" {
		t.Fatalf("fallback is hidden: %v", r)
	}
}

func TestNativeFontCollectionSelectsMatchingFace(t *testing.T) {
	// Build a tiny TTC from bundled fonts. Each face's table offsets must be
	// relative to the collection so the two families are genuinely distinct.
	fonts := [][]byte{goregular.TTF, gomono.TTF}
	data := make([]byte, 20)
	copy(data, "ttcf")
	binary.BigEndian.PutUint32(data[4:], 0x00010000)
	binary.BigEndian.PutUint32(data[8:], 2)
	for i, font := range fonts {
		for len(data)%4 != 0 {
			data = append(data, 0)
		}
		base := len(data)
		binary.BigEndian.PutUint32(data[12+i*4:], uint32(base))
		face := append([]byte(nil), font...)
		for j := 0; j < int(binary.BigEndian.Uint16(face[4:])); j++ {
			table := face[12+j*16:]
			binary.BigEndian.PutUint32(table[8:], binary.BigEndian.Uint32(table[8:])+uint32(base))
		}
		data = append(data, face...)
	}
	path := filepath.Join(t.TempDir(), "different-families.ttc")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	records := readNativeFontRecords(path)
	if len(records) != 2 {
		t.Fatalf("got %d collection faces", len(records))
	}
	f := testNativeFonts(records)
	k := fontKey{family: "Go Mono"}
	if f.load(k) == nil || f.resolved[k].index != 1 {
		t.Fatalf("selected first face instead of requested family: %v", f.resolved[k])
	}
}

func TestNativeFontNerdFamilyAndWeights(t *testing.T) {
	regular := nativeFontRecord{family: "Sarasa Term SC Nerd Font", style: "Regular", names: []string{"Sarasa Term SC Nerd Font"}, path: "sarasa-term-sc-regular-nerd-font.ttf"}
	light, semi, bold := regular, regular, regular
	light.style, semi.style, bold.style = "ExtraLight", "SemiBold", "Bold"
	for _, family := range []string{"Sarasa Term SC Nerd Font", "Sarasa Term SC"} {
		k := fontKey{family: family}
		if nativeFontScore(regular, k) >= 1<<20 || nativeFontScore(regular, k) >= nativeFontScore(light, k) || nativeFontScore(regular, k) >= nativeFontScore(semi, k) {
			t.Fatalf("regular family/weight not preferred for %s", family)
		}
		k.bold = true
		if nativeFontScore(bold, k) >= nativeFontScore(semi, k) {
			t.Fatal("semibold replaced bold")
		}
	}
	if nativeFontScore(regular, fontKey{family: "Sarasa Gothic SC Nerd Font"}) < 1<<20 {
		t.Fatal("different font family matched")
	}
}
