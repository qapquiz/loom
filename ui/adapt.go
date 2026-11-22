package ui

// The composition root's seam: hc's stream contract on the outside, the
// pane's own contract on the inside. hc.StreamOut satisfies pane.StreamOut
// as-is (same method set), and the CloseReason structs share their field
// layout, so the conversion is field-for-field.
//
// hcStream is a pointer with one mutable cell: the write half Connected
// hands over on the session goroutine. Detach reads it from the tea
// goroutine — StreamOut writes are mutex-guarded on the transport side and
// the swap itself is atomic, so the pane's single-owner rule holds.

import (
	"sync/atomic"

	"loom/hc"
	"loom/pane"
)

type hcStream struct {
	p   *pane.Pane
	out atomic.Value // hc.StreamOut; nil until the attach confirms
}

func (s *hcStream) Connected(out hc.StreamOut, autoInput bool) {
	s.out.Store(out)
	s.p.Connected(out, autoInput)
}

func (s *hcStream) Delta(b []byte) { s.p.Delta(b) }

func (s *hcStream) Closed(r hc.CloseReason) { s.p.Closed(pane.CloseReason(r)) }

// Detach closes the stream's write half: CLOSE on the wire, and the core's
// empty CLOSE ack lands as Closed("detached"), walking the pane's machine
// back to idle. The terminal itself keeps running in the core, untouched.
func (s *hcStream) Detach() {
	if v, ok := s.out.Load().(hc.StreamOut); ok {
		v.Close()
	}
}
