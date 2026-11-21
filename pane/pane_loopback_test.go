package pane

// Loopback end-to-end (graft 2): real handshake, demux, credit, and the real
// emulator behind the pane. The testcore provides the other side. The app
// wiring this mirrors (SetPump + SetAttachFn + Change(ChangeAttach)) is
// exactly what wave 2's App will do.

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
	"time"

	"loom/hc"
	"loom/internal/testcore"
)

const firstPaint = "hello \x1b[1;32mloom\x1b[0m"

func wirePane(t *testing.T, core *testcore.Core, autoInput bool) (*hc.Session, *Pane) {
	t.Helper()
	core.AutoInput = autoInput
	sess, err := hc.Dial(core.SocketPath(), hc.Features{AutomaticInput: autoInput})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	p := New("t7", nil)
	p.SetPump(sess.Do)
	p.SetAttachFn(func() { sess.Attach("t7", p) })
	return sess, p
}

func attach(t *testing.T, p *Pane) {
	t.Helper()
	p.Change(ChangeAttach) // machine: idle -> attaching, runs the attach effect
	if !waitFor(2*time.Second, func() bool { return p.Phase() == StateAttached }) {
		t.Fatalf("attach never confirmed; phase = %v", p.Phase())
	}
}

func screenText(s *Screen) string {
	if s == nil {
		return ""
	}
	var b strings.Builder
	for row := 0; row < s.Rows; row++ {
		line := make([]rune, 0, s.Cols)
		skipTail := false
		for col := 0; col < s.Cols; col++ {
			c := s.Cells[row*s.Cols+col]
			if skipTail { // spacer after a wide head: not rendered
				skipTail = false
				line = append(line, '\x00')
				continue
			}
			text := c.Text
			if text == "" {
				text = " "
			}
			line = append(line, []rune(text)...)
			skipTail = c.Wide
		}
		// Wide tails are the only '\x00' cells; collapse them out.
		text := strings.ReplaceAll(string(line), "\x00", "")
		b.WriteString(strings.TrimRight(text, " "))
		b.WriteByte('\n')
	}
	return strings.TrimSpace(b.String())
}

func waitFor(d time.Duration, fn func() bool) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if fn() {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return fn()
}

func TestAttachFirstPaintAndDeltasIntoScreen(t *testing.T) {
	core := testcore.Serve(t)
	_, p := wirePane(t, core, true)
	attach(t, p)

	// First paint with styling lands in the Screen.
	core.Delta([]byte(firstPaint))
	if !waitFor(2*time.Second, func() bool {
		s := p.Screen()
		return s != nil && strings.Contains(screenText(s), "loom")
	}) {
		t.Fatalf("screen never painted: %q", screenText(p.Screen()))
	}
	s := p.Screen()
	if s.Cols != 80 || s.Rows != 24 || len(s.Cells) != s.Cols*s.Rows {
		t.Fatalf("screen geometry = %dx%d, cells %d", s.Cols, s.Rows, len(s.Cells))
	}

	// Credit for the consumed paint bytes, observed by the core.
	if !waitFor(2*time.Second, func() bool {
		return core.Has(func(f testcore.Frame) bool {
			return f.Kind == 1 && f.Ch == 7 && len(f.Payload) == 4 &&
				binary.LittleEndian.Uint32(f.Payload) == uint32(len(firstPaint))
		})
	}) {
		t.Fatal("no CREDIT for first paint")
	}

	// A delta moves the screen forward.
	core.Delta([]byte("\r\nsecond line"))
	if !waitFor(2*time.Second, func() bool {
		return strings.Contains(screenText(p.Screen()), "second line")
	}) {
		t.Fatalf("delta not applied: %q", screenText(p.Screen()))
	}
}

func TestWideFlagOnDoubleWidthRunes(t *testing.T) {
	core := testcore.Serve(t)
	_, p := wirePane(t, core, false)
	attach(t, p)

	core.Delta([]byte("ok \xe4\xb8\x96\xe7\x95\x8c")) // "ok 世界"
	if !waitFor(2*time.Second, func() bool {
		s := p.Screen()
		return s != nil && strings.Contains(screenText(s), "世界")
	}) {
		t.Fatalf("wide text never painted: %q", screenText(p.Screen()))
	}
	s := p.Screen()
	found := 0
	for i, c := range s.Cells {
		if c.Wide {
			found++
			col := i % s.Cols
			if col+1 >= s.Cols {
				t.Fatal("wide head on last column without room for tail")
			}
		}
	}
	if found != 2 { // 世 and 界 heads
		t.Fatalf("wide flags = %d, want 2", found)
	}
}

func TestKeysProduceDataAndWritePtyAutoInput(t *testing.T) {
	core := testcore.Serve(t)
	_, p := wirePane(t, core, true)
	attach(t, p)

	// Keys out as DATA.
	p.Enqueue(Op{Kind: OpKey, Key: Key{Text: "x"}})
	if !waitFor(2*time.Second, func() bool {
		return core.Has(func(f testcore.Frame) bool {
			return f.Kind == 0 && f.Ch == 7 && bytes.Contains(f.Payload, []byte("x"))
		})
	}) {
		t.Fatal("key op never became DATA")
	}

	// Emulator-generated reply: paint a DA query; ghostty answers through the
	// WritePty effect, which routes to AUTOMATIC_INPUT (negotiated).
	core.Delta([]byte("\x1b[c"))
	if !waitFor(2*time.Second, func() bool {
		return core.Has(func(f testcore.Frame) bool { return f.Kind == 6 && f.Ch == 7 })
	}) {
		t.Fatal("DA query produced no AUTOMATIC_INPUT frame")
	}
}

func TestWritesDroppedBeforeNegotiation(t *testing.T) {
	core := testcore.Serve(t)
	sess, err := hc.Dial(core.SocketPath(), hc.Features{AutomaticInput: true})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer sess.Close()

	// Auto-input refused by the core: pane attaches with Connected(false),
	// DA queries must NOT produce kind 6 (hc gate, drops silently).
	core.AutoInput = false
	p := New("t7", nil)
	p.SetPump(sess.Do)
	p.SetAttachFn(func() { sess.Attach("t7", p) })
	attach(t, p)
	core.Delta([]byte("\x1b[c"))
	time.Sleep(200 * time.Millisecond)
	if core.Has(func(f testcore.Frame) bool { return f.Kind == 6 }) {
		t.Fatal("AUTOMATIC_INPUT sent without confirmation")
	}
}

func TestPhaseGatedWritesBeforeLive(t *testing.T) {
	core := testcore.Serve(t)
	core.AutoInput = true
	sess, err := hc.Dial(core.SocketPath(), hc.Features{AutomaticInput: true})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer sess.Close()

	// Pane wired but never attached: idle. Writes are refused (graft 4)...
	p := New("t7", nil)
	p.SetPump(sess.Do)
	p.SetAttachFn(func() { sess.Attach("t7", p) })
	p.Enqueue(Op{Kind: OpKey, Key: Key{Text: "x"}})
	p.Enqueue(Op{Kind: OpActive, Active: true})
	time.Sleep(150 * time.Millisecond) // let the queue drain onto the session goroutine
	if got := p.Phase(); got != StateIdle {
		t.Fatalf("phase = %v", got)
	}
	if core.Has(func(f testcore.Frame) bool { return f.Ch == 7 }) {
		t.Fatal("frames written while not live")
	}
	// ...but OpResize records intent; attach applies it via reconcile.
	p.Enqueue(Op{Kind: OpResize, Cols: 100, Rows: 30})
	attach(t, p)
	if !waitFor(2*time.Second, func() bool {
		return core.Has(func(f testcore.Frame) bool {
			return f.Kind == 3 && f.Ch == 7 && bytes.Equal(f.Payload, []byte{100, 0, 30, 0})
		})
	}) {
		t.Fatal("recorded want not imposed after going live")
	}
}

func TestDirtySkipPublishesOnlyOnChange(t *testing.T) {
	core := testcore.Serve(t)
	_, p := wirePane(t, core, false)
	attach(t, p)

	core.Delta([]byte("paint one"))
	if !waitFor(2*time.Second, func() bool { return p.Screen() != nil && strings.Contains(screenText(p.Screen()), "paint one") }) {
		t.Fatal("first paint missing")
	}
	first := p.Screen()

	// Empty delta: ghostty reports the frame clean; publish is skipped and the
	// Screen pointer must not change.
	core.Delta(nil)
	time.Sleep(150 * time.Millisecond)
	if p.Screen() != first {
		t.Fatal("clean frame republished")
	}

	// Real change: new pointer.
	core.Delta([]byte("!"))
	if !waitFor(2*time.Second, func() bool { return strings.Contains(screenText(p.Screen()), "paint one!") }) {
		t.Fatalf("changed frame not published: %q", screenText(p.Screen()))
	}
}

func TestClosedExitAndLossKeepWant(t *testing.T) {
	core := testcore.Serve(t)
	_, p := wirePane(t, core, false)
	attach(t, p)

	core.CloseStream(`{"reason":"exited","exit_code":0,"signal":null}`)
	if !waitFor(2*time.Second, func() bool { return p.Phase() == StateExited }) {
		t.Fatalf("phase after exited CLOSE = %v", p.Phase())
	}

	// Reattach from exited; then the stream closes with a loss reason and
	// want-attach (LostLive) must survive it. The restarted change flips the
	// machine back to attaching and the attach effect reconfirms live.
	p.Change(ChangeAttach)
	if !waitFor(2*time.Second, func() bool { return p.Phase() == StateAttached }) {
		t.Fatalf("reattach failed; phase = %v", p.Phase())
	}
	core.CloseStream(`{"reason":"holder_lost"}`)
	if !waitFor(2*time.Second, func() bool { return p.Phase() == StateLostLive }) {
		t.Fatalf("phase after loss CLOSE = %v, want lost-live", p.Phase())
	}
	p.Change(ChangeRestarted)
	if !waitFor(2*time.Second, func() bool { return p.Phase() == StateAttached }) {
		t.Fatalf("phase after restarted = %v, want attached", p.Phase())
	}
}
