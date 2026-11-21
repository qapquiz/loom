package hc

import (
	"encoding/json"
)

// Stream and StreamOut: the pane side of an attach. All three Stream methods
// run on the session read-loop goroutine. StreamOut writes are mutex-guarded,
// so tests may drive them from any goroutine.

type Stream interface {
	// Connected reports the attach reply: the write half and the negotiated
	// auto-input truth (hello grant AND attach confirmation). The pane never
	// re-derives it.
	Connected(out StreamOut, autoInput bool)
	// Delta delivers first paint, then program output, already demuxed by
	// channel. Credit is sent automatically after each Delta returns.
	Delta(p []byte)
	// Closed is the only error path: detach, exit, loss, refused attach, or
	// session teardown.
	Closed(r CloseReason)
}

type StreamOut interface {
	// Send writes keys and mouse bytes as DATA on the stream channel.
	Send(p []byte)
	// AutoReply writes AUTOMATIC_INPUT (frame kind 6) for emulator-generated
	// replies. Silently dropped unless both negotiation steps confirmed it —
	// old cores ignore kind 6 and lose the bytes, so this gate is not optional.
	AutoReply(p []byte)
	// Resize sends a RESIZE frame (u16 LE cols, u16 LE rows).
	Resize(cols, rows uint16)
	// Facts sends a FACTS frame (kind 5): viewer-reported Display facts as
	// raw JSON on the stream channel (the schema's Display type; the caller
	// owns the payload, hc only transports it).
	Facts(raw json.RawMessage)
	// Focus sends a FOCUS frame: 1 gained, 0 lost.
	Focus(gained bool)
	// Close sends CLOSE: the viewer detaches; the core answers with an empty
	// CLOSE that surfaces as Closed(CloseReason{Reason: "detached"}).
	Close()
}

type streamState struct {
	in  Stream
	out streamOut
}

// streamOut is the per-stream write half held by the pane.
type streamOut struct {
	s         *Session
	ch        uint32
	autoInput bool
}

func (o streamOut) Send(p []byte) {
	o.s.writeFrame(frame{ch: o.ch, kind: frameData, payload: p})
}

func (o streamOut) AutoReply(p []byte) {
	if !o.autoInput {
		return
	}
	o.s.writeFrame(frame{ch: o.ch, kind: frameAutoInput, payload: p})
}

func (o streamOut) Resize(cols, rows uint16) {
	payload := []byte{byte(cols), byte(cols >> 8), byte(rows), byte(rows >> 8)}
	o.s.writeFrame(frame{ch: o.ch, kind: frameResize, payload: payload})
}

func (o streamOut) Focus(gained bool) {
	b := byte(0)
	if gained {
		b = 1
	}
	o.s.writeFrame(frame{ch: o.ch, kind: frameFocus, payload: []byte{b}})
}

func (o streamOut) Facts(raw json.RawMessage) {
	o.s.writeFrame(frame{ch: o.ch, kind: frameFacts, payload: raw})
}

func (o streamOut) Close() {
	o.s.writeFrame(frame{ch: o.ch, kind: frameClose, payload: nil})
}

// parseClose reads a CLOSE payload: JSON {"reason":...,"exit_code":...} from
// the core; empty means the ack to a viewer-initiated detach.
func parseClose(payload []byte) CloseReason {
	if len(payload) == 0 {
		return CloseReason{Reason: "detached"}
	}
	var d struct {
		Reason   string `json:"reason"`
		ExitCode *int32 `json:"exit_code"`
	}
	if err := json.Unmarshal(payload, &d); err != nil {
		return CloseReason{Reason: "closed"}
	}
	cr := CloseReason{Reason: d.Reason}
	if cr.Reason == "" {
		cr.Reason = "closed"
	}
	if d.ExitCode != nil {
		cr.ExitCode = int(*d.ExitCode)
	}
	return cr
}
