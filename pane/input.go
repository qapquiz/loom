package pane

// Input: UI ops become encoder bytes on the session goroutine. All writes are
// phase-gated (graft 4): refused unless the machine says live — derived from
// p.state, never a parallel flag. OpResize and OpActive still record intent
// while not live (want/active), but no frame leaves the pane.

import (
	"go.mitchellh.com/libghostty"
)

func (p *Pane) apply(op Op) {
	switch op.Kind {
	case OpKey:
		if !p.live() {
			return
		}
		if p.emu == nil {
			return
		}
		ev, err := libghostty.NewKeyEvent()
		if err != nil {
			return
		}
		defer ev.Close()
		ev.SetAction(libghostty.KeyActionPress)
		if op.Key.Text != "" {
			ev.SetUTF8(op.Key.Text)
		}
		if op.Key.Code != libghostty.KeyUnidentified {
			ev.SetKey(op.Key.Code)
		}
		if op.Key.Mods != 0 {
			ev.SetMods(op.Key.Mods)
		}
		b, err := p.emu.keyEnc.Encode(ev)
		if err != nil || len(b) == 0 {
			return
		}
		p.out.Send(b)
	case OpMouse:
		if !p.live() {
			return
		}
		if p.emu == nil {
			return
		}
		ev, err := libghostty.NewMouseEvent()
		if err != nil {
			return
		}
		defer ev.Close()
		ev.SetAction(op.Mouse.Action)
		ev.SetButton(op.Mouse.Button)
		ev.SetMods(op.Mouse.Mods)
		ev.SetPosition(libghostty.MousePosition{X: float32(op.Mouse.Col), Y: float32(op.Mouse.Row)})
		b, err := p.emu.mouseEnc.Encode(ev)
		if err != nil || len(b) == 0 {
			return
		}
		p.out.Send(b)
	case OpResize:
		p.wantCol, p.wantRow = op.Cols, op.Rows
		p.reconcile(false)
	case OpActive:
		p.active = op.Active
		if !p.live() {
			return
		}
		if op.Active {
			// Last active viewer wins: reassert size and take focus.
			p.reconcile(true)
			p.out.Focus(true)
		} else {
			p.out.Focus(false)
		}
	}
}

// live is derived from the machine, per graft 4.
func (p *Pane) live() bool {
	return State(p.state.Load()) == StateAttached && p.out != nil
}
