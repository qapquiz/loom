# Grounding: go.mitchellh.com/libghostty

Go bindings for libghostty-vt, maintained by the Ghostty author. Pinned at pseudo-version v0.0.0-20261107144202-b13f277ddcb2 (commit b13f277ddcb2, the "libghostty 1.0 compat" commit, Nov 7 2026). The Go API is explicitly unstable. The C core under it just crossed 1.0 with major-version discipline.

## Build on this machine

libghostty-vt builds in-repo: `make deps` shallow-clones ghostty at pinned commit 5813c9461d5af78b6307a1254d00b1bc190eaccd and runs `zig build -Demit-lib-vt -Demit-xcframework=false -Doptimize=ReleaseFast --prefix build/libghostty`. The flags mirror ghostty's own CMakeLists.txt for a release build (CMake RELEASE maps to ReleaseFast); zig 0.17.1 is the compiler. pkg-config files land in build/libghostty/lib/pkgconfig, and the Makefile's build/test/run targets export that PKG_CONFIG_PATH (plus LD_LIBRARY_PATH for the shared lib) so no sibling checkout is consulted. The stamp build/.deps-<sha> makes deps idempotent and rebuilds on pin change. The ~/Projects/go-libghostty checkout remains the reference for the pinned build the Makefile reproduces.

## API inventory (from the repo sources, read this session)

- `NewTerminal(WithSize(80,24), WithMaxScrollbackLines(n))`. `Terminal.VTWrite([]byte)` or `Write` as io.Writer for program deltas.
- Effects callbacks set per terminal: WritePty (bytes the program expects back; these go to hc AUTOMATIC_INPUT), TitleChanged, ProgramStatus, Bell, ClipboardWrite/Read, DesktopNotification, ProgressReport, Enquiry, Xtversion, SizeReport (mode 2048 and CSI 14t/16t/18t/19t), ColorScheme, DeviceAttributes, SemanticPrompt, RenderHold (synchronized output), UnknownSequence, Reset, WindowResize.
- `RenderState.Update` runs under exclusive terminal access; after it returns, reads are safe until the next Update. This is the pane-to-UI read path.
- `Formatter` renders screen contents (plain and styled formats) with an internal write buffer.
- `KeyEncoder`: legacy plus Kitty keyboard protocol. `MouseEncoder`: X10, UTF-8, SGR, URxvt, SGR-pixels, with `SetFromTerminal` to sync tracking mode.
- `Snapshot` encode/restore of terminal state to bytes.
- Handles are single-goroutine unless documented (doc.go). RenderState and Search allow limited concurrent reads.

## Verification assets

The repo ships benchmarks (formatter, render state, snapshot) and extensive tests. Run them before trusting any frame-cost number. Read ~/Projects/go-libghostty/doc.go first; it states the concurrency contract in full.
