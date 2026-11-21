# loom

Working name. A terminal client for herdr-core (`hc`) built with bloom. The name is a placeholder and can change without ceremony.

Status: spike milestone. Design candidates in `design/`, decision trail in `decisions.tsv` (uncommitted, per show-me-your-work).

## Feature playbook steps (verbatim from poteto-mode)

1. `how` over the affected subsystem. Done across this session. Artifacts: `docs/grounding-hc.md`, `docs/grounding-bindings.md`, `docs/grounding-bloom.md`.
2. `architect` for parallel design exploration. Running. Three candidates from the `architect runners` line (zai/glm-5.3-flash x3), spawned as hc children per the operator's delegation protocol.
3. Write the throughput checkpoint as four todo items. See below.
4. Delegate code-writing to a subagent using the configured feature model (`feature, refactoring: zai/glm-5.3-flash`) with a specific scope. Pending design synthesis.
5. Verify on the matching surface. Pending. Spike gate is a real terminal running vim.
6. Rebase into small, ordered commits. Pending. Units below.
7. If the design is contested, `interrogate` before shipping. Pending.
8. Run **Opening a PR**. Pending. Local spike may stay unPR'd until the operator asks.

## Throughput checkpoint

- **Blocking first steps.** Go 1.27.4 active. libghostty-vt static lib building in hc terminal `libbuild` (pinned ghostty 5813c9461d5af78b6307a1254d00b1bc190eaccd). Design synthesis gates all code.
- **Independent workstreams.** After synthesis: hc session client, bloom pane node, and test harness are disjoint files and can go to parallel delegates.
- **Shared mutable state.** Pane state is written only by the session goroutine. The UI reads RenderState snapshots per the bindings contract. No shared writes.
- **Smallest safe decomposition.** Spike stays single-owner. Only the design sketch fans out (three runners).

## Spike units (sequence-verifiable-units)

Each unit ends in a check before the next starts.

- U0 toolchain. go 1.27.4 plus libghostty-vt built. Check: pkg-config finds ghostty-vt, `libbuild` exits 0.
- U1 bindings hello. NewTerminal 80x24, VTWrite ANSI bytes, formatter plain text equals the expected string. Check: go test.
- U2 hc session. Socket connect, terminal.list, events.subscribe snapshot parsed into domain types. Check: test observes the `libbuild` terminal record.
- U3 attach. Framed handshake, first paint and deltas into a Terminal, RenderState.Update. Check: formatter screen text matches a known program's output.
- U4 input. Keys as DATA drive a child shell; WRITE_PTY effect bytes route to hc AUTOMATIC_INPUT. Check: screen shows the echo output of a programmatically typed command.
- U5 bloom frame. One pane node in a bubbletea program, RESIZE sent on resize, detach and reattach keeps the screen. Check: real-surface capture of vim.
- U6 numbers. Run the bindings' own benchmarks first. Apply the benchmark-checklist skill before reporting any number.

## M2 queue, ordered by run-end evidence

1. DONE (hillclimb, 5 iterations, 3 kept): dense assembly 2.55 ms -> 0.47 ms (-81%, 92,428 -> 2 allocs, 3.6 -> 1.06 MB); typical 0.38 -> 0.16 ms; idle unchanged at 0 allocs. A dense changed frame is now ~1.2 ms total with publish and parse, 15% of the 8.33 ms budget; two busy panes now fit at 120 fps. Parked for M2: hint-based sizing (needs a steady-state bench first, /tmp/hc5-hint-sizing.patch), kitty key releases (in git stash, unreviewed).
2. DONE: hc/live_test.go, four live-gated tests (snapshot by name, attach+paint+autoInput, typed echo e2e, resize), double-green plus 10/10 post-race-fix; subscribe-window feed race found and fixed (5bed340); final commit live-gated post-audit.
3. DONE in two phases: c8dfd58 documented that no honest px source exists in the stack (pixel queries answered with silence, not misreports) and 2b99d58 built the source: own TIOCGWINSZ, real cell px into Resize/SetSize/SetEffectSize, FACTS Display {cell:[w,h]} on the wire; 1x1 survives only where the tty reports no pixels.
4. DONE: make deps pins ghostty 5813c94 and builds via zig ReleaseFast (flag mapping from upstream CMake), fresh-prefix verified; local-lib builds confirmed. make test is skip-green for live gates; use make live-test.
5. Multi-pane perf re-check absorbed by the hillclimb ledger (two dense panes ~1.2ms each fit the 8.33ms budget). NEXT RUN: splits and tabs, scrollback viewing, parked candidates (kitty key-release stash, hint-sizing patch with a steady-state bench).
