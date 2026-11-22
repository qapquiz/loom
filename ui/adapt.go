package ui

// The composition root's seam: hc's stream contract on the outside, the
// pane's own contract on the inside. hc.StreamOut satisfies pane.StreamOut
// as-is (same method set), and the CloseReason structs share their field
// layout, so the conversion is field-for-field.

import (
	"loom/hc"
	"loom/pane"
)

type hcStream struct{ p *pane.Pane }

func (s hcStream) Connected(out hc.StreamOut, autoInput bool) {
	s.p.Connected(out, autoInput)
}

func (s hcStream) Delta(b []byte) { s.p.Delta(b) }

func (s hcStream) Closed(r hc.CloseReason) { s.p.Closed(pane.CloseReason(r)) }
