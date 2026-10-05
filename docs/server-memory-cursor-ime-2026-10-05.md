# Server memory, cursor preferences and macOS IME

Date: 2026-10-05. Platform: macOS, Apple M1 Pro, native Metal GUI, Retina
display with backing scale 2.

## Memory finding and change

The running release server had RSS 156272 KiB at the first observation, five
registered terminals, and 13483386 bytes of retained raw replay. RSS includes
Go runtime, reserved heap and other process allocations; this observation alone
does not establish a leak or attribute every resident byte.

The PTY reader allocated all 64 queue blocks at startup, each 128 KiB. An idle
terminal therefore retained 8 MiB before it needed any burst capacity. Five
terminals reserved 40 MiB solely for this pool. Queue capacity is a backpressure
limit; it does not require every buffer to exist while idle.

Readers now allocate blocks on demand. The raw queue retains its 64-event
bound, and only two spare blocks are cached for reuse. Returning excess blocks
is nonblocking and allows GC to reclaim burst allocations. Flushed pending
slice entries are cleared so their backing array cannot keep discarded blocks
alive. Output batching, immutable replay data, subscriber backpressure and the
ordered Output → Resize → Exit path are preserved. Replay limits are unchanged.

The same four-live-idle-PTY regression test, measured after GC:

| Version | Additional retained Go heap |
| --- | ---: |
| Before | 33583544 bytes |
| After | 1064848 bytes |

This is approximately a 97% reduction in this controlled workload, not a
prediction of a 97% reduction in total production RSS. Continuous output still
requires bounded replay and transient buffers. Production diagnostics now expose
`heap_alloc_bytes`, `heap_inuse_bytes`, `heap_idle_bytes` and
`heap_released_bytes`; they do not force a collection.

Single-iteration 64 MiB stream benchmarks passed: direct 114.56 MB/s, server
102.03 MB/s, four-pane direct 86.56 MB/s and four-pane server 80.41 MB/s.
These are workload smoke measurements, not a controlled throughput comparison
against the old implementation.

## Cursor preferences

Settings → Terminal includes block, bar and underline choices plus an
independent blinking toggle. The six combinations persist and apply to live
native terminals. Old configurations retain the blinking-block default.
Invalid style values normalize to block. Emulator defaults and DECRQSS style
reports use these preferences; DECSCUSR application overrides remain effective,
and CSI 0 SP q returns to the configured defaults.

The native GUI smoke test clicked the actual Settings controls, saved every
combination, checked the file and runtime snapshot, and captured screenshots.

## macOS IME finding and change

Correction after observing the 小企鹅 candidate window: the previous backing-scale
compensation applied the conversion twice. Ebitengine v2.10.0 converts logical
caret coordinates to client coordinates, and on macOS `dipToGLFWPixel` is an
identity operation because GLFW already uses AppKit points. Water must pass the
rendered grid's logical framebuffer bounds unchanged to `SessionOptions`.
Dividing them by the Retina backing scale placed the candidate window at about
half the cursor's X and Y coordinates. That extra division is now removed.

Terminal geometry comes from the same fitted grid and cursor snapshot used for
rendering; editors use their measured insertion position. Offscreen terminal
cursors do not start a session.

Composer otherwise captures caret bounds only at session creation. Water now
refreshes an idle session when the cursor or layout changes. A live composition
is preserved; session restarts are deferred until marked text is empty. This
avoids discarding input when asynchronous terminal output changes the cursor.
If the dependency adds a live
caret-update API, review this restart policy during upgrade.

The original Retina smoke test parked a raw PTY at two known cursor positions
and checked these supplied bounds. These historical results asserted the
incorrect extra division; the smoke test now expects logical framebuffer bounds:

| Cursor (zero based) | Previous bounds | Correct logical bounds |
| --- | --- | --- |
| column 10, row 4 | [278, 136, 286, 158] | [556, 272, 572, 316] |
| column 30, row 19 | [438, 466, 446, 488] | [876, 932, 892, 976] |

This verifies native GUI geometry and session refresh. It does not constitute
physical keyboard acceptance or direct observation of the 小企鹅 candidate
window. Movement during an ongoing composition is deferred as described above.

## Validation and artifacts

- `go test ./... -timeout=60s` and `go vet ./...`: passed.
- Race tests for goterminal, govt, goui and goserver: passed.
- Scenarios workspace_basic, terminal_basic, agent_detection and terminal_zsh:
  passed against an isolated dev server.
- `scripts/go-ui-cursor-smoke.py`: all six combinations, saved preferences and
  Retina caret geometry passed; artifacts `/tmp/water-cursor.vu6l4i5x`.
- `scripts/go-ui-settings-smoke.py`: complete native settings, keyboard and
  process-interrupt regression passed; artifacts `/tmp/water-settings.eg45f5xc`.

Tests used unique sockets, temporary configuration and recorded owned PIDs.
All owned GUI/server processes were closed. The existing release GUI/server
and the pre-existing untracked remote payloads were left in place. Repository
sources were updated; the installed release has not been replaced.
