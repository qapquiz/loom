            p.handleInput(in) // see below
         case <-p.pump.sizeTick.C: // 50 ms coalescing
            if p.pump.pending != p.pump.lastSent && p.pump.phase == PhaseLive {
               conn.Send(Resize(p.ch, p.pump.pending))       // u16 LE cols, rows
               p.pump.term.Resize(x, y)                       // verified API (doc.go)
               p.pump.lastSent = p.pump.pending
            }
         }
      }
   }

   func (p *Pane) handleInput(in Input) {
      switch in.Kind {
      case InputKey:
         p.pump.keyEnc.SetFromTerminal(p.pump.term) // modes → kitty flags etc.
         b, err := p.pump.keyEnc.Encode(...)        // KeyEvent built pump-side
         conn.Send(Data(p.ch, b))                   // legacy or kitty, encoder's call
      case InputMouse:
         p.pump.mouseEnc.SetFromTerminal(p.pump.term) // tracking mode sync (SGR vs X10...)
         b, err := p.pump.mouseEnc.Encode(...)
         if len(b) > 0 { conn.Send(Data(p.ch, b)) }
      case InputSize:
         p.pump.pending = in.Size // sent by sizeTick, coalesced
      case InputFocus:
         conn.Send(Focus(p.ch, in.Focus)) // 1 gained / 0 lost
      }
   }
 ```

 Two properties fall out for free. First, ordering: the emulator's answers to program queries (DA, ENQ, XTGETTCAP, size reports) are produced by the WritePty effect during VTWrite — synchronously, on
 the pump — and flushed as AUTOMATIC_INPUT immediately after, so query/reply order can never interleave wrongly. Second, negotiation: hello requests automatic_input in features; the attach reply's
 confirmation sets p.pump.auto; until it is true, flushReplies drops (an old core would ignore frame kind 6 anyway, but loom never sends it — belt and suspenders, one field, no sync flags).

 Flow control and desync. The pump is never slow (VTWrite+Update is µs), so feed (buffered 256) practically never fills and the demux never blocks on a pane — honoring hc's guarantee that a stalled
 stream never blocks channel 0. The safety valve: if feed is somehow full, the demux drops the payload and does not credit it; hc then falls >4 MiB behind and sends a fresh repaint; the pump, seeing
 it set a desync note on drop, emits EvDesync and the reducer takes the ActDetachAndAttach path (a fresh attach is a byte-stream-aligned first paint — no partial-sequence repair logic exists
 anywhere).

 ### Resize plumbing

 Rule: the last active viewer sets the size. Loom becomes the last active viewer by attaching, so on attach it sends RESIZE(cols, rows) (attach params) and thereafter its current rect is
 authoritative. The flow: bloom composites the tree and records each node's rectangle on the returned tea.View.Frame → SyncResize reads the pane node's content rect (inside borders), and if it
 differs from the last sent size posts Pane.NeedSize(cols, rows) → the pump's 50 ms tick coalesces bursts (border drags relayout every frame) → one RESIZE frame per settled layout, plus term.Resize
 so the local emulator stays size-consistent with the pty. When the pane is not in PhaseLive the pump refuses the send (reducer-derived check); on re-attach the size is re-sent, which is exactly the
 last-active-viewer contract. Rects clamp to ≥ 2×2 before encoding (u16, and terminals degenerate below that).

 ### Input routing summary (one table, one place)

 ┌─────────────────────┬──────────────────────────────────────┬───────────────────────────────────────┬─────────────────┐
 │ UI event            │ lane                                 │ pump action                           │ hc frame        │
 ├─────────────────────┼──────────────────────────────────────┼───────────────────────────────────────┼─────────────────┤
 │ tea.KeyMsg          │ ui.ToPaneKey → hc.KeyEvent value     │ KeyEncoder.SetFromTerminal + Encode   │ DATA            │
 ├─────────────────────┼──────────────────────────────────────┼───────────────────────────────────────┼─────────────────┤
 │ tea.MouseMsg        │ ui.ToPaneMouse (abs → pane-relative) │ MouseEncoder.SetFromTerminal + Encode │ DATA            │
 ├─────────────────────┼──────────────────────────────────────┼───────────────────────────────────────┼─────────────────┤
 │ program query bytes │ WritePty effect during VTWrite       │ flush after Update, if auto confirmed │ AUTOMATIC_INPUT │
 ├─────────────────────┼──────────────────────────────────────┼───────────────────────────────────────┼─────────────────┤
 │ layout change       │ SyncResize → NeedSize                │ pending size, 50 ms tick              │ RESIZE          │
 ├─────────────────────┼──────────────────────────────────────┼───────────────────────────────────────┼─────────────────┤
 │ focus change        │ SendInput{Focus}                     │ —                                     │ FOCUS           │
 └─────────────────────┴──────────────────────────────────────┴───────────────────────────────────────┴─────────────────┘

 Test seams (one obvious check per unit)

 - U1 bindings hello — seam: extracted buildPaneFrame + bindings Terminal, no sockets. TestPaneFrameFromANSI: NewTerminal(80×24), VTWrite a known ANSI fixture (colors, wide rune, cursor moves,
   reverse video), Update, buildPaneFrame, assert exact cells at named coordinates, plus Formatter plain-text cross-check. Pins the PaneFrame shape before any socket code exists.
 - U2 hc session — two seams. (a) Pure: recorded notification JSON lines → parse → Event → Registry.Apply → expected registry map, including the missed/watermark drop rule (table-driven, no socket).
   (b) Live: Connect to the real core, Subscribe, assert the libbuild terminal record appears in the snapshot (per PLAN).
 - U3 attach — seam: the pump's feed channel, frames injected directly (bypassing the socket), plus a real attach end-to-end. Pump consumes a fixture first-paint, Update, publish; assert Frame() cell
   grid matches. Reducer table test lives here too: feed the transition table's lost→restarted→attached sequences to Step. Also pins the term.Resize API assumption.
 - U4 input — seam: a fake core listener (a net.Listener on a temp socket speaking hcwire frames) with a scripted attach reply carrying automatic_input: true. Assert: key inputs produce DATA frames
   with the expected legacy sequences; query bytes in the stream produce AUTOMATIC_INPUT frames with the right replies; mouse-tracking mode bytes followed by mouse inputs produce SGR sequences. Live
   variant per PLAN: real core, child shell, post echo hi\r, screen shows hi — the end-to-end proof.
 - U5 bloom frame — two seams. (a) SizeSink interface: SyncResize calls Pane.NeedSize through a one-method interface, so a spy records RESIZE payloads when a tree is rendered at two sizes — pure, no
   surface. (b) Real surface per PLAN: run loom in an hc pane running vim, capture, assert vim's UI renders; resize the pane, assert vim redraws (proves RESIZE + term.Resize + repaint loop). This
   unit also resolves the bloom cell-path assumption.

 Rationale

 ### Shape — load-bearing decisions

 - One pump goroutine per pane owns every ghostty handle (single source of truth per invariant, separate-before-serializing-shared-state). The bindings contract ("handles are single-goroutine; Update
   under exclusive access, reads safe after") becomes a property of the architecture rather than a discipline callers must keep: there is exactly one goroutine that can touch the Terminal, and
   buildPaneFrame runs on it immediately after Update — inside the contract's safe window, not merely adjacent to it. The UI never touches RenderState, so the contract cannot be violated by a future
   UI change.
 - The boundary is one immutable value, published atomically (encode-lessons-in-structure). atomic.Pointer[PaneFrame] + a coalesced signal means readers can never see a torn frame and can never block
   the pump. Immutability is enforced by the type being plain copied values only (the bindings guarantee retention safety for Cell-like values after Update).
 - One event alphabet for the reducer, including loom's own outcomes (no hand-synced state). EvAttached, EvChannelClosed, EvDesync, EvSnapshot are synthesized into Event so Step has a single input
   kind and the transition table is exhaustive by construction. No phase is ever represented twice — attach-in-flight is PhaseAttaching, not attaching bool.
 - Domain structs cross the ui→pane channel, not ghostty handles (boundary-discipline). KeyEvent/MouseEvent are plain values; encoders are constructed and synced pump-side, satisfying the encoders'
   own "caller must serialize access" notes trivially.
 - Wire parsing dies in hcwire (make internals unreachable). ui imports hc, never hcwire; the compiler enforces the constraint. hc exports domain types only; nothing about [u32][u8][u32] framing or
   JSON-RPC is nameable outside hcwire.
 - Interface depth, judged: the public surface is nine methods and five types, behind which hides the plain→framed handshake, credit accounting, demultiplexing, snapshot resync, effect routing,
   encoder mode sync, and coalescing. The caller's total knowledge is: connect, list, attach, subscribe, read frame, send input, close. That is the trade the spike exists to validate — and the parts
   most likely to be wrong (bloom cell path, resize API, status vocabulary) are each pinned by a named unit test seam before they can spread.

 ### Tradeoffs accepted

 - We accept a fresh Cells allocation per publish (~150 KB at 200×60, only when dirty) in exchange for lock-free reads and zero torn-frame risk; a pump-side arena or double buffer is a
   U6-benchmark-driven change, not a design change.
 - We accept one channel hop of input latency (UI → pump) in exchange for strict single-goroutine handle ownership; no locks exist anywhere in the design.
 - We accept detach+reattach as the desync repair, in exchange for having no partial-stream resynchronization logic at all.
 - We accept that agent status comes only from hc's status events (one hop of possible lag vs the local ProgramStatus effect) in exchange for one source of truth that works identically for attached
   and unattached panes.
 - We accept no auto-retry on EvAttachFailed in M1 (user action retries) to keep the reducer free of timers.
 - We accept dropping scrollback history from the first paint on the floor in M1; the viewport-only PaneFrame is why this is cheap to add later.
 - We accept single-pane scope; the registry and reducer are pane-count-neutral, so M2 splits and tabs need no redesign — only more pane instances.

 ### Alternatives considered

 1. UI reads RenderState directly under the bindings contract (shared read window, handshake per frame). Lost: it spreads the concurrency contract into the ui package — every bloom frame needs a
    "safe to read?" negotiation with the pump, and a future contributor reading only model.go can't see why. This shape hides strictly less: the pump must exist anyway; the only question is whether
    the contract leaks past it.
 2. Styled-strings boundary ([]styled lines published per paint, node renders strings). Lost: bloom re-parses strings every frame; at 120 fps against a pane publishing at content rate, the parse cost
    is paid where it can't be coalesced, and cell fidelity (wide chars, cursor styling, reverse video on the cursor cell) degrades into ad-hoc string surgery. It is kept only as the defined fallback,
    not the design.
 3. Everything on the tea goroutine (model.Update performs VTWrite via queued bytes; tea owns all handles). Lost: rendering blocks on socket I/O, or — if the demux stays separate — every DATA frame
    becomes a tea.Msg, flooding the message loop at output rates terminals easily sustain. The coalescing pump decouples output rate from fps with one channel.

 ### Open questions and risks

 - bloom cell draw path: is canvas.Surface/Drawable real, stable enough, and cell-oriented as the docs index suggests? U5 resolves; the styled-lines fallback is specified, so this is a cost risk, not
   a design risk.
 - Status vocabulary: what exact values does OSC 7501 carry (idle/running/waiting/…)? Confirm from the schema README at U2; AgentStatus constants are placeholders until then.
 - Socket discovery: what is hc.DefaultSocket() — env var, well-known path, both? Confirm at U2.
 - Attach reply fields: confirm the automatic_input confirmation field name and whether attach params carry initial size (making the explicit post-attach RESIZE redundant-but-harmless). U2/U4.
 - term.Resize vs repaint latency: after a local resize, the program's repaint arrives as deltas; if some programs need a nudge, does hc forward a SIGWINCH-equivalent promptly? Watch this in U5's vim
   check.

 ### Next implementation step

 Write the U1 test and buildPaneFrame against a hardcoded PaneFrame — pinning cell extraction from the bindings' row iterators before any socket code, since that is the cheapest place to discover
 whether the boundary type's shape survives contact with real cell data.

 ### Synthesis decision

 Pending — filled by arena synthesis.


 <hc from="t3w">Your full package scrolled past what the orchestrator could recover. Re-output ONLY the missing opening sections of your candidate package, as plain text in this order: (1) the
 package title line, (2) the problem statement, (3) the usage sketch with every call site. Do not repeat your type sketch, module map, or anything after the usage. Keep it tight.</hc>


 The orchestrator says the opening scrolled past. Re-output only: (1) title line, (2) problem statement, (3) usage sketch with every call site. No type sketch, module map, or anything after. Plain
 text, tight.

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




 ### Module map
 ```
   loom/
   ├── cmd/loom/main.go          // thin shell: flags, connect/list/attach, tea.Run (~80 lines)
   ├── internal/hcwire/          // bytes and frames only. stdlib. imported by nobody but hc.
   │   ├── frame.go              //   Frame, Kind, [ch u32 LE][kind u8][len u32 LE][payload] codec
   │   ├── conn.go               //   Connect: dial → plain hello{framed:true} → framed Conn
   │   └── rpc.go                //   JSON-RPC over channel 0: Call, Notify, ParseNotification
   ├── internal/hc/              // the domain API. imports hcwire + libghostty only.
   │   ├── session.go            //   Session, Connect, List, Subscribe, Attach, demux loop
   │   ├── events.go             //   Event (one alphabet), Kind, Registry (seq + snapshot)
   │   ├── terminal.go           //   TerminalInfo, AgentStatus parse (OSC 7501 via hc)
   │   ├── reducer.go            //   Phase, Action, Step() — pure state machine
   │   ├── pane.go               //   Pane, pump goroutine, Input, publish
   │   └── paneframe.go          //   PaneFrame, PaneCell — THE boundary type
   └── internal/ui/              // imports bloom, bubbletea, hc. never hcwire.
       ├── model.go              //   tea.Model, message lanes, rearm cmds
       ├── panenode.go           //   bloom node blitting PaneFrame cells
       ├── input.go              //   tea.KeyMsg/MouseMsg → hc.KeyEvent/MouseEvent (domain structs)
       └── resize.go             //   SyncResize: bloom Frame rect → Pane.NeedSize
 ```
 Dependency direction is a DAG: ui → hc → hcwire, hc → libghostty. hcwire is internal, so an import from ui fails the build — the "never leak wire types" rule is enforced by the compiler, not review.
 ### The boundary type
 PaneFrame is the only type that carries emulator state to the UI. It is screen content only — title and status live in the registry, fed by hc's title/status events, so the badge channel is uniform
 for attached and unattached panes and there is no second source of truth.
 ```go
   // internal/hc/paneframe.go
   // PaneFrame is the complete visible state of one terminal's screen.
   // Published by the pane pump after each RenderState.Update and stored
   // atomically; readers treat it as immutable. Plain copied values only,
   // per the bindings contract (GridRef views die at the next Update).
   type PaneFrame struct {
      Cols, Rows       int
      Cells            []PaneCell // row-major, len == Cols*Rows
      CursorX, CursorY int        // viewport coords; meaningful when CursorVisible
      CursorVisible    bool
      CursorStyle      libghostty.CursorStyle // block/bar/underline
   }
   // PaneCell is one screen cell. Copyable plain value, retained freely.
   type PaneCell struct {
      Rune  rune
      Fg, Bg Color    // 0x00RRGGBB, DefaultColor = max uint32
      Flags CellFlags // bold | italic | underline | strikethrough | reverse
      Wide  bool      // this cell is the head of a double-width rune
   }
   // buildPaneFrame copies the rendered viewport out of the render state into
   // a fresh PaneFrame. Runs on the pump goroutine only, after RenderState.Update
   // returned (reads are contract-safe there). Allocates Cells per publish in M1.
   func buildPaneFrame(rs *libghostty.RenderState, prev *PaneFrame) *PaneFrame {
      not implemented // row iterator → PaneCell copy; cursor via RenderStateCursor
   }
 ```
 What the bloom node consumes per frame and why it is cheap. The node consumes the model's cached *PaneFrame pointer (swapped only when the pump publishes). Per bloom frame it blits cells
 row-run-wise into bloom's cell canvas — zero string parsing, no lipgloss re-parse, the exact cost bloom's docs warn about for styled strings. Cost is O(visible cells) on change; bloom's own
 unchanged-frame fast path (~0.02 ms) covers everything outside the pane rect. Publish rate is bounded by content: RenderState.Update reports dirty state, and the pump skips the publish entirely when
 RenderStateDirtyFalse, so an idle pane costs bloom nothing and a busy pane costs one blit per coalesced paint signal, not per fps. Flagged assumption: bloom's cell-level draw path
 (canvas.Surface/Drawable) was not fully read this session. Primary path targets it; the defined fallback is a node-side cache of styled lines built once per PaneFrame swap (re-parse cost then tracks
 pane publishes instead of bloom frames) — viable but strictly worse, so U5 resolves this first.
 ### Session and the events alphabet
 ```go
   // internal/hc/session.go
   // Session is a live connection to one hc core process.
   type Session struct {
      conn   *hcwire.Conn
      rpc    *hcwire.RPC
      events chan Event        // delivered to the app; read loop never blocks on it
      panes  map[uint32]*Pane  // channel → pane; written by Attach/Close under mu
      mu     sync.Mutex
      done   chan struct{}
   }
   func Connect(path string) (*Session, error) { not implemented } // hcwire.Connect + demux goroutine
   func (s *Session) List() ([]TerminalInfo, error) { not implemented }
   func (s *Session) Subscribe() (<-chan Event, error) { not implemented }
   func (s *Session) Attach(id string, opts AttachOpts) (*Pane, error) { not implemented }
   func (s *Session) Close() { not implemented }
 ```
 The demux loop owns conn.Recv() and never blocks: channel 0 goes to the RPC dispatcher, channel N to panes[N]'s buffered feed channel. Attach is internally sequenced so there is no gap — the RPC
 resolves, the Pane and its pump are constructed and registered under mu, and only then does Attach return; first-paint DATA frames can only arrive after the core processed the attach, so
 registration happens-before delivery.
 ```go
   // internal/hc/events.go
   // Event is ONE alphabet: every hc notification kind plus loom-synthesized
   // outcomes, so downstream code (registry, reducer) has a single input
   // vocabulary and no parallel boolean bookkeeping.
   type Event struct {
      Seq      uint64 // hc's per-process seq; 0 for loom-synthesized
      Term     string // terminal id
      Kind     Kind
      Status   AgentStatus     // EvStatus
      Title    string          // EvTitle
      Size     image.Point     // EvSize
      ExitCode int             // EvFinished, EvChannelClosed
      Snapshot []TerminalInfo  // EvSnapshot (missed → fresh registry)
   }
   type Kind uint8
   const (
      EvCreated Kind = iota; EvLost; EvRestarted; EvKeep; EvFinished; EvRemoved
      EvStatus; EvTurn; EvTitle; EvLabels; EvSize; EvForeground
      // loom-synthesized:
      EvAttached      // attach RPC resolved
      EvAttachFailed  // attach RPC errored
      EvChannelClosed // CLOSE frame on the pane channel (reason + exit code)
      EvDesync        // pump dropped stream bytes; needs detach+reattach
      EvSnapshot      // missed arrived: replace registry wholesale
   )
   // Registry applies events with a seq watermark: replaces its map on
   // EvSnapshot, drops any event whose Seq ≤ watermark. Pure except for its
   // own map; the app-side self-heal for any delivery gap is the next
   // EvSnapshot (see below).
   type Registry struct { not implemented }
   func (r *Registry) Apply(ev Event) { not implemented }
   func (r *Registry) Title(id string) string { not implemented }
   func (r *Registry) Status(id string) AgentStatus { not implemented }
 ```
 Snapshot/missed resync. Events flow on one channel; every Event carries Seq. On EvSnapshot (hc's missed + fresh snapshot) the Registry replaces its map and sets the watermark to the snapshot's seq;
 queued events with seq ≤ watermark are dropped on Apply. Because the watermark lives in the Registry (app side) rather than a shared counter, no locks are needed: the demux produces, the app
 consumes, the watermark arbitrates. Two self-heals compose: (a) if hc itself declares us behind, missed arrives and heals; (b) if loom's own events channel overflows (app stalled), Subscribe
 re-issues events.subscribe internally, which returns a fresh snapshot through the same EvSnapshot path — the app cannot tell the difference, by construction. If a snapshot arrives without the
 attached pane's id, the demux synthesizes EvRemoved for it and the reducer handles detach through the normal path. seq restarts per core process; a core restart kills the socket, so Session treats
 it as Close — the app exits with a message in M1, no auto-reconnect.
 ### Pane lifecycle as a pure state machine
 ```go
   // internal/hc/reducer.go
   type Phase uint8
   const (
      PhaseKnown       Phase = iota // record listed, not attached
      PhaseAttaching                // attach RPC in flight
      PhaseLive                     // channel open, first paint consumed
      PhaseReattaching              // lost: record persists, channel closed
      PhaseExited                   // program finished
      PhaseGone                     // removed from the core
   )
   type Action uint8
   const (
      ActNone Action = iota
      ActAttach          // open channel, create pump
      ActDetach          // CLOSE the channel, keep the record
      ActDetachAndAttach // desync path: close, then attach again
   )
   // Step is the entire lifecycle: phase in, event in → phase out, action for
   // the owner. Pure. No I/O. No booleans anywhere — attach-in-flight, desync,
   // and channel-closed are phases reached via the synthesized kinds.
   // This is the lifecycle test seam (table-driven, no sockets).
   func Step(phase Phase, ev Event) (Phase, Action) { not implemented }
 ```
 Transition table (the implementation is this table, nothing else):
 ┌─────────────┬──────────────────────────────────────────────────────────────┬─────────────┬─────────────────────────────────────────┐
 │ phase       │ event                                                        │ next        │ action                                  │
 ├─────────────┼──────────────────────────────────────────────────────────────┼─────────────┼─────────────────────────────────────────┤
 │ Known       │ keep, status, title, size, turn, labels, foreground, created │ Known       │ none                                    │
 ├─────────────┼──────────────────────────────────────────────────────────────┼─────────────┼─────────────────────────────────────────┤
 │ Known       │ EvAttached                                                   │ Live        │ none (pump already running)             │
 ├─────────────┼──────────────────────────────────────────────────────────────┼─────────────┼─────────────────────────────────────────┤
 │ Known       │ EvAttachFailed                                               │ Known       │ none (retry is a user action in M1)     │
 ├─────────────┼──────────────────────────────────────────────────────────────┼─────────────┼─────────────────────────────────────────┤
 │ Known       │ removed                                                      │ Gone        │ none                                    │
 ├─────────────┼──────────────────────────────────────────────────────────────┼─────────────┼─────────────────────────────────────────┤
 │ Attaching   │ EvAttached                                                   │ Live        │ none                                    │
 ├─────────────┼──────────────────────────────────────────────────────────────┼─────────────┼─────────────────────────────────────────┤
 │ Attaching   │ EvAttachFailed                                               │ Known       │ ActDetach (defensive channel close)     │
 ├─────────────┼──────────────────────────────────────────────────────────────┼─────────────┼─────────────────────────────────────────┤
 │ Attaching   │ lost, removed                                                │ Gone        │ ActDetach                               │
 ├─────────────┼──────────────────────────────────────────────────────────────┼─────────────┼─────────────────────────────────────────┤
 │ Live        │ lost                                                         │ Reattaching │ ActDetach                               │
 ├─────────────┼──────────────────────────────────────────────────────────────┼─────────────┼─────────────────────────────────────────┤
 │ Live        │ EvChannelClosed{reason=exited}                               │ Exited      │ ActDetach (record exit code for the UI) │
 ├─────────────┼──────────────────────────────────────────────────────────────┼─────────────┼─────────────────────────────────────────┤
 │ Live        │ EvChannelClosed{other}, EvDesync                             │ Reattaching │ ActDetachAndAttach                      │
 ├─────────────┼──────────────────────────────────────────────────────────────┼─────────────┼─────────────────────────────────────────┤
 │ Live        │ status, title, size, turn, labels, foreground, keep          │ Live        │ none (registry metadata only)           │
 ├─────────────┼──────────────────────────────────────────────────────────────┼─────────────┼─────────────────────────────────────────┤
 │ Live        │ restarted                                                    │ Live        │ none (should not happen; logged)        │
 ├─────────────┼──────────────────────────────────────────────────────────────┼─────────────┼─────────────────────────────────────────┤
 │ Reattaching │ restarted, created                                           │ Attaching   │ ActAttach                               │
 ├─────────────┼──────────────────────────────────────────────────────────────┼─────────────┼─────────────────────────────────────────┤
 │ Reattaching │ removed                                                      │ Gone        │ none (already detached)                 │
 ├─────────────┼──────────────────────────────────────────────────────────────┼─────────────┼─────────────────────────────────────────┤
 │ Reattaching │ EvSnapshot without id                                        │ Gone        │ none                                    │
 ├─────────────┼──────────────────────────────────────────────────────────────┼─────────────┼─────────────────────────────────────────┤
 │ Exited      │ restarted, created                                           │ Attaching   │ ActAttach (user restarts via core)      │
 ├─────────────┼──────────────────────────────────────────────────────────────┼─────────────┼─────────────────────────────────────────┤
 │ Exited      │ removed                                                      │ Gone        │ none                                    │
 ├─────────────┼──────────────────────────────────────────────────────────────┼─────────────┼─────────────────────────────────────────┤
 │ Gone        │ created                                                      │ Known       │ none                                    │
 └─────────────┴──────────────────────────────────────────────────────────────┴─────────────┴─────────────────────────────────────────┘
 All writes (SendInput, NeedSize, RESIZE/FOCUS/AUTOMATIC_INPUT frames) are refused unless the pane is in PhaseLive — one check in the pump, derived from the phase, never a parallel flag. Status
 arrives via hc's status events (OSC 7501 reports relayed by the core); ProgramStatus effect is deliberately unused in M1 — one source of truth, see Tradeoffs.
 ### Pane: the single-owner pump
 ```go
   // internal/hc/pane.go
   // Input is one UI→pane request. Keys, mouse, size, and focus ride one type
   // so the pump has one inbox and ordering is trivially serial.
   type Input struct {
      Kind  InputKind // Key, Mouse, Size, Focus
      Key   KeyEvent   // domain struct; ui/input.go maps tea.KeyMsg → this
      Mouse MouseEvent // action, button, pane-relative x/y
      Size  image.Point
      Focus bool
   }
   // KeyEvent/MouseEvent are plain domain structs (code, mods, text / action,
   // button, x, y) — deliberately NOT libghostty handles, so ghostty types
   // stay pump-side and the ui→pump channel handoff carries only values.
   type KeyEvent struct { not implemented }  // fields: Code rune, Text string, Mods
   type MouseEvent struct { not implemented } // fields: Action, Button, X, Y
   // Pane is one attached terminal. All ghostty state (Terminal, RenderState,
   // Formatter, KeyEncoder, MouseEncoder) is owned by the pump goroutine.
   // The UI touches Frame() and the Send*/NeedSize methods, nothing else.
   type Pane struct {
      ID    string
      ch    uint32
      send  chan Input              // UI → pump, buffered 128
      feed  chan []byte             // demux → pump, buffered 256
      frame atomic.Pointer[PaneFrame]
      paint chan struct{}           // 1-buffered, coalesced repaint signal
      pump  pumpState               // everything below is pump-goroutine-only
   }
   func (p *Pane) Frame() *PaneFrame      { return p.frame.Load() }
   func (p *Pane) PaintChan() <-chan struct{} { return p.paint }
   func (p *Pane) SendInput(in Input) { not implemented } // non-blocking; drop on full
   func (p *Pane) NeedSize(cols, rows int) { not implemented } // → Input{Kind: Size}
   func (p *Pane) Close() { not implemented }  // CLOSE frame, cancel pump, free handles
 ```
 Pump loop (the heart of the design — every ghostty and hcwrite touch happens here):
 ```go
   func (p *Pane) run(ctx context.Context, conn *hcwire.Conn) {
      for {
         select {
         case <-ctx.Done():
            return
         case b := <-p.feed: // program output from hc
            p.pump.term.VTWrite(b)
            // WritePty effect fired inline above (same goroutine) →
            // p.pump.replies accumulated in order.
            dirty := p.pump.term.Update(p.pump.state) // exclusive window ends here
            if dirty != RenderStateDirtyFalse {
               p.publish() // buildPaneFrame → atomic store → signal p.paint
            }
            conn.Send(Credit(p.ch, len(b))) // keep the credit window honest
            p.flushReplies() // AUTOMATIC_INPUT if confirmed, else drop
         case in := <-p.send:
            p.handleInput(in) // see below
         case <-p.pump.sizeTick.C: // 50 ms coalescing
            if p.pump.pending != p.pump.lastSent && p.pump.phase == PhaseLive {
               conn.Send(Resize(p.ch, p.pump.pending))       // u16 LE cols, rows
               p.pump.term.Resize(x, y)                       // verified API (doc.go)
               p.pump.lastSent = p.pump.pending
            }
         }
      }
   }
   func (p *Pane) handleInput(in Input) {
      switch in.Kind {
      case InputKey:
         p.pump.keyEnc.SetFromTerminal(p.pump.term) // modes → kitty flags etc.
         b, err := p.pump.keyEnc.Encode(...)        // KeyEvent built pump-side
         conn.Send(Data(p.ch, b))                   // legacy or kitty, encoder's call
      case InputMouse:
         p.pump.mouseEnc.SetFromTerminal(p.pump.term) // tracking mode sync (SGR vs X10...)
         b, err := p.pump.mouseEnc.Encode(...)
         if len(b) > 0 { conn.Send(Data(p.ch, b)) }
      case InputSize:
         p.pump.pending = in.Size // sent by sizeTick, coalesced
      case InputFocus:
         conn.Send(Focus(p.ch, in.Focus)) // 1 gained / 0 lost
      }
   }
 ```

 Two properties fall out for free. First, ordering: the emulator's answers to program queries (DA, ENQ, XTGETTCAP, size reports) are produced by the WritePty effect during VTWrite — synchronously, on
 the pump — and flushed as AUTOMATIC_INPUT immediately after, so query/reply order can never interleave wrongly. Second, negotiation: hello requests automatic_input in features; the attach reply's
 confirmation sets p.pump.auto; until it is true, flushReplies drops (an old core would ignore frame kind 6 anyway, but loom never sends it — belt and suspenders, one field, no sync flags).

 Flow control and desync. The pump is never slow (VTWrite+Update is µs), so feed (buffered 256) practically never fills and the demux never blocks on a pane — honoring hc's guarantee that a stalled
 stream never blocks channel 0. The safety valve: if feed is somehow full, the demux drops the payload and does not credit it; hc then falls >4 MiB behind and sends a fresh repaint; the pump, seeing
 it set a desync note on drop, emits EvDesync and the reducer takes the ActDetachAndAttach path (a fresh attach is a byte-stream-aligned first paint — no partial-sequence repair logic exists
 anywhere).

 ### Resize plumbing

 Rule: the last active viewer sets the size. Loom becomes the last active viewer by attaching, so on attach it sends RESIZE(cols, rows) (attach params) and thereafter its current rect is
 authoritative. The flow: bloom composites the tree and records each node's rectangle on the returned tea.View.Frame → SyncResize reads the pane node's content rect (inside borders), and if it
 differs from the last sent size posts Pane.NeedSize(cols, rows) → the pump's 50 ms tick coalesces bursts (border drags relayout every frame) → one RESIZE frame per settled layout, plus term.Resize
 so the local emulator stays size-consistent with the pty. When the pane is not in PhaseLive the pump refuses the send (reducer-derived check); on re-attach the size is re-sent, which is exactly the
 last-active-viewer contract. Rects clamp to ≥ 2×2 before encoding (u16, and terminals degenerate below that).

 ### Input routing summary (one table, one place)

 ┌─────────────────────┬──────────────────────────────────────┬───────────────────────────────────────┬─────────────────┐
 │ UI event            │ lane                                 │ pump action                           │ hc frame
 ├─────────────────────┼──────────────────────────────────────┼───────────────────────────────────────┼─────────────────┤
 │ tea.KeyMsg          │ ui.ToPaneKey → hc.KeyEvent value     │ KeyEncoder.SetFromTerminal + Encode   │ DATA
 ├─────────────────────┼──────────────────────────────────────┼───────────────────────────────────────┼─────────────────┤
 │ tea.MouseMsg        │ ui.ToPaneMouse (abs → pane-relative) │ MouseEncoder.SetFromTerminal + Encode │ DATA
 ├─────────────────────┼──────────────────────────────────────┼───────────────────────────────────────┼─────────────────┤
 │ program query bytes │ WritePty effect during VTWrite       │ flush after Update, if auto confirmed │ AUTOMATIC_INPUT
 ├─────────────────────┼──────────────────────────────────────┼───────────────────────────────────────┼─────────────────┤
 │ layout change       │ SyncResize → NeedSize                │ pending size, 50 ms tick              │ RESIZE
 ├─────────────────────┼──────────────────────────────────────┼───────────────────────────────────────┼─────────────────┤
 │ focus change        │ SendInput{Focus}                     │ —                                     │ FOCUS
 └─────────────────────┴──────────────────────────────────────┴───────────────────────────────────────┴─────────────────┘

 Test seams (one obvious check per unit)

 - U1 bindings hello — seam: extracted buildPaneFrame + bindings Terminal, no sockets. TestPaneFrameFromANSI: NewTerminal(80×24), VTWrite a known ANSI fixture (colors, wide rune, cursor moves,
   reverse video), Update, buildPaneFrame, assert exact cells at named coordinates, plus Formatter plain-text cross-check. Pins the PaneFrame shape before any socket code exists.
 - U2 hc session — two seams. (a) Pure: recorded notification JSON lines → parse → Event → Registry.Apply → expected registry map, including the missed/watermark drop rule (table-driven, no socket).
   (b) Live: Connect to the real core, Subscribe, assert the libbuild terminal record appears in the snapshot (per PLAN).
 - U3 attach — seam: the pump's feed channel, frames injected directly (bypassing the socket), plus a real attach end-to-end. Pump consumes a fixture first-paint, Update, publish; assert Frame() cell
   grid matches. Reducer table test lives here too: feed the transition table's lost→restarted→attached sequences to Step. Also pins the term.Resize API assumption.
 - U4 input — seam: a fake core listener (a net.Listener on a temp socket speaking hcwire frames) with a scripted attach reply carrying automatic_input: true. Assert: key inputs produce DATA frames
   with the expected legacy sequences; query bytes in the stream produce AUTOMATIC_INPUT frames with the right replies; mouse-tracking mode bytes followed by mouse inputs produce SGR sequences. Live
   variant per PLAN: real core, child shell, post echo hi\r, screen shows hi — the end-to-end proof.
 - U5 bloom frame — two seams. (a) SizeSink interface: SyncResize calls Pane.NeedSize through a one-method interface, so a spy records RESIZE payloads when a tree is rendered at two sizes — pure, no
   surface. (b) Real surface per PLAN: run loom in an hc pane running vim, capture, assert vim's UI renders; resize the pane, assert vim redraws (proves RESIZE + term.Resize + repaint loop). This
   unit also resolves the bloom cell-path assumption.

 Rationale

 ### Shape — load-bearing decisions

 - One pump goroutine per pane owns every ghostty handle (single source of truth per invariant, separate-before-serializing-shared-state). The bindings contract ("handles are single-goroutine; Update
   under exclusive access, reads safe after") becomes a property of the architecture rather than a discipline callers must keep: there is exactly one goroutine that can touch the Terminal, and
   buildPaneFrame runs on it immediately after Update — inside the contract's safe window, not merely adjacent to it. The UI never touches RenderState, so the contract cannot be violated by a future
   UI change.
 - The boundary is one immutable value, published atomically (encode-lessons-in-structure). atomic.Pointer[PaneFrame] + a coalesced signal means readers can never see a torn frame and can never block
   the pump. Immutability is enforced by the type being plain copied values only (the bindings guarantee retention safety for Cell-like values after Update).
 - One event alphabet for the reducer, including loom's own outcomes (no hand-synced state). EvAttached, EvChannelClosed, EvDesync, EvSnapshot are synthesized into Event so Step has a single input
   kind and the transition table is exhaustive by construction. No phase is ever represented twice — attach-in-flight is PhaseAttaching, not attaching bool.
 - Domain structs cross the ui→pane channel, not ghostty handles (boundary-discipline). KeyEvent/MouseEvent are plain values; encoders are constructed and synced pump-side, satisfying the encoders'
   own "caller must serialize access" notes trivially.
 - Wire parsing dies in hcwire (make internals unreachable). ui imports hc, never hcwire; the compiler enforces the constraint. hc exports domain types only; nothing about [u32][u8][u32] framing or
   JSON-RPC is nameable outside hcwire.
 - Interface depth, judged: the public surface is nine methods and five types, behind which hides the plain→framed handshake, credit accounting, demultiplexing, snapshot resync, effect routing,
   encoder mode sync, and coalescing. The caller's total knowledge is: connect, list, attach, subscribe, read frame, send input, close. That is the trade the spike exists to validate — and the parts
   most likely to be wrong (bloom cell path, resize API, status vocabulary) are each pinned by a named unit test seam before they can spread.

 ### Tradeoffs accepted

 - We accept a fresh Cells allocation per publish (~150 KB at 200×60, only when dirty) in exchange for lock-free reads and zero torn-frame risk; a pump-side arena or double buffer is a
   U6-benchmark-driven change, not a design change.
 - We accept one channel hop of input latency (UI → pump) in exchange for strict single-goroutine handle ownership; no locks exist anywhere in the design.
 - We accept detach+reattach as the desync repair, in exchange for having no partial-stream resynchronization logic at all.
 - We accept that agent status comes only from hc's status events (one hop of possible lag vs the local ProgramStatus effect) in exchange for one source of truth that works identically for attached
   and unattached panes.
 - We accept no auto-retry on EvAttachFailed in M1 (user action retries) to keep the reducer free of timers.
 - We accept dropping scrollback history from the first paint on the floor in M1; the viewport-only PaneFrame is why this is cheap to add later.
 - We accept single-pane scope; the registry and reducer are pane-count-neutral, so M2 splits and tabs need no redesign — only more pane instances.

 ### Alternatives considered

 1. UI reads RenderState directly under the bindings contract (shared read window, handshake per frame). Lost: it spreads the concurrency contract into the ui package — every bloom frame needs a
    "safe to read?" negotiation with the pump, and a future contributor reading only model.go can't see why. This shape hides strictly less: the pump must exist anyway; the only question is whether
    the contract leaks past it.
 2. Styled-strings boundary ([]styled lines published per paint, node renders strings). Lost: bloom re-parses strings every frame; at 120 fps against a pane publishing at content rate, the parse cost
    is paid where it can't be coalesced, and cell fidelity (wide chars, cursor styling, reverse video on the cursor cell) degrades into ad-hoc string surgery. It is kept only as the defined fallback,
    not the design.
 3. Everything on the tea goroutine (model.Update performs VTWrite via queued bytes; tea owns all handles). Lost: rendering blocks on socket I/O, or — if the demux stays separate — every DATA frame
    becomes a tea.Msg, flooding the message loop at output rates terminals easily sustain. The coalescing pump decouples output rate from fps with one channel.

 ### Open questions and risks

 - bloom cell draw path: is canvas.Surface/Drawable real, stable enough, and cell-oriented as the docs index suggests? U5 resolves; the styled-lines fallback is specified, so this is a cost risk, not
   a design risk.
 - Status vocabulary: what exact values does OSC 7501 carry (idle/running/waiting/…)? Confirm from the schema README at U2; AgentStatus constants are placeholders until then.
 - Socket discovery: what is hc.DefaultSocket() — env var, well-known path, both? Confirm at U2.
 - Attach reply fields: confirm the automatic_input confirmation field name and whether attach params carry initial size (making the explicit post-attach RESIZE redundant-but-harmless). U2/U4.
 - term.Resize vs repaint latency: after a local resize, the program's repaint arrives as deltas; if some programs need a nudge, does hc forward a SIGWINCH-equivalent promptly? Watch this in U5's vim
   check.

 ### Next implementation step

 Write the U1 test and buildPaneFrame against a hardcoded PaneFrame — pinning cell extraction from the bindings' row iterators before any socket code, since that is the cheapest place to discover
 whether the boundary type's shape survives contact with real cell data.

 ### Synthesis decision

 Pending — filled by arena synthesis.
