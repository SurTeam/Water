# Water font memory patch

This directory contains the production sources and licenses from
`github.com/go-text/typesetting v0.3.5`. Upstream tests and Unicode test datasets
are omitted. The root module uses a local replacement so builds are reproducible.

Changes are in `font/font.go`, `font/glyphs.go`, `font/glyphs_lazy.go`,
`font/metrics.go`, and `font/renderer.go`. Static `glyf` fonts keep the mapped
outline bytes and parse contour points on demand into a bounded 1024-entry
cache. Loca offsets are validated at load, but glyph header bytes are read only
when that glyph's metrics or outline are requested, so an unused CJK glyf page
stays out of RSS. Reads of the outline cache are synchronized and returned glyph
data is immutable. Fonts with `gvar` continue to use the upstream eager parser.
Metrics use the same original glyph bounds and hmtx. An empty loca span is a
zero glyph and is not read; a span shorter than 10 bytes still fails the load.

Run `go test ./font` and `go test -race ./font` in this directory in addition
to Water's tests when changing the patch. On an upstream upgrade, reapply only
these changes and rerun the eager/lazy equivalence and cache-bound tests.
