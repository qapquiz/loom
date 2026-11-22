package ui

// Multi-pane loopback tests: splits through the real key path, per-pane
// RESIZE from a solved tree at two sizes, focused-only key routing, click
// focus, wheel routing to the hit pane, and slots surviving exit.

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"loom/hc"
	"loom/internal/testcore"
	"loom/pane"
)

// newRunApp is newApp with terminal.run scripted (addresses popped in
// order), set before Dial like every other testcore knob.
func newRunApp(t *testing.T, runAddrs ...string) (*testcore.Core, *App) {
	t.Helper()
	core := testcore.Serve(t)
	core.Terminals = `[{"address":"t7","title":"vim","cols":80,"rows":24}]`
	core.RunAddresses = runAddrs
	sess, err := hc.Dial(core.SocketPath(), hc.Features{})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	a := New(sess)
	a.Update(eventMsg{<-sess.Events()}) // the subscribe snapshot
	return core, a
}

// pumpCmd drives cmd the way the tea runtime does: batch results unwrap and
// every produced message goes back through Update.
func pumpCmd(t *testing.T, a *App, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		return
	}
	switch msg := cmd().(type) {
	case nil:
	case tea.BatchMsg:
		for _, sub := range msg {
			pumpCmd(t, a, sub)
		}
	default:
		_, next := a.Update(msg)
		pumpCmd(t, a, next)
	}
}

// press runs one input message through Update and pumps whatever it spawned.
func press(t *testing.T, a *App, k tea.Msg) {
	t.Helper()
	_, cmd := a.Update(k)
	pumpCmd(t, a, cmd)
}

func splitApp(t *testing.T, vert bool) (*testcore.Core, *App) {
	t.Helper()
	c, a := newRunApp(t, "t9")
	if !waitForPhase(t, a.panes["t7"].p, 2*time.Second, pane.StateAttached) {
		t.Fatal("no attach for t7")
	}
	a.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	press(t, a, tea.KeyPressMsg{Code: 'b', Mod: tea.ModCtrl})
	if a.mode != modePrefix {
		t.Fatalf("ctrl+b left mode = %v", a.mode)
	}
	if vert {
		press(t, a, tea.KeyPressMsg{Code: 'v', Text: "v"})
	} else {
		press(t, a, tea.KeyPressMsg{Code: '-', Text: "-"})
	}
	if a.mode != modePassthrough {
		t.Fatalf("split key left mode = %v", a.mode)
	}
	if len(a.panes) != 2 {
		t.Fatalf("panes = %v, want t7 and t9", a.panes)
	}
	if !waitForPhase(t, a.panes["t9"].p, 2*time.Second, pane.StateAttached) {
		t.Fatalf("t9 phase = %v", a.panes["t9"].p.Phase())
	}
	return c, a
}

// The split runs a shell in the background with $HOME as cwd, attaches the
// returned id without waiting for the room's record, and focuses the new
// pane.
func TestSplitVerticalTreeFocusAndRun(t *testing.T) {
	c, a := splitApp(t, true)
	if runs := c.Runs(); len(runs) != 1 {
		t.Fatalf("terminal.run calls = %d, want 1", len(runs))
	} else {
		if len(runs[0].Argv) != 0 {
			t.Fatalf("argv = %v, want the core's own shell", runs[0].Argv)
		}
		if want := homeDir(); runs[0].Cwd != want {
			t.Fatalf("cwd = %q, want %q", runs[0].Cwd, want)
		}
	}
	if a.tree == nil || a.tree.leaf != nil || !a.tree.vert || a.tree.a.leaf.id != "t7" || a.tree.b.leaf.id != "t9" {
		t.Fatalf("tree = %+v, want a vert split of t7 and t9", a.tree)
	}
	if a.focus != "t9" {
		t.Fatalf("focus = %v, want the new pane t9", a.focus)
	}
}

// Side-by-side halves: both panes report their own solved rect as RESIZE,
// and both follow it again when the window changes size.
func TestSplitVerticalResizesBothPanesAtTwoSizes(t *testing.T) {
	c, a := splitApp(t, true)
	// 100 wide minus the 20-cell sidebar leaves 80; halves are 40x30.
	waitForFrame(t, c, 2*time.Second, resizeOn(7, 40, 30))
	waitForFrame(t, c, 2*time.Second, resizeOn(8, 40, 30))
	// Second size: 120x40 leaves 100; halves are 50x40.
	a.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	waitForFrame(t, c, 2*time.Second, resizeOn(7, 50, 40))
	waitForFrame(t, c, 2*time.Second, resizeOn(8, 50, 40))
	// No drift, no third RESIZE for either pane.
	n := len(c.Frames())
	a.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	time.Sleep(150 * time.Millisecond)
	for _, f := range c.Frames()[n:] {
		if f.Kind == 3 && (f.Ch == 7 || f.Ch == 8) {
			t.Fatalf("resize posted without drift: ch=%d %v", f.Ch, f.Payload)
		}
	}
}

// Stacked halves: full width each, half the height.
func TestSplitHorizontalResizesBothPanes(t *testing.T) {
	c, a := splitApp(t, false)
	if a.tree.vert {
		t.Fatal("horizontal split laid out as a Row")
	}
	waitForFrame(t, c, 2*time.Second, resizeOn(7, 80, 15))
	waitForFrame(t, c, 2*time.Second, resizeOn(8, 80, 15))
}

// A failed run leaves loom single-pane: no slot, no tree change, keys still
// reach the one pane.
func TestSplitRunFailureStaysSinglePane(t *testing.T) {
	c, a := newRunApp(t) // no scripted addresses: terminal.run refuses
	if !waitForPhase(t, a.panes["t7"].p, 2*time.Second, pane.StateAttached) {
		t.Fatal("no attach")
	}
	a.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	press(t, a, tea.KeyPressMsg{Code: 'b', Mod: tea.ModCtrl})
	press(t, a, tea.KeyPressMsg{Code: 'v', Text: "v"})
	if len(a.panes) != 1 || a.tree.leaf == nil || a.focus != "t7" {
		t.Fatalf("failed run changed the layout: panes=%v focus=%v", a.panes, a.focus)
	}
	if a.mode != modePassthrough {
		t.Fatalf("mode = %v", a.mode)
	}
	press(t, a, tea.KeyPressMsg{Code: 'x', Text: "x"})
	waitForFrame(t, c, 2*time.Second, func(f testcore.Frame) bool {
		return f.Kind == 0 && f.Ch == 7 && string(f.Payload) == "x"
	})
}

// Keys go to the focused pane only; a click inside another pane's rect
// moves the focus before anything else is decided.
func TestKeysRouteToFocusedPaneAndClickFocusesHitPane(t *testing.T) {
	c, a := splitApp(t, true)
	a.Update(tea.WindowSizeMsg{Width: 120, Height: 40}) // panes: t7 x∈[20,70), t9 x∈[70,120)

	// The split focused t9 (channel 8): 'x' lands there only.
	n := len(c.Frames())
	press(t, a, tea.KeyPressMsg{Code: 'x', Text: "x"})
	waitForFrame(t, c, 2*time.Second, func(f testcore.Frame) bool {
		return f.Kind == 0 && f.Ch == 8 && string(f.Payload) == "x"
	})
	time.Sleep(150 * time.Millisecond)
	for _, f := range c.Frames()[n:] {
		if f.Kind == 0 && f.Ch == 7 {
			t.Fatalf("key reached the unfocused pane: %q", f.Payload)
		}
	}

	// A click over t7's half focuses it; 'y' now lands on channel 7 only.
	press(t, a, tea.MouseClickMsg{X: 30, Y: 5, Button: tea.MouseLeft})
	if a.focus != "t7" {
		t.Fatalf("click focus = %v, want t7", a.focus)
	}
	n = len(c.Frames())
	press(t, a, tea.KeyPressMsg{Code: 'y', Text: "y"})
	waitForFrame(t, c, 2*time.Second, func(f testcore.Frame) bool {
		return f.Kind == 0 && f.Ch == 7 && string(f.Payload) == "y"
	})
	time.Sleep(150 * time.Millisecond)
	for _, f := range c.Frames()[n:] {
		if f.Kind == 0 && f.Ch == 8 {
			t.Fatalf("key reached the unfocused pane after focus move: %q", f.Payload)
		}
	}

	// Paste follows the focus too.
	press(t, a, tea.PasteMsg{Content: "pasted"})
	waitForFrame(t, c, 2*time.Second, func(f testcore.Frame) bool {
		return f.Kind == 0 && f.Ch == 7 && string(f.Payload) == "pasted"
	})
}

// Wheel routing and MouseOn forwarding apply to the pane under the pointer,
// not the focused one.
func TestMouseForwardsToHitPaneNotFocusedPane(t *testing.T) {
	c, a := splitApp(t, true)
	a.Update(tea.WindowSizeMsg{Width: 120, Height: 40}) // t7 x∈[20,70)
	c.DeltaCh(7, []byte("\x1b[?1000h\x1b[?1006h"))      // t7's program owns the mouse
	waitForSlotMouseOn(t, a, "t7")

	// Focus is still t9; the wheel over t7's half forwards to t7, relative
	// to its rect: screen (30,3) over x=20 is pane cell (10,3), SGR 1-based
	// (11,4), wheel-up is button 64.
	press(t, a, tea.MouseWheelMsg{X: 30, Y: 3, Button: tea.MouseWheelUp})
	waitForFrame(t, c, 2*time.Second, func(f testcore.Frame) bool {
		return f.Kind == 0 && f.Ch == 7 && strings.Contains(string(f.Payload), "\x1b[<64;11;4M")
	})

	// A wheel over the focused pane while its program owns nothing scrolls
	// locally instead: no DATA leaves on either channel.
	n := len(c.Frames())
	press(t, a, tea.MouseWheelMsg{X: 90, Y: 3, Button: tea.MouseWheelUp})
	time.Sleep(150 * time.Millisecond)
	for _, f := range c.Frames()[n:] {
		if f.Kind == 0 && (f.Ch == 7 || f.Ch == 8) {
			t.Fatalf("wheel forwarded without mouse ownership: ch=%d %q", f.Ch, f.Payload)
		}
	}
}

// The sidebar reads the multi-pane truth: the run's created event (which
// arrives on its own, after runMsg already attached) brings the record, and
// the marks separate focused from open from unopened.
func TestSidebarMarksAfterSplit(t *testing.T) {
	_, a := splitApp(t, true)
	a.Update(eventMsg{<-a.sess.Events()}) // the created event for t9
	s := sidebar(&a.room, a.focus, a.openPanes())
	lines := strings.Split(s, "\n")
	if len(lines) != 2 {
		t.Fatalf("sidebar = %q", s)
	}
	if !strings.HasPrefix(lines[0], "· vim") {
		t.Fatalf("open non-focused pane not marked: %q", lines[0])
	}
	if !strings.HasPrefix(lines[1], "▸") {
		t.Fatalf("focused pane not marked: %q", lines[1])
	}
}

// A finished pane keeps its slot and its final content; the tree never
// empty-panics and loom keeps rendering.
func TestPaneFinishKeepsSlotShowingFinalContent(t *testing.T) {
	c, a := splitApp(t, true)
	c.DeltaCh(8, []byte("final-words"))
	waitForScreenText(t, a.panes["t9"].p, 2*time.Second, "final-words")
	c.CloseStreamCh(8, `{"reason":"exited","exit_code":0}`)
	if !waitForPhase(t, a.panes["t9"].p, 2*time.Second, pane.StateExited) {
		t.Fatalf("t9 phase = %v", a.panes["t9"].p.Phase())
	}
	if _, ok := a.panes["t9"]; !ok {
		t.Fatal("finished pane lost its slot")
	}
	if !strings.Contains(plainText(a.panes["t9"].p.Screen()), "final-words") {
		t.Fatal("final content dropped on exit")
	}
	a.View() // the tree with an exited leaf still lays out
}

// The only pane exiting keeps its slot too; and before any pane ever
// attaches, the placeholder body renders instead of an empty tree.
func TestTreeNeverEmptyPanics(t *testing.T) {
	c, a := newApp(t, `[{"address":"t7","title":"vim","cols":80,"rows":24}]`)
	if !waitForPhase(t, a.panes["t7"].p, 2*time.Second, pane.StateAttached) {
		t.Fatal("no attach")
	}
	c.Delta([]byte("bye"))
	waitForScreenText(t, a.panes["t7"].p, 2*time.Second, "bye")
	c.CloseStream(`{"reason":"exited","exit_code":0}`)
	if !waitForPhase(t, a.panes["t7"].p, 2*time.Second, pane.StateExited) {
		t.Fatalf("phase = %v", a.panes["t7"].p.Phase())
	}
	if a.tree == nil || a.tree.leaf == nil {
		t.Fatal("exited only pane dropped the tree")
	}
	a.View()

	_, empty := newApp(t, `[]`)
	empty.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	if empty.tree != nil || len(empty.panes) != 0 {
		t.Fatalf("no-terminal app grew a tree: %v", empty.tree)
	}
	empty.View()
}

// Focus churn between panes swaps viewer activity on the wire: the newly
// focused pane reasserts (FOCUS 1 after its RESIZE), the old one stands
// down (FOCUS 0).
func TestFocusSwitchSwapsViewerActivity(t *testing.T) {
	c, a := splitApp(t, true)
	a.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	waitForFrame(t, c, 2*time.Second, resizeOn(8, 50, 40))

	press(t, a, tea.MouseClickMsg{X: 30, Y: 5, Button: tea.MouseLeft}) // focus t7
	waitForFrame(t, c, 2*time.Second, func(f testcore.Frame) bool {
		return f.Kind == 4 && f.Ch == 7 && len(f.Payload) == 1 && f.Payload[0] == 1
	})
	waitForFrame(t, c, 2*time.Second, func(f testcore.Frame) bool {
		return f.Kind == 4 && f.Ch == 8 && len(f.Payload) == 1 && f.Payload[0] == 0
	})

	press(t, a, tea.MouseClickMsg{X: 90, Y: 5, Button: tea.MouseLeft}) // focus t9 again
	waitForFrame(t, c, 2*time.Second, func(f testcore.Frame) bool {
		return f.Kind == 4 && f.Ch == 8 && len(f.Payload) == 1 && f.Payload[0] == 1
	})
}

// The layout tree's own unit contract: split replaces the focused leaf,
// nested splits stay binary, and leaves walk in layout order.
func TestLayoutNodeSplitAndLeaves(t *testing.T) {
	mk := func(id hc.TerminalID) *layoutNode {
		return &layoutNode{leaf: &paneSlot{id: id}}
	}
	root := mk("t7")
	if !root.split("t7", mk("t9"), true) {
		t.Fatal("split of the focused leaf failed")
	}
	if root.leaf != nil || !root.vert || root.a.leaf.id != "t7" || root.b.leaf.id != "t9" {
		t.Fatalf("tree = %+v", root)
	}
	if root.split("nope", mk("t1"), false) {
		t.Fatal("split of a missing id changed the tree")
	}
	if !root.split("t9", mk("t2"), false) {
		t.Fatal("nested split failed")
	}
	if root.b.leaf != nil || root.b.vert {
		t.Fatalf("nested tree = %+v", root.b)
	}
	var order []hc.TerminalID
	root.leaves(func(s *paneSlot) { order = append(order, s.id) })
	if len(order) != 3 || order[0] != "t7" || order[1] != "t9" || order[2] != "t2" {
		t.Fatalf("leaf order = %v, want [t7 t9 t2]", order)
	}
}

// bindRune's contract: bare printable runes bind, chords and shifted
// capitals never do.
func TestBindRune(t *testing.T) {
	for _, c := range []struct {
		k  tea.KeyPressMsg
		r  rune
		ok bool
	}{
		{tea.KeyPressMsg{Code: 'v', Text: "v"}, 'v', true},
		{tea.KeyPressMsg{Code: '-', Text: "-"}, '-', true},
		{tea.KeyPressMsg{Code: 'b', Mod: tea.ModCtrl}, 0, false},
		{tea.KeyPressMsg{Code: 'V', Text: "V", Mod: tea.ModShift}, 0, false},
		{tea.KeyPressMsg{Code: tea.KeyEnter}, 0, false},
	} {
		r, ok := bindRune(c.k)
		if ok != c.ok || r != c.r {
			t.Fatalf("bindRune(%+v) = %q,%v want %q,%v", c.k, r, ok, c.r, c.ok)
		}
	}
	if _, ok := bindings['v']; !ok {
		t.Fatal("'v' unbound")
	}
	if _, ok := bindings['-']; !ok {
		t.Fatal("'-' unbound")
	}
	if _, ok := bindings['w']; !ok {
		t.Fatal("'w' unbound")
	}
}

func resizeOn(ch uint32, cols, rows int) func(testcore.Frame) bool {
	return func(f testcore.Frame) bool {
		return f.Kind == 3 && f.Ch == ch && len(f.Payload) == 4 &&
			int(f.Payload[0]) == cols && int(f.Payload[2]) == rows
	}
}

func waitForSlotMouseOn(t *testing.T, a *App, id hc.TerminalID) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s := a.panes[id].p.Screen(); s != nil && s.MouseOn {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("pane %s never reported mouse tracking on", id)
}
