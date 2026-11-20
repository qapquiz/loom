# Task: one candidate design package for loom M1

You are one of several parallel candidates producing competing designs. Produce the best design you can. Do not hedge against the other candidates.

## Artifact

A design package written to the output path given in your briefing: usage sketch, type sketch with `not implemented` bodies, module map, and prose rationale shaped per /home/nullphase/.agents/skills/architect/references/rationale-template.md. No working code beyond sketch bodies and pseudocode.

## Product shape

loom is a terminal client for herdr-core (hc), a Rust server that owns persistent terminals and streams them over a local Unix socket. loom renders those terminals as panes in a bloom TUI: splits, tabs, agent status badges, zoom, popups. Local only, single user, macOS/Linux, no remote machines in v1.

## Required coverage

Design the milestone M1 spike scope: connect to the core, list and watch terminals, attach one terminal, render its screen inside one bloom pane, send keys and mouse, resize on layout change. Cover at least:

1. Usage first. Write the README-style usage of the loom package: a bubbletea program that connects, opens one pane, renders it, forwards keys, and reacts to resize. Two or three real call sites before any types. The usage is the spec.
2. Session. Socket connection, plain then framed mode, the events stream, and how snapshot and missed resynchronize state.
3. Pane lifecycle as a state machine over hc's event vocabulary (created, lost, restarted, finished, status, size, title). No booleans that must stay in sync.
4. The pane-to-bloom boundary. Exactly one type crosses from emulator state to UI. Define it. Name what the bloom pane node consumes per frame and how it stays cheap for a pane that changes every frame.
5. Input routing. Keys to DATA, emulator reply bytes (WRITE_PTY effect) to AUTOMATIC_INPUT after negotiation, mouse via MouseEncoder with tracking synced from terminal state.
6. Resize plumbing. bloom Frame rectangles to RESIZE frames, including the last-active-viewer size rule.
7. Test seams. Each spike unit (U1 hello, U2 session, U3 attach, U4 input, U5 bloom frame) needs an obvious check. Name the seam per unit.

## Constraints

- Go 1.27. Dependencies: bloomfx.dev/bloom, charm.land/bubbletea/v2, go.mitchellh.com/libghostty (pinned), stdlib. Nothing else.
- Never leak hc wire types (frames, JSON-RPC) into the UI layer. Parse at the boundary.
- Respect the bindings concurrency contract: handles are single-goroutine; RenderState.Update under exclusive access, reads safe after.
- hc's status comes from OSC 7501 reports. Never scrape screens for agent state.
- Smallest type set that proves the spike. Bias to deletion.

## Read first, in this order

1. /home/nullphase/Projects/loom/docs/grounding-hc.md
2. /home/nullphase/Projects/loom/docs/grounding-bindings.md
3. /home/nullphase/Projects/loom/docs/grounding-bloom.md
4. /home/nullphase/.agents/skills/architect/SKILL.md (in full)
5. /home/nullphase/.agents/skills/architect/references/rationale-template.md

Write the package to your assigned output path, then report done with `hc send t3w` from your pane.
