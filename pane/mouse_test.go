package pane

// Regression for the dead mouse path: the MouseEncoder needs a size context
// or Encode returns empty bytes and OpMouse dies silently. With synthetic
// 1x1 px cells, a cell-coord event must land as an SGR cell sequence.

import (
	"bytes"
	"testing"
	"time"

	"go.mitchellh.com/libghostty"

	"loom/internal/testcore"
)

func TestMouseCellCoordsReachWireAsSGR(t *testing.T) {
	core := testcore.Serve(t)
	_, p := wirePane(t, core, false)
	attach(t, p)

	// Normal tracking plus SGR ext mode, so the wire sequence is SGR.
	core.Delta([]byte("\x1b[?1000h\x1b[?1006h"))
	if !waitFor(2*time.Second, func() bool {
		s := p.Screen()
		return s != nil && s.MouseOn
	}) {
		t.Fatal("mouse tracking never reported on")
	}

	// SGR is 1-based: pane cell (5,3) encodes as 6;4 on the wire.
	p.Enqueue(Op{Kind: OpMouse, Mouse: Mouse{
		Action: libghostty.MouseActionPress,
		Button: libghostty.MouseButtonLeft,
		Col:    5,
		Row:    3,
	}})
	if !waitFor(2*time.Second, func() bool {
		return core.Has(func(f testcore.Frame) bool {
			return f.Kind == 0 && f.Ch == 7 && bytes.Contains(f.Payload, []byte("\x1b[<0;6;4M"))
		})
	}) {
		t.Fatal("no SGR mouse cell sequence on the wire")
	}
}
