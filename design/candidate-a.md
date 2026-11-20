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

 Let me settle the event set: session.Msg variants (implement tea.Msg via marker):
 1. Snapshot{Seq uint64, Terminals []TerminalInfo} (subscribe reply)
 2. TerminalChanged{ID, Change} where Change = enum {Created, Lost, Restarted, Finished, Removed, Meta} + Info TerminalInfo (current cached record; Info.ExitCode for Finished... TerminalInfo carries
    Status/Title/Labels/Size/Exists?)
 3. Resynced{Seq, Terminals} (missed recovery)
 4. StreamData{ID TerminalID, Data []byte}
 5. StreamClosed{ID, Reason CloseReason} where CloseReason = domain (Exited{Code} | Detached | Error(msg))
 6. SessionDead{Err error}
 7. Heartbeat? — heartbeat is keepalive; session uses it internally (timeout detection); don't surface. Deletion!

 TerminalInfo domain record: {ID, Name?, Title, Status (AgentStatus enum?), Labels, Cols, Rows, Running bool, ExitCode *int} — smallest for badges+list: ID, Title, Status, Size, Finished/Exit. Status
 from OSC 7501 → domain enum with unknown passthrough.

 pane package:
 - PaneSet — map + focus + Apply(msg-ish) — wait Apply takes session msgs? pane package shouldn't import session? It consumes domain events; if session.Event is the domain type, pane imports session
   (fine — ui imports both; hcwire imported only by session). Direction: session → hcwire; pane → session (domain types) + libghostty; ui → pane + session + bloom. acyclic. ✓
 - Pane — state machine state + Terminal + RenderState + encoders + Screen cache + Channel id + negotiated flags + lastSent size/focus.
 - State enum: Idle, Attaching, Attached, Exited, Lost, Gone. Transition fn advance(st, Change/trigger) (State, actions).
 - Screen, Cell, Cursor — boundary.
 - Actions out of transitions: wantAttach, wantDetach, resetEmulator — machine returns effects list; PaneSet executes (owner goroutine).

 ui package:
 - PaneNode — bloom node drawing Pane.Screen; consumes *Screen + Generation; implements bloom.Drawable/canvas draw; fallback styled lines.
 - model lives in cmd/loom (thin shell per boundary-discipline: business logic pure, shell thin). Actually put App model in ui package? cmd/loom/model.go as the thin shell — plan says main model
   wiring is integration; keep in cmd.

 Hmm, "loom package" in the task ("README-style usage of the loom package") — maybe root package loom exposing Dial/Attach/PaneSet/Screen, with internal hcwire. Flat root package = friendlier usage;
 subpackages optional. Let me structure: root loom = session + domain types + PaneSet + Screen (the boundary), internal/hcwire = wire, loom/ui = bloom nodes + encoders glue. Hmm — root doing both
 session and emulator ownership is big but for spike OK? Module map bias-to-deletion: 2 packages (root + ui) + internal wire. I think that's genuinely smaller and the usage reads loom.Dial. But
 single-goroutine ownership + state machine deserve a clean seam... root package files: session.go, events.go, pane.go, lifecycle.go, screen.go. internal/hcwire. ui/. That's my map: root loom,
 loom/ui, internal/hcwire. The rule "hcwire imported only by root" is checkable by grep.

 Wait — does loom/ui need pane internals (encoders)? Input routing: ui gets KeyMsg → needs encoded bytes. Encode needs KeyEncoder synced from terminal state (pane-owned). So root exposes
 Pane.EncodeKey(tea.KeyMsg) ([]byte, error) and Pane.EncodeMouse(tea.MouseMsg, rel position). But root importing bubbletea for KeyMsg types — root imports bubbletea anyway (Listen returns tea msgs?
 or custom?). Hmm: bubbletea in root? The dep is allowed. But keep root UI-agnostic: encoding accepts small structs (loom.Key{...})? Then ui converts tea.KeyMsg → loom.Key — conversion boilerplate
 (tea v2 Key type is already a nice enum). Bias to deletion: root may import bubbletea/v2 for key/mouse msg types only — pragmatic, flag it. Alternatively put Encode* on ui wrapper... no, encoders
 sync from terminal state on owner goroutine; must live with Pane. Root imports bubbletea types. Accept + note.

 Hmm wait, actually — maybe cleaner: loom.Listen, loom.Attach return tea.Cmd / process tea.Msg → root is a bubbletea extension library. Yes, own it: root = session + pane + tea glue. ui = bloom nodes
 + model shell? Model shell in cmd/loom. OK.

 Usage call sites (write before types):

 1. main.go: dial, hello, attach known id, run program (the required one).
 2. A picker variant: List + Subscribe feed, sidebar lists TerminalInfo, click to attach (shows PaneSet lifecycle consumption).
 3. Test call site: DialConn(pipe) loopback driving U4.

 Now the RESIZE-last-active-viewer rule concrete: bloom Frame msg gives node rect; model: if pane.Focused() && rect != pane.SentSize { SendResize }; on focus gained event: SendFocus(1) + if rect !=
 SentSize send RESIZE (active viewer imposes its size). On blur: SendFocus(0), no resize. Core-side: last active viewer wins — consistent. Also initial attach: attach reply → send FACTS + RESIZE +
 FOCUS(1) once node geometry known (first Frame after attach). Order: RESIZE then FOCUS? Grounding doesn't dictate; send FOCUS 1 then RESIZE (active first, then size). Flag.

 Debounce: layout drag produces many rects per second; tea is 120fps; send RESIZE at most once per frame per pane — coalesce in Update: model stores pendingSize; on Frame msg, compute rect; if !=
 lastSent, send. No timer needed. Good — idempotent-ish.

 FACTS: minimal loom.DisplayFacts{AppName, Cols, Rows}? hc schema would define; spike sends {"name":"loom","cols":c,"rows":r} on attach + resize? "FACTS as they change" — display facts = viewer's
 display characteristics; M1: send once on attach; flag schema lookup via hc schema. Keep type Facts with TODO.

 ** hello.features**: automatic_input — negotiated: Hello(features) → reply may confirm feature set; attach reply confirms automatic_input per grounding ("requires automatic_input in hello.features
 and confirmation on the attach reply"). So Attach returns AutoInputOK bool. If false: Pane drops WritePty effects (counter).

 CSI 9001 win32-input-mode: first paint may include the flag — affects whether keys should be encoded in win32-input-mode. libghostty KeyEncoder capabilities: unknown support; flag as risk/open
 question; M1: if flag present and encoder lacks mode, note degradation (kitty/legacy still works since core relays? win32-input-mode is a program-facing mode the core sets on pty; viewer must emit
 matching sequences... risk: out of scope, log if seen). List in open questions.

 Credit: after VTWrite of StreamData bytes (in Update), enqueue CREDIT(len). Reader counts uncredited per channel and pauses forwarding DATA beyond threshold (e.g., 1MiB) to bound memory — wait, if
 reader pauses, frames sit in socket buffer; core sees slow credit; fresh-repaint path kicks in >4MiB. Spike: credit-after-consume, no reader pause (tea channel unbounded) — risk note: pathological
 output flood grows memory until fresh-repaint. Actually fresh-repaint is core-side when viewer is 4MiB behind (credit not returned). If we never credit, core stops sending after credit exhaustion
 (256KiB + whatever) — no! "A viewer more than 4 MiB behind gets a fresh repaint instead of the backlog" — behind measured in uncredited bytes. We consume promptly (tea loop fast) so credit flows.
 OK: credit-after-consume in Update; simplest correct.

 CLOSE from core on ch0: reason JSON — session emits SessionDead. CLOSE on stream channel: StreamClosed{Reason}.

 restarted while attached: machine: Attached + Restarted → drop channel (old stream closes? core may close it — StreamClosed arrives → idempotent), transition Attaching (effects: reset Terminal via
 NewTerminal fresh? or Snapshot restore? fresh Terminal simplest), issue Attach again. Note: the machine effect list: {Reattach}. Also Lost: stream may still be alive? lost = core lost track (child
 crash?) — machine: Attached+Lost → Lost state, keep stream? Grounding doesn't specify; spike: Lost → detach (CLOSE) and wait for restarted/removed/finished. Flag.

 Let me define the transition table (pure, total):

 ```
   Idle:      Created→Idle(meta)   Restarted→Idle    Lost→Lost? hmm
 ```

 Wait — events arrive for all terminals; panes exist for terminals we know (from snapshot/list) even unattached. Attach is a user action (ui) valid from Idle/Lost/Exited? Attach(id) when Attached =
 no-op (idempotent). Machine states & events:

 States: Idle (known, not attached), Attaching, Attached, Exited (CLOSE reason exited), Gone. Plus Lost with transition back to Attaching on restart? Earlier I had Lost→Attaching on Restarted; keep
 consistent:

 - attach() from Idle/Exited/Lost → Attaching (effect: RPC attach)

 - attach() from Attaching/Attached → no-op

 - firstPaint (StreamData first after attach reply? or explicit reply) from Attaching → Attached. Actually reply-arrival = Attaching-with-channel; first paint flips to Attached? Grounding: core sends
   first paint right after attach — treat first StreamData as paint → Attached (effect: none). Simpler: reply → Attached (channel known); first paint is just data. But Screen may need size setup
   before paint... size comes from our RESIZE. OK reply→Attached.

 - StreamClosed{Exited{code}} from Attached/Attaching/Idle → Exited

 - StreamClosed{other} → Idle (detached/error; user can reattach)

 - Lost from Idle/Attached → Lost (effect if attached: detach stream)

 - Restarted from Lost → Idle? or auto-reattach? If user had it attached before loss, auto-reattach restores content: effect Reattach if previously attached (track wantAttach inside state: Lost
   variants?). To avoid booleans: states LostAttached/LostIdle? Ugh. Alternative: Restarted → Lost→Attaching when the pane was attached pre-loss. Encode as two states: Lost (was idle) and LostLive?
   Naming: Detached vs... Let me think: the invariant "user wants this pane attached" IS state, not a boolean-to-sync — it's part of the machine. States: Idle, Attaching, Attached, Exited,
   Lost{wantAttach bool}? that's a boolean but machine-owned single-writer... The task says "No booleans that must stay in sync" — a single state field bool inside the state struct owned by the
   machine is fine? Prefer explicit states: LostIdle, LostAttached → Restarted: LostAttached→Attaching, LostIdle→Idle. Total 6 states. Clean, no bools.

 - Restarted from Attached → Attaching (re-first-paint; effect: fresh Terminal + reattach)

 - Restarted from Idle/Attaching → Attaching? if Attaching, restart likely invalidates in-flight attach — effect: cancel+reattach → Attaching (idempotent)

 - Finished from any(≠Gone) → Gone (record exit code; effect: detach if attached)

 - Removed from any → Gone

 - Created from Gone → Idle (fresh incarnation! reset emulator state) — hmm, does hc reuse ids? created after removed for same id — plausible (new terminal same id? probably new id). Keep transition
   anyway (total function).

 - Meta (status/title/labels/size/turn) from Idle/Attached/Lost*/Exited → same state, update meta. From Attaching → same.

 - keep → no-op everywhere (or confirms liveness).

 Event StreamData from Attached → VTWrite+refresh+credit. From Attaching → first paint → Attached. From Idle (shouldn't happen; stale frames post-detach) → drop.

 Also Snapshot/Resynced reconcile at PaneSet level: for each pane: id in snapshot? keep/derive (Lost→? snapshot is truth: absent → Removed-transition, present → keep); ids not in set +
 Created-equivalent → add Idle panes. Keep reconcile small.

 This machine = pure function advance(state State, ev Change) (State, []Effect) with exhaustive switch — testable table.

 Goroutine/locking recap (must appear in rationale): all libghostty handles on tea goroutine (Update); reader/writer goroutines own socket only; handoff via tea mailbox (ordered) and futures for RPC;
 no locks except reply-future map + writer queue internals.

 Wait, one problem: Listen recurring-cmd pattern — does bubbletea v2 support a Cmd returning (msg, cmd) pairs? The classic: func listen(sess) tea.Cmd { return func() tea.Msg { m := <-sess.feed;
 return listenBatch{m, listen(sess)} } } — v2 has tea.Batch and cmd returning next cmd? In bubbletea v1, Cmd returns Msg only; long-lived feeds use p.Send from goroutine. v2 —
 charm.land/bubbletea/v2: I believe same Cmd model (func() Msg). So the feed must be a goroutine calling Program.Send. That means loom needs the *tea.Program or a Sender interface at Listen time:
 sess.PumpInto(sender) where type Sender interface{ Send(tea.Msg) }... or main.go does go func(){ for m := range sess.Feed() { p.Send(m) } }(). Usage sketch: after p.Run()? Program created, then go
 loom.Pump(sess, p). Hmm — but ordering vs Init cmds? Fine (mailbox). But "Send from goroutine" is idiomatic bubbletea. Adjust usage:

 ```go
   p := tea.NewProgram(app)
   go loom.Pump(sess, p)      // session feed → program mailbox, ordered
   snap := ... subscribe inside pump?
   p.Run()
 ```

 Where does Subscribe happen? In loom.Pump: subscribe first, send Snapshot, then pump events. Or app.Init returns a Cmd doing subscribe... Cmd-based keeps program-managed. Eh — simplest usage:
 loom.Pump(ctx, sess, p) does hello? No — hello is dial-time. Pump = subscribe + forward. And attach in Init via Cmd. Fine. Actually even simpler for usage: loom.Program(sess, model, opts) wrapping
 tea.NewProgram + pump goroutine? That hides bubbletea options... spike: keep Pump helper, explicit. OK.

 Hold on — ordering issue: Pump goroutine sends Snapshot msg; meanwhile Init cmd runs Attach cmd; Attach reply is a future resolved by reader; Attach cmd returns AttachedMsg... all through mailbox,
 order: reader fulfills future only after attach RPC done; Snapshot sent at subscribe time (before attach RPC enqueued? if Pump subscribes concurrently with Init's attach cmd — race: subscribe RPC
 and attach RPC both enqueued, order in queue = order of enqueue; attach cmd goroutine vs pump goroutine. Deterministic enough for spike; machine handles any order (attach reply while unattached →
 machine from Idle gets reply? reply matches pending attach keyed by pane state Attaching...). To keep M1 usage deterministic: do subscribe synchronously before NewProgram in main (blocking, ~ms on
 local socket), then pump only events. sess, err := loom.Dial(...); sess.Hello(...); snap, _ := sess.Subscribe(ctx) all synchronous in main; then go loom.Pump(sess, p) for events only; attach in
 Init. Serial setup in main = spec-like. But PaneSet then seeds from snap — pass snap to model constructor. Clean usage:

 ```go
   func main() {
     sess := loom.Dial(...)          // or DialConn for tests
     sess.Hello(ctx, loom.Features{AutomaticInput: true})
     snap, _ := sess.Subscribe(ctx)  // framed switched inside Hello
     panes := loom.NewPaneSet(snap)
     pane := panes.Open("libbuild")  // state machine: Idle→Attaching, attach issued in Init cmd?
     ...
   }
 ```

 Hmm, Open issuing RPC needs ctx/async. panes.Open(id) marks want+returns cmd? Let the model's Init return pane.AttachCmd(sess). Usage:

 ```go
   app := ui.New(sess, panes, paneID)
   p := tea.NewProgram(app, tea.WithAltScreen())
   go loom.Pump(sess, p)
   p.Run()
 ```

 and app.Init() → pane.AttachCmd(sess); loom.AttachDone{...} msg etc. Good.

 Now the model Update in cmd/loom: processes: loom msgs (Snapshot? no—pre-subscribed; TerminalChanged, Resynced, StreamData, StreamClosed, SessionDead, AttachDone), tea keys/mouse, bloom Frame. The
 PaneSet.Apply methods do the work; model stays thin shell (boundary discipline).

 bloom node: what does bloom's node interface look like? Unknown exact (View builds tree of nodes: Row, Column, Text, Box, Viewport...). Custom node with draw hook = presumably implementing an
 interface (Node with Draw?) — the "canvas.Surface, Drawable" hints type Drawable interface{ Draw(dst *canvas.Surface, rect Rect) }. Sketch: PaneNode implements bloom's content-node interface — name
 assumed bloom.Drawable; flag assumption + fallback: wrap cached styled lines in bloom.Box/Viewport. Per-frame: node.Draw gets dst surface + its rect; blits Screen.Cells (crop to rect); cost
 O(visible cells), ~µs; Generation guard avoids re-render of styled fallback.

 Also list sidebar: Text nodes from TerminalInfo (title/status badge) — cheap strings, fine per bloom docs.

 Mouse → cell coords: bloom Frame gives node rect; mouse msg coords − rect.origin = cell. Pass to Pane.EncodeMouse(tea.MouseMsg, x, y). MouseEncoder set from terminal state (tracking modes) after
 refresh. Buttons wheel etc. handled by encoder. If tracking off (program doesn't want mouse), encode nothing (encoder returns empty?) — UI then does selection? Out of scope M1; note.

 Focus scopes: bloom on.Down/focus scopes — click-to-focus sets model's focused pane; model sends FOCUS frames on change. Bloom-side focus integration sketched minimally.

 Module map:

 ```
   loom/
     go.mod                  (module loom; deps: bloom, bubbletea/v2, lipgloss/v2 (transitive), libghostty pinned)
     loom.go                 — package doc: goroutine ownership contract
     session.go              — Dial, DialConn, Hello/framed switch, Features, RPC futures, send queue
     events.go               — Subscribe, seq guard, Pump, domain events, TerminalInfo normalization
     stream.go               — channel demux table, credit accounting, CloseReason
     pane.go                 — Pane struct, Open/Apply, refresh, encoders, Screen cache
     lifecycle.go            — State enum, Change enum, advance() pure fn, Effect list
     screen.go               — Screen/Cell/Cursor + buildScreen (bytes→cells)
     internal/hcwire/
       frame.go              — frame codec [ch u32][kind u8][len u32][payload], kinds, 16MiB guard
       jsonrpc.go            — line/envelope codec for ch0
     ui/
       panenode.go           — bloom node drawing Screen (cell path; styled fallback)
       sidebar.go            — terminal list + status badges (Text nodes)
     cmd/loom/main.go        — thin shell: dial/hello/subscribe/panes/program/pump
 ```

 Dependency direction: cmd → ui → loom(root) → hcwire/internal + libghostty + bubbletea types + bloom? Does root import bloom? Screen.Cell style type — if aliasing bloom cell types, root imports
 bloom. Prefer root NOT importing bloom (boundary pure): define CellStyle minimally (rune + fg/bg Color + attrs bitmask); ui converts to bloom style at blit (or bloom's Drawable takes generic cells).
 Hmm — if bloom's canvas.Surface has its own cell struct, ui's PaneNode converts Screen.Cell→bloom cell per blit (cheap field copies) or Screen aliases... Keep root bloom-free: screen.go owns
 Cell/CellStyle with plain color type (libghostty may define Color — use its color type? that's bindings, allowed dep). CellStyle: FG, BG Color; Attrs uint8 using libghostty color if exposed, else
 basic 8-bit/RGB. Flag.

 So deps per package: root → bubbletea (key/mouse types only) + libghostty + stdlib; ui → root + bloom + bubbletea + lipgloss; hcwire → stdlib only.

 Effects/hooks from libghostty: WritePty → auto-input (negotiated); TitleChanged → pane meta (title) — wait, title also comes from hc title event (core observes OSC?). Both? hc forwards title events
 from its side; the attached stream gives us raw program output → our emulator ALSO sees OSC title sequences. Double source. Which wins? hc events stream is the multiplexed truth for the list UI; for
 the attached pane title... same value. Rule: hc title event (single source; ignore TitleChanged effect in M1? or use it for instant local update). Bias to deletion: M1 uses hc events only;
 TitleChanged ignored (note). Status: OSC 7501 → hc status event; ProgramStatus effect would also fire — ignore locally (never scrape screens; hc's event IS the OSC report relayed). ✓ consistent with
 "status comes from OSC 7501 reports" via hc.

 Other effects (Bell, Clipboard, notifications): out of M1 — set no-op hooks explicitly (contract visibility).

 RenderHold (synchronized output): bindings may hold rendering; refresh respects it (if RenderHold active, defer refresh until released — the effect callback tells us; minimal: ignore in M1? torn
 frames at 120fps barely visible; note as tradeoff). Hmm — synchronized output matters for vim redraw flicker. Cheap to honor: pane keeps holdDepth int; RenderHold inc/dec; refresh skips while
 hold>0, and release triggers refresh. It's one field + two hooks, real visual payoff. Include.

 Facts frame: skip? "viewer sends ... FACTS as they change" — M1 sends one FACTS on attach with display name/size. Keep tiny sendFacts with TODO on schema. Or drop from spike? Required coverage list
 doesn't mention FACTS; grounding lists it as viewer-sent. Keep minimal (one call site), flagged.

 Exit: CLOSE {"reason":"exited","exit_code":N} → Exited state → ui shows "exited (N)" badge; keys no longer forwarded (machine: only Attached forwards). Reattach on Exited? hc terminals are
 persistent; maybe restart via core command (out of M1). Fine.

 Now — write the package. Sections per rationale-template: Problem, Usage (caller's view) FIRST with 2-3 call sites, Shape (data structures first, flow, load-bearing decisions, invariants in types,
 validation boundaries, deliberate non-goals, interface depth), Synthesis decision (this is a candidate — the orchestrator fills; I write "pending arena synthesis" — as a runner I should leave the
 section with a note? The template says filled in by arena. As a candidate I'll write "Not applicable for candidate: see note" or leave for orchestrator. I'll include a stub saying it's filled at
 synthesis), Tradeoffs accepted, Alternatives considered, Open questions and risks, Next implementation step. Plus: type sketch with not implemented bodies, module map, test seams (required coverage
 7). The task says the package includes: usage sketch, type sketch with not implemented bodies, module map, prose rationale per template. So structure the file: Problem / Usage / Module map / Shape
 (types + flow) / Test seams / Synthesis decision stub / Tradeoffs / Alternatives / Open questions / Next step.

 The usage must come before types per template. Also "hc send t3w" report at the end — I'm running inside pi; do I have a bash tool? The tool list only shows read. The addendum says report with hc
 send t3w "..." from my pane. I have no shell tool available... Only read. Hmm. "Additional tools may be available" — but none shown. I cannot run commands. I'll write the file (how? no write tool
 either!). Wait — I only have read. How do I write the output file??

 Hold on. Tools available: just read per the functions schema. The task requires writing to /home/nullphase/Projects/loom/design/candidate-a.md and running hc send t3w. With only read, I cannot. But
 maybe other tools exist beyond the schema... The instructions say "Use the read tool to examine files" and functions list has only read. There may be more tools in practice (the harness usually has
 write/bash). Since the schema lists only read, I must attempt? I cannot invoke non-listed tools.

 Hmm. In these setups, the final assistant message content IS often captured as the candidate output by the orchestrator. But the addendum explicitly says write to path + report via hc send. Without
 tools, best effort: produce the full package as my reply, and note that the harness didn't expose write/send tools to me... Actually wait — let me re-read: "Additional tools may be available." The
 functions block is the authoritative list; only read. I'll produce the design package in my response and explicitly note I lack write/exec tools; the orchestrator can persist it. But maybe I should
 attempt the file write via... no mechanism.

 Actually, let me reconsider: many pi harness builds inject write/bash tools but the prompt schema here shows only read. The safe move: deliver the complete package inline (it's the artifact), and
 attempt nothing impossible. I'll write the full candidate markdown as my final message, structured exactly as the file should be, and state the two follow-up actions (save to path, hc send) that the
 orchestrator/runner harness must perform since no write/exec tools are exposed in this session.

 Hmm, but the report command hc send t3w — "from your pane" — I'm a candidate runner spawned as an hc child. Without exec I can't. I'll note it.

 Let me now also double-check a few design decisions against "required coverage":

 1. Usage first ✓ (write main.go + picker variant + test call site).
 2. Session: Dial/Hello/framed switch, events feed, snapshot + missed resync ✓ (seq guard in reader; Resynced reconcile in PaneSet).
 3. Pane lifecycle state machine ✓ (State enum, advance() pure, exhaustive, no sync booleans).
 4. Pane-to-bloom boundary: one type = Screen; node consumes *Screen per frame; cheap via cell blit + Generation; cell path assumption flagged ✓.
 5. Input routing ✓ keys→DATA via KeyEncoder; WritePty→AUTOMATIC_INPUT post-negotiation (drop otherwise); mouse→MouseEncoder w/ SetFromTerminal sync.
 6. Resize: Frame rects → RESIZE on change; focused-only; focus-gained reasserts (last-active-viewer rule) ✓.
 7. Test seams ✓ U1 buildScreen table test; U2 hcwire codec roundtrip + live hc env-gated + lifecycle table test; U3 canned-deltas unit + live attach screen-compare; U4 DialConn loopback
    typed-command echo + wire capture; U5 golden node draw + real-surface vim capture (env-gated).

 Constraints: deps ✓ (bloom, bubbletea v2, libghostty pinned, stdlib; lipgloss arrives via bloom transitively — mention), no wire leak ✓ (hcwire internal, imported only by root session files; ui
 never), bindings contract ✓ (tea goroutine sole handle owner; RenderState.Update then reads), OSC 7501 via hc events not screen scraping ✓, smallest type set — count: session (Session, Features,
 TerminalInfo, AgentStatus?, Change, msgs ~6, CloseReason, Facts) + pane (PaneSet, Pane, State, Effect, Screen, Cell, CellStyle, Cursor) + ui (PaneNode). Trim: AgentStatus as string passthrough? OSC
 7501 status — domain enum with Unknown string. Keep Status string (opaque badge text) for spike — deletion! TerminalInfo: {ID, Title, Status string, Cols, Rows, Exited bool, ExitCode int}? "Exited
 bool + ExitCode" — a bool+int pair... ExitCode *int single field (nil = running) — better, single source. Labels: skip in M1? badges want status; labels skip. Title from events. Size for... nothing
 in M1 badge? skip size in Info? RESIZE rule uses our rect not core size. Skip Cols/Rows in Info. Keep Info minimal: ID, Title, Status, ExitCode *int. Hmm "smallest type set that proves the spike" —
 yes.

 Also do we need Turn? no. Foreground event? foreground = which terminal is foreground in core? Not needed M1 (our focus is local). Note as ignored.

 Facts: keep as unexported helper, no public type? sendFacts(ch, cols, rows) marshals inline map — no type. Deletion ✓ but "define domain types at boundary" — one-liner with TODO is fine.

 Effect enum for machine: reattach, detach, reset — three.

 PaneSet responsibilities: seed from Snapshot; Apply(TerminalChanged) → route to pane (create if unknown + Created); reconcile(Resynced); focus; Apply(StreamData/StreamClosed) → pane. Actually
 StreamData: PaneSet.Apply routes by id to pane.pumpBytes. Names: keep small API: Seed(snap), Handle(tea.Msg) (cmds?), Focused(), Open(id). Hmm Handle returning commands for effects
 (reattach→AttachCmd). Let me define:

 ```go
   func (ps *PaneSet) Apply(m tea.Msg, sess *Session) tea.Cmd
 ```

 — Apply performs machine transitions + emulator pump and returns a Cmd for async effects (attach RPC). One entry point from Update. Sidebar needs iteration: ps.All() []TerminalInfo (from Info cache)
 and ps.Attached() *Pane.

 One more check — "Session. ... and how snapshot and missed resynchronize state": spelled: Subscribe returns snapshot synchronously (main seeds PaneSet); missed → reader swaps in snapshot seq + emits
 Resynced; PaneSet.reconcile diffs membership (adds Idle panes, marks absent attached pane lost, updates Info cache), drops stale events via seq guard in reader. Core restart: socket close →
 SessionDead → usage exits (v1 local; reconnect later). ✓

 "pane lifecycle ... (created, lost, restarted, finished, status, size, title)" — covered: created/lost/restarted/finished/removed → machine; status/size/title (+turn/labels) → Meta transition
 updating Info; size in Meta ignored (our rect rules) but stored? Info drops size... task lists size as part of vocabulary — machine must handle it (Meta transition, no state change). Info has no
 size field then Meta carries nothing for size — fine: Change enum has MetaSize? No — keep Change ∈ {Created, Lost, Restarted, Finished, Removed, Meta}; size event → Meta (size dropped, or keep
 Cols/Rows in Info after all to show badge "80×24"? sidebar showing size is nice but M1 deletion — I'll keep Cols/Rows out; Meta handles title/status/turn/labels uniformly). Document that size event
 only refreshes Info cache fields we keep (none) — actually then why normalize... Keep Cols, Rows in Info after all? Sidebar showing "vim 120×40" is a plausible M1 nicety and it gives size a home.
 Hmm, bias to deletion vs coverage "size" listed explicitly in required event vocab. Keep Cols/Rows int in Info — two fields, gives the size event a destination. OK.

 Now write buildScreen signature:

 ```go
   // buildScreen renders the terminal's visible surface into a Screen value.
   // Caller holds exclusive terminal access (see libghostty doc.go).
   func buildScreen(rs *ghostty.RenderState, gen uint64) Screen { not implemented }
 ```

 Implementation notes pseudocode: formatter → styled text → cell parser OR direct cell reads; cursor from rs.

 Pane.refresh:

 ```go
   func (p *Pane) pump(data []byte) {  // tea goroutine only
     p.term.VTWrite(data)              // or p.term.Write
     p.gen++
     if p.holdDepth > 0 { p.dirty = true; return }
     p.render.Update(p.term)
     p.keyEnc.SyncFromTerminal(p.term)   // assumed names; flag
     p.mouseEnc.SetFromTerminal(p.term)
     p.screen = buildScreen(p.render, p.gen)
     p.dirty = false
     // credit
   }
 ```

 Wait — dirty flag + holdDepth = booleans to sync? dirty is a single-writer (tea goroutine) latch; acceptable? "No booleans that must stay in sync" refers to lifecycle; render hold latch is a tiny
 counter — use holdDepth int and drop dirty (refresh unconditionally after pump; hold just defers). Simpler: pump always: VTWrite; if hold>0 skip update (next release does it); else Update+build. No
 dirty flag. ✓

 RenderHold effect semantics: bindings effect callbacks — RenderHold(bool)? Assume begin/end via two callbacks or one with bool; sketch onRenderHold(on bool) adjusting holdDepth. Flag exact
 signature.

 KeyEncoder sync: grounding lists KeyEncoder without SetFromTerminal mention (MouseEncoder has it). Kitty keyboard flags live in terminal state; KeyEncoder likely constructed WithTerminal or has
 method — sketch p.keyEnc.Sync(term) flagged as assumed-name. Fine — sketch marks assumption.

 EncodeKey input type: tea.KeyPressMsg (v2). Route in model: case tea.KeyPressMsg: if p := panes.Attached(); p != nil && p.State()==Attached { sess.SendData(p.Channel(), p.EncodeKey(msg)) }. Also
 cursor/bracketed paste: PasteMsg → bytes → DATA (note). Ctrl combos handled by KeyEncoder.

 Multi-byte writes: SendData enqueues one DATA frame (≤16MiB fine).
                                                                                                                                                                                                       ┃
 Mouse: case tea.MouseClickMsg/MotionMsg... v2 mouse msgs; encode p.EncodeMouse(msg, cellX, cellY) returns bytes or nil (tracking off) → DATA.                                                         ┃
                                                                                                                                                                                                       ┃
 Now the writer queue: Session.SendData/SendResize/SendFocus/SendAutoInput(ch, payload) — enqueue frame, writer goroutine writes. Ordering per channel preserved by single queue. ✅ Also attach RPC   ┃
 uses same queue for its request frame (ch0 DATA) — ordering with subsequent keys on the terminal channel: different channels, no constraint.

 Close: sess.Close() closes channels (CLOSE frames?) — viewer detach = CLOSE frame to core on stream channel. PaneSet detach on machine Lost/Gone effects; app exit: close attached stream then socket.
 sess.Close() does both for all live channels. Idempotent (sync.Once). ✓ (make-operations-idempotent: attach twice no-op; resize sends guarded by lastSent; focus frames guarded; close idempotent.)

 Frame codec unit:

 ```go
   type Frame struct { Channel uint32; Kind Kind; Payload []byte }
   func AppendFrame(dst []byte, f Frame) []byte
   func NextFrame(r *bufio.Reader) (Frame, error)  // 16MiB cap
 ```

 JSON-RPC (ch0): line-delimited requests; replies/notifications — in framed mode are they also line-delimited inside DATA payload? "channel 0 carries JSON-RPC" — payloads are JSON-RPC messages; each
 frame payload = one message (probably). Plain mode: one per line; framed: one per frame. Assume one-per-frame; flag.

 internal/hcwire also holds method param/result structs? Domain conversions in root (events.go maps hc result JSON → TerminalInfo). Param structs like terminal.attach params — where? hcwire
 (wire-shaped) vs root builds maps inline. Deletion: root marshals small structs via anonymous structs or maps with named constants for method names. Keep method names + minimal param structs in
 hcwire? Wire shape lives in hcwire: type AttachParams struct{...}? hmm — those are wire types; fine in hcwire, converted at boundary. But smallest: root defines request bodies as anonymous structs
 at call sites (they're one-liners). hcwire = framing + envelope only. Yes: jsonrpc.go = Request{ID, Method, Params json.RawMessage}, Response, Notification envelope codec. Root builds params inline,
 parses results into domain Info. ✓ boundary honored.

 Test seam for U2 "observes the libbuild terminal record": env-gated integration: LOOM_TEST_HC=1 → dial real socket (hc run --bg provides path; default $XDG_RUNTIME_DIR/hc.sock? unknown — env
 LOOM_SOCK), subscribe, assert libbuild present. Flag socket path discovery as open question (hc schema/`hc ls` knows; spike uses LOOM_SOCK).

 U4 check per PLAN: screen shows echo output of programmatically typed command. Loopback variant: fake core that echoes DATA back — asserts EncodeKey path + Screen shows chars (local echo needs real
 program though — use real hc shell, type echo marker123 via injected KeyPressMsgs, poll Screen until marker appears). Env-gated. Loopback fake covers encoder→frame-bytes assertions deterministically
 (capture frames from fake core side). Both listed.

 U5: golden draw test: PaneNode.Draw into bloom canvas 40x10 from fixed Screen → golden cells (bloom test helpers permitting; else compare via bloom's render-to-string). Real-surface: script vim
 under script/tmux? capture via VTE? PLAN says real-surface capture — env-gated manual-ish check: run loom against hc running vim, assert RESIZE frames seen + screenshot diff by human. Keep as
 documented check.

 Node interface assumption: type Drawable interface { Draw(canvas *bloom.Canvas) }? I'll write: PaneNode implements bloom's cell-level node interface (docs index: canvas.Surface, Drawable — assumed
 bloom.Drawable with a Draw(dst)); fallback FallbackNode builds cached styled lines → bloom.Viewport. Flag both names as to-verify against bloom docs (open question).

 Frame msg type name: bloom "Frame records where every node went" — msg presumably delivered per frame with the Frame (assumed bloom.FrameMsg{Frame bloom.Frame}); lookup: frame.Rect(nodeID) — nodes
 need IDs or the model keeps the node reference. Assume Frame.RectOf(node)/node has ID; flag. Model stores paneNodeID.

 Usage call site #3: test loopback:

 ```go
   func TestTypedCommandEchoes(t *testing.T) {
     core := hcwiretest.EchoCore(t)          // in-memory net.Conn pair speaking frames
     sess := loom.DialConn(core.ClientSide())
     sess.Hello(ctx, loom.Features{AutomaticInput: true})
     ...attach, pump StreamData through PaneSet...
     for _, k := range typeKeys("echo hi\r") { panes.Apply(loom.KeyInjected{...}) }
     // assert captured DATA frames == keyEnc expectations; real-hc variant asserts Screen text
   }
 ```

 Hmm — KeyInjected isn't a thing; the model converts tea keys. Test constructs tea.KeyPressMsg directly and calls the same helper the model uses: loom.ForwardKey(sess, pane, key). Make that a root
 helper so model is one-liner and tests target it. ✓ (usage site 2 shows picker; site 3 test.)

 Right — also who calls refresh for first paint sizing: attach reply gives nothing about size; we RESIZE immediately after attach with rect (or default 80x24 before first Frame). Sequence: AttachDone
 → machine Attached → cmd chain sends FOCUS 1 + RESIZE(rect or default) + FACTS. Then core resizes pty; first paint may arrive before RESIZE processed at 80x24 then deltas at new size — fine,
 transient.

 Terminal initial size must match what core pty starts at (80x24 default assumption) — flag.

 Also scrollback: WithMaxScrollbackLines — RenderState screen only; scrolling back = out of M1 (View would need history; skip; note).

 Now, the "one type crosses" — strictly: ui consumes Screen (and TerminalInfo for sidebar — but that's session→UI metadata, not emulator state). Task: "Exactly one type crosses from emulator state to
 UI" ✓ Screen. TerminalInfo crosses session→UI (allowed, different boundary).

 Module map with the never-leak rule: "grep -r hcwire ui/ cmd/ → empty except cmd via loom only".

 Alright — also the report line: summary of shape = "single-owner: tea goroutine owns all emulator handles; reader/writer goroutines own the socket; one Screen value crosses to a Drawable pane node;
 lifecycle = exhaustive state machine; DialConn seam for tests".

 Length: aim for a thorough but tight package. Let me draft the file content.

 Also noteGo version "Go 1.27" ✓ (go.mod line).

 Let me write it now. I'll structure:

 ```
   # Candidate A — loom M1: one owner, one boundary value

   ## Problem
   ## Usage (caller's view)
   ### main.go (the spike)
   ### call site 2 — picker sidebar (events → PaneSet)
   ### call site 3 — loopback test (U4 seam)
   ## Module map
   ## Shape
   ### Data structures (types first)
      session, events, pane/lifecycle, screen boundary
   ### Flow (bytes in → pixels; keys in → bytes out)
   ### Load-bearing decisions
   ### Invariants in types / validation at boundaries / non-goals
   ### Interface depth judgment
   ## Test seams (U1–U5)
   ## Synthesis decision
   ## Tradeoffs accepted
   ## Alternatives considered
   ## Open questions and risks
   ## Next implementation step
 ```

 Types with not implemented bodies. Keep bodies one-liners with TODO pseudocode. Make it complete but not bloated. Write final answer as the file content plus a note about missing write/send tools.

 One more decision to lock: package layout root loom vs subpackages — final: root package loom (session+pane+screen), loom/internal/hcwire, loom/ui, loom/cmd/loom. Root imports bubbletea v2 for
 Key/Mouse msg types — justify.

 Effect enum: type effect int; const (effectAttach effect = iota; effectDetach; effectReset). advance returns []effect.

 State enum: idle, attaching, attached, exited, lostIdle, lostAttached, gone. Transitions total function with default no-op. Table in comments.

 Change enum (domain): created, lost, restarted, finished, removed, meta.

 Session msgs (tea.Msg): snapshot handled pre-program; feed msgs: TerminalChanged{ID, Change, Info}, Resynced{Seq, Terminals}, StreamData{ID, []byte}, StreamClosed{ID, CloseReason},
 SessionDead{error}, plus RPC results as plain values returned from Cmds: AttachOK{ID, Channel, AutoInput bool} (AttachCmd returns it), errors AttachFail{ID, error}.

 PaneSet.Apply(m) handles TerminalChanged/Resynced/StreamData/StreamClosed/AttachOK/AttachFail; returns tea.Cmd (for attach/detach effects).

 Session methods: Dial(path)(*Session,error), DialConn(net.Conn), (*Session) Hello(ctx, Features) error [switches framed], List(ctx) ([]TerminalInfo, error), Subscribe(ctx) (Snapshot, error) —
 Snapshot{Seq, Terminals} exported (pre-program seed), AttachCmd(pane) tea.Cmd? hmm Attach as: (s *Session) Attach(ctx, id) (AttachGrant, error) where AttachGrant{Channel uint32, AutoInput bool};
 pane wraps in cmd. SendData(ch, b), SendAutoInput(ch, b), SendResize(ch, cols, rows), SendFocus(ch, bool), SendFacts(ch, cols, rows), Close(). Feed() <-chan tea.Msg. Pump(sess, p) helper.

 Pane methods: ID(), State(), Info(), Screen() *Screen, EncodeKey(tea.Key or KeyPressMsg) []byte, EncodeMouse(tea.MouseMsg, x, y int) []byte, AttachCmd(sess) tea.Cmd, gen guard.

 PaneSet: NewPaneSet(Snapshot), Apply(tea.Msg, *Session) tea.Cmd, Open(id), Focus(id), Focused() *Pane, All() []TerminalInfo.

 ui: NewPaneNode(*loom.Pane) *PaneNode (bloom node), Sidebar(panes) bloom.Node.

 Good. Cursor type inside Screen. Cell: Rune rune; Style CellStyle{FG,BG Color; Attrs Attrs}. Color = define minimal type Color uint32? libghostty likely has color API; flag: use whatever
 formatter/style yields; sketch CellStyle with TODO to align with bloom cell path.

 Write it. Also include the assumptions flag prominently (cell path names, encoder sync method names, Frame msg shape, one-JSON-per-frame).

 Final message: I'll present the package content (as the file body) and note tool absence for write + hc send. Actually — should I really claim no tools? The environment gave me only read. I'll state
 it plainly at the end.

 Reading done — I have the hc protocol, the bindings contract, bloom's model, the architect skill, and the rationale template. Here is the candidate package.

 (Harness note: this runner session exposes only a file-read tool — no write or exec — so I cannot persist to design/candidate-a.md or run hc send t3w myself. The full file body follows; the
 orchestrator should save it verbatim to /home/nullphase/Projects/loom/design/candidate-a.md and relay hc send t3w "candidate-a done: single-owner tea-goroutine emulator, one Screen value to a
 Drawable pane node, exhaustive lifecycle machine, DialConn test seam".)


