 loom M1 — candidate B: one pump per pane, one type across the boundary

 Problem

 loom is a terminal multiplexer client for herdr-core (hc): hc owns persistent terminals on a Unix socket and streams them framed; loom renders one attached terminal's screen inside a bloom pane in a
 bubbletea program. The shape is non-obvious because three concurrency contracts intersect: (1) libghostty handles are single-goroutine, and RenderState.Update requires exclusive terminal access with
 reads safe only until the next Update; (2) bubbletea owns the UI goroutine and its Update/View run at up to 120 fps; (3) hc multiplexes JSON-RPC and per-terminal byte streams over one socket with
 credit-based flow control and a snapshot/missed resync rule. The design must also keep hc wire types (frames, JSON-RPC) out of the UI layer entirely, and take agent status only from OSC 7501 as
 relayed by hc's status events — never scraped from screens.

 Usage (caller's view)

 Quickstart (cmd/loom/main.go):

 ```go
   package main

   import (
      "charm.land/bubbletea/v2"
      "loom/internal/hc"
      "loom/internal/ui"
   )

   func main() {
      sess, err := hc.Connect(hc.DefaultSocket()) // plain hello → framed mode
      if err != nil {
         fmt.Fprintln(os.Stderr, "connect:", err)
         os.Exit(1)
      }
      defer sess.Close()

      ids, err := sess.List() // terminal.list → []hc.TerminalInfo
      if err != nil {
         fmt.Fprintln(os.Stderr, "list:", err)
         os.Exit(1)
      }
      target := pick(ids, *termFlag) // M1: -t flag, else first record

      pane, err := sess.Attach(target, hc.AttachOpts{
         Viewer: "loom",
         Cols:   120,
         Rows:   40,
      }) // attach RPC, pump starts, RESIZE+FOCUS+FACTS sent, size established
      if err != nil {
         fmt.Fprintln(os.Stderr, "attach:", err)
         os.Exit(1)
      }
      defer pane.Close() // CLOSE frame = detach

      events, err := sess.Subscribe() // events.subscribe → snapshot then notifications
      if err != nil {
         fmt.Fprintln(os.Stderr, "subscribe:", err)
         os.Exit(1)
      }

      m := ui.New(sess, pane, events)
      if _, err := tea.NewProgram(m, tea.WithAltScreen()).Run(); err != nil {
         fmt.Fprintln(os.Stderr, err)
         os.Exit(1)
      }
   }
 ```

 Call site 1 — the model routes every message through exactly one of three lanes (internal/ui/model.go):

 ```go
   func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
      switch msg := msg.(type) {
      case hc.Event: // one alphabet: hc notifications + loom-synthesized outcomes
         m.reg.Apply(msg)     // registry: seq watermark, snapshot/missed replace
         m.applyToPane(msg)   // pure reducer → attach/detach decisions
         return m, m.rearm()
      case hc.PanePainted: // zero-field signal; content rides m.pane.Frame()
         m.frame = m.pane.Frame()
         return m, m.rearm()
      case tea.KeyMsg:
         if in, ok := ui.ToPaneKey(msg); ok {
            m.pane.SendInput(in) // → KeyEncoder on pump goroutine → DATA
         }
         return m, nil
      case tea.MouseMsg:
         m.pane.SendInput(ui.ToPaneMouse(msg, m.paneRect)) // → MouseEncoder → DATA
         return m, nil
      case tea.WindowSizeMsg:
         m.size = ... // bloom relayouts; pane rect change → RESIZE via SyncResize in View
         return m, nil
      }
      return m, nil
   }
 ```

 Call site 2 — View builds the bloom tree and syncs geometry to the core (internal/ui/model.go):

 ```go
   func (m *Model) View() tea.View {
      tree := bloom.Row(
         bloom.Fixed(1, ui.StatusLine(m.reg.Status(m.pane.ID), m.reg.Title(m.pane.ID))),
         bloom.Fill(ui.PaneNode(m.pane, m.frame)), // consumes *hc.PaneFrame cells
      )
      v := bloom.Render(tree, m.size)
      ui.SyncResize(v.Frame, ui.PaneNodeID, m.pane) // rect changed → Pane.NeedSize → RESIZE
      return v
   }
 ```

 That is the whole public surface: Connect, List, Subscribe, Attach, Close on Session; SendInput, NeedSize, Frame, PaintChan, Close on Pane; Event, Registry, PaneFrame, KeyEvent/MouseEvent domain
 structs. The caller never sees a frame, a JSON-RPC message, or a ghostty handle.

 encoder input type is bindings'. If ui constructs bindings key structs and hands them via hc.Input, hc package and ui package both reference libghostty types — fine, both may depend on it. Simplest:
 ui maps tea.KeyMsg → ghostty input key directly, pump encodes. Decide: Input.Key holds the bindings key type (type alias in hc? just use the bindings type). But then hc.Input exposes bindings type
 to ui — ui imports bindings anyway. OK.

 Hmm, wait — is that right though? libghostty key encoding might need the key event the way ghostty models it (keybinds.Key from the bindings). tea.KeyMsg → that mapping is real work (shifted chars,
 ctrl codes, kitty). ui/input.go owns it. ✔

 pane:

 ```go
   // Pane is one attached terminal. All ghostty state is owned by the pump
   // goroutine; the UI touches only Frame() and the Send* methods.
   type Pane struct {
      ID    string
      ch    uint32
      send  chan Input        // UI → pump
      feed  chan []byte       // demux → pump (DATA payloads)
      frame atomic.Pointer[PaneFrame]
      paint chan struct{}     // 1-buffered: coalesced repaint signal
      pump  pumpState         // pump-goroutine-only fields below
      // pump-owned:
      term  *ghostty.Terminal
      state *ghostty.RenderState
      fmt   *ghostty.Formatter
      mouse ghostty.MouseEncoder
      auto  bool  // AUTOMATIC_INPUT confirmed by attach reply
      desync bool // dropped stream bytes; needs reattach
      lastSent image.Point
      pending  image.Point // size waiting to go out
   }

   func (p *Pane) Frame() *PaneFrame { return p.frame.Load() }

   func (p *Pane) SendInput(in Input) { select { case p.send <- in: default: /* drop stale mouse/size; keys: grow? M1 drop+note */ } }

   func (p *Pane) NeedSize(cols, rows int) { p.SendInput(Input{Kind: InputSize, Size: ...}) }

   func (p *Pane) PaintChan() <-chan struct{} // for waitPaint cmd

   func (p *Pane) Close() // CLOSE frame, cancel pump
 ```

 Dropping keys on full chan is bad (typed chars lost). send chan buffered 128; overflow only if pump wedged → desync territory anyway. Note.

 pump pseudocode:

 ```
   loop select:
    case b := <-p.feed:                       // program output
       if p.desync: term.Reset? ; p.desync=false   // fresh repaint heals
       term.VTWrite(b)                        // effects fire inline:
                                              //   WritePty → p.replies = append(...) (same goroutine)
       term.Update(p.state)                   // exclusive window ends
       p.publish()                            // build PaneFrame from state+formatter, atomic store
       conn.Send(CREDIT ch len(b))
       signal p.paint (non-blocking)
    case in := <-p.send:
       switch in.Kind:
       case Key:   b := term.KeyEncoder().Encode(in.Key, ...); DATA(b)
       case Mouse: p.mouse.SetFromTerminal(term); b := p.mouse.Encode(in.Mouse); DATA(b)
       case Size:  p.pending = in.Size   // coalesced below (50ms tick)
       case Focus: conn.Send(FOCUS ch 1/0)
    case <-p.sizeTick:                        // 50ms
       if p.pending != p.lastSent:
          conn.Send(RESIZE ch cols u16, rows u16)
          term.Resize(cols, rows)             // ASSUMPTION: verify at U3
          p.lastSent = p.pending
    case <-ctx.Done: return
 ```

 WritePty replies: fire during VTWrite; accumulate; after VTWrite returns, if auto: DATA? No — AUTOMATIC_INPUT frame kind 6 with those bytes. Send immediately after publish (ordering: replies belong
 to the query just parsed; ordering vs subsequent DATA irrelevant — different direction). ✔ If !auto: drop (old core ignores kind 6 — but we don't send without confirmation, so never).

 publish():

 ```
   // pump goroutine only. Runs after Update; reads are contract-safe here.
   cells := p.fmt... or state read → fill PaneFrame{
     Cols, Rows from term size,
     Cells: arena (alloc per publish in M1),
     Cursor from state,
     Title: from TitleChanged effect (also arrives via hc title event; frame title used for border)
   }
   p.frame.Store(f)
 ```

 Title: effect vs event — border title can come from hc title event (registry) — keep PaneFrame.Title out? "Exactly one type crosses" — keep title/status OUT of PaneFrame (they're registry fields
 from events, not emulator state). PaneFrame = pure screen: cells + cursor. Title for border from TerminalInfo. Cleaner separation, smaller type. ✔ Yes — PaneFrame is emulator screen state only.

 internal/hc/paneframe.go:

 ```go
   // PaneFrame is the only type that carries emulator state to the UI.
   // Published by the pump after each RenderState.Update; immutable to readers.
   type PaneFrame struct {
      Cols, Rows int
      Cells      []PaneCell // row-major, len == Cols*Rows
      CursorX, CursorY int
      Cursor     CursorStyle
   }

   type PaneCell struct {
      Rune  rune
      Fg    Color // RGB24 or Default
      Bg    Color
      Flags CellFlags // bold italic underline strikethrough reverse
      Wide  bool      // cell is the wide half of a double-width rune
   }

   type Color uint32 // 0x00RRGGBB; 0xFFFFFFFF = default
 ```

 internal/hc/reducer.go:

 ```go
   type Phase uint8

   const (
      PhaseKnown Phase = iota // record listed, not attached
      PhaseAttaching
      PhaseLive
      PhaseReattaching // lost: terminal record persists, channel closed
      PhaseExited      // program finished
      PhaseGone        // removed from the core
   )

   type Action uint8

   const (
      ActNone Action = iota
      ActAttach
      ActDetach          // CLOSE the channel, keep the record
      ActDetachAndAttach // lost→desync path: close, then attach again
   )

   // Event is the single vocabulary — hc notifications plus loom-synthesized
   // outcomes — so the reducer has exactly one input alphabet.
   type Event struct { ... }

   // Step is pure: phase in, event in → phase out, action for the owner.
   // The test seam for the lifecycle (table-driven).
   func Step(phase Phase, ev Event) (Phase, Action) { not implemented }
 ```

 Transition table (doc):

 ┌─────────────┬───────────────────────────────────────────────────────┬───────────────┬─────────────────────────────────────────────────────────────────┐
 │ phase       │ event                                                 │ next          │ action                                                          │
 ├─────────────┼───────────────────────────────────────────────────────┼───────────────┼─────────────────────────────────────────────────────────────────┤
 │ Known       │ created/keep/status/title/size/turn/labels/foreground │ Known         │ none                                                            │
 ├─────────────┼───────────────────────────────────────────────────────┼───────────────┼─────────────────────────────────────────────────────────────────┤
 │ Known       │ EvAttached                                            │ Live          │ none (pump already running)                                     │
 ├─────────────┼───────────────────────────────────────────────────────┼───────────────┼─────────────────────────────────────────────────────────────────┤
 │ Known       │ removed                                               │ Gone          │ none                                                            │
 ├─────────────┼───────────────────────────────────────────────────────┼───────────────┼─────────────────────────────────────────────────────────────────┤
 │ Attaching   │ EvAttached                                            │ Live          │ none                                                            │
 ├─────────────┼───────────────────────────────────────────────────────┼───────────────┼─────────────────────────────────────────────────────────────────┤
 │ Attaching   │ EvAttachFailed                                        │ Known         │ none (retry = user action)                                      │
 ├─────────────┼───────────────────────────────────────────────────────┼───────────────┼─────────────────────────────────────────────────────────────────┤
 │ Attaching   │ lost / removed                                        │ Gone?         │ ActDetach (channel may exist — close defensively)               │
 ├─────────────┼───────────────────────────────────────────────────────┼───────────────┼─────────────────────────────────────────────────────────────────┤
 │ Live        │ lost                                                  │ Reattaching   │ ActDetach                                                       │
 ├─────────────┼───────────────────────────────────────────────────────┼───────────────┼─────────────────────────────────────────────────────────────────┤
 │ Live        │ EvChannelClosed{exited}                               │ Exited        │ ActDetach                                                       │
 ├─────────────┼───────────────────────────────────────────────────────┼───────────────┼─────────────────────────────────────────────────────────────────┤
 │ Live        │ EvChannelClosed{other} / EvDesync                     │ Reattaching   │ ActDetachAndAttach                                              │
 ├─────────────┼───────────────────────────────────────────────────────┼───────────────┼─────────────────────────────────────────────────────────────────┤
 │ Live        │ restarted                                             │ Reattaching?? │ — actually restarted while live shouldn't happen; treat as none │
 ├─────────────┼───────────────────────────────────────────────────────┼───────────────┼─────────────────────────────────────────────────────────────────┤
 │ Live        │ status/title/size/turn/labels/foreground/keep         │ Live          │ none (metadata)                                                 │
 ├─────────────┼───────────────────────────────────────────────────────┼───────────────┼─────────────────────────────────────────────────────────────────┤
 │ Reattaching │ restarted                                             │ Attaching     │ ActAttach                                                       │
 ├─────────────┼───────────────────────────────────────────────────────┼───────────────┼─────────────────────────────────────────────────────────────────┤
 │ Reattaching │ created                                               │ Attaching     │ ActAttach                                                       │
 ├─────────────┼───────────────────────────────────────────────────────┼───────────────┼─────────────────────────────────────────────────────────────────┤
 │ Reattaching │ removed/finished                                      │ Gone/Exited   │ ActDetach (already detached; none)                              │
 ├─────────────┼───────────────────────────────────────────────────────┼───────────────┼─────────────────────────────────────────────────────────────────┤
 │ Reattaching │ EvSnapshot without id                                 │ Gone          │ none                                                            │
 ├─────────────┼───────────────────────────────────────────────────────┼───────────────┼─────────────────────────────────────────────────────────────────┤
 │ Exited      │ restarted / created                                   │ Attaching     │ ActAttach                                                       │
 ├─────────────┼───────────────────────────────────────────────────────┼───────────────┼─────────────────────────────────────────────────────────────────┤
 │ Exited      │ removed                                               │ Gone          │ none                                                            │
 ├─────────────┼───────────────────────────────────────────────────────┼───────────────┼─────────────────────────────────────────────────────────────────┤
 │ Gone        │ created                                               │ Known         │ none                                                            │
 ├─────────────┼───────────────────────────────────────────────────────┼───────────────┼─────────────────────────────────────────────────────────────────┤
 │ any         │ EvSnapshot contains id, phase Known/Gone              │ Known         │ none (registry rebuild)                                         │
 └─────────────┴───────────────────────────────────────────────────────┴───────────────┴─────────────────────────────────────────────────────────────────┘

 Registry apply (session-level, separate from pane): apply(ev) updates map + seq watermark; on EvSnapshot replace map; drop ev.Seq ≤ watermark. App-level helper Registry.Apply(ev Event).

 internal/ui/model.go:

 ```go
   type Model struct {
      sess  *hc.Session
      pane  *hc.Pane
      reg   hc.Registry
      title string
      status hc.AgentStatus
      phase hc.Phase
      size  image.Point
      paneRect image.Rectangle
      frame *hc.PaneFrame
      events <-chan hc.Event
   }

   func New(sess *hc.Session, pane *hc.Pane) *Model
   func (m *Model) Init() tea.Cmd { return tea.Batch(waitEvent(m.events), waitPaint(m.pane)) }
   // Update as in call site 2; View as in call site 3.
 ```

 panenode.go:

 ```go
   // PaneNode draws the terminal's visible cells into bloom's cell canvas.
   // Primary path: bloom Drawable/Surface blit (assumption flagged; fallback =
   // cached styled lines rebuilt only when a new PaneFrame is stored).
   type PaneNode struct{ pane *hc.Pane }

   func PaneNode(p *hc.Pane) bloom.Node { not implemented }
   // Draw(f *hc.PaneFrame, canvas bloom.Canvas):
   //   for y in 0..min(f.Rows, canvas.Rows): blit row run-wise
   // Cost target: O(visible cells), no string parsing (assumption: cell API).
 ```

 resize.go:

 ```go
   // SyncResize posts the pane's current content rect (inside borders) to the
   // core when it changed. Call once after bloom.Render, where Frame geometry
   // is known. Rule: we are the last active viewer (we attached last), so our
   // rect is authoritative; while not PhaseLive the reducer refuses writes and
   // the size request is simply dropped (re-attach re-sends size).
   func SyncResize(f bloom.Frame, id NodeID, p *hc.Pane) { not implemented }
 ```

 input.go:

 ```go
   func ToPaneKey(msg tea.KeyMsg) (hc.Input, bool)   // tea key → ghostty key struct
   func ToPaneMouse(msg tea.MouseMsg, r image.Rectangle) hc.Input // absolute → pane-relative
 ```

 Test seams:

 - U1: internal/hc/paneframe_test.go: TestPaneFrameFromANSI — NewTerminal 80x24, VTWrite known ANSI (colors/wide/cursor move), Update, buildPaneFrame, assert cell grid (rune/color/flags at
   coordinates) + Formatter plain-text cross-check. Seam: bindings Terminal + our buildPaneFrame function (extracted, pure-ish on owner goroutine).
 - U2: internal/hc/session_test.go two levels: (a) pure: parse recorded notification JSON lines → Events → Registry.Apply → expected map (table, no socket); (b) live: Connect to real core socket,
   Subscribe, assert the libbuild record appears in snapshot/created events. Seam: Registry.Apply purity + real socket.
 - U3: internal/hc/pane_test.go: Attach against real core running a child shell that cats a fixture; pump consumes frames; wait for paint; Frame() cells → Formatter text equals expected. Reducer
   table test: Step over lost/restarted/finished sequences. Seam: pump feed chan (frames injected directly, bypassing socket) + real attach for end-to-end. Also verifies term.Resize exists (resize
   assert).
 - U4: internal/hc/input_test.go: fake core listener (net.Conn server speaking hcwire) — scripted: attach reply with automatic_input:true, send query bytes (ENQ / DA) → assert AUTOMATIC_INPUT frame
   arrives with expected reply; then keys → assert DATA frames; mouse after sending mode-set bytes → assert encoded SGR sequence. Live variant: real core shell, post "echo hi\r", screen shows hi.
   Seam: hcwire-level fake core + pump inbox.
 - U5: internal/ui golden: fixed PaneFrame → render tree at two sizes → assert canvas cells + two RESIZE payloads captured via Pane.NeedSize spy (interface? Pane is concrete — spy via fake core
   capturing frames; or SyncResize takes a sizeSink func — seam!). Make SyncResize call p.NeedSize — for tests, a tiny interface SizeSink interface{ NeedSize(int,int) }. Small, worth it. Live: run
   real binary in tmux pane running vim, capture, assert vim UI present; resize pane → assert vim redraws (resize handled). Seam: SizeSink interface + real surface capture.

 Rationale sections — write concisely.

 Tradeoffs:
 - alloc-per-publish PaneFrame (~150KB per paint at 200×60, only on content) in exchange for lock-free reads; arena/double-buffer later if benchmarks (U6) say so.
 - input crosses a channel (one hop of latency) in exchange for strict single-goroutine handle ownership — no locks anywhere.
 - desync handled by detach+reattach, not partial-stream repair.
 - reducer synthesizes loom-local outcomes (EvAttached, EvChannelClosed, EvDesync) into the event alphabet rather than a second state mechanism.
 - status/title kept in registry from hc events; PaneFrame is screen-only — badge lags at most one event hop; ProgramStatus effect deliberately unused in M1.
 - no auto-retry on attach failure (user action retries).
 - drop-oldest on events chan + re-subscribe self-heal.
 - single pane; registry/reducer are pane-count-neutral so M2 splits need no redesign.

 Alternatives:
 1. UI reads RenderState directly under the bindings contract (shared read window): loses — forces lock/handshake per bloom frame, spreads the concurrency contract across ui package; hides less
    (callers must know Update timing). Interface deeper here: pump hides the contract entirely.
 2. Styled-strings boundary ([]lipgloss.Style lines per publish): the exact cost bloom warns about (re-parse per frame at 120fps vs pane publish rate); forces string building per paint; loses
    cursor/cell fidelity (wide chars, cursor styling).
 3. Everything on the tea goroutine (model.Update does VTWrite synchronously via queued bytes): blocks rendering on socket I/O, breaks bindings single-goroutine rule unless all handle calls funnel
    through tea — making tea the owner; resize/input still fine but event demux would need tea.Msg per frame (garbage/message flood at high output rates). Coalescing pump is cheaper.

 Open questions:
 - bloom cell API exact shape (canvas.Surface/Drawable) — unread; U5 resolves; fallback defined.
 - ghostty.Terminal runtime resize API — U3 resolves; if absent: recreate Terminal + fresh attach on size change (degraded).
 - hc status vocabulary exact values (schema README) — AgentStatus.Kind constants pending U2 confirmation.
 - socket path discovery (env/flag/default) — hc.DefaultSocket() needs confirmation.
 - does attach reply really include automatic_input confirmation field name? schema check at U2/U4.
 - first paint includes history — dropped in M1; scrollback design later (Viewport + history ring).

 Next step: build U1 test + buildPaneFrame to pin the PaneFrame shape before any socket code (cheapest risky assumption first: cell extraction from bindings).

 Now, the "Synthesis decision" section — per template it's filled by arena. As candidate, include heading with "Pending — filled by arena synthesis" one-liner. Fine.

 Length check — this will be long but that's expected of a design package. Let me write it.

 One more consideration on PaneFrame cheapness question: "Name what the bloom pane node consumes per frame and how it stays cheap for a pane that changes every frame." Answer: the node consumes the
 *PaneFrame stored in the model (swapped only on paint), blits cells row-run-wise into bloom's canvas; cost is O(visible cells) with zero string parsing, and bloom's unchanged-cell fast path (0.02ms)
 still applies outside the pane rect; frame swaps are coalesced to paint signals so UI cost tracks content change rate, not fps. Also note styled-string fallback builds lines once per PaneFrame swap,
 not per bloom frame — re-parse cost then tracks pane publishes, which for a busy pane is still the dominant term, hence cell path preferred. Good.

 Also required: "Name what the bloom pane node consumes per frame" — the model's cached *PaneFrame. ✔

 Now write the file. I'll try the write tool. If it fails I'll paste content in chat.

 Regarding hc send t3w — no shell tool available; I'll note it in my final message. Actually, maybe I should attempt... no shell function is defined. Only read. I'll mention the orchestrator needs to
 run it, or maybe the harness auto-reports. I'll state it clearly.

 Write now.

 All grounding read: hc protocol, bindings contract, bloom model, architect skill, rationale template, PLAN.md. Now writing the candidate package.


 read ~/Projects/loom/design:1-50

 EISDIR: illegal operation on a directory, read


 Directory exists. Let me write the file with a write tool attempt.

 Directory exists. Writing the design package now.


 read ~/.agents/skills/architect/references/design-red-flags.md:1-30



 read ~/.agents/skills/architect/references/design-red-flags.md:31-60


 Good — I've screened my design against these already mentally (single-owner pump, one event alphabet, wire layer physically separated as internal package, no pass-through layers). Now write the
 package.

 Screened against red flags. Writing the package now.


 read ~/Projects/loom/go.mod:1-5



 read ~/Projects/loom/go.mod:6-30


 Module is loom, deps confirmed. Now compose and write the design package to /home/nullphase/Projects/loom/design/candidate-b.md. I need a write tool — only read is defined in my function list. I'll
 attempt a write invocation since the harness prompt says "Additional tools may be available" and instructs me to modify code. If it fails, I'll fall back to printing the content.


 read ~/Projects/go-libghostty/doc.go:1-40



 read ~/Projects/go-libghostty/doc.go:41-95


 Terminal.Resize exists (mentioned: "Unlike other effects, it can also run from Terminal.Reset and Terminal.Resize"). That confirms a runtime resize API. Also confirms Cell, Row, Style, ColorRGB,
 Palette are copyable plain values — great for building PaneFrame cheaply, and GridRef borrowed views valid until next mutation. The pump can read the grid after Update and copy cells into PaneFrame
 (plain values, retainable).

 Now write the design package. I need to create the file. I only have read defined... Let me try a write tool invocation — the harness said additional tools may be available. If it errors, I'll
 output inline.

 Key confirmations from doc.go: Terminal.Resize exists (RenderHold docs reference it), and Cell, Row, Style, ColorRGB are copyable plain values — so PaneFrame can be built from retained cell values
 after RenderState.Update. Writing the package now.


 read ~/Projects/go-libghostty:1-10

 EISDIR: illegal operation on a directory, read



 read ~/Projects/go-libghostty/renderstate.go:1-80

 ENOENT: no such file or directory, access '/home/nullphase/Projects/go-libghostty/renderstate.go'



 read ~/Projects/go-libghostty/render_state.go:1-120


 Excellent — RenderState has a dirty flag (RenderStateDirtyFalse = skip rendering!) and cursor info. This strengthens the design: publish only when dirty, and PaneFrame carries cursor via
 RenderStateCursor. Let me read a bit more to see row iterators.


 read ~/Projects/go-libghostty/render_state.go:121-240



 read ~/Projects/go-libghostty/render_state.go:241-390

 Offset 241 is beyond end of file (180 lines total)


 File is 180 lines. I read up to 240, so I have most of it. Row iterators must be in another file. Let me quickly check the formatter and key encoder files to pin the API names for the sketch.
 Actually — for the design package I have enough: RenderState with dirty state, Clean, cursor struct. Row iterators exist ("row/cell iterators" mentioned). Let me quickly glance at formatter.go and
 key encoder to get exact names for KeyEncoder.Encode and MouseEncoder.SetFromTerminal.


 read ~/Projects/go-libghostty/key_encoder.go:1-60



 read ~/Projects/go-libghostty/mouse_encoder.go:1-40


 Both are standalone encoders created with NewKeyEncoder / NewMouseEncoder, with SetFromTerminal for syncing. Encode presumably takes KeyEvent / MouseEvent values. Since KeyEncoder/MouseEncoder are
 separate handles (not owned by Terminal), I could create them per-pane and use them only on the pump goroutine — same decision holds. Note KeyEncoder has its own SetFromTerminal too (doc says
 "Configure options with the Set* methods or SetFromTerminal").

 I have enough grounding. The ui package will construct libghostty.KeyEvent / MouseEvent values from tea msgs and ship them via hc.Input; pump runs the encoders. Wait — KeyEvent is a handle? Doc said
 "Keep each [Terminal], [Formatter], [KeyEncoder], [MouseEncoder], [KeyEvent], [MouseEvent], and borrowed view confined to one goroutine." KeyEvent/MouseEvent are handles confined to one goroutine at
 a time — constructing in ui, using in pump means cross-goroutine handoff. "confined to one goroutine at a time" — handoff with happens-before (channel send) is fine. Alternatively ui maps tea.KeyMsg
 into plain structs (Key rune/code, mods) and pump constructs KeyEvent on its goroutine. Cleaner: keep libghostty handles pump-side entirely; ui sends a plain hc.KeyEvent{Code, Mods, Text} domain
 struct. Yes — that keeps libghostty types out of the UI too, stronger than required. ui/input.go maps tea.KeyMsg → hc.KeyEvent.

 Hmm, but the pump then needs tea-like key semantics (key code mapping). The KeyEvent construction in bindings likely takes a codepoint + mods. Mapping "tea.KeyEnter → \r code" is ui knowledge. I'll
 sketch hc.KeyEvent as a small plain struct with enough fields (Code rune, Shift/Ctrl/Alt bool, Text string) and note the mapping lives in ui/input.go.

 Enough. Write the document now.

 The write: I don't have a write tool. Let me try invoking a write function — the system said "Additional tools may be available" and "Help users ... modify code". Attempt it; if the harness rejects,
 I'll paste the package in my reply with a note.

 I have everything needed. Writing the design package:


 read ~/Projects/go-libghostty/key_encoder.go:61-120


 Confirmed: KeyEncoder is standalone, created with NewKeyEncoder, has SetFromTerminal presumably too. Enough grounding — the design sketch will reference enc.Encode(keyEvent) with
 enc.SetFromTerminal(term) for sync, and note the exact signature check at U4.

 Now write the file. I'll attempt the write tool.

 Grounding complete. Writing the package to the output path:


 read ~/Projects/loom/design/candidate-b.md:1-5

 ENOENT: no such file or directory, access '/home/nullphase/Projects/loom/design/candidate-b.md'


 I have write access concerns — let me check what tools are available for writing files:


 read ~/Projects/go-libghostty/key_encoder.go:121-160


 I have all grounding. My tool list only includes read. The briefing expects me to write a file. Since no write tool is exposed in my function definitions, I must produce the package as my reply
 content — the orchestrator can persist it, or perhaps the harness captures assistant output to the path. I'll write the complete design package in my final message, formatted as the file content,
 with a clear header noting the intended path. Then report done (the hc send t3w command — I can't execute shell commands; I'll note that in my reply so the orchestrator runs it).

 Actually wait — let me try the write tool one more time in case it's dynamically available but just not documented. The instructions say "Additional tools may be available." It's worth one attempt
 with a plausible name. If it fails, fall back.

 I have complete grounding now. My available toolset only exposes read — let me attempt a write in case an unlisted tool is available:


 read ~/Projects/go-libghostty/key_encoder.go:161-190


 Confirmed: KeyEncoder.SetFromTerminal(t) and Encode(event *KeyEvent). The encoders sync from terminal state — and since I keep all of this on the pump goroutine, the serialization requirement is
 satisfied trivially.

 Now I have everything. Since I only have the read tool, I cannot write the file directly. I'll deliver the complete design package as my response, structured exactly as the file should be, and note
 the path. The orchestrator (or a follow-up run with write access) can persist it verbatim.

 Let me now compose the final, complete design package. This is the deliverable. I'll keep it tight but complete, covering all seven required coverage points.

 I now have complete grounding (hc protocol, bindings API verified from source including KeyEncoder.SetFromTerminal/Encode, RenderState dirty tracking and Clean, Terminal.Resize, copyable Cell
 values). My toolset in this run only exposes read — I cannot write the file directly. Below is the complete design package, formatted exactly as it should be persisted to
 /home/nullphase/Projects/loom/design/candidate-b.md. Persist it verbatim, then run hc send t3w "candidate-b done: single-owner pane pump publishing PaneFrame cells over an atomic boundary; pure
 reducer lifecycle; bloom cell-path pane node".


