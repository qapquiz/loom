# Rubric for loom M1 design candidates (picker's tool, not shown to candidates)

Score each criterion 0 to 2. Two means the candidate handles it cleanly. Zero means it ignores it.

1. **Boundary depth.** hc wire types never reach the UI layer. The pane node consumes one narrow type (cells or an equivalent). Count the types that cross.
2. **Concurrency model.** Matches the bindings contract exactly. One goroutine owns the Terminal per pane. The tea loop reads RenderState, never the Terminal. No locks invented beyond the contract.
3. **Domain modeling.** Pane lifecycle is a state machine over hc's event vocabulary. No boolean pairs. Lost, restarted, and finished are states, not flags.
4. **Spike size.** Smallest type set that proves attach, render, input, resize. Penalize speculative layers, extra packages, and options nothing calls yet.
5. **Verifiability.** Each unit U1 through U5 names a concrete seam and check. U6 has a benchmark path.
6. **Usage honesty.** The usage sketch compiles in the reader's head against bloom and the bindings as documented. Call sites would actually work.
