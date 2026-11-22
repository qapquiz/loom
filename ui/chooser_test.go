package ui

// The window chooser (<leader> w) through the real loopback: mode
// transitions, cursor movement with wrap, the two pick semantics (focus an
// open pane vs replace the focused slot with a fresh attach, CLOSE observed
// on the wire), key swallowing while open, the empty-room case, and the
// live refresh on room changes.

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"loom/hc"
	"loom/internal/testcore"
	"loom/pane"
)

// openChooser presses <leader> w and asserts the mode landed.
func openChooser(t *testing.T, a *App) {
	t.Helper()
	press(t, a, tea.KeyPressMsg{Code: 'b', Mod: tea.ModCtrl})
	press(t, a, tea.KeyPressMsg{Code: 'w', Text: "w"})
	if a.mode != modeChooser || a.chooser == nil {
		t.Fatalf("mode = %v chooser = %v, want the chooser open", a.mode, a.chooser)
	}
}

func chooserRowIDs(c *chooserState) []hc.TerminalID {
	ids := make([]hc.TerminalID, len(c.rows))
	for i, t := range c.rows {
		ids[i] = t.ID
	}
	return ids
}

// <leader> w opens the chooser; esc and q both close it back to passthrough,
// and the layout renders with the layer up (panes beneath, chooser over).
func TestChooserOpenCloseTransitions(t *testing.T) {
	_, a := newRunApp(t, "t9")
	if !waitForPhase(t, a.panes["t7"].p, 2*time.Second, pane.StateAttached) {
		t.Fatal("no attach")
	}
	openChooser(t, a)
	if got := chooserRowIDs(a.chooser); len(got) != 1 || got[0] != "t7" {
		t.Fatalf("rows = %v, want just the open t7", got)
	}
	a.View() // the overlay renders over the pane area

	press(t, a, tea.KeyPressMsg{Code: tea.KeyEscape})
	if a.mode != modePassthrough || a.chooser != nil {
		t.Fatalf("esc left mode = %v chooser = %v", a.mode, a.chooser)
	}
	a.View()

	openChooser(t, a)
	press(t, a, tea.KeyPressMsg{Code: 'q', Text: "q"})
	if a.mode != modePassthrough || a.chooser != nil {
		t.Fatalf("q left mode = %v chooser = %v", a.mode, a.chooser)
	}
}

// j/k and the arrows move the cursor; the movement wraps at both ends.
func TestChooserCursorMovesAndWraps(t *testing.T) {
	_, a := newApp(t, `[{"address":"t7","title":"vim","cols":80,"rows":24},{"address":"t44","title":"spare","cols":80,"rows":24},{"address":"t45","title":"other","cols":80,"rows":24}]`)
	if !waitForPhase(t, a.panes["t7"].p, 2*time.Second, pane.StateAttached) {
		t.Fatal("no attach")
	}
	openChooser(t, a)
	// Open panes first, then the rest, stable: t7 is the only open one.
	if got := chooserRowIDs(a.chooser); len(got) != 3 || got[0] != "t7" || got[1] != "t44" || got[2] != "t45" {
		t.Fatalf("rows = %v, want open-first [t7 t44 t45]", got)
	}
	for _, step := range []struct {
		key  tea.KeyPressMsg
		want int
	}{
		{tea.KeyPressMsg{Code: 'j', Text: "j"}, 1},
		{tea.KeyPressMsg{Code: tea.KeyDown}, 2},
		{tea.KeyPressMsg{Code: 'j', Text: "j"}, 0}, // wraps down past the end
		{tea.KeyPressMsg{Code: 'k', Text: "k"}, 2}, // wraps up past the top
		{tea.KeyPressMsg{Code: tea.KeyUp}, 1},
	} {
		press(t, a, step.key)
		if a.chooser.cursor != step.want {
			t.Fatalf("cursor after %v = %d, want %d", step.key, a.chooser.cursor, step.want)
		}
	}
}

// Picking a terminal that is already open focuses its pane and swaps
// nothing: no new pane, no slot replaced, no CLOSE on the wire.
func TestChooserPickAlreadyOpenFocusesPaneSwapsNothing(t *testing.T) {
	c, a := splitApp(t, true)             // t7 (ch7) + t9 (ch8), focus on t9
	a.Update(eventMsg{<-a.sess.Events()}) // the run's created event: the room learns t9
	openChooser(t, a)
	if got := chooserRowIDs(a.chooser); len(got) != 2 || got[0] != "t7" || got[1] != "t9" {
		t.Fatalf("rows = %v, want both open panes first", got)
	}
	// Cursor starts on t7 (row 0); t9 is the focused pane, t7 is not.
	press(t, a, tea.KeyPressMsg{Code: tea.KeyEnter})
	if a.mode != modePassthrough || a.chooser != nil {
		t.Fatalf("pick left mode = %v chooser = %v", a.mode, a.chooser)
	}
	if a.focus != "t7" {
		t.Fatalf("focus = %v, want t7", a.focus)
	}
	if len(a.panes) != 2 || a.panes["t7"] == nil || a.panes["t9"] == nil {
		t.Fatalf("panes = %v, want both untouched", a.panes)
	}
	// The focus move is real on the wire: t7 reasserts FOCUS 1.
	waitForFrame(t, c, 2*time.Second, func(f testcore.Frame) bool {
		return f.Kind == 4 && f.Ch == 7 && len(f.Payload) == 1 && f.Payload[0] == 1
	})
	// Nothing detached: no CLOSE ever left the viewer.
	time.Sleep(150 * time.Millisecond)
	for _, f := range c.Frames() {
		if f.Kind == 2 {
			t.Fatalf("pick of an open pane closed a stream: ch=%d", f.Ch)
		}
	}
}

// Picking a fresh terminal replaces the focused slot: the new pane attaches
// into the same leaf, the old pane's stream CLOSEs (observed on the wire)
// and its machine walks back to idle, and the tree keeps its shape.
func TestChooserPickFreshReplacesFocusedSlot(t *testing.T) {
	c, a := splitApp(t, true)             // t7 (ch7) + t9 (ch8), focus on t9
	a.Update(eventMsg{<-a.sess.Events()}) // the run's created event: the room learns t9
	ev := sendCoreEvent(t, c, a.sess, 10, "created", "t44", `{"address":"t44","title":"spare","cols":80,"rows":24}`)
	a.Update(eventMsg{ev})

	openChooser(t, a)
	if got := chooserRowIDs(a.chooser); len(got) != 3 || got[2] != "t44" {
		t.Fatalf("rows = %v, want t44 after the two open panes", got)
	}
	press(t, a, tea.KeyPressMsg{Code: 'j', Text: "j"})
	press(t, a, tea.KeyPressMsg{Code: 'j', Text: "j"}) // cursor on t44
	old := a.panes["t9"]
	press(t, a, tea.KeyPressMsg{Code: tea.KeyEnter})

	if a.mode != modePassthrough || a.chooser != nil {
		t.Fatalf("pick left mode = %v chooser = %v", a.mode, a.chooser)
	}
	if a.focus != "t44" {
		t.Fatalf("focus = %v, want t44", a.focus)
	}
	if len(a.panes) != 2 || a.panes["t7"] == nil || a.panes["t44"] == nil || a.panes["t9"] != nil {
		t.Fatalf("panes = %v, want t7 and t44 only", a.panes)
	}
	// The tree keeps its shape: t44 took t9's leaf, not a new split.
	if a.tree == nil || a.tree.leaf != nil || !a.tree.vert || a.tree.a.leaf.id != "t7" || a.tree.b.leaf.id != "t44" {
		t.Fatalf("tree = %+v, want t44 in t9's old half", a.tree)
	}
	// The old pane detached cleanly: CLOSE on its channel, machine to idle.
	waitForFrame(t, c, 2*time.Second, func(f testcore.Frame) bool {
		return f.Kind == 2 && f.Ch == 8
	})
	if !waitForPhase(t, old.p, 2*time.Second, pane.StateIdle) {
		t.Fatalf("detached pane phase = %v, want idle", old.p.Phase())
	}
	// The new pane attaches into the slot and takes its size.
	if !waitForPhase(t, a.panes["t44"].p, 2*time.Second, pane.StateAttached) {
		t.Fatalf("t44 phase = %v", a.panes["t44"].p.Phase())
	}
	waitForFrame(t, c, 2*time.Second, resizeOn(9, 40, 30))
	// A pick never runs a program: the split's one run is all there is.
	if got := len(c.Runs()); got != 1 {
		t.Fatalf("terminal.run calls = %d, want 1", got)
	}
}

// While the chooser is open every key is swallowed: no DATA reaches the
// panes, ctrl+b does not nest (a following 'v' never splits), and keys flow
// again the moment it closes.
func TestChooserSwallowsKeys(t *testing.T) {
	c, a := newRunApp(t, "t9")
	if !waitForPhase(t, a.panes["t7"].p, 2*time.Second, pane.StateAttached) {
		t.Fatal("no attach")
	}
	openChooser(t, a)
	press(t, a, tea.KeyPressMsg{Code: 'j', Text: "j"})
	press(t, a, tea.KeyPressMsg{Code: 'k', Text: "k"})
	press(t, a, tea.KeyPressMsg{Code: 'x', Text: "x"})
	press(t, a, tea.KeyPressMsg{Code: 'b', Mod: tea.ModCtrl}) // not nested
	press(t, a, tea.KeyPressMsg{Code: 'v', Text: "v"})        // no split through the chooser
	press(t, a, tea.KeyPressMsg{Code: 'w', Text: "w"})
	press(t, a, tea.KeyPressMsg{Code: tea.KeyPgUp}) // scrollback stays behind it too
	if a.mode != modeChooser {
		t.Fatalf("mode = %v, want the chooser still owning keys", a.mode)
	}
	if got := len(c.Runs()); got != 0 {
		t.Fatalf("terminal.run calls = %d, want 0", got)
	}
	time.Sleep(150 * time.Millisecond)
	for _, f := range c.Frames() {
		if f.Kind == 0 && f.Ch == 7 {
			t.Fatalf("key leaked to the pane while the chooser was open: %q", f.Payload)
		}
	}

	press(t, a, tea.KeyPressMsg{Code: tea.KeyEscape})
	press(t, a, tea.KeyPressMsg{Code: 'x', Text: "x"})
	waitForFrame(t, c, 2*time.Second, func(f testcore.Frame) bool {
		return f.Kind == 0 && f.Ch == 7 && string(f.Payload) == "x"
	})
}

// An empty room shows "no terminals"; only esc leaves, movement and pick
// are dead; the first pick-less close returns to passthrough with no tree.
func TestChooserEmptyRoom(t *testing.T) {
	_, a := newApp(t, `[]`)
	openChooser(t, a)
	if len(a.chooser.rows) != 0 || a.chooser.view(a) != "no terminals" {
		t.Fatalf("rows = %v view = %q, want the empty-room shape", a.chooser.rows, a.chooser.view(a))
	}
	a.View() // the overlay renders over the placeholder body too

	press(t, a, tea.KeyPressMsg{Code: 'q', Text: "q"})
	if a.mode != modeChooser {
		t.Fatal("q closed an empty chooser; only esc should")
	}
	press(t, a, tea.KeyPressMsg{Code: 'j', Text: "j"})
	press(t, a, tea.KeyPressMsg{Code: tea.KeyEnter})
	if a.mode != modeChooser || a.tree != nil {
		t.Fatalf("enter built something from nothing: mode = %v tree = %v", a.mode, a.tree)
	}
	press(t, a, tea.KeyPressMsg{Code: tea.KeyEscape})
	if a.mode != modePassthrough || a.chooser != nil {
		t.Fatalf("esc left mode = %v chooser = %v", a.mode, a.chooser)
	}
}

// The list is live while open: created and removed events refresh the rows,
// and the cursor follows the terminal it was on through a refresh.
func TestChooserRefreshesOnRoomChangesWhileOpen(t *testing.T) {
	c, a := newApp(t, `[{"address":"t7","title":"vim","cols":80,"rows":24}]`)
	openChooser(t, a)
	if got := chooserRowIDs(a.chooser); len(got) != 1 {
		t.Fatalf("rows = %v", got)
	}

	ev := sendCoreEvent(t, c, a.sess, 10, "created", "t44", `{"address":"t44","title":"spare","cols":80,"rows":24}`)
	a.Update(eventMsg{ev})
	if got := chooserRowIDs(a.chooser); len(got) != 2 || got[1] != "t44" {
		t.Fatalf("rows after created = %v", got)
	}

	press(t, a, tea.KeyPressMsg{Code: 'j', Text: "j"}) // cursor on t44
	ev = sendCoreEvent(t, c, a.sess, 11, "created", "t45", `{"address":"t45","title":"other","cols":80,"rows":24}`)
	a.Update(eventMsg{ev})
	if got := chooserRowIDs(a.chooser); len(got) != 3 {
		t.Fatalf("rows after second created = %v", got)
	}
	if a.chooser.cursor != 1 || a.chooser.rows[a.chooser.cursor].ID != "t44" {
		t.Fatalf("cursor = %d, want to stay on t44 through the refresh", a.chooser.cursor)
	}

	ev = sendCoreEvent(t, c, a.sess, 12, "removed", "t44", `{}`)
	a.Update(eventMsg{ev})
	if got := chooserRowIDs(a.chooser); len(got) != 2 || got[0] != "t7" || got[1] != "t45" {
		t.Fatalf("rows after removed = %v", got)
	}
	if a.chooser.cursor != 0 { // the terminal under the cursor is gone: top
		t.Fatalf("cursor = %d, want 0 after the row vanished", a.chooser.cursor)
	}
}

// The rendered body carries the contract's columns: cursor marker, open
// marks (▸ focused, · open elsewhere, blank unopened), title with name
// fallback, and the OSC 7501 status badge.
func TestChooserViewRendersMarksAndBadge(t *testing.T) {
	c, a := newApp(t, `[{"address":"t7","title":"vim","cols":80,"rows":24},{"address":"t44","name":"loomspare","cols":80,"rows":24}]`)
	ev := sendCoreEvent(t, c, a.sess, 10, "status", "t44", `{"status":{"state":"building"}}`)
	a.Update(eventMsg{ev})

	openChooser(t, a)
	v := a.chooser.view(a)
	lines := strings.Split(v, "\n")
	if len(lines) != 4 { // header, two rows, hint
		t.Fatalf("view = %q", v)
	}
	if lines[0] != "windows" {
		t.Fatalf("header = %q", lines[0])
	}
	if !strings.Contains(lines[1], "> ▸ vim") {
		t.Fatalf("focused row lacks cursor and ▸: %q", lines[1])
	}
	if !strings.Contains(lines[2], "loomspare building") || strings.Contains(lines[2], "·") {
		t.Fatalf("unopened row wants the name fallback and badge, no open mark: %q", lines[2])
	}
	if !strings.Contains(lines[3], "esc") {
		t.Fatalf("hint = %q", lines[3])
	}

	// A second open pane renders ·; the ▸ follows the focus, not the cursor.
	press(t, a, tea.KeyPressMsg{Code: tea.KeyEscape})
	a.panes["t44"] = &paneSlot{id: "t44"} // render-only: the marker reads the panes map
	openChooser(t, a)
	press(t, a, tea.KeyPressMsg{Code: 'j', Text: "j"}) // cursor on the · row
	v = a.chooser.view(a)
	if !strings.Contains(v, "▸ vim") || !strings.Contains(v, "· loomspare") {
		t.Fatalf("marks wrong with a second open pane: %q", v)
	}
	if !strings.Contains(v, "> ·") {
		t.Fatalf("cursor row lacks the > column over a · row: %q", v)
	}
}
