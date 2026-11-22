package pane

// Stream and StreamOut: the pane side of an attach, the contract the pane
// owns so it is a standalone renderer kit over libghostty-vt. All three
// Stream methods run on the session read-loop goroutine. StreamOut writes
// are mutex-guarded on the transport side, so tests may drive them from any
// goroutine.

import (
	"encoding/json"
)

// Stream is the pane's receive half of an attached stream channel.
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

// StreamOut is the per-stream write half held by the pane.
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
	// owns the payload, the pane only transports it).
	Facts(raw json.RawMessage)
	// Focus sends a FOCUS frame: 1 gained, 0 lost.
	Focus(gained bool)
	// Close sends CLOSE: the viewer detaches; the core answers with an empty
	// CLOSE that surfaces as Closed(CloseReason{Reason: "detached"}).
	Close()
}

// CloseReason is why a stream ended: "exited" (program end, exit code set),
// "detached" (our own CLOSE ack), or a loss reason ("signaled",
// "holder_lost", ...).
type CloseReason struct {
	Reason   string
	ExitCode int
}
