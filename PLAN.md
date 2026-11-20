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

1. SGR assembly hillclimb. 2.54 ms is 78% of the per-changed-frame cost and allocation-bound (92k allocs at 200x60); two busy panes exceed the 120 fps budget. Cache SGR per style, append bytes directly, emit fewer resets. Benchmarks already committed.
2. Live-gated integration tests. Grow LOOM_TEST_HC=1 tests from cmd/loomprobe (dial, snapshot, attach, first paint, resize). Live behavior currently rests on manual captures, which do not rerun.
3. FACTS with real cell pixels. The synthetic 1x1 MouseEncoder size makes pixel-geometry queries (CSI 14t/16t) misreport. Send display facts per hc's FACTS frame.
4. Build story. The lib depends on a sibling repo's untracked build dir via PKG_CONFIG_PATH. Vendor the built lib or add a Makefile target that builds it pinned.
5. Multi-pane perf re-check after item 1, splits and tabs, scrollback viewing.
