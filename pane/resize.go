package pane

// Resize reconcile: the last-active-viewer rule. want is what the UI asked
// for; sent is the last RESIZE actually put on the wire. Both mutate only
// here, so the invariant "local emulator size == last RESIZE sent" holds
// structurally. Passive viewers follow hc size events by the app forwarding
// them as OpResize with the event's own size — reconcile's idempotence makes
// that an echo, not a fight; while active the app drops those events.
func (p *Pane) reconcile(force bool) {
	if !p.live() {
		return
	}
	if !force && p.wantCol == p.sentCol && p.wantRow == p.sentRow {
		return
	}
	if p.emu != nil && p.emu.term != nil {
		// Cell px unknown headless; size-report replies are out of spike scope.
		if err := p.emu.term.Resize(p.wantCol, p.wantRow, 0, 0); err != nil {
			return
		}
	}
	p.out.Resize(p.wantCol, p.wantRow)
	p.sentCol, p.sentRow = p.wantCol, p.wantRow
}
