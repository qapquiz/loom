 loom M1 — candidate C
 One candidate design package from Phase B arena. Intended path: design/candidate-c.md.
 Problem
 loom is a local terminal client for herdr-core (hc): hc owns persistent terminals, loom renders one of them as a pane in a bloom-driven Bubble Tea program. The shape is non-obvious because three
 foreign contracts meet at a single pane:
 - hc is push-shaped and framed: a Unix socket speaking JSON-RPC lines until hello{framed:true}, then length-prefixed frames on 32-bit channels; per-stream credit; a 15-second heartbeat; an events
   stream whose subscribers can fall behind and must resynchronize from missed + snapshot.
 - libghostty is single-goroutine per handle: Terminal, encoders, Formatter live on one goroutine; RenderState.Update runs under exclusive access and only reads taken after it returns are safe, until
   the next Update.
 - bloom rebuilds the node tree every frame and re-parses styled strings every frame, so live pane content must not cross to the UI as styled strings.
 Phase A constraints this design honors: hc wire types (frames, JSON-RPC) never cross into the UI layer; binding handles stay single-goroutine; agent status comes only from OSC 7501 (the
 ProgramStatus effect and hc status events), never screen scraping; the spike is one terminal, one pane, biased to deletion.
 Usage (caller's view)
 The caller imports loom/hc (session client), loom/pane (emulator owner), and loom (the Bubble Tea app). The usage is the spec; everything below is derived from these three call sites.
 Call site 1 — the spike binary. Connect, list, auto-attach the first terminal, render, forward keys, react to resize:
 ```go
   // cmd/loom/main.go
   package main
   import (
      tea "charm.land/bubbletea/v2"
      "loom"
      "loom/hc"
   )
   func main() {
      sess, err := hc.Dial(hc.DefaultSocket(), hc.Features{AutomaticInput: true})
      if err != nil {
         log.Fatal(err) // no core running; hc run --bg starts one
      }
      defer sess.Close()
      app := loom.NewApp(sess) // a tea.Model: sidebar + one pane, pumps wired in Init
      if _, err := tea.NewProgram(app, tea.WithMouseAllMotion()).Run(); err != nil {
         log.Fatal(err)
      }
   }
 ```
 That is the whole binary. NewApp subscribes to sess.Events() itself; on the first snapshot it constructs a pane.Pane for the first listed terminal and calls sess.Attach(id, pane). Key, mouse, and
 resize handling are inside the model.
 Call site 2 — a custom bloom tree. The pane node and room are reusable for callers building their own layout (this is how the spike's zoom/popup future stays open without being built):
 ```go
   // embed one pane in a hand-rolled tree; app exposes the domain for composition
   root := bloom.Column(
      bloom.Fixed(24, ui.Sidebar(app.Room())),   // room.Terminal values, plain domain
      bloom.Fill(loom.PaneNode(app.Pane())),     // consumes *pane.Screen per frame
   )
 ```
 Call site 3 — headless session drive (the test seam). No UI at all; proves hc.Stream/hc.StreamOut suffice end to end:
 ```go
   // pane/stream_test.go
   sess, _ := hc.Dial(sock, hc.Features{AutomaticInput: true})
   rec := newRecorder()                       // implements hc.Stream
   sess.Attach(id, rec)                       // async; attach result arrives on rec
   rec.WaitConnected()                        // recorder got (out hc.StreamOut, autoInput bool)
   rec.out.Send([]byte("echo loom\r"))        // keys as DATA
   line := rec.WaitScreenLine("loom")         // screen text via RenderState/Formatter
 ```
 Everything the caller can do is on those three sites: hc.Dial, sess.Events, sess.Attach, loom.NewApp, loom.PaneNode, pane.Pane (implementing hc.Stream), and hc.StreamOut. No wire types appear
 anywhere.
 Shape
 ### Goroutine model (the load-bearing decision)
 Two owners, no locks:
 - Session goroutine (one per hc.Session): owns the socket, frame codec, RPC correlation, credit accounting, and — through the stream callbacks — every binding handle of every attached pane
   (Terminal.VTWrite, RenderState.Update, encoders, Formatter). All pane mutation happens here. It publishes immutable *pane.Screen snapshots via atomic pointer store.
 - UI goroutine (Bubble Tea): owns the room (the list/watch domain model) and the bloom tree. It never touches a binding handle. It reads pane.Screen() (atomic load), reads room directly (it is the
   only writer, in Update), and routes input by sending pane.Op values into the pane's op channel, which the session goroutine drains.
 Session→UI is one channel (Events()) plus pane wake-ups; UI→session is enqueue-only methods. Nothing is both written from two goroutines, so the bindings' concurrency contract is satisfied by
 construction, not discipline.
 ### Package hc — session client
 Parse-at-the-boundary: frames, JSON-RPC, seq, credit, and negotiation live here, unexported.
 ```go
   package hc
   type TerminalID string
   type Features struct{ AutomaticInput bool } // hello.features; granted set is what the core confirms
   type TerminalInfo struct { // what the sidebar and badges need; nothing more
      ID     TerminalID
      Title  string
      Status string // OSC 7501 status, verbatim from hc; never scraped
      Cols   uint16
      Rows   uint16
      Labels map[string]string
   }
   type CloseReason struct {
      Reason   string // "exited", "detached", "attach_failed", ...
      ExitCode int
   }
   type EventKind uint8
   const (
      EventSnapshot EventKind = iota // wholesale replacement, carries All + Seq
      EventCreated
      EventLost
      EventRestarted
      EventFinished // Info.Exit != nil
      EventRemoved
      EventStatus // field update
      EventTitle  // field update
      EventSize   // field update — carries the size another viewer set
      EventLabels // field update
      EventKeep   // heartbeat, no-op for the room
   )
   type Event struct {
      Kind EventKind
      Seq  uint64
      ID   TerminalID
      Info TerminalInfo // always the full current record; snapshot carries All []TerminalInfo instead
      Exit *CloseReason // set on EventFinished
   }
   type Session struct{ /* socket, goroutine, pending RPCs, credit map — unexported */ }
   func Dial(socket string, f Features) (*Session, error) // hello → framed upgrade; not implemented
   func (s *Session) Events() <-chan Event                // snapshot first, then ordered, seq-filtered events
   func (s *Session) Attach(id TerminalID, in Stream)     // enqueue; result arrives via in.Connected/in.Closed
   func (s *Session) Close() error                        // not implemented
   type Stream interface { // implemented by pane.Pane (and test recorders); called on session goroutine
      Connected(out StreamOut, autoInput bool) // attach confirmed; autoInput is the negotiated truth
      Delta(p []byte)                          // first paint, then program output: feed VTWrite
      Closed(r CloseReason)                    // detach, exit, or refusal — the only error path
   }
   type StreamOut interface { // write half of the stream; held by the pane
      Send(p []byte)              // DATA: keys and mouse bytes
      AutoReply(p []byte)         // AUTOMATIC_INPUT; silently drops unless negotiated AND attach-confirmed
      Resize(cols, rows uint16)   // RESIZE frame
      Focus(gained bool)          // FOCUS frame
   }
 ```
 Internal rules this package enforces alone: seq monotonicity (drop Seq <= lastSeq; on missed, emit one wholesale EventSnapshot and continue); CREDIT sent after each Delta callback returns (the pane
 processes synchronously, so credit flows without pane cooperation); AUTOMATIC_INPUT gated on the double negotiation (hello feature granted ∧ attach confirmation) — the pane never needs to know;
 channel 0 never queues behind a stream delivery.
 ### Package room — list/watch domain (pure; imports only hc)
 The pane lifecycle state machine over hc's event vocabulary. States, not flags:
 ```go
   package room
   type State uint8
   const (
      Missing State = iota
      Listed            // live and attachable; plain/agent alike
      Restarting        // lost: program gone, record kept, restart pending
      Exited            // finished: exit code known, record may vanish soon
   )
   type Terminal struct {
      ID    hc.TerminalID
      State State
      Info  hc.TerminalInfo
   }
   type Room struct {
      order []hc.TerminalID            // snapshot order; created appends; removed deletes
      byID  map[hc.TerminalID]*Terminal
   }
   func (r *Room) Apply(ev hc.Event) (changed bool) // the one entry point; not implemented
 ```
 Apply is a single switch — the only place the event vocabulary maps to transitions: Snapshot rebuilds wholesale; Created Missing→Listed; Lost Listed→Restarting; Restarted →Listed; Finished →Exited;
 Removed →Missing; Status/Title/Size/Labels overwrite Info fields only; Keep is a no-op. Illegal transitions (Lost for an unknown ID) are logged no-ops. There is no attached bool, no restarting bool:
 the sidebar renders State, and nothing anywhere else in loom keeps a parallel opinion about lifecycle.
 Two machines, one per source, never cross-checked: the room reflects the events stream (used by the sidebar), the pane reflects its stream channel (used by the pane view). They can transiently
 disagree (e.g. finished event vs CLOSE frame) by design; each consumer renders its own source, and no code reconciles them, so no boolean can desync.
 ### Package pane — emulator owner (the boundary)
 ```go
   package pane
   // Screen is the ONE type that crosses from emulator state to the UI.
   // Published immutable; the UI may hold and read it on any goroutine, forever.
   type Screen struct {
      Cols, Rows int
      Cells      []Cell  // row-major, len == Cols*Rows; pre-parsed, blit-ready
      Cursor     Cursor
      Title      string  // last TitleChanged effect
      Status     string  // last ProgramStatus effect (OSC 7501) — never screen-scraped
      MouseOn    bool    // program has mouse tracking enabled (bloom MouseMode / selection yield)
   }
   type Cell struct {
      R rune
      S Style // packed fg/bg/attrs, mirrored to the bloom cell layout at draw
   }
   type Cursor struct{ Col, Row int; Visible bool }
   type Style struct{ FG, BG uint32; Attr uint16 }
   type OpKind uint8
   const (OpKey OpKind = iota; OpMouse; OpResize; OpActive)
   type Op struct { // tagged struct, not an interface: one type, four intents
      Kind   OpKind
      Key    ghostty.KeyInput   // binding input values cross the channel; data, not handles
      Mouse  ghostty.MouseInput // cell-local coords: loom maps surface→cell via bloom Frame
      Cols   uint16             // OpResize / OpActive reassert
      Rows   uint16
      Active bool               // OpActive: loom became the active viewer
   }
   type Pane struct {
      /* id, op chan, wake func, binding handles, out hc.StreamOut, want/sent size, active — unexported */
   }
   func New(id hc.TerminalID, wake func()) *Pane // wake called after each publish; UI pumps it into tea.Msgs
   // hc.Stream — all three run on the session goroutine
   func (p *Pane) Connected(out hc.StreamOut, autoInput bool) // not implemented
   func (p *Pane) Delta(b []byte)                             // VTWrite → effects → RenderState.Update → publish. not implemented
   func (p *Pane) Closed(r hc.CloseReason)                    // publish final Screen with State=closed. not implemented
   // UI side
   func (p *Pane) Enqueue(op Op)      // non-blocking send; the only method the UI may call to act
   func (p *Pane) Screen() *Screen    // atomic load
   // session-goroutine internals
   func (p *Pane) apply(op Op)               // pseudocode: switch op.Kind { key/mouse → encode → out.Send; resize → reconcile; active → focus }
   func (p *Pane) publish()                  // pseudocode: Update RenderState; SetFromTerminal on both encoders; read cells; Store(&Screen{...}); wake()
   func (p *Pane) reconcile(cols, rows uint16, force bool) // pseudocode: if force || sent != want { resize local Terminal; out.Resize; sent = want }
 ```
 Why the UI stays cheap. The node consumes exactly *pane.Screen per frame and blits Cells into the cell canvas — O(cells) copy of pre-parsed data, no styled-string construction, no bloom-side
 re-parse of what changed. This is the cheap path bloom's docs point to. The per-frame cost claim is measured in U6 against the bindings' render-state benchmarks. Fallback (flagged assumption): if
 bloom's cell draw path (canvas.Surface/Drawable) doesn't admit direct blit, the node falls back to per-line styled strings into a Viewport — documented by bloom as the expensive path, kept only as
 plan B, and the Screen type does not change either way, which is why the boundary is the Screen and not a rendering strategy.
 ### Package loom — the Bubble Tea app
 ```go
   package loom
   type App struct{ /* sess, room, pane, wake chan, pump handles, focused — unexported */ }
   func NewApp(sess *hc.Session) *App                       // not implemented
   func (a *App) Init() tea.Cmd                             // batch: pump(sess.Events()), pumpWake(). not implemented
   func (a *App) Update(tea.Msg) (tea.Model, tea.Cmd)       // not implemented
   func (a *App) View() bloom.Node                          // sidebar (room) + PaneNode; records frame rects
   func (a *App) Room() *room.Room                          // UI-goroutine-owned; safe to read in custom Views
   func (a *App) Pane() *pane.Pane                          // nil until first attach
   func PaneNode(p *pane.Pane) bloom.Node                   // per-frame: blit p.Screen(); record rect; enqueue OpResize on drift
 ```
 Update routing, one switch per message source: hc.Event → room.Apply (and on first snapshot, construct the pane and sess.Attach); pane wake → nothing but a redraw; tea.KeyMsg → key adapter →
 pane.Enqueue(OpKey); tea.MouseMsg → map via recorded frame rect → OpMouse; tea.FocusMsg/BlurMsg (or bloom focus scope change) → OpActive; window size changes flow through bloom's layout → frame rect
 drift → OpResize.
 ### The four flows, traced
 - Attach: snapshot → App picks first Listed → pane.New → sess.Attach → session goroutine runs the RPC → core sends first paint as DATA → pane.Delta (VTWrite; effects fire: Title, Status, WritePty) →
   RenderState.Update → publish → wake → next frame renders. Connected(out, autoInput) sets the write half; negotiation truth lives in hc only.
 - Key: UI Update → OpKey into the op chan → session goroutine → KeyEncoder (win32-input-mode/kitty state comes from the terminal itself) → out.Send → core → program echo returns as DATA → Delta →
   publish → redraw. Round trip is the multiplexer's true path; nothing is echoed locally.
 - Mouse: tea.MouseMsg → bloom rect → cell coords → OpMouse → MouseEncoder.Encode (tracking modes synced via SetFromTerminal after each Update; when the program disabled mouse, the encoder emits
   nothing). Screen.MouseOn tells bloom's MouseMode/selection to stand down when the program owns the mouse.
 - Resize (the last-active-viewer rule): pane holds want (bloom rect) and sent (last RESIZE). OpResize sets want; reconcile sends only on drift — idempotent under repeat or crash. OpActive(true)
   reconciles with force: we may have been resized by another viewer while passive, and the last active viewer wins. While passive, an hc EventSize for our terminal is applied to the local terminal
   only (follow, don't fight); while active it is ignored — our sent is authoritative in flight. The rule is genuinely exercised in the spike: hc attach in another pane is a second viewer. Invariant,
   encoded structurally: local emulator size == last RESIZE sent — both mutations happen only inside reconcile, which is why size-report replies via AUTOMATIC_INPUT never lie.
 - Resync: normal events apply in channel order; hc drops Seq <= lastSeq; a missed becomes one EventSnapshot and the room rebuilds wholesale (state machines re-derived, streams untouched — the
   attached stream doesn't ride the events channel, so a resync cannot disturb an attached pane).
 ### Module map
 ```
   loom/
     go.mod                 module loom; deps: bloomfx.dev/bloom, charm.land/bubbletea/v2,
                            go.mitchellh.com/libghostty (pinned b13f277ddcb2), stdlib
     hc/
       hc.go                Session, Dial, Events, Attach, Close; goroutine + pumps
       stream.go            Stream, StreamOut, CloseReason; credit-after-Delta; auto-input gate
       event.go             Event, EventKind, TerminalInfo, TerminalID, Features; seq/missed handling
       wire.go              (unexported) frame codec, JSON-RPC, hello, negotiation — nothing here is exported
     room/
       room.go              Room, Terminal, State, Apply — the lifecycle machine
     pane/
       pane.go              Pane, Screen, Cell, Style, Cursor, Op; publish; Enqueue/Screen
       emulator.go          binding ownership: VTWrite, effects wiring, Update+publish, encoders resync
       input.go             apply(Op): encoders → out.Send; WritePty effect → out.AutoReply
       resize.go            reconcile + the last-active rule + EventSize follow
     ui/                    (package loom)
       app.go               App: Init/Update/View, pumps, first-snapshot attach, focus routing
       tree.go              PaneNode (blit Screen → cell canvas), frame-rect recording
       sidebar.go           room → bloom list; state + status badge from room.Info
       keymap.go            tea.Key → ghostty.KeyInput adapter (the one hairy table)
     cmd/loom/main.go       call site 1
 ```

 ### Invariants encoded in types; what is deliberately absent

 - Emulator→UI crossing: exactly one value type (pane.Screen); no hc frame, no JSON-RPC, no binding handle ever crosses it (boundary-discipline).
 - Lifecycle as room.State enum + single Apply switch; no booleans anywhere that mirror state.
 - Negotiation truth (autoInput) lives only in hc; StreamOut.AutoReply is a no-op until confirmed, so a pane cannot send frame kind 6 before negotiation, even if its code is wrong.
 - Single writer per state: session goroutine → emulator + Screen; UI goroutine → room + tree; atomic pointer is the only shared cell.
 - Absent on purpose, named so absence isn't mistaken for oversight: no zoom/tabs/palette types, no FACTS sender, no reconnect or dead-core detection, no scrollback browsing UI, no multi-pane tree
   (PaneNode is already per-pane; the tree grows around it in M2).

 Interface depth: hc hides connection lifecycle, framing, flow control, event ordering, and negotiation behind four methods and two small interfaces; loom hides pumping, routing, geometry mapping,
 and the bloom tree behind NewApp + PaneNode. Callers never coordinate steps to do one thing.

 Test seams

 ┌──────────────┬────────────────────────────────────────────────────────────────────────────────────────────────────┬────────────────────────────────────────────────────────────────────────────────┐
 │ Unit         │ Seam                                                                                               │ Obvious check
 ├──────────────┼────────────────────────────────────────────────────────────────────────────────────────────────────┼────────────────────────────────────────────────────────────────────────────────┤
 │ U1 bindings  │ none needed — direct against the pinned bindings                                                   │ pane package test: NewTerminal 80x24, VTWrite ANSI, Formatter plain text
 │ hello        │                                                                                                    │ equals expected (already green per PLAN)
 ├──────────────┼────────────────────────────────────────────────────────────────────────────────────────────────────┼────────────────────────────────────────────────────────────────────────────────┤
 │ U2 session   │ live hc socket (observe the libbuild record, per PLAN) + a scripted fake core (net.Listen("unix")  │ room receives snapshot → records match hc ls; scripted events drive Apply
 │              │ in-package: speaks plain hello, flips to framed, serves events.subscribe with canned               │ through every transition
 │              │ snapshot/events)                                                                                   │
 ├──────────────┼────────────────────────────────────────────────────────────────────────────────────────────────────┼────────────────────────────────────────────────────────────────────────────────┤
 │ U3 attach    │ same fake core streaming a canned first paint + deltas; plus the live libbuild/hc run --bg bash    │ first paint + deltas → RenderState.Update → Formatter screen text contains the
 │              │ check                                                                                              │ known marker
 ├──────────────┼────────────────────────────────────────────────────────────────────────────────────────────────────┼────────────────────────────────────────────────────────────────────────────────┤
 │ U4 input     │ fake core (deterministic) + live shell: fake sends a DA query \x1b[c post-paint and asserts it     │ typed echo loom\r keys appear as echo output on screen; WRITE_PTY bytes
 │              │ received frame kind 6                                                                              │ observed leaving as AUTOMATIC_INPUT only after the attach confirmation was
 │              │                                                                                                    │ replayed
 ├──────────────┼────────────────────────────────────────────────────────────────────────────────────────────────────┼────────────────────────────────────────────────────────────────────────────────┤
 │ U5 bloom     │ real surface: the App in tea.NewProgram; fake core records RESIZE payloads; the human gate is the  │ resize the program → exactly one RESIZE with the new rect per drift
 │ frame        │ PLAN's vim capture                                                                                 │ (repeat-safe); detach/reattach keeps screen via canned repaint
 ├──────────────┼────────────────────────────────────────────────────────────────────────────────────────────────────┼────────────────────────────────────────────────────────────────────────────────┤
 │ missed       │ fake core sends missed + fresh snapshot out of band                                                │ room rebuilds wholesale; Seq continues above the new snapshot
 │ resync       │                                                                                                    │
 └──────────────┴────────────────────────────────────────────────────────────────────────────────────────────────────┴────────────────────────────────────────────────────────────────────────────────┘

 The fake core is ~150 lines in hc's test files (scripted, not a protocol reimplementation) — it is the seam that makes U2–U5 deterministic; the live-core checks keep the fake honest.

 Synthesis decision

 Candidate C of Phase B arena (this document is one candidate; the section is arena's to fill). If chosen as base, graft points I'd flag for the synthesizer: the Screen boundary and single-writer
 goroutine split are the load-bearing choices; sidebar composition and key-adapter detail are where other candidates likely differ most.

 Tradeoffs accepted

 - We accept an O(cells) snapshot copy + allocation per publish (≈2 KB at 80x24) in exchange for a lock-free UI that can hold a frame forever.
 - We accept a full round trip per keystroke through the core in exchange for zero local echo logic — the terminal is the single source of screen truth.
 - We accept every keystroke crossing one goroutine (session) in exchange for the bindings' single-goroutine contract holding by construction; channel-0 latency is tied to render cost and is measured
   in U6.
 - We accept two title/status sources (room events for the sidebar, Screen for the pane header) that can transiently disagree, in exchange for no reconciliation code — each consumer renders its own
   source.
 - We accept first-paint at default 80x24 before the first RESIZE lands (one frame of wrong size), because attach happens before the node has a rect.
 - We accept no dead-core detection in the spike (frozen UI if hc dies) — one tea.Tick later, not a session feature now.

 Alternatives considered

 - Hand the UI the RenderState itself. The UI formats/reads cells on its own goroutine. Lost: it either races the next Update (violates the bindings' read window) or forces a lock that serializes
   emulator writes with UI frames. The Screen publish is the same shape without the lock — per-actor state, merge at the read boundary.
 - Synchronous request/reply session API called from tea.Cmds. Attach/list/resize as blocking calls with a mutex. Lost: locks at every boundary, re-entry hazards with the event pump, and hc's real
   shape is push anyway; the enqueue+one-channel design needs no locking and no reply plumbing in the UI.
 - Styled strings into a bloom Viewport as the primary pane path. Lost on the documented per-frame re-parse cost for content that changes every frame — exactly our case; kept only as the flagged
   fallback behind an unchanged Screen type.
 - Generalize to N panes, tabs, zoom now. Lost for M1: the spike must prove one vertical slice against a real vim; PaneNode is already per-pane so the M2 tree grows without reworking the boundary.
   Generalizing now would blur the room/pane split the lifecycle machine depends on.

 Open questions and risks

 - Does bloom's cell-level path (canvas.Surface/Drawable) admit direct blit of a []Cell slice, and does the pinned page support it? Must be read in bloom's source before tree.go lands; the fallback
   path is designed but unmeasured.
 - Does the pinned RenderState expose per-cell reads, or only Formatter output? The Screen type survives either; the cheap-path claim needs the former.
 - Does Terminal expose a resize (or must we Snapshot→new Terminal→restore)? The design only needs a local size change inside reconcile; both forms fit.
 - Does the hc size event payload actually carry cols/rows (needed for passive-viewer follow), and does the first paint's win32-input-mode flag arrive as in-band CSI 9001 vs an attach-reply field?
   Both verified cheaply against the live core in U2/U3.
 - Key adapter fidelity: tea.KeyMsg gives downs only; kitty release events are out of spike scope. Enough for vim+arrows; revisit when kitty full-mode matters.
 - If the pane callback ever grows slow (big repaints), channel-0 RPCs queue behind it on one goroutine; U6 numbers decide whether the write half needs its own goroutine.

 Next implementation step

 Build the fake core and hc/wire.go against the U2 seam — Dial→hello→framed→snapshot parsed into hc.Event — because every other unit's seam stands on it.


 Report line (I could not execute it — no shell tool in this runner; orchestrator should run from t3w):

 ```
   hc send t3w "candidate-c done: single Screen snapshot crosses emulator→UI; session+pane on one goroutine, lock-free UI; room state machine seq-blind; resize owned by reconcile with last-active
 rule"
 ```
