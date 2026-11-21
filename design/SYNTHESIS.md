# loom M1 synthesis

Base: candidate C, per the cross-judge verdict and orchestrator agreement after reading all three candidates end to end. The four grafts below fold in the strongest pieces of A and B. Where this file conflicts with any candidate, this file wins.

## Scores (cross-judge, rubric in design/rubric.md)

| criterion | a | b | c |
|---|---|---|---|
| boundary depth | 2 | 2 | 2 |
| concurrency model | 2 | 2 | 2 |
| domain modeling | 2 | 2 | 1 |
| spike size | 2 | 1 | 2 |
| verifiability | 1 | 1 | 2 |
| usage honesty | 2 | 1 | 2 |

Orchestrator divergence, resolved. Caveat on this table, recorded at run-end audit: the cross-judge scored candidate-a and candidate-b while they were still padded scrollback reconstructions missing their openings (two were 1113 lines of wrapped text). The base candidate C was complete, and the orchestrator re-read all three corrected candidates in full before accepting the verdict, so the base pick and the grafts rest on the corrected text. The per-criterion scores for a and b carry the reconstruction caveat; do not cite them without it.

## Module map (base C)

```
loom/
  hc/        Session, Dial, DialConn, Events, Attach, Close.
             wire.go unexported: frame codec, JSON-RPC, hello, negotiation.
             stream.go: Stream, StreamOut, credit-after-Delta, auto-input gate.
             event.go: Event vocabulary, seq/missed, TerminalInfo.
  room/      Room, Terminal, State, Apply. Lifecycle over hc events (sidebar source).
  pane/      Pane, Screen, Cell, Style, Cursor, Op. Publish, reconcile,
             stream lifecycle machine (graft 1).
  ui/        App, PaneNode, sidebar, keymap. Wave 2.
  cmd/loom   thin shell. Wave 2.
```

Wire types stay unexported inside hc. UI imports hc, never wire. Compiler-enforced.

## Grafts adopted (judge verdict, adopted by orchestrator)

1. From A: the pane stream gets a total `advance(state, change)` machine. LostLive encodes want-attach as state, EffReset covers restart rows. Distinct from room's machine; room watches events, pane watches its stream.
2. From A: `DialConn(net.Conn)` plus a loopback testcore. Framed handshake, demux, and credit run as real code in every unit test. C's scripted fake core survives only for room event-table tests.
3. From B: skip publish when RenderState reports not dirty. Idle panes cost bloom nothing.
4. From B: writes phase-gated. SendInput, Resize, and Focus are refused unless the stream machine says live, derived from the machine, never a parallel flag.

## The boundary

`pane.Screen` is the only type crossing emulator state to UI. Cols, Rows, Cells (row-major, with a Wide flag for double-width runes, from candidate B's PaneCell), Cursor, Title, Status, MouseOn. Status comes from hc's OSC 7501 status events for the sidebar; Screen carries the ProgramStatus effect value for the pane header. MouseOn reflects tracking mode and tells bloom to stand down its MouseMode and selection when the program owns the mouse.

## Flagged assumptions, resolved

- bloom cell draw path, RESOLVED by reading the source this session. Nodes have no per-cell draw API. The node contract is `View() string` (bloom parses it into cells); `canvas.Surface` with `Set` and `Reach` is for effects only, and effects are compared with `==`, so a function-based effect would break the idle-frame skip. Therefore PaneNode renders Screen into a styled string (direct SGR assembly from cells, no lipgloss) and returns it. Cost: one assemble pass plus one bloom parse pass per changed pane frame, and bloom's own unchanged-frame cache makes idle panes free. Ultraviolet cells carry Width for wide glyphs, so Screen's Wide flag maps cleanly. U6 validates the number.
- RenderState per-cell reads vs Formatter-only, RESOLVED by the wave 1 implementer against the bindings source: RenderStateRowIterator and RenderStateRowCells give real per-cell reads, valid until the next Update. The cheap Screen path stands.
- Terminal resize API, RESOLVED: `Terminal.Resize(cols, rows uint16, cellWidthPx, cellHeightPx uint32)`, pixel args passable as 0 headless.
- hc socket discovery, RESOLVED in wave 1: HC_SOCKET, HC_TERMINAL_SOCKET, HC_SESSION, then XDG paths.
- OSC 7501 status vocabulary, RESOLVED live in wave 2: hc relays `idle`, `working`, `done`, `blocked`, `error`, stored as reported, never inferred (schema README). Sidebar badges rendered [working] and [done] from live events.
- Mouse geometry, RESOLVED with a conditional compromise, landed M2 item 3 phase 2: loom now does its own TIOCGWINSZ on os.Stdin beside bubbletea (`golang.org/x/sys/unix`, promoted to a direct require) — WindowSizeMsg's cell counts divide the ioctl's xpixel/ypixel into real cell px, threaded `App.setCellPx` → `Pane.SetCellPx` (stored pump-side, session-goroutine only). Real px now feeds every term.Resize call (never 0), publish's MouseEncoder.SetSize (screen px = cols×cellW), and the SetEffectSize handler registered in newEmulator, which answers every report style (14/16/18/19 t, mode 2048) from emulator rows/cols + stored px and returns true. The 1x1 synthetic cell survives as the fallback only: zero pixels, failed ioctl or sub-cell quotients (headless) leave 1x1, where cells and pixels coincide so cell-coordinate protocols (X10, SGR, URxvt) encode correctly and only SGR-pixels stays approximate. FACTS is wired end to end: StreamOut.Facts sends frame kind 5 on the stream channel, the pane transports the app-built Display payload (`{"cell":[w,h]}`, the schema's exact field names; bg/fg/cursor stay null until a color source exists) phase-gated like every write, and the app reports after attach confirmation (the first live wake, the same seam that pays FOCUS) and on every cell-px change. History: M2 item 3 phase 1 measured both then-candidate truth sources dead — bubbletea v2.1.2's WindowSizeMsg is cells-only (x/term's GetSize discards xpixel/ypixel) and the emulator's px reads were arithmetic on the cell px Resize was given, 0 headless. Wave 1 commit 0f0c07d carried the original code comment; the conditional-compromise record lives beside the SetSize call in emulator.go.

## Wave plan

- Wave 1, done and verified: hc package, room, pane, loopback testcore. Race-clean suite, vet clean.
- Wave 2, now: ui App, PaneNode as View-string renderer from Screen, sidebar, keymap, cmd. U5 real-surface vim gate, run by the orchestrator on a live core. U6 benchmarks, through the benchmark-checklist skill before any number is reported.
