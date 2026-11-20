 and StreamOut presumably still usable → C's pane could send keys after CLOSE until StreamOut errors; A/B refuse by state). That's a correctness guard; I'll take B's phase-gate. And I want at least
 one graft each from A and B — satisfied.

 Final 4 grafts:
 1. from candidate-a: lifecycle.go "advance" total state machine (LostLive = want-attach as state; EffReset restart rows) — C's attached stream has no lifecycle machine, only Room's listed-lifecycle.
 2. from candidate-a: DialConn(net.Conn)/testcore seam — every test exercises the real framed handshake/demux/credit rather than C's scripted fake-core slices.
 3. from candidate-b: pump skip-publish when Terminal/RenderState reports dirty==RenderStateDirtyFalse — C publishes on every Delta; idle panes cost bloom nothing in B.
 4. from candidate-b: phase-gated writes — refuse SendInput/NeedSize/RESIZE unless the machine says live (derived, no parallel flag) — C only gates AutoReply on negotiation.

 Evidence must be one line per score in the SCORES block: "criterion: a=X b=X c=X  (one line per criterion, each with a short evidence phrase)". So the evidence phrase per line — probably one
 combined phrase or per-candidate? "each with a short evidence phrase" — singular; I'll give a compact phrase covering the discriminating evidence.

 Keep line lengths reasonable. Output exactly the shape, nothing after.

 SCORES
 boundary depth: a=2 b=2 c=2  (a: "Screen... the ONLY type that crosses"; b: "PaneFrame is THE boundary type", hcwire compiler-enforced; c: "Screen is the ONE type that crosses... no binding handle
 ever crosses")
 concurrency model: a=2 b=2 c=2  (a: "tea goroutine owns every libghostty handle... no locks anywhere on the emulator path"; b: "one pump goroutine per pane owns every ghostty handle" (Session.mu
 nit); c: "two owners, no locks... UI never touches a binding handle")
 domain modeling: a=2 b=2 c=1  (a: "advance is total and pure... wantAttach is encoded as the LostLive state, not a flag"; b: "Step is the entire lifecycle... No booleans anywhere" though
 Live+restarted only logs; c: Room has Lost/Restarted/Finished as states but the attached-stream phases have no machine — "two machines... never cross-checked")
 spike size: a=2 b=1 c=2  (a: "DialConn is the whole mock story", lean root+ui+hcwire; b: unused "opts AttachOpts", six-file hc + Registry + sizeTick machinery; c: "The usage is the spec; everything
 below is derived from these three call sites")
 verifiability: a=1 b=1 c=2  (a: full U1–U5 seam table but U6 only "if U6 numbers disagree", no path; b: SizeSink spy + fake-core listener but U6 only "U6-benchmark-driven change"; c: U1–U5 plus
 missed-resync unit and "measured in U6 against the bindings' render-state benchmarks")
 usage honesty: a=2 b=1 c=2  (a: View/loopback call sites compile in-head (file head truncated); b: no usage/call-site section, only "the caller's total knowledge" prose; c: three complete call sites
 — Dial/NewApp, custom bloom tree, headless recorder drive)
 BASE: c because it is the only candidate that closes every rubric loop — complete usage-as-spec call sites, a named U6 benchmark path, and lock-free single-writer ownership — while its gaps
 (pane-side lifecycle machine, test seam strength) are exactly what candidate-a grafts cleanly.
 GRAFTS
 - from candidate-a: the lifecycle.go advance(st, Change) (State, []Effect) total machine — 7 states with LostLive encoding want-attach as state (no flag) and EffReset restart rows; fills C's missing
   pane-side lifecycle machine (its attached stream is callback-wired, and Restarted-while-attached is unhandled).
 - from candidate-a: the DialConn(net.Conn) + testcore loopback seam — C's scripted fake core re-implements protocol slices per test, while this seam runs the real handshake, demux, credit, and
   machine in every unit (U2–U4).
 - from candidate-b: the pump's dirty-check publish skip ("skips the publish entirely when RenderStateDirtyFalse") — C publishes a fresh Screen on every Delta, so idle panes cost bloom; B makes them
   free.
 - from candidate-b: phase-gated writes — "All writes (SendInput, NeedSize, RESIZE/FOCUS...) refused unless the pane is in PhaseLive — one check... derived from the phase, never a parallel flag"; C
   only gates AutoReply on negotiation and would let keys flow at a closed/Exited stream.


