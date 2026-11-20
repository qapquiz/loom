package ui

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"loom/hc"
	"loom/internal/testcore"
	"loom/pane"
)

func newApp(t *testing.T, terminals string) (*testcore.Core, *App) {
	t.Helper()
	core := testcore.Serve(t)
	core.Terminals = terminals
	sess, err := hc.Dial(core.SocketPath(), hc.Features{})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	a := New(sess)
	a.Update(eventMsg{<-sess.Events()}) // the subscribe snapshot
	return core, a
}

// sendCoreEvent pushes one hc notification through the events channel and
// returns the event hc delivers for it.
func sendCoreEvent(t *testing.T, c *testcore.Core, sess *hc.Session, seq uint64, typ, id, data string) hc.Event {
	t.Helper()
	c.Event("event", map[string]any{
		"seq": seq, "type": typ, "terminal": id, "data": json.RawMessage(data),
	})
	select {
	case ev := <-sess.Events():
		return ev
	case <-time.After(2 * time.Second):
		t.Fatalf("event %s never delivered", typ)
		return hc.Event{}
	}
}

func waitForFrame(t *testing.T, c *testcore.Core, d time.Duration, pred func(f testcore.Frame) bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if c.Has(pred) {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("frame never arrived")
}

func waitMouseOn(t *testing.T, a *App) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s := a.paneScreen(); s != nil && s.MouseOn {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("mouse tracking never reported on")
}

func TestAppAutoAttachesFirstListed(t *testing.T) {
	_, a := newApp(t, `[{"address":"t7","title":"vim","cols":80,"rows":24}]`)
	if a.pane == nil || a.active != "t7" {
		t.Fatalf("pane = %v active = %v", a.pane, a.active)
	}
	if !waitForPhase(t, a.pane, 2*time.Second, pane.StateAttached) {
		t.Fatalf("phase = %v", a.pane.Phase())
	}
	if a.node == nil {
		t.Fatal("pane node missing")
	}
}

func TestAppKeyRouteToData(t *testing.T) {
	c, a := newApp(t, `[{"address":"t7","title":"vim","cols":80,"rows":24}]`)
	if !waitForPhase(t, a.pane, 2*time.Second, pane.StateAttached) {
		t.Fatal("no attach")
	}
	a.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	waitForFrame(t, c, 2*time.Second, func(f testcore.Frame) bool {
		return f.Kind == 0 && f.Ch == 7 && len(f.Payload) > 0 && f.Payload[0] == 'x'
	})
}

func TestAppCtrlQQuits(t *testing.T) {
	_, a := newApp(t, `[{"address":"t7","title":"vim","cols":80,"rows":24}]`)
	_, cmd := a.Update(tea.KeyPressMsg{Code: 'q', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatal("ctrl+q produced no command")
	}
	if got := reflect.TypeOf(cmd()); got != reflect.TypeOf(tea.Quit()) {
		t.Fatalf("ctrl+q command yields %v, want quitMsg", got)
	}
	// Any other ctrl chord routes to the pane, not to quit.
	_, cmd = a.Update(tea.KeyPressMsg{Code: 'a', Mod: tea.ModCtrl})
	if cmd != nil && reflect.TypeOf(cmd()) == reflect.TypeOf(tea.Quit()) {
		t.Fatal("ctrl+a quit the app")
	}
}

func TestAppResizeFromFrameGeometry(t *testing.T) {
	c, a := newApp(t, `[{"address":"t7","title":"vim","cols":80,"rows":24}]`)
	if !waitForPhase(t, a.pane, 2*time.Second, pane.StateAttached) {
		t.Fatal("no attach")
	}
	// 100 wide minus the 20-cell sidebar leaves the pane 80x30.
	a.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	waitForFrame(t, c, 2*time.Second, func(f testcore.Frame) bool {
		return f.Kind == 3 && f.Ch == 7 && len(f.Payload) == 4 &&
			f.Payload[0] == 80 && f.Payload[2] == 30
	})
	// Same size again: no drift, no second RESIZE.
	n := len(c.Frames())
	a.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	time.Sleep(100 * time.Millisecond)
	for _, f := range c.Frames()[n:] {
		if f.Kind == 3 && f.Ch == 7 {
			t.Fatal("resize posted without drift")
		}
	}
}

func TestAppMouseModeFollowsPaneOwnership(t *testing.T) {
	c, a := newApp(t, `[{"address":"t7","title":"vim","cols":80,"rows":24}]`)
	if !waitForPhase(t, a.pane, 2*time.Second, pane.StateAttached) {
		t.Fatal("no attach")
	}
	a.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	if v := a.View(); v.MouseMode != tea.MouseModeNone {
		t.Fatalf("mouse mode before tracking on = %v, want none", v.MouseMode)
	}
	c.Delta([]byte("\x1b[?1000h"))
	waitMouseOn(t, a)
	if v := a.View(); v.MouseMode != tea.MouseModeAllMotion {
		t.Fatalf("mouse mode while forwarding = %v, want all motion", v.MouseMode)
	}
}

func TestAppMouseDroppedWhenProgramDoesNotOwnIt(t *testing.T) {
	c, a := newApp(t, `[{"address":"t7","title":"vim","cols":80,"rows":24}]`)
	if !waitForPhase(t, a.pane, 2*time.Second, pane.StateAttached) {
		t.Fatal("no attach")
	}
	a.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	a.View() // put a frame on screen so the pane rect exists
	n := len(c.Frames())
	a.Update(tea.MouseClickMsg{X: 25, Y: 3, Button: tea.MouseLeft})
	time.Sleep(100 * time.Millisecond)
	for _, f := range c.Frames()[n:] {
		if f.Kind == 0 && f.Ch == 7 {
			t.Fatalf("mouse forwarded while program does not own it: %q", f.Payload)
		}
	}
}

func TestAppMouseRoutingBoundary(t *testing.T) {
	c, a := newApp(t, `[{"address":"t7","title":"vim","cols":80,"rows":24}]`)
	if !waitForPhase(t, a.pane, 2*time.Second, pane.StateAttached) {
		t.Fatal("no attach")
	}
	a.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	c.Delta([]byte("\x1b[?1000h\x1b[?1006h"))
	waitMouseOn(t, a)
	a.View()
	r, ok := a.ui.Frame().Rect(paneRect)
	if !ok || r.Min.X != sidebarWidth {
		t.Fatalf("pane rect = %v ok=%v", r, ok)
	}
	// Screen (25,3) over a rect starting at x=20 is pane cell (5,3); SGR is
	// 1-based, so the wire sequence must carry 6;4.
	a.Update(tea.MouseClickMsg{X: 25, Y: 3, Button: tea.MouseLeft})
	waitForFrame(t, c, 2*time.Second, func(f testcore.Frame) bool {
		return f.Kind == 0 && f.Ch == 7 && bytes.Contains(f.Payload, []byte("\x1b[<0;6;4M"))
	})
}

func TestAppEventSizeFollowedWhilePassive(t *testing.T) {
	c, a := newApp(t, `[{"address":"t7","title":"vim","cols":80,"rows":24}]`)
	if !waitForPhase(t, a.pane, 2*time.Second, pane.StateAttached) {
		t.Fatal("no attach")
	}
	a.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	waitForFrame(t, c, 2*time.Second, func(f testcore.Frame) bool { return f.Kind == 3 })

	// Blur: the passive viewer follows the core's size instead of fighting.
	a.Update(tea.BlurMsg{})
	a.Update(eventMsg{sendCoreEvent(t, c, a.sess, 1, "size", "t7", `{"cols":60,"rows":20}`)})
	waitForFrame(t, c, 2*time.Second, func(f testcore.Frame) bool {
		return f.Kind == 3 && f.Ch == 7 && len(f.Payload) == 4 &&
			f.Payload[0] == 60 && f.Payload[2] == 20
	})

	// Focus again: our size is authoritative; a core size event is dropped.
	a.Update(tea.FocusMsg{})
	n := len(c.Frames())
	a.Update(eventMsg{sendCoreEvent(t, c, a.sess, 2, "size", "t7", `{"cols":44,"rows":11}`)})
	time.Sleep(100 * time.Millisecond)
	for _, f := range c.Frames()[n:] {
		if f.Kind == 3 && f.Ch == 7 && f.Payload[0] == 44 {
			t.Fatal("active viewer followed a core size event")
		}
	}
}

func TestAppQuitsWhenSessionCloses(t *testing.T) {
	_, a := newApp(t, `[{"address":"t7","title":"vim","cols":80,"rows":24}]`)
	a.sess.Close()
	msg := a.waitEvent()()
	if _, ok := msg.(sessionClosedMsg); !ok {
		t.Fatalf("closed session produced %T, want sessionClosedMsg", msg)
	}
	_, cmd := a.Update(msg)
	if got := reflect.TypeOf(cmd()); got != reflect.TypeOf(tea.Quit()) {
		t.Fatalf("session close yields %v, want quitMsg", got)
	}
}
