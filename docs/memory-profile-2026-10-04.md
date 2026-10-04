# Native GUI memory diagnosis — 2026-10-04

The CPU changes were committed as `bd1bdc5`. Memory comparisons below use that
commit as the baseline and the same native macOS GUI, default fonts, 96×30 grid,
and 1 MiB/s bounded ANSI workload. Existing user GUI/server processes were not
stopped or replaced.

## Evidence and root cause

The installed GUI initially had a physical footprint of about 2.2 GiB. Its
graphics allocations were approximately 300 MiB; GPU textures alone did not
explain the footprint. In the isolated baseline, sampled live heap attributed
719 MiB out of 735 MiB (98%) to font initialization. Most bytes were parsed font
tables and glyph points, not terminal history.

Font initialization loaded all backup CJK families in four styles, although the
fallback chain only used the first available regular face. Loading a selected
face from a TTC additionally parsed every other face in that collection.
Terminal geometry measurement requested all four terminal styles even when no
bold or italic text had appeared.

The fix loads only the first usable regular CJK fallback, preloads only UI styles
that labels use, and loads terminal bold/italic styles on the existing worker
when their cells actually render. Geometry measurement uses cached or bundled
faces without requesting unused styles. TTC loading presents only the selected
face's directory and original tables to the parser, avoiding parsing unrelated
faces and copying the entire collection. Font-family selection and glyph
fallback order remain intact.

## Matched measurements

Each sampled window lasts four seconds. Three successive output cycles run for
six seconds each. HeapAlloc is read after forced GC outside the timed CPU window;
pprof live-byte totals are statistical estimates and differ from HeapAlloc.
Physical footprint comes from `vmmap -summary`; its large-unit rounding limits
precision. RSS is reported separately because macOS can retain Go's released
pages as reusable resident memory.

| State | Baseline HeapAlloc | Fixed HeapAlloc | Baseline footprint | Fixed footprint |
|---|---:|---:|---:|---:|
| Idle | 737 MiB | 366 MiB | ~1.40 GiB | 538 MiB |
| Output cycle 1 | 790 MiB | 415 MiB | ~1.20 GiB | 822 MiB |
| Output cycle 2 | 790 MiB | 423 MiB | ~1.10 GiB | 679 MiB |
| Output cycle 3 | 793 MiB | 420 MiB | ~1.10 GiB | 742 MiB |

Idle footprint fell about 62%, and retained heap fell about 50%. Output CPU was
30–33% on both versions; the memory fix did not materially regress it. Fixed
Ebitengine GPU image accounting was 91 MiB idle and 107 MiB under output. This
counter covers images, not all driver/IOSurface allocations.

The fixed heap plateaus over these three cycles; this bounded experiment does
not establish the absence of every long-running leak. Remaining live heap is
still dominated by the selected large CJK/emoji fonts parsed by the text engine.
Using additional configured families or actual styles legitimately increases
it. RSS alone should not be used to infer an ever-growing live heap.

Reports and profiles on this machine:

- Baseline: `/tmp/water-cpu.ehelebl1/report.json`
- Fixed: `/tmp/water-cpu.ffp4yid5/report.json`
- Each directory includes CPU/heap profiles, native samples, vmmap summaries,
  and a screenshot; these temporary artifacts are not committed.

Reproduce with a `water_cpu_diagnostic` build and:

```sh
~/.venv/bin/python scripts/diagnose-gui-cpu.py --water <diagnostic-binary> \
  --profile --seconds 4 --rate-mib 1 --cycles 3
```

The diagnostic HTTP endpoints are excluded from ordinary builds.

## Verification

`go test ./...`, `go vet ./...`, and race tests for goui, goclient and goserver
passed. Regression tests cover selected TTC faces, rejection of invalid indices,
ignoring malformed unselected faces, skipping unused fallback families/styles,
and loading a rendered style exactly once while metric measurement queues none.
Native GUI query checks passed 36 real PTY replies plus OSC7/OSC52.
Native settings/geometry and multilingual/restart/grid checks also passed. The
first settings screenshot run crashed in MetalDrawable.Texture; a repeat passed.
The crash's cause has not been established and is not claimed fixed here. The
first locale run lacked its sibling server fixture; after building the matching
dev server, its complete regression passed.

The pre-existing wide-glyph smoke failure at 8 pt remains documented in the CPU
report; it reproduced on the original baseline and is outside this memory fix.
