# Water font memory patch

This directory contains the production sources and licenses from
`github.com/go-text/typesetting v0.3.5`. Upstream tests and Unicode test datasets
are omitted. The root module uses a local replacement so builds are reproducible.

Changes are restricted to `font/font.go`, `font/glyphs.go` and
`font/glyphs_lazy.go`: static `glyf` fonts retain compact glyph headers and raw
compressed outline bytes, then parse contour points on demand into a bounded
1024-entry cache. Reads are synchronized and returned glyph data is immutable.
Fonts with `gvar` continue to use the upstream eager parser to preserve variation
point-count requirements. Metrics use the same original glyph bounds and hmtx.

Run `go test ./font` and `go test -race ./font` in this directory in addition
to Water's tests when changing the patch. On an upstream upgrade, reapply only
these changes and rerun the eager/lazy equivalence and cache-bound tests.
