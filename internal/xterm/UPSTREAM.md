# Water xterm core fork

This directory is derived from `github.com/gitpod-io/xterm-go` at commit
`73e8ebcf2735` (MIT licensed; see `LICENSE`).

Water keeps the fork in-tree so terminal-emulator hot-path fixes can be pinned
and benchmarked together with the Go client. The initial local change reuses
`BufferLine` sparse maps during `Fill`/`CopyFrom` instead of allocating two
new maps for every recycled scrollback line. Upstream synchronization should
preserve that behavior or remove the fork once upstream provides an equivalent
fix.
