# loom

A terminal client for herdr-core (`hc`) built with bloom. Working name; may change.

Status: spike. See PLAN.md.

## Build

Requires go, zig (0.17.0+), git, and pkg-config. One command fetches and
builds the native VT library, then everything else is plain go:

    make deps && make test

`make deps` clones ghostty at commit
`5813c9461d5af78b6307a1254d00b1bc190eaccd` (shallow), builds
libghostty-vt with `zig build -Demit-lib-vt -Demit-xcframework=false
-Doptimize=ReleaseFast`, and installs it under `build/libghostty`.
Targets `build`, `test` (race), and `run` export
`PKG_CONFIG_PATH=build/libghostty/lib/pkgconfig` for you.

Pins. ghostty at the commit above. The Go bindings are
`go.mitchellh.com/libghostty v0.0.0-20261107144202-b13f277ddcb2`
(commit b13f277ddcb2). `make clean` removes the whole `build/` tree.
