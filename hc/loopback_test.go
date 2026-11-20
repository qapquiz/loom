package hc

// Loopback tests (graft 2): every hc test runs the real handshake, demux and
// credit against the scripted testcore, which speaks the wire independently.

import (
	"bytes"
	"encoding/binary"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"loom/internal/testcore"
)

func terminalJSON(id string) string {
	return `{"address":"` + id + `","title":"bash","cols":80,"rows":24,"labels":{},"state":"live","status":{"state":"idle"}}`
}

func TestDialPlainToFramedHandshake(t *testing.T) {
	core := testcore.Serve(t)
	core.AutoInput = true
	core.Seq = 5
	core.Terminals = "[" + terminalJSON("t7") + "]"

	sess, err := Dial(core.SocketPath(), Features{AutomaticInput: true})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer sess.Close()

	evs := sess.Events()
	snap := <-evs
	if snap.Kind != EventSnapshot || snap.Seq != 5 || len(snap.All) != 1 {
		t.Fatalf("snapshot = %+v", snap)
	}
	if snap.All[0].ID != "t7" || snap.All[0].Cols != 80 || snap.All[0].Status != "idle" {
		t.Fatalf("snapshot record = %+v", snap.All[0])
	}

	// Events drip through the events channel with credit returned per payload.
	core.Event("event", map[string]any{"seq": 6, "type": "title", "terminal": "t7", "data": map[string]any{"title": "vim"}})
	ev := <-evs
	if ev.Kind != EventTitle || ev.Info.Title != "vim" {
		t.Fatalf("event = %+v", ev)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !core.Has(func(f testcore.Frame) bool {
		return f.Kind == 1 && f.Ch == 2 && binary.LittleEndian.Uint32(f.Payload) > 0 // CREDIT on events channel
	}) {
		time.Sleep(2 * time.Millisecond)
	}
	if !core.Has(func(f testcore.Frame) bool { return f.Kind == 1 && f.Ch == 2 }) {
		t.Fatal("no CREDIT sent for consumed event bytes")
	}
}

type recorder struct {
	connected atomic.Bool
	autoInput atomic.Bool
	out       atomic.Pointer[streamOutStub]
	deltasMu  sync.Mutex
	deltas    [][]byte
	closedMu  sync.Mutex
	closed    []CloseReason
}

// streamOutStub records the write half the session handed over. Methods pass
// straight through to the real streamOut; only the pointer is stubbed.
type streamOutStub struct {
	StreamOut
}

func (r *recorder) Connected(out StreamOut, autoInput bool) {
	r.connected.Store(true)
	r.autoInput.Store(autoInput)
	r.out.Store(&streamOutStub{out})
}

func (r *recorder) Delta(p []byte) {
	r.deltasMu.Lock()
	r.deltas = append(r.deltas, bytes.Clone(p))
	r.deltasMu.Unlock()
}

func (r *recorder) Closed(reason CloseReason) {
	r.closedMu.Lock()
	r.closed = append(r.closed, reason)
	r.closedMu.Unlock()
}

func (r *recorder) Out() StreamOut { return r.out.Load() }

func (r *recorder) reasons() []CloseReason {
	r.closedMu.Lock()
	defer r.closedMu.Unlock()
	return append([]CloseReason(nil), r.closed...)
}

func attachRecorder(t *testing.T, sess *Session) *recorder {
	t.Helper()
	rec := &recorder{}
	sess.Attach("t7", rec)
	if !waitUntil(2*time.Second, func() bool { return rec.connected.Load() }) {
		t.Fatal("attach never confirmed")
	}
	return rec
}

func TestAttachKeysAsDataAndCredit(t *testing.T) {
	core := testcore.Serve(t)
	core.AutoInput = true
	sess, err := Dial(core.SocketPath(), Features{AutomaticInput: true})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer sess.Close()
	rec := attachRecorder(t, sess)
	if !rec.autoInput.Load() {
		t.Fatal("autoInput not negotiated though core confirmed it")
	}

	// First paint arrives as DATA and is credited after the callback returns.
	core.Delta([]byte("hello \x1b[1mloom\x1b[0m"))
	if !waitUntil(2*time.Second, func() bool { return rec.firstDelta() != nil }) {
		t.Fatal("first paint never delivered")
	}
	if !core.Has(creditFrame(7, len(rec.firstDelta()))) {
		t.Fatal("no CREDIT matching consumed first-paint bytes")
	}

	// Keys go out as DATA on the stream channel.
	rec.Out().Send([]byte("echo loom\r"))
	if !waitUntil(2*time.Second, func() bool {
		return core.Has(func(f testcore.Frame) bool {
			return f.Kind == 0 && f.Ch == 7 && bytes.Contains(f.Payload, []byte("echo loom\r"))
		})
	}) {
		t.Fatal("keys not observed as stream DATA")
	}

	// Resize and Focus frame payloads per the schema.
	rec.Out().Resize(120, 40)
	rec.Out().Focus(true)
	if !waitUntil(2*time.Second, func() bool {
		return core.Has(func(f testcore.Frame) bool { return f.Kind == 3 && f.Ch == 7 })
	}) {
		t.Fatal("resize never arrived")
	}
	if !core.Has(func(f testcore.Frame) bool { return f.Kind == 4 && f.Ch == 7 }) {
		t.Fatal("focus never arrived")
	}
	for _, f := range core.Frames() {
		if f.Ch != 7 {
			continue
		}
		if f.Kind == 3 && !bytes.Equal(f.Payload, []byte{120, 0, 40, 0}) {
			t.Fatalf("resize payload = %v", f.Payload)
		}
		if f.Kind == 4 && !bytes.Equal(f.Payload, []byte{1}) {
			t.Fatalf("focus payload = %v", f.Payload)
		}
	}
}

func TestAutoInputGatedOnNegotiation(t *testing.T) {
	// Refused: hello did not grant, attach did not confirm — AutoReply must
	// drop silently.
	refused := testcore.Serve(t)
	refused.AutoInput = false
	sessNo, err := Dial(refused.SocketPath(), Features{AutomaticInput: true})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer sessNo.Close()
	recNo := attachRecorder(t, sessNo)
	if recNo.autoInput.Load() {
		t.Fatal("autoInput negotiated without confirmation")
	}
	recNo.Out().AutoReply([]byte("\x1b[?1;2c"))
	if !waitUntil(300*time.Millisecond, func() bool { return len(refused.Frames()) > 0 }) {
		t.Fatal("connection dead")
	}
	time.Sleep(100 * time.Millisecond)
	if refused.Has(func(f testcore.Frame) bool { return f.Kind == 6 }) {
		t.Fatal("AUTOMATIC_INPUT sent without negotiation")
	}

	// Granted: hello advertised and attach confirmed — kind 6 flows.
	granted := testcore.Serve(t)
	granted.AutoInput = true
	sessYes, err := Dial(granted.SocketPath(), Features{AutomaticInput: true})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer sessYes.Close()
	recYes := attachRecorder(t, sessYes)
	if !recYes.autoInput.Load() {
		t.Fatal("autoInput not negotiated though core confirmed it")
	}
	recYes.Out().AutoReply([]byte("\x1b[?1;2c"))
	if !waitUntil(2*time.Second, func() bool {
		return granted.Has(func(f testcore.Frame) bool {
			return f.Kind == 6 && bytes.Equal(f.Payload, []byte("\x1b[?1;2c"))
		})
	}) {
		t.Fatal("AUTOMATIC_INPUT frame not observed after negotiation")
	}
}

func TestAttachRefusalClosesStream(t *testing.T) {
	core := testcore.Serve(t)
	core.AttachErrKind = "finished"
	sess, err := Dial(core.SocketPath(), Features{})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer sess.Close()
	rec := &recorder{}
	sess.Attach("t7", rec)
	if !waitUntil(2*time.Second, func() bool {
		return len(rec.reasons()) > 0 && rec.reasons()[0].Reason == "attach_failed"
	}) {
		t.Fatalf("attach refusal not surfaced: %+v", rec.reasons())
	}
}

func TestStreamCloseReasons(t *testing.T) {
	core := testcore.Serve(t)
	sess, err := Dial(core.SocketPath(), Features{})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer sess.Close()
	rec := attachRecorder(t, sess)

	core.CloseStream(`{"reason":"exited","exit_code":3,"signal":null}`)
	if !waitUntil(2*time.Second, func() bool { return len(rec.reasons()) > 0 }) {
		t.Fatal("CLOSE never delivered")
	}
	if got := rec.reasons()[0]; got.Reason != "exited" || got.ExitCode != 3 {
		t.Fatalf("close reason = %+v", got)
	}

	// Detach: viewer CLOSE then empty CLOSE ack from the core.
	rec2 := attachRecorder(t, sess)
	rec2.Out().Close()
	if !waitUntil(2*time.Second, func() bool {
		return len(rec2.reasons()) > 0 && rec2.reasons()[0].Reason == "detached"
	}) {
		t.Fatalf("detach ack not surfaced: %+v", rec2.reasons())
	}
}

func creditFrame(ch uint32, n int) func(testcore.Frame) bool {
	return func(f testcore.Frame) bool {
		return f.Kind == 1 && f.Ch == ch && len(f.Payload) == 4 && binary.LittleEndian.Uint32(f.Payload) == uint32(n)
	}
}

func waitUntil(d time.Duration, fn func() bool) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if fn() {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return fn()
}

func (r *recorder) firstDelta() []byte {
	r.deltasMu.Lock()
	defer r.deltasMu.Unlock()
	if len(r.deltas) == 0 {
		return nil
	}
	return r.deltas[0]
}
