   func (p *Pane) pump(data []byte, s *Session) {           // not implemented
       p.term.VTWrite(data)                                  // 1. emulator eats bytes
       if p.holdDepth > 0 { return }                         // RenderHold: defer repaint
       p.render.Update(p.term)                               // 2. exclusive access, then reads safe
       p.keyEnc.SyncFrom(p.term); p.mouseEnc.SetFromTerminal(p.term) // 3. input modes track state
       p.screen = buildScreen(p.render, p.nextGen())         // 4. boundary value swap
       s.SendCredit(p.channel, len(data))                    // 5. credit after consume
   }
 ```

 Credit-after-consume means the tea mailbox depth is the only buffer between wire and emulator; if tea ever fell behind, uncredited bytes accumulate and hc's >4 MiB fresh-repaint rule is the natural
 recovery. Control events are never blocked by a stalled pane (hc guarantee).

 Keys out: tea.KeyPressMsg → ForwardKey → Pane.EncodeKey (KeyEncoder, already synced with kitty/legacy mode from step 3) → SendData(ch, bytes) → ordered writer queue → DATA frame. Encode happens on
 the tea goroutine inside Update — owner-safe, and enqueue order equals keystroke order, so the wire never reorders typed characters.

 Emulator replies: libghostty WritePty effect fires in pump (owner goroutine) → if grant.AutoInput (confirmed at attach, negotiated at hello) → SendAutoInput(ch, bytes); otherwise count-and-drop.
 Because the core strips queries it already answered, our emulator only emits replies for queries that actually reached it — no double-answer suppression logic needed.

 Mouse: tea.MouseMsg → cell coords via last Frame rect → Pane.EncodeMouse(msg, x, y) (MouseEncoder; SGR/UTF-8/X10 chosen by tracking mode synced in step 3; tracking off ⇒ encode nothing) → DATA.
 Bytes to DATA because mouse is user input; AUTOMATIC_INPUT is strictly terminal-generated traffic.

 Resize: bloom records geometry every frame → SyncGeometry on FrameMsg:

 ```go
   // not implemented; the last-active-viewer rule:
   //   rect := frame.RectOf(node); cols, rows := inner rect in cells
   //   if pane focused and (cols,rows) != pane.sentSize:
   //       pane.sentSize = cols,rows; pane.termResize(cols,rows)  // local, immediate
   //       sess.SendResize(ch, cols, rows)
   //   on focus gained: SendFocus(ch, true) first, then the RESIZE above
   //     (active viewer imposes its rect — hc: last active viewer sets the size)
   //   on focus lost: SendFocus(ch, false); never resize an unfocused pane
 ```

 sentSize per pane makes RESIZE idempotent (send only on delta; a drag frame storm coalesces to one frame per actual change, at most once per display frame). Local Terminal size is set in the same
 breath as the outgoing RESIZE so buildScreen produces the right-shaped Screen on the next delta without waiting for the round-trip.

 Resync: missed → reader adopts snapshot seq, emits Resynced → PaneSet.Apply reconciles membership (diff against panes; absent+attached ⇒ Lost transition, which detaches), refreshes Info cache. Later
 stale events were already dropped at the reader. Core process death kills the socket ⇒ SessionDead ⇒ M1 usage exits with the error; reconnect is post-spike.

 ### Load-bearing decisions

 1. The tea goroutine owns every libghostty handle. Reader and writer goroutines own only the socket; handoff to handles is the tea mailbox. This satisfies "handles are single-goroutine" and
    "RenderState.Update under exclusive access, reads safe after" by construction — View reads p.screen (an immutable value produced earlier in the same goroutine's message loop), so there are no
    locks anywhere on the emulator path (per separate-before-serializing-shared-state: three actors, one owner each, merge at the read boundary).
 2. Request/response is call-and-return; events are a feed. Hello/Subscribe/List/Attach are synchronous futures resolved by the reader; everything after subscribe flows through Feed in order. Callers
    read like RPC; streams read like streams (per boundary-discipline).
 3. The boundary is a value, not a view. Screen is built once per terminal delta, immutable, swapped by assignment. The UI can never reach a Terminal, RenderState, or encoder; interface depth is
    high: behind PaneSet.Apply + *Screen hide the demux, credit accounting, seq guard, state machine, formatter, and encoder sync.
 4. hcwire is the only wire-typed package, internal, stdlib-only, and its types never appear in any signature that ui/ or cmd/ can call. Frame codec and JSON-RPC envelope are ~150 lines;
    params/results are built inline at root call sites so wire shapes cannot accrete in the domain.
 5. Status hygiene by construction: the only source of TerminalInfo.Status is hc's status event (OSC 7501 relayed). The pane ignores libghostty's ProgramStatus/TitleChanged effects in M1 (hc is the
    single source; no screen scraping, no dual-source divergence).

 ### Invariants in types, validation at boundaries, non-goals

 - Encoded in types: lifecycle (7-state enum + total advance, no sync booleans); "finished" (ExitCode *int, no Exited bool beside an int); sealed CloseReason; sealed-by-enum Change; Screen.Cells
   length pinned to Rows*Cols by construction in buildScreen.
 - Validation lives at boundaries only: frame codec enforces 16 MiB cap and kind validity; JSON-RPC decode errors become SessionDead/StreamError; everything inside trusts the types (per
   validate-at-boundaries).
 - Deliberately not doing in M1: scrollback viewing (RenderState screen only), zoom/popups/tabs, text selection, drag-to-resize borders, reconnect after core restart, libghostty Snapshot persistence
   across pane reset, SGR-pixel mouse, win32-input-mode (CSI 9001) key encoding — all logged-if-seen, none blocking.
 - Interface depth judgment: the public surface is ~20 functions and 15 types for a full multiplexer loop. The complexity hidden (frame codec, credit, seq, attach negotiation, emulator ownership,
   mode sync) is roughly 10× the surface; the complexity exposed (bloom node + 6 message kinds) is what a TUI author actually thinks in.

 Test seams (one obvious check per spike unit)

 ┌──────────────┬───────────────────────────────────────────────────────────────────────────────────────────────────────────────────┬─────────────────────────────────────────────────────────────────┐
 │ Unit         │ Check                                                                                                             │ Seam
 ├──────────────┼───────────────────────────────────────────────────────────────────────────────────────────────────────────────────┼─────────────────────────────────────────────────────────────────┤
 │ U1 bindings  │ screen_test.go: table of canned ANSI byte strings → VTWrite → buildScreen → assert Cells/Cursor (plain text and   │ buildScreen(rs, gen) as the only function under test; Terminal
 │ hello        │ styled cases). Pure, no I/O.                                                                                      │ built in-test per bindings' own tests.
 ├──────────────┼───────────────────────────────────────────────────────────────────────────────────────────────────────────────────┼─────────────────────────────────────────────────────────────────┤
 │ U2 hc        │ (a) hcwire roundtrip: encode frames → NextFrame → deep-equal, incl. 16 MiB rejection. (b) lifecycle table test:   │ DialConn(net.Conn) for (a)/(b); real socket path from LOOM_SOCK
 │ session      │ every (State, Change) row of advance. (c) env-gated LOOM_TEST_HC=1: dial real socket, Subscribe, assert the       │ only for (c).
 │              │ libbuild record parses into TerminalInfo.                                                                         │
 ├──────────────┼───────────────────────────────────────────────────────────────────────────────────────────────────────────────────┼─────────────────────────────────────────────────────────────────┤
 │ U3 attach    │ (a) unit: canned DATA/CLOSE frame sequence through DialConn peer → assert Pane.Screen() matches expected text     │ Same DialConn seam + fake-core helper (testcore); real hc for
 │              │ after first paint + deltas, credit frames observed. (b) env-gated: attach a program with known output, screen     │ (b).
 │              │ text equality.                                                                                                    │
 ├──────────────┼───────────────────────────────────────────────────────────────────────────────────────────────────────────────────┼─────────────────────────────────────────────────────────────────┤
 │ U4 input     │ Loopback: fake core echoes; inject tea.KeyPressMsgs via ForwardKey; assert captured DATA bytes equal KeyEncoder   │ DialConn + testcore capture side; ForwardKey/ForwardMouse as
 │              │ expectations and Screen shows the typed line. Env-gated real shell: echo marker visible in Screen.                │ the single entry points under test.
 ├──────────────┼───────────────────────────────────────────────────────────────────────────────────────────────────────────────────┼─────────────────────────────────────────────────────────────────┤
 │ U5 bloom     │ (a) golden: PaneNode.Draw from a fixed Screen into a small canvas → golden cells. (b) env-gated real surface:     │ The pane node consumes only *Screen + rect, so (a) needs no
 │ frame        │ loom running vim in an hc terminal; assert RESIZE frames on layout change and detach/reattach preserves the       │ bubbletea program at all; (b) is the documented real-surface
 │              │ screen; PLAN's visual gate.                                                                                       │ check.
 └──────────────┴───────────────────────────────────────────────────────────────────────────────────────────────────────────────────┴─────────────────────────────────────────────────────────────────┘

 DialConn is the whole mock story: one constructor taking net.Conn. Everything downstream (framed handshake, demux, credit, machine) runs real code in every test.

 Synthesis decision

 Not filled in by this runner — this is a Phase B candidate. The arena synthesis step records here which candidate became the base and what was grafted.

 Tradeoffs accepted

 - We accept unbounded tea-mailbox depth in exchange for zero backpressure machinery. Credit-after-consume means a pane that outruns the UI loop grows the mailbox until hc's fresh-repaint kicks in;
   we bet 8 ms frame budgets swallow realistic output. If U6 numbers disagree, the fix is local (reader-side per-channel pause), not structural.
 - We accept a tiny mutex around RPC futures in exchange for call-and-return attach. Alternative was routing replies through the feed; that pushes correlation into the state machine, which is exactly
   the complexity the boundary exists to hide.
 - We accept Cell/CellStyle as loom-owned types in exchange for a bloom-free root package. If bloom's cell path exposes a compatible cell type we alias and delete ours (deletion bias).
 - We accept the root package importing bubbletea v2 for Key/Mouse message types in exchange for not building a parallel key vocabulary and a conversion layer. The root stays free of bloom and of any
   UI rendering concept.
 - We accept one-shot FACTS with an inline payload (no domain type) in exchange for not modeling display facts before reading hc schema's exact shape — flagged, not forgotten.
 - We honor RenderHold with a depth counter and a deferred repaint in exchange for tear-free vim redraws; cost is two effect hooks and one int.

 Alternatives considered

 - Per-pane goroutine owning its Terminal, publishing pre-rendered styled strings to the UI. Lost: ownership of N handles across N goroutines makes every handle invariant a cross-goroutine claim;
   RenderState reads then race with Updates unless we snapshot per frame anyway — the same cost as building Screen, plus locks, plus goroutine lifecycle for panes that come and go. Exposes
   concurrency to callers; hides nothing.
 - Emulator-free UI: hand hc stream bytes straight to a styled-string Viewport per frame (bloom parses ANSI). Lost: violates the one-type boundary (raw stream bytes ARE wire-adjacent state leaking to
   UI), pays re-parse per frame per bloom's own warning, and leaves no seam for U1/U3 assertions (nothing owns parsed terminal state to assert against).
 - Session as a bubbletea tea.Model wrapper that callers embed (loom ships the whole app, callers only supply config). Lost: hides bloom from callers today and blocks every future layout need behind
   loom's API surface; interface depth collapses into a framework lock-in. The thin-shell model in cmd/loom keeps loom a library.

 Open questions and risks

 - Bloom cell path: does canvas.Surface/Drawable exist with per-cell Set semantics as assumed? If the page read changes the story, PaneNode falls back to cached styled lines (already in the sketch);
   what we'd want from bloom is a word on Set cost for mostly-changed surfaces.
 - FrameMsg shape and node identity: assumed bloom.FrameMsg{Frame} with rect lookup by node. Verify against bloom docs before U5; SyncGeometry is the only consumer, so drift is contained.
 - Encoder sync method names: KeyEncoder kitty-flag sync from terminal state and the Terminal resize setter are assumed names (SyncFrom, termResize); the bindings' doc.go is the arbiter. Signature
   drift touches only pane.go.
 - Does one framed-mode channel-0 DATA payload carry exactly one JSON-RPC message? Assumed yes (plain mode is one-per-line). If the core batches, hcwire absorbs it.
 - CSI 9001 win32-input-mode in first paint: if a program flips it and libghostty's KeyEncoder lacks the mode, keys degrade. Is there a core-side query loom should send to disable it, or does the
   core translate?
 - Terminal initial size at attach: assumed core pty starts 80×24; loom RESIZEs immediately after grant, so transient wrong-size paints are self-healing — confirm hc honors viewer RESIZE before first
   full paint.
 - Lost semantics: grounding doesn't say whether the stream survives a lost event; the machine detaches conservatively. If hc keeps streams alive across lost, LostLive → restart-recovery needs a
   cheaper row.

 Next implementation step

 Write internal/hcwire frame codec + the DialConn/testcore loopback, then U2's table and roundtrip tests — the seam every other unit hangs off exists before any UI code.


 <hc from="t3w">Your full package scrolled past what the orchestrator could recover. Re-output ONLY the missing opening sections of your candidate package, as plain text in this order: (1) the
 package title line, (2) the problem statement, (3) the usage sketch with every call site. Do not repeat your type sketch, module map, or anything after the usage. Keep it tight.</hc>


 The orchestrator says the opening scrolled past. Re-output only: (1) title line, (2) problem statement, (3) usage sketch with every call site. Tight. Plain text. No type sketch, module map, or
 anything after.

 Candidate A — loom M1: one owner, one boundary value

 Problem

 loom is a terminal multiplexer client for herdr-core: hc owns persistent terminals on a Unix socket; loom renders them as panes in a bloom TUI. M1 must prove the full loop — connect, watch the
 terminal list, attach one terminal, render its screen in a pane, forward keys and mouse, resize on layout change.

 Three constraints make the shape non-obvious:

 1. The bindings concurrency contract (libghostty doc.go): terminal handles are single-goroutine; RenderState.Update needs exclusive access, after which reads are safe until the next Update.
    Something must own every Terminal on exactly one goroutine while socket bytes arrive at arbitrary times and bloom's View runs per frame.
 2. Two flow directions with different natural owners: hc pushes stream bytes and events asynchronously; bubbletea wants everything to arrive as ordered messages on one loop; bloom wants per-frame
    reads that are cheap even when a pane repaints every frame (styled strings re-parse per frame — bloom's own perf notes).
 3. Wire hygiene: hc's frames and JSON-RPC must never reach the UI layer; status must come from OSC 7501 relayed as hc events, never scraped off screens.

 Phase A artifacts honored: frame kinds DATA/CREDIT/CLOSE/RESIZE/FOCUS/FACTS/AUTOMATIC_INPUT with credit-after-consume flow control; plain-then-framed hello; events.subscribe snapshot + missed resync
 with seq guard; WritePty effects → AUTOMATIC_INPUT only after feature negotiation; last-active-viewer size rule.

 Usage (caller's view)

 The loom root package is the spec. Everything a caller needs is Dial → Hello → Subscribe → NewPaneSet → Pump → Apply, plus one bloom node from loom/ui.

 ### Call site 1 — the spike (cmd/loom/main.go)

 ```go
   package main

   import (
       "context"
       tea "charm.land/bubbletea/v2"
       "loom"
       "loom/ui"
   )

   func main() {
       ctx := context.Background()

       sess, err := loom.Dial(ctx, loom.DefaultSocket()) // plain JSON-RPC lines
       if err != nil { panic(err) }
       defer sess.Close()
       if err := sess.Hello(ctx, loom.Features{AutomaticInput: true}); err != nil {
           panic(err) // returns ⇒ socket is now framed
       }

       snap, err := sess.Subscribe(ctx) // synchronous; seq baseline established
       if err != nil { panic(err) }

       panes := loom.NewPaneSet(snap)
       pane := panes.Open("libbuild") // lifecycle: Idle → (attach in Init)

       app := ui.NewApp(sess, panes, pane.ID())
       p := tea.NewProgram(app, tea.WithAltScreen())
       go loom.Pump(sess, p) // session feed → program mailbox, in order
       p.Run()
   }
 ```

 ### Call site 2 — the app model (thin shell; all logic behind loom)

 ```go
   func (m App) Init() tea.Cmd { return m.Panes.Open(m.FocusID).AttachCmd(m.Sess) }

   func (m App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
       switch msg := msg.(type) {
       case tea.KeyPressMsg, tea.KeyMsg, tea.PasteMsg:
           if p := m.Panes.Attached(); p != nil && p.State() == loom.Attached {
               return m, loom.ForwardKey(m.Sess, p, msg) // KeyEncoder → DATA
           }
       case tea.MouseMsg:
           if p := m.Panes.Attached(); p != nil && p.State() == loom.Attached {
               x, y := m.Frame.CellOf(m.Node, msg) // geometry recorded last frame
               return m, loom.ForwardMouse(m.Sess, p, msg, x, y)
           }
       case bloom.FrameMsg: // bloom recorded where every node went
           return m, loom.SyncGeometry(m.Sess, m.Panes, m.Node, msg.Frame)
       default:
           return m, m.Panes.Apply(msg, m.Sess) // lifecycle + stream bytes + resync
       }
       return m, nil
   }

   func (m App) View() bloom.Node {
       return bloom.Row(
           bloom.Fixed(24, ui.Sidebar(m.Panes.All(), m.FocusID)), // list + OSC 7501 badges
           bloom.Fill(ui.NewPaneNode(m.Panes.Focused())),         // the live screen
       )
   }
 ```

 ### Call site 3 — the loopback test seam (U4, no real hc)

 ```go
   func TestTypedCommandEchoes(t *testing.T) {
       core := testcore.Echo(t)                 // in-memory peer speaking hc frames
       sess := loom.DialConn(core.ClientSide()) // ← the seam: net.Conn, not a socket path
       sess.Hello(ctx, loom.Features{AutomaticInput: true})
       panes := loom.NewPaneSet(loom.Snapshot{Terminals: []loom.TerminalInfo{{ID: "t1"}}})
       pane := panes.Open("t1")
       grant, err := sess.Attach(ctx, "t1")     // fake core grants channel 7, auto-input ok

       for _, k := range "echo hi\r" {
           panes.Apply(loom.InjectKey(pane, tea.KeyPressMsg(...k...)), sess)
       }
       // assert: captured DATA frames on ch 7 == expected KeyEncoder output;
       // pane.Screen() shows the echoed characters.
   }
 ```

 The real-hc variant of the same test (env-gated, LOOM_TEST_HC=1) attaches a shell and asserts pane.Screen() text contains the marker output — that is PLAN's U4 gate.




   func (m App) View() bloom.Node {
       return bloom.Row(
           bloom.Fixed(24, ui.Sidebar(m.Panes.All(), m.FocusID)), // list + OSC 7501 badges
           bloom.Fill(ui.NewPaneNode(m.Panes.Focused())),         // the live screen
       )
   }
 ```
 ### Call site 3 — the loopback test seam (U4, no real hc)
 ```go
   func TestTypedCommandEchoes(t *testing.T) {
       core := testcore.Echo(t)                 // in-memory peer speaking hc frames
       sess := loom.DialConn(core.ClientSide()) // ← the seam: net.Conn, not a socket path
       sess.Hello(ctx, loom.Features{AutomaticInput: true})
       panes := loom.NewPaneSet(loom.Snapshot{Terminals: []loom.TerminalInfo{{ID: "t1"}}})
       pane := panes.Open("t1")
       grant, err := sess.Attach(ctx, "t1")     // fake core grants channel 7, auto-input ok
       for _, k := range "echo hi\r" {
           panes.Apply(loom.InjectKey(pane, tea.KeyPressMsg(...k...)), sess)
       }
       // assert: captured DATA frames on ch 7 == expected KeyEncoder output;
       // pane.Screen() shows the echoed characters.
   }
 ```
 The real-hc variant of the same test (env-gated, LOOM_TEST_HC=1) attaches a shell and asserts pane.Screen() text contains the marker output — that is PLAN's U4 gate.
 Module map
 ```
   loom/
     go.mod                    module loom · go 1.27
                               deps: bloomfx.dev/bloom, charm.land/bubbletea/v2,
                                     go.mitchellh.com/libghostty (pinned b13f277ddcb2), stdlib
     loom.go                   package doc: goroutine-ownership contract (see Shape)
     session.go                Dial, DialConn, Hello+framed switch, RPC futures, send queue,
                               SendData/SendAutoInput/SendResize/SendFocus, Close
     events.go                 Subscribe, seq guard + missed→Resynced, Pump, Feed,
                               wire-event → TerminalInfo normalization (boundary)
     stream.go                 attach channels, credit-after-consume accounting, CloseReason
     pane.go                   Pane: Terminal owner, effects hooks, encoders, refresh()
     lifecycle.go              State enum, Change enum, advance() pure transition fn, effects
     screen.go                 Screen/Cell/CellStyle/Cursor + buildScreen (emulator→UI)
     internal/hcwire/          frame codec ([ch u32 LE][kind u8][len u32 LE][payload], 16 MiB cap),
                               JSON-RPC envelope for channel 0. Imported ONLY by root package.
     ui/panenode.go            bloom pane node: draws *loom.Screen (cell path; styled fallback)
     ui/sidebar.go             terminal list + status badges from TerminalInfo
     cmd/loom/main.go          dial/hello/subscribe/pump wiring; thin shell
 ```
 Dependency direction is a DAG: cmd → ui → loom(root) → {internal/hcwire, libghostty, bubbletea types}; hcwire → stdlib only. The wire-hygiene constraint is enforced by package layout: grep -r hcwire
 ui/ cmd/ is empty by construction; only root parses frames.
 Shape
 ### Data structures first
 Session side (root package, session.go / events.go / stream.go).
 ```go
   // Session owns the socket. Reader goroutine: decode frames, demux, publish Msgs.
   // Writer goroutine: consume one ordered queue, write frames. All handle work
   // (Terminals, RenderState, encoders) happens on the tea goroutine, never here.
   type Session struct { /* conn, writerCh, futures, feed, seq */ }
   func Dial(ctx context.Context, path string) (*Session, error)        // real socket
   func DialConn(c net.Conn) *Session                                   // test seam (U2–U4)
   type Features struct{ AutomaticInput bool }
   // Hello is plain-mode JSON-RPC; on reply the conn switches to frame codec.
   // not implemented
   func (s *Session) Hello(ctx context.Context, f Features) error
   // Subscribe returns the snapshot synchronously (caller seeds PaneSet before
   // the program starts ⇒ causal order). Notifications flow on Feed afterwards.
   func (s *Session) Subscribe(ctx context.Context) (Snapshot, error)   // not implemented
   func (s *Session) List(ctx context.Context) ([]TerminalInfo, error)  // not implemented (picker)
   type Snapshot struct {
       Seq       uint64
       Terminals []TerminalInfo
   }
   type TerminalInfo struct {
       ID       string
       Title    string
       Status   string // OSC 7501 relayed by hc; opaque badge text in M1
       Cols     int    // destination of hc `size` events; shown in sidebar
       Rows     int
       ExitCode *int   // nil = running; single source for finished-ness (no bool)
   }
   // Attach is call-and-return (runs on a tea.Cmd goroutine, waits on a future
   // the reader fulfills). Channel-0 request ⇒ reply carries the stream channel.
   type AttachGrant struct {
       Channel   uint32
       AutoInput bool // attach-reply confirmation of hello.features
   }
   func (s *Session) Attach(ctx context.Context, id string) (AttachGrant, error) // not implemented
   // Outgoing frames; all enqueue onto the single ordered writer queue.
   func (s *Session) SendData(ch uint32, b []byte)              // keys, paste, mouse
   func (s *Session) SendAutoInput(ch uint32, b []byte)         // WritePty effects, post-negotiation
   func (s *Session) SendResize(ch uint32, cols, rows int)      // bloom rect → RESIZE
   func (s *Session) SendFocus(ch uint32, gained bool)
   func (s *Session) Close()                                    // idempotent: CLOSE streams, then socket
   func (s *Session) Feed() <-chan tea.Msg                      // session→UI message feed
   func Pump(s *Session, p *tea.Program)                        // goroutine: Feed → p.Send
 ```
 Domain events (events.go) — the only event vocabulary the UI ever sees.
 ```go
   // One struct, one enum: hc's twelve notification types collapse into six
   // domain changes at the boundary. status/turn/title/labels/size ⇒ Meta
   // (Info carries the merged record; single source of truth is s.infoCache).
   type Change uint8
   const (
       Created Change = iota // new terminal
       Lost                  // core lost track of the program
       Restarted             // program restarted ⇒ terminal content is stale
       Finished              // exited; ExitCode set in Info
       Removed               // gone from the list
       Meta                  // title/status/labels/size/turn refresh
   )
   type TerminalChanged struct {
       ID    string
       Change Change
       Info  TerminalInfo // current full record after applying this event
   }
   type Resynced struct {
       Seq       uint64
       Terminals []TerminalInfo // fresh snapshot after `missed`
   }
   type StreamData   struct { ID string; Data []byte }
   type StreamClosed struct { ID string; Reason CloseReason }
   type SessionDead  struct { Err error } // socket died / core process restarted
   type CloseReason interface{ isCloseReason() } // sealed
   type Exited  struct{ Code int }               // {"reason":"exited","exit_code":N}
   type Detached struct{}                        // we sent CLOSE, or core closed politely
   type StreamError struct{ Msg string }         // any other CLOSE reason
 ```
 The seq guard lives producer-side (reader goroutine): drop any notification whose seq ≤ applied seq; on missed, adopt the snapshot's seq and emit Resynced. The UI consumes an already-ordered,
 already-deduped feed and never reasons about seq.
 Pane side (pane.go / lifecycle.go).
 ```go
   // PaneSet: membership + focus + routing. Lives on the tea goroutine like Pane.
   type PaneSet struct { /* panes map[string]*Pane, order []string, focused string */ }
   func NewPaneSet(snap Snapshot) *PaneSet                            // not implemented
   func (ps *PaneSet) Open(id string) *Pane                           // get-or-create, no RPC
   func (ps *PaneSet) Focus(id string)                                // ⇒ FOCUS frames via SyncGeometry
   func (ps *PaneSet) Focused() *Pane
   func (ps *PaneSet) Attached() *Pane                                // M1: the one attached pane
   func (ps *PaneSet) All() []TerminalInfo                            // sidebar, stable order
   func (ps *PaneSet) Apply(m tea.Msg, s *Session) tea.Cmd            // not implemented
   // Apply routes TerminalChanged/Resynced/StreamData/StreamClosed/Attach*
   // to the right Pane, runs the machine, pumps emulator bytes, and returns
   // a Cmd for async effects (attach/detach RPCs).
 ```
 The state machine (lifecycle.go) — no booleans, a total function.
 ```go
   type State uint8
   const (
       Idle State = iota  // known, not attached
       Attaching          // RPC in flight / first paint pending
       Attached           // streaming
       Exited             // CLOSE{reason:exited}; ExitCode recorded in Info
       LostIdle           // Lost while not attached
       LostLive           // Lost while attached ⇒ detach happened, want reattach
       Gone               // finished or removed; terminal record inert
   )
   type Effect uint8
   const (
       EffAttach Effect = iota // issue Attach RPC (via returned Cmd)
       EffDetach               // CLOSE the stream channel
       EffReset                // discard Terminal, fresh NewTerminal (restart semantics)
   )
   // advance is total and pure: every (state, change) pair has exactly one row.
   // Unknown/no-op rows return the state unchanged with no effects.
   // not implemented — the table it must encode:
   //
   //   Idle:        Created→Idle·Meta   Restarted→Idle      Lost→LostIdle
   //                Finished→Gone       Removed→Gone        attach()→Attaching[EffAttach]
   //   Attaching:   grant→Attached      StreamClosed{Exited}→Exited
   //                StreamClosed{other}→Idle                Lost→LostIdle[EffDetach]
   //                Restarted→Attaching[EffReset,EffAttach] Finished/Removed→Gone[EffDetach]
   //   Attached:    data→Attached(pump) StreamClosed{Exited}→Exited
   //                StreamClosed{other}→Idle                Lost→LostLive[EffDetach]
   //                Restarted→Attaching[EffReset,EffAttach] Finished/Removed→Gone[EffDetach]
   //   Exited:      attach()→Attaching[EffAttach]           Removed→Gone  Restarted→Idle
   //   LostIdle:    Restarted→Idle       Finished/Removed→Gone
   //   LostLive:    Restarted→Attaching[EffReset,EffAttach] Finished/Removed→Gone
   //   Gone:        Created→Idle[EffReset]                  (fresh incarnation)
   func advance(st State, ch Change) (State, []Effect) // not implemented
 ```
 Membership transitions the machine cannot express — snapshot reconciliation — is one PaneSet method: absent id ⇒ Removed transition, new id ⇒ Created, present ⇒ keep. wantAttach is encoded as the
 LostLive state, not a flag: "user wanted this attached" is machine state, so restart-recovery falls out of the table instead of a bool that must survive transitions.
 The one boundary type (screen.go) — the only thing emulator state leaks through.
 ```go
   // Screen is the terminal's visible surface, produced on the tea goroutine
   // under exclusive terminal access immediately after RenderState.Update,
   // and immutable afterwards. UI code reads it freely; nothing writes it.
   // This is the ONLY type that crosses from emulator state to UI.
   type Screen struct {
       Cols, Rows int
       Cells      []Cell // Rows×Cols, row-major, exactly Cols*Rows len
       Cursor     Cursor
       Generation uint64 // pane-local monotonic; nodes compare to skip redraw
   }
   type Cell struct {
       Rune  rune
       Style CellStyle
   }
   type CellStyle struct {
       FG, BG uint32   // RGB; palette resolution happens at build time
       Attrs  uint8    // bold|italic|underline|reverse|strikethrough bits
   }
   type Cursor struct{ X, Y int; Visible bool }
   // buildScreen renders the visible surface into a Screen value.
   // Caller holds exclusive terminal access (libghostty doc.go contract).
   // Implementation: RenderState cell reads if exposed; otherwise Formatter
   // styled output parsed once into cells. Either way the boundary type is
   // identical, so ui/ never learns which.
   func buildScreen(rs *ghostty.RenderState, gen uint64) Screen // not implemented
 ```
 Per-frame consumption. The bloom pane node consumes exactly *Screen (plus its rect, from bloom's Frame):
 ```go
   // ui/panenode.go — implements bloom's cell-level draw interface
   // (docs index: canvas.Surface / Drawable; exact name to verify — flagged below).
   type PaneNode struct {
       pane     *loom.Pane
       lastGen  uint64
       fallback []string // styled lines, rebuilt only when Generation changes
   }
   // not implemented; pseudocode:
   //   func (n *PaneNode) Draw(dst *bloom.Surface, r bloom.Rect) {
   //       s := n.pane.Screen()
   //       if s == nil { drawPlaceholder(dst, r); return }
   //       if n.lastGen != s.Generation { n.lastGen = s.Generation; rebuildFallback() }
   //       for y := 0; y < min(s.Rows, r.H); y++ {
   //           for x := 0; x < min(s.Cols, r.W); x++ {
   //               dst.SetCell(r.X+x, r.Y+y, s.Cells[y*s.Cols+x])  // cell blit
   //           }}}
 ```
 Cost shape for a pane that changes every frame: production (Formatter/cell read + cell build over ~80×24) runs once per terminal delta on the owning goroutine — microseconds; per-frame UI cost is an
 O(visible cells) blit with no string parsing, the exact "cheaper path than styled strings per frame" bloom's docs call for. When the pane is idle, Generation is unchanged and the frame is the ~0.02
 ms unchanged-frame case. The styled fallback exists only if bloom's cell path disappoints; it is rebuilt per change, not per frame, so it degrades to Viewport-of-strings, never to per-frame parsing.
 ### Flow, end to end
 Bytes in: core stream channel → reader goroutine decodes DATA frame → StreamData{ID, bytes} on Feed → p.Send → tea mailbox → PaneSet.Apply → Pane.pump:
 ```go
   // runs on the tea goroutine only — this is the handle-ownership contract
   func (p *Pane) pump(data []byte, s *Session) {           // not implemented
       p.term.VTWrite(data)                                  // 1. emulator eats bytes
       if p.holdDepth > 0 { return }                         // RenderHold: defer repaint
       p.render.Update(p.term)                               // 2. exclusive access, then reads safe
       p.keyEnc.SyncFrom(p.term); p.mouseEnc.SetFromTerminal(p.term) // 3. input modes track state
       p.screen = buildScreen(p.render, p.nextGen())         // 4. boundary value swap
       s.SendCredit(p.channel, len(data))                    // 5. credit after consume
   }
 ```
 Credit-after-consume means the tea mailbox depth is the only buffer between wire and emulator; if tea ever fell behind, uncredited bytes accumulate and hc's >4 MiB fresh-repaint rule is the natural
 recovery. Control events are never blocked by a stalled pane (hc guarantee).
 Keys out: tea.KeyPressMsg → ForwardKey → Pane.EncodeKey (KeyEncoder, already synced with kitty/legacy mode from step 3) → SendData(ch, bytes) → ordered writer queue → DATA frame. Encode happens on
 the tea goroutine inside Update — owner-safe, and enqueue order equals keystroke order, so the wire never reorders typed characters.
 Emulator replies: libghostty WritePty effect fires in pump (owner goroutine) → if grant.AutoInput (confirmed at attach, negotiated at hello) → SendAutoInput(ch, bytes); otherwise count-and-drop.
 Because the core strips queries it already answered, our emulator only emits replies for queries that actually reached it — no double-answer suppression logic needed.
 Mouse: tea.MouseMsg → cell coords via last Frame rect → Pane.EncodeMouse(msg, x, y) (MouseEncoder; SGR/UTF-8/X10 chosen by tracking mode synced in step 3; tracking off ⇒ encode nothing) → DATA.
 Bytes to DATA because mouse is user input; AUTOMATIC_INPUT is strictly terminal-generated traffic.
 Resize: bloom records geometry every frame → SyncGeometry on FrameMsg:

 ```go
   // not implemented; the last-active-viewer rule:
   //   rect := frame.RectOf(node); cols, rows := inner rect in cells
   //   if pane focused and (cols,rows) != pane.sentSize:
   //       pane.sentSize = cols,rows; pane.termResize(cols,rows)  // local, immediate
   //       sess.SendResize(ch, cols, rows)
   //   on focus gained: SendFocus(ch, true) first, then the RESIZE above
   //     (active viewer imposes its rect — hc: last active viewer sets the size)
   //   on focus lost: SendFocus(ch, false); never resize an unfocused pane
 ```

 sentSize per pane makes RESIZE idempotent (send only on delta; a drag frame storm coalesces to one frame per actual change, at most once per display frame). Local Terminal size is set in the same
 breath as the outgoing RESIZE so buildScreen produces the right-shaped Screen on the next delta without waiting for the round-trip.

 Resync: missed → reader adopts snapshot seq, emits Resynced → PaneSet.Apply reconciles membership (diff against panes; absent+attached ⇒ Lost transition, which detaches), refreshes Info cache. Later
 stale events were already dropped at the reader. Core process death kills the socket ⇒ SessionDead ⇒ M1 usage exits with the error; reconnect is post-spike.

 ### Load-bearing decisions

 1. The tea goroutine owns every libghostty handle. Reader and writer goroutines own only the socket; handoff to handles is the tea mailbox. This satisfies "handles are single-goroutine" and
    "RenderState.Update under exclusive access, reads safe after" by construction — View reads p.screen (an immutable value produced earlier in the same goroutine's message loop), so there are no
    locks anywhere on the emulator path (per separate-before-serializing-shared-state: three actors, one owner each, merge at the read boundary).
 2. Request/response is call-and-return; events are a feed. Hello/Subscribe/List/Attach are synchronous futures resolved by the reader; everything after subscribe flows through Feed in order. Callers
    read like RPC; streams read like streams (per boundary-discipline).
 3. The boundary is a value, not a view. Screen is built once per terminal delta, immutable, swapped by assignment. The UI can never reach a Terminal, RenderState, or encoder; interface depth is
    high: behind PaneSet.Apply + *Screen hide the demux, credit accounting, seq guard, state machine, formatter, and encoder sync.
 4. hcwire is the only wire-typed package, internal, stdlib-only, and its types never appear in any signature that ui/ or cmd/ can call. Frame codec and JSON-RPC envelope are ~150 lines;
    params/results are built inline at root call sites so wire shapes cannot accrete in the domain.
 5. Status hygiene by construction: the only source of TerminalInfo.Status is hc's status event (OSC 7501 relayed). The pane ignores libghostty's ProgramStatus/TitleChanged effects in M1 (hc is the
    single source; no screen scraping, no dual-source divergence).

 ### Invariants in types, validation at boundaries, non-goals

 - Encoded in types: lifecycle (7-state enum + total advance, no sync booleans); "finished" (ExitCode *int, no Exited bool beside an int); sealed CloseReason; sealed-by-enum Change; Screen.Cells
   length pinned to Rows*Cols by construction in buildScreen.
 - Validation lives at boundaries only: frame codec enforces 16 MiB cap and kind validity; JSON-RPC decode errors become SessionDead/StreamError; everything inside trusts the types (per
   validate-at-boundaries).
 - Deliberately not doing in M1: scrollback viewing (RenderState screen only), zoom/popups/tabs, text selection, drag-to-resize borders, reconnect after core restart, libghostty Snapshot persistence
   across pane reset, SGR-pixel mouse, win32-input-mode (CSI 9001) key encoding — all logged-if-seen, none blocking.
 - Interface depth judgment: the public surface is ~20 functions and 15 types for a full multiplexer loop. The complexity hidden (frame codec, credit, seq, attach negotiation, emulator ownership,
   mode sync) is roughly 10× the surface; the complexity exposed (bloom node + 6 message kinds) is what a TUI author actually thinks in.

 Test seams (one obvious check per spike unit)

 ┌──────────────┬───────────────────────────────────────────────────────────────────────────────────────────────────────────────────┬─────────────────────────────────────────────────────────────────┐
 │ Unit         │ Check                                                                                                             │ Seam
 ├──────────────┼───────────────────────────────────────────────────────────────────────────────────────────────────────────────────┼─────────────────────────────────────────────────────────────────┤
 │ U1 bindings  │ screen_test.go: table of canned ANSI byte strings → VTWrite → buildScreen → assert Cells/Cursor (plain text and   │ buildScreen(rs, gen) as the only function under test; Terminal
 │ hello        │ styled cases). Pure, no I/O.                                                                                      │ built in-test per bindings' own tests.
 ├──────────────┼───────────────────────────────────────────────────────────────────────────────────────────────────────────────────┼─────────────────────────────────────────────────────────────────┤
 │ U2 hc        │ (a) hcwire roundtrip: encode frames → NextFrame → deep-equal, incl. 16 MiB rejection. (b) lifecycle table test:   │ DialConn(net.Conn) for (a)/(b); real socket path from LOOM_SOCK
 │ session      │ every (State, Change) row of advance. (c) env-gated LOOM_TEST_HC=1: dial real socket, Subscribe, assert the       │ only for (c).
 │              │ libbuild record parses into TerminalInfo.                                                                         │
 ├──────────────┼───────────────────────────────────────────────────────────────────────────────────────────────────────────────────┼─────────────────────────────────────────────────────────────────┤
 │ U3 attach    │ (a) unit: canned DATA/CLOSE frame sequence through DialConn peer → assert Pane.Screen() matches expected text     │ Same DialConn seam + fake-core helper (testcore); real hc for
 │              │ after first paint + deltas, credit frames observed. (b) env-gated: attach a program with known output, screen     │ (b).
 │              │ text equality.                                                                                                    │
 ├──────────────┼───────────────────────────────────────────────────────────────────────────────────────────────────────────────────┼─────────────────────────────────────────────────────────────────┤
 │ U4 input     │ Loopback: fake core echoes; inject tea.KeyPressMsgs via ForwardKey; assert captured DATA bytes equal KeyEncoder   │ DialConn + testcore capture side; ForwardKey/ForwardMouse as
 │              │ expectations and Screen shows the typed line. Env-gated real shell: echo marker visible in Screen.                │ the single entry points under test.
 ├──────────────┼───────────────────────────────────────────────────────────────────────────────────────────────────────────────────┼─────────────────────────────────────────────────────────────────┤
 │ U5 bloom     │ (a) golden: PaneNode.Draw from a fixed Screen into a small canvas → golden cells. (b) env-gated real surface:     │ The pane node consumes only *Screen + rect, so (a) needs no
 │ frame        │ loom running vim in an hc terminal; assert RESIZE frames on layout change and detach/reattach preserves the       │ bubbletea program at all; (b) is the documented real-surface
 │              │ screen; PLAN's visual gate.                                                                                       │ check.
 └──────────────┴───────────────────────────────────────────────────────────────────────────────────────────────────────────────────┴─────────────────────────────────────────────────────────────────┘

 DialConn is the whole mock story: one constructor taking net.Conn. Everything downstream (framed handshake, demux, credit, machine) runs real code in every test.

 Synthesis decision

 Not filled in by this runner — this is a Phase B candidate. The arena synthesis step records here which candidate became the base and what was grafted.

 Tradeoffs accepted

 - We accept unbounded tea-mailbox depth in exchange for zero backpressure machinery. Credit-after-consume means a pane that outruns the UI loop grows the mailbox until hc's fresh-repaint kicks in;
   we bet 8 ms frame budgets swallow realistic output. If U6 numbers disagree, the fix is local (reader-side per-channel pause), not structural.
 - We accept a tiny mutex around RPC futures in exchange for call-and-return attach. Alternative was routing replies through the feed; that pushes correlation into the state machine, which is exactly
   the complexity the boundary exists to hide.
 - We accept Cell/CellStyle as loom-owned types in exchange for a bloom-free root package. If bloom's cell path exposes a compatible cell type we alias and delete ours (deletion bias).
 - We accept the root package importing bubbletea v2 for Key/Mouse message types in exchange for not building a parallel key vocabulary and a conversion layer. The root stays free of bloom and of any
   UI rendering concept.
 - We accept one-shot FACTS with an inline payload (no domain type) in exchange for not modeling display facts before reading hc schema's exact shape — flagged, not forgotten.
 - We honor RenderHold with a depth counter and a deferred repaint in exchange for tear-free vim redraws; cost is two effect hooks and one int.

 Alternatives considered

 - Per-pane goroutine owning its Terminal, publishing pre-rendered styled strings to the UI. Lost: ownership of N handles across N goroutines makes every handle invariant a cross-goroutine claim;
   RenderState reads then race with Updates unless we snapshot per frame anyway — the same cost as building Screen, plus locks, plus goroutine lifecycle for panes that come and go. Exposes
   concurrency to callers; hides nothing.
 - Emulator-free UI: hand hc stream bytes straight to a styled-string Viewport per frame (bloom parses ANSI). Lost: violates the one-type boundary (raw stream bytes ARE wire-adjacent state leaking to
   UI), pays re-parse per frame per bloom's own warning, and leaves no seam for U1/U3 assertions (nothing owns parsed terminal state to assert against).
 - Session as a bubbletea tea.Model wrapper that callers embed (loom ships the whole app, callers only supply config). Lost: hides bloom from callers today and blocks every future layout need behind
   loom's API surface; interface depth collapses into a framework lock-in. The thin-shell model in cmd/loom keeps loom a library.

 Open questions and risks

 - Bloom cell path: does canvas.Surface/Drawable exist with per-cell Set semantics as assumed? If the page read changes the story, PaneNode falls back to cached styled lines (already in the sketch);
   what we'd want from bloom is a word on Set cost for mostly-changed surfaces.
 - FrameMsg shape and node identity: assumed bloom.FrameMsg{Frame} with rect lookup by node. Verify against bloom docs before U5; SyncGeometry is the only consumer, so drift is contained.
 - Encoder sync method names: KeyEncoder kitty-flag sync from terminal state and the Terminal resize setter are assumed names (SyncFrom, termResize); the bindings' doc.go is the arbiter. Signature
   drift touches only pane.go.
 - Does one framed-mode channel-0 DATA payload carry exactly one JSON-RPC message? Assumed yes (plain mode is one-per-line). If the core batches, hcwire absorbs it.
 - CSI 9001 win32-input-mode in first paint: if a program flips it and libghostty's KeyEncoder lacks the mode, keys degrade. Is there a core-side query loom should send to disable it, or does the
   core translate?
 - Terminal initial size at attach: assumed core pty starts 80×24; loom RESIZEs immediately after grant, so transient wrong-size paints are self-healing — confirm hc honors viewer RESIZE before first
   full paint.
 - Lost semantics: grounding doesn't say whether the stream survives a lost event; the machine detaches conservatively. If hc keeps streams alive across lost, LostLive → restart-recovery needs a
   cheaper row.

 Next implementation step

 Write internal/hcwire frame codec + the DialConn/testcore loopback, then U2's table and roundtrip tests — the seam every other unit hangs off exists before any UI code.
