package goui

import (
	"bytes"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"unicode"

	"github.com/SurTeam/Water/internal/goconfig"
	"github.com/hajimehoshi/ebiten/v2/text/v2"
	"golang.org/x/image/font/gofont/gomono"
	"golang.org/x/image/font/gofont/gomonobold"
	"golang.org/x/image/font/gofont/gomonobolditalic"
	"golang.org/x/image/font/gofont/gomonoitalic"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/sfnt"
)

type fontKey struct {
	family       string
	bold, italic bool
	bitmapPixels int
}
type nativeFontRecord struct {
	path          string
	index         int
	family, style string
	names         []string
}
type nativeFonts struct {
	generation  atomic.Uint64
	mu          sync.RWMutex
	records     []nativeFontRecord
	resolved    map[fontKey]nativeFontRecord
	fileSources map[string]*text.GoTextFaceSource
	loaded      map[fontKey]*text.GoTextFaceSource
	pending     map[fontKey]bool
	requests    chan fontKey
	done        chan struct{}
	ui          *text.GoTextFaceSource
	mono        [4]*text.GoTextFaceSource
	fallback    *text.GoTextFaceSource
	emoji       *text.GoTextFaceSource
	emojiFamily string
}

func fontName(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, s)
}

func nativeFontFamilies(value string) []string {
	var families []string
	seen := map[string]bool{}
	for _, family := range strings.Split(value, ",") {
		family = strings.TrimSpace(family)
		key := strings.ToLower(family)
		if family != "" && !seen[key] {
			families = append(families, family)
			seen[key] = true
		}
	}
	return families
}
func fontSource(data []byte) *text.GoTextFaceSource {
	source, _ := text.NewGoTextFaceSource(bytes.NewReader(data))
	return source
}
func newNativeFonts(cfg goconfig.AppConfig) *nativeFonts {
	f := &nativeFonts{loaded: map[fontKey]*text.GoTextFaceSource{}, resolved: map[fontKey]nativeFontRecord{}, fileSources: map[string]*text.GoTextFaceSource{}, pending: map[fontKey]bool{}, requests: make(chan fontKey, 32), done: make(chan struct{}), ui: fontSource(goregular.TTF)}
	f.mono = [4]*text.GoTextFaceSource{fontSource(gomono.TTF), fontSource(gomonobold.TTF), fontSource(gomonoitalic.TTF), fontSource(gomonobolditalic.TTF)}
	home, _ := os.UserHomeDir()
	for _, root := range []string{filepath.Join(home, "Library/Fonts"), "/Library/Fonts", "/System/Library/Fonts", filepath.Join(home, ".local/share/fonts"), filepath.Join(home, ".fonts"), "/usr/share/fonts", "/usr/local/share/fonts", filepath.Join(os.Getenv("WINDIR"), "Fonts")} {
		if !filepath.IsAbs(root) {
			continue
		}
		_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				ext := strings.ToLower(filepath.Ext(path))
				if ext == ".ttf" || ext == ".otf" || ext == ".ttc" {
					f.records = append(f.records, readNativeFontRecords(path)...)
				}
			}
			return nil
		})
	}
	// Resolve the requested family before opening the window. Future changes
	// are loaded by the worker and use the bundled fallback until ready.
	if len(nativeFontFamilies(cfg.UI.UIFontFamily)) == 0 {
		if source := f.load(fontKey{family: "SFNS"}); source != nil {
			f.ui = source
		}
	}
	for _, family := range nativeFontFamilies(cfg.Terminal.FontFamily) {
		k := fontKey{family: family}
		f.loaded[k] = f.load(k)
	}
	// UI labels use regular and bold. Other styles are requested on demand.
	for _, family := range nativeFontFamilies(cfg.UI.UIFontFamily) {
		for _, style := range []fontKey{{family: family}, {family: family, bold: true}} {
			if _, loaded := f.loaded[style]; !loaded {
				f.loaded[style] = f.load(style)
			}
		}
	}
	// Sarasa's SC UI/Term faces share the CJK repertoire. Reuse a configured
	// regular SC face before loading another full copy of those glyph tables.
	for _, family := range append(nativeFontFamilies(cfg.UI.UIFontFamily), nativeFontFamilies(cfg.Terminal.FontFamily)...) {
		name := fontName(family)
		if name == "sarasatermsc" || name == "sarasatermscnerdfont" || name == "sarasauisc" {
			if source := f.loaded[fontKey{family: family}]; source != nil {
				f.fallback = source
				break
			}
		}
	}
	// Only the first available regular CJK fallback participates in face().
	// Loading every backup family in four styles retained hundreds of MB of
	// parsed glyph tables that could never be used by the rendering chain.
	for _, family := range []string{"Sarasa UI SC", "PingFang", "Noto Sans CJK"} {
		if f.fallback != nil {
			break
		}
		k := fontKey{family: family}
		s, loaded := f.loaded[k]
		if !loaded {
			s = f.load(k)
			f.loaded[k] = s
		}
		if s != nil {
			f.fallback = s
			break
		}
	}
	// Emoji must precede patched monospace fonts for emoji cells: some Nerd
	// Font patches supply a visible placeholder instead of a missing glyph.
	for _, family := range []string{"Apple Color Emoji", "Noto Color Emoji", "Segoe UI Emoji", "Noto Emoji"} {
		if source := f.load(fontKey{family: family}); source != nil {
			f.emoji = source
			f.emojiFamily = family
			f.loaded[fontKey{family: family}] = source
			break
		}
	}
	go func() {
		for {
			select {
			case k := <-f.requests:
				source := f.load(k)
				f.mu.Lock()
				if len(f.loaded) >= 64 {
					for old := range f.loaded {
						delete(f.loaded, old)
						delete(f.resolved, old)
						break
					}
				}
				f.loaded[k] = source
				delete(f.pending, k)
				f.generation.Add(1)
				f.mu.Unlock()
			case <-f.done:
				return
			}
		}
	}()
	return f
}
func (f *nativeFonts) close() { close(f.done) }

// Read only metadata at startup, keeping font data and rasterization out of the
// frame loop. Collections are indexed per face, never assumed to use face zero.
func readNativeFontRecords(path string) []nativeFontRecord {
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()
	collection, err := sfnt.ParseCollectionReaderAt(file)
	if err != nil {
		return nil
	}
	var records []nativeFontRecord
	var buffer sfnt.Buffer
	for i := 0; i < collection.NumFonts(); i++ {
		face, err := collection.Font(i)
		if err != nil {
			continue
		}
		name := func(id sfnt.NameID) string { value, _ := face.Name(&buffer, id); return value }
		family, style := name(sfnt.NameIDTypographicFamily), name(sfnt.NameIDTypographicSubfamily)
		if family == "" {
			family = name(sfnt.NameIDFamily)
		}
		if style == "" {
			style = name(sfnt.NameIDSubfamily)
		}
		records = append(records, nativeFontRecord{path: path, index: i, family: family, style: style,
			names: []string{family, name(sfnt.NameIDFamily), name(sfnt.NameIDFull), name(sfnt.NameIDPostScript)}})
	}
	return records
}

func nativeFontScore(record nativeFontRecord, k fontKey) int {
	requested := fontName(k.family)
	match := 1 << 20
	for _, name := range record.names {
		candidate := fontName(name)
		if candidate == requested {
			match = 0
			break
		}
		// The old default omitted the Nerd Font suffix. Prefer exact families,
		// but allow that installed variant when no unpatched family exists.
		if strings.TrimSuffix(candidate, "nerdfont") == requested {
			match = min(match, 10000)
		}
	}
	if fontName(strings.TrimSuffix(filepath.Base(record.path), filepath.Ext(record.path))) == requested {
		match = min(match, 100)
	}
	if match == 1<<20 {
		return match
	}
	style := fontName(record.style)
	weight := 400
	switch {
	case strings.Contains(style, "thin"):
		weight = 100
	case strings.Contains(style, "extralight"), strings.Contains(style, "ultralight"):
		weight = 200
	case strings.Contains(style, "light"):
		weight = 300
	case strings.Contains(style, "semibold"), strings.Contains(style, "demibold"):
		weight = 600
	case strings.Contains(style, "extrabold"), strings.Contains(style, "ultrabold"):
		weight = 800
	case strings.Contains(style, "black"), strings.Contains(style, "heavy"):
		weight = 900
	case strings.Contains(style, "bold"):
		weight = 700
	case strings.Contains(style, "medium"):
		weight = 500
	}
	target := 400
	if k.bold {
		target = 700
	}
	if weight < target {
		match += target - weight
	} else {
		match += weight - target
	}
	italic := strings.Contains(style, "italic") || strings.Contains(style, "oblique")
	if italic != k.italic {
		match += 1000
	}
	return match
}

func (f *nativeFonts) load(k fontKey) *text.GoTextFaceSource {
	name := fontName(k.family)
	if name == "" {
		return nil
	}
	var matches []nativeFontRecord
	for _, record := range f.records {
		if nativeFontScore(record, k) < 1<<20 {
			matches = append(matches, record)
		}
	}
	sort.SliceStable(matches, func(i, j int) bool { return nativeFontScore(matches[i], k) < nativeFontScore(matches[j], k) })
	for _, record := range matches {
		cacheKey := fmt.Sprintf("%s:%d:%d", record.path, record.index, k.bitmapPixels)
		remember := func(source *text.GoTextFaceSource) *text.GoTextFaceSource {
			f.mu.Lock()
			f.resolved[k] = record
			f.mu.Unlock()
			return source
		}
		if cached := f.fileSources[cacheKey]; cached != nil {
			return remember(cached)
		}
		source, err := loadNativeFontSource(record.path, record.index, k.bitmapPixels)
		if err == nil {
			if len(f.fileSources) >= 64 {
				for old := range f.fileSources {
					delete(f.fileSources, old)
					break
				}
			}
			f.fileSources[cacheKey] = source
			return remember(source)
		}
	}
	return nil
}

func (f *nativeFonts) resolution(k fontKey) map[string]any {
	if families := nativeFontFamilies(k.family); len(families) > 1 || len(families) == 1 && families[0] != k.family {
		chain := make([]map[string]any, 0, len(families))
		var primary map[string]any
		for _, family := range families {
			r := f.resolution(fontKey{family: family, bold: k.bold, italic: k.italic, bitmapPixels: k.bitmapPixels})
			chain = append(chain, r)
			if primary == nil && r["status"] == "ready" {
				primary = r
			}
		}
		if primary == nil {
			primary = chain[0]
		}
		result := make(map[string]any, len(primary)+1)
		for key, value := range primary {
			result[key] = value
		}
		result["requested_family"], result["fallback_chain"] = k.family, chain
		return result
	}
	f.mu.RLock()
	record, ok := f.resolved[k]
	source, loaded := f.loaded[k]
	f.mu.RUnlock()
	status := "ready"
	if !ok || source == nil {
		status = "fallback"
		record = nativeFontRecord{family: "Go Mono", style: "Regular", path: "builtin:Go Mono"}
		if k.bold {
			record.style = "Bold"
		}
		if k.italic {
			record.style += " Italic"
		}
	}
	if !loaded {
		status = "loading"
	}
	return map[string]any{"requested_family": k.family, "resolved_family": record.family,
		"style": record.style, "path": record.path, "face_index": record.index, "status": status}
}
func (f *nativeFonts) face(family string, size float64, mono, bold, italic bool, ligatures ...bool) text.Face {
	return f.faceWithRequest(family, size, mono, bold, italic, true, ligatures...)
}

// Measuring styles must not load an entire font for a style never displayed.
// Cached faces use the bundled styled fallback until the renderer requests it.
func (f *nativeFonts) cachedFace(family string, size float64, mono, bold, italic bool, ligatures ...bool) text.Face {
	return f.faceWithRequest(family, size, mono, bold, italic, false, ligatures...)
}

func (f *nativeFonts) faceWithRequest(family string, size float64, mono, bold, italic, request bool, ligatures ...bool) text.Face {
	var faces []text.Face
	seen := map[*text.GoTextFaceSource]bool{}
	makeFace := func(source *text.GoTextFaceSource) {
		if source == nil || seen[source] {
			return
		}
		seen[source] = true
		face := &text.GoTextFace{Source: source, Size: size}
		if len(ligatures) > 0 && !ligatures[0] {
			face.SetFeature(text.MustParseTag("liga"), 0)
			face.SetFeature(text.MustParseTag("calt"), 0)
		}
		faces = append(faces, face)
	}
	for _, family := range nativeFontFamilies(family) {
		k := fontKey{family: family, bold: bold, italic: italic}
		if strings.Contains(fontName(family), "emoji") {
			k.bitmapPixels = nativeBitmapPixels(size)
		}
		makeFace(f.source(k, request))
	}
	if len(faces) == 0 {
		if mono {
			index := 0
			if bold {
				index++
			}
			if italic {
				index += 2
			}
			makeFace(f.mono[index])
		} else {
			makeFace(f.ui)
		}
	}
	makeFace(f.fallback)
	if f.emojiFamily != "" {
		source := f.source(fontKey{family: f.emojiFamily, bitmapPixels: nativeBitmapPixels(size)}, request)
		if source == nil {
			source = f.emoji
		}
		makeFace(source)
	} else {
		makeFace(f.emoji)
	}
	if len(faces) == 1 {
		return faces[0]
	}
	combined, err := text.NewMultiFace(faces...)
	if err != nil {
		return faces[0]
	}
	return combined
}

// Small UI/terminal sizes share the 32px strike; larger fonts and display-scale
// changes request a suitable source asynchronously, without capping resolution.
func nativeBitmapPixels(size float64) int {
	if size <= 32 {
		return 0
	}
	pixels := 64
	for pixels < int(math.Ceil(size)) && pixels < 4096 {
		pixels *= 2
	}
	return pixels
}

func (f *nativeFonts) requestedSource(k fontKey) *text.GoTextFaceSource {
	return f.source(k, true)
}

func (f *nativeFonts) source(k fontKey, request bool) *text.GoTextFaceSource {
	f.mu.RLock()
	source, exists := f.loaded[k]
	f.mu.RUnlock()
	if request && !exists && k.family != "" {
		f.mu.Lock()
		if !f.pending[k] {
			select {
			case f.requests <- k:
				f.pending[k] = true
			default:
			}
		}
		f.mu.Unlock()
	}
	return source
}
