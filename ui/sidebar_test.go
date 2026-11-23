package ui

// The sidebar rail: its rendering (title, marks, badge, header) and the
// list mode (<leader> w) through the real loopback: mode transitions, the
// cursor starting on the focused pane, cursor movement with wrap, the two
// pick semantics (focus an open pane vs replace the focused slot with a
// fresh attach, CLOSE observed on the wire), key swallowing while the list
// has the keyboard, a click handing it back, the empty-room case, and the
// cursor keeping its terminal through room changes.

import (
	"strings"
	"testing"
	"time"

	"bloomfx.dev/bloom"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"loom/hc"
	"loom/internal/testcore"
	"loom/pane"
	"loom/room"
)

func roomFixture() *room.Room {
	r := &room.Room{}
	r.Apply(hc.Event{Kind: hc.EventSnapshot, All: []hc.TerminalInfo{
		{ID: "t7", Title: "vim", Cols: 80, Rows: 24},
		{ID: "t9", Title: "build", Status: "working"},
		{ID: "t3", Title: "logs", Cols: 80, Rows: 24},
	}})
	return r
}

func TestSidebarShowsTitleAndStatus(t *testing.T) {
	s := sidebar(roomFixture(), "t7", map[hc.TerminalID]bool{"t7": true}, nil)
	lines := strings.Split(s, "\n")
	if len(lines) != 4 { // header + three terminals
		t.Fatalf("sidebar = %q", s)
	}
	if lines[0] != sidebarHeader {
		t.Fatalf("header = %q", lines[0])
	}
	if !strings.Contains(lines[1], "vim") {
		t.Fatalf("vim line missing title: %q", lines[1])
	}
	if !strings.Contains(lines[1], "▸") {
		t.Fatalf("focused terminal not marked: %q", lines[1])
	}
	if !strings.Contains(lines[2], "build") || !strings.Contains(lines[2], "working") {
		t.Fatalf("status badge missing: %q", lines[2])
	}
	if strings.Contains(lines[1], "listed") {
		t.Fatalf("plain listed state badges: %q", lines[1])
	}
	// The panes have the keyboard: nothing is highlighted and no hint shows.
	if strings.Contains(s, "\x1b[7m") || strings.Contains(s, sidebarHint) {
		t.Fatalf("inactive rail highlights or hints: %q", s)
	}
}

// The focused pane reads ▸, other open panes ·, unopened terminals nothing —
// several attachments must be distinguishable at a glance.
func TestSidebarMarksOpenPanes(t *testing.T) {
	s := sidebar(roomFixture(), "t9", map[hc.TerminalID]bool{"t7": true, "t9": true}, nil)
	lines := strings.Split(s, "\n")
	if !strings.HasPrefix(lines[1], "· vim") {
		t.Fatalf("open non-focused pane not marked ·: %q", lines[1])
	}
	if !strings.HasPrefix(lines[2], "▸ build working") {
		t.Fatalf("focused pane not marked ▸: %q", lines[2])
	}
	if strings.HasPrefix(lines[3], "·") || strings.HasPrefix(lines[3], "▸") {
		t.Fatalf("unopened terminal marked: %q", lines[3])
	}
}

// Open panes come first, in room order, then the unopened ones: the one row
// order the cursor moves through.
func TestSidebarRowsOpenPanesFirst(t *testing.T) {
	open := map[hc.TerminalID]bool{"t3": true}
	if got := rowIDsOf(sidebarRows(roomFixture(), open)); len(got) != 3 || got[0] != "t3" || got[1] != "t7" || got[2] != "t9" {
		t.Fatalf("rows = %v, want the open t3 first, then t7, t9 in room order", got)
	}
}

// hc programs often set no title; the record name carries the identity then.
func TestSidebarShowsNameWhenTitleEmpty(t *testing.T) {
	vim := hc.TerminalInfo{ID: "t44", Cols: 80, Rows: 24, Name: "loomvim"}
	r := roomWith(vim)
	if got := sidebar(r, "t44", map[hc.TerminalID]bool{"t44": true}, nil); got != sidebarHeader+"\n▸ loomvim" {
		t.Fatalf("sidebar = %q, want the header and ▸ loomvim", got)
	}
}

func TestSidebarLifecycleBadge(t *testing.T) {
	r := roomFixture()
	r.Apply(hc.Event{Kind: hc.EventLost, ID: "t9"})
	s := sidebar(r, "", nil, nil)
	if !strings.Contains(s, "restarting") {
		t.Fatalf("lost terminal not badged: %q", s)
	}
	if strings.Contains(s, "▸") {
		t.Fatalf("marked focused with no pane: %q", s)
	}
}

func TestSidebarEmpty(t *testing.T) {
	if s := sidebar(&room.Room{}, "", nil, nil); s != sidebarHeader+"\nno terminals" {
		t.Fatalf("empty room sidebar = %q", s)
	}
}

// enterSidebar presses <leader> w and asserts the list took the keyboard.
func enterSidebar(t *testing.T, a *App) {
	t.Helper()
	press(t, a, tea.KeyPressMsg{Code: 'b', Mod: tea.ModCtrl})
	press(t, a, tea.KeyPressMsg{Code: 'w', Text: "w"})
	if a.mode != modeSidebar || a.nav == nil {
		t.Fatalf("mode = %v nav = %v, want the sidebar list owning the keys", a.mode, a.nav)
	}
}

func rowIDsOf(rows []room.Terminal) []hc.TerminalID {
	ids := make([]hc.TerminalID, len(rows))
	for i, tr := range rows {
		ids[i] = tr.ID
	}
	return ids
}

func roomRows(a *App) []hc.TerminalID {
	return rowIDsOf(sidebarRows(&a.room, a.openPanes()))
}

// inverseRuns lists the rail's SGR-inverse runs as plain text, padding
// kept, in order: the highlighted bars the terminal shows.
func inverseRuns(v string) []string {
	var out []string
	for rest := v; ; {
		i := strings.Index(rest, "\x1b[7m")
		if i < 0 {
			return out
		}
		rest = rest[i+len("\x1b[7m"):]
		j := strings.Index(rest, "\x1b[0m")
		if j < 0 {
			return append(out, rest)
		}
		out = append(out, rest[:j])
		rest = rest[j:]
	}
}

// <leader> w hands the keyboard to the list; esc and q both hand it back,
// and the layout renders in both states with the rail beside the panes.
func TestSidebarEnterAndLeaveTransitions(t *testing.T) {
	_, a := newApp(t, `[{"address":"t7","title":"vim","cols":80,"rows":24}]`)
	if !waitForPhase(t, a.panes["t7"].p, 2*time.Second, pane.StateAttached) {
		t.Fatal("no attach")
	}
	enterSidebar(t, a)
	if got := roomRows(a); len(got) != 1 || got[0] != "t7" {
		t.Fatalf("rows = %v, want just the open t7", got)
	}
	if a.nav.sel != "t7" {
		t.Fatalf("cursor = %q, want the focused t7", a.nav.sel)
	}
	a.View()

	press(t, a, tea.KeyPressMsg{Code: tea.KeyEscape})
	if a.mode != modePassthrough || a.nav != nil {
		t.Fatalf("esc left mode = %v nav = %v", a.mode, a.nav)
	}
	a.View()

	enterSidebar(t, a)
	press(t, a, tea.KeyPressMsg{Code: 'q', Text: "q"})
	if a.mode != modePassthrough || a.nav != nil {
		t.Fatalf("q left mode = %v nav = %v", a.mode, a.nav)
	}
}

// The cursor starts on the focused pane's row, then j/k and the arrows move
// it; the movement wraps at both ends.
func TestSidebarCursorStartsFocusedAndWraps(t *testing.T) {
	_, a := newApp(t, `[{"address":"t7","title":"vim","cols":80,"rows":24},{"address":"t44","title":"spare","cols":80,"rows":24},{"address":"t45","title":"other","cols":80,"rows":24}]`)
	if !waitForPhase(t, a.panes["t7"].p, 2*time.Second, pane.StateAttached) {
		t.Fatal("no attach")
	}
	enterSidebar(t, a)
	// Open panes first, then the rest, stable: t7 is the only open one, and
	// the focused one.
	if got := roomRows(a); len(got) != 3 || got[0] != "t7" || got[1] != "t44" || got[2] != "t45" {
		t.Fatalf("rows = %v, want open-first [t7 t44 t45]", got)
	}
	for _, step := range []struct {
		key  tea.KeyPressMsg
		want hc.TerminalID
	}{
		{tea.KeyPressMsg{Code: 'j', Text: "j"}, "t44"},
		{tea.KeyPressMsg{Code: tea.KeyDown}, "t45"},
		{tea.KeyPressMsg{Code: 'j', Text: "j"}, "t7"},  // wraps down past the end
		{tea.KeyPressMsg{Code: 'k', Text: "k"}, "t45"}, // wraps up past the top
		{tea.KeyPressMsg{Code: tea.KeyUp}, "t44"},
	} {
		press(t, a, step.key)
		if a.nav.sel != step.want {
			t.Fatalf("cursor after %v = %q, want %q", step.key, a.nav.sel, step.want)
		}
	}
}

// With several panes open the cursor lands on the focused one, not the top.
// Enter on an already-open terminal focuses its pane and swaps nothing: no
// new pane, no slot replaced, no CLOSE on the wire.
func TestSidebarEnterOnOpenPaneFocusesItSwapsNothing(t *testing.T) {
	c, a := splitApp(t, true)             // t7 (ch7) + t9 (ch8), focus on t9
	a.Update(eventMsg{<-a.sess.Events()}) // the run's created event: the room learns t9
	enterSidebar(t, a)
	if got := roomRows(a); len(got) != 2 || got[0] != "t7" || got[1] != "t9" {
		t.Fatalf("rows = %v, want both open panes first", got)
	}
	if a.nav.sel != "t9" {
		t.Fatalf("cursor = %q on entry, want the focused t9", a.nav.sel)
	}
	press(t, a, tea.KeyPressMsg{Code: 'k', Text: "k"}) // cursor on t7
	press(t, a, tea.KeyPressMsg{Code: tea.KeyEnter})
	if a.mode != modePassthrough || a.nav != nil {
		t.Fatalf("pick left mode = %v nav = %v", a.mode, a.nav)
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

// Enter on a fresh terminal replaces the focused slot: the new pane
// attaches into the same leaf, the old pane's stream CLOSEs (observed on
// the wire) and its machine walks back to idle, and the tree keeps its
// shape.
func TestSidebarEnterOnFreshReplacesFocusedSlot(t *testing.T) {
	c, a := splitApp(t, true)             // t7 (ch7) + t9 (ch8), focus on t9
	a.Update(eventMsg{<-a.sess.Events()}) // the run's created event: the room learns t9
	ev := sendCoreEvent(t, c, a.sess, 10, "created", "t44", `{"address":"t44","title":"spare","cols":80,"rows":24}`)
	a.Update(eventMsg{ev})

	enterSidebar(t, a)
	if got := roomRows(a); len(got) != 3 || got[2] != "t44" {
		t.Fatalf("rows = %v, want t44 after the two open panes", got)
	}
	press(t, a, tea.KeyPressMsg{Code: 'j', Text: "j"}) // cursor on t44, one past the focused t9
	if a.nav.sel != "t44" {
		t.Fatalf("cursor = %q, want t44", a.nav.sel)
	}
	old := a.panes["t9"]
	press(t, a, tea.KeyPressMsg{Code: tea.KeyEnter})

	if a.mode != modePassthrough || a.nav != nil {
		t.Fatalf("pick left mode = %v nav = %v", a.mode, a.nav)
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

// While the list has the keyboard every key is swallowed: no DATA reaches
// the panes, ctrl+b does not nest (a following 'v' never splits), and keys
// flow again the moment it hands them back.
func TestSidebarSwallowsKeys(t *testing.T) {
	c, a := newApp(t, `[{"address":"t7","title":"vim","cols":80,"rows":24}]`)
	if !waitForPhase(t, a.panes["t7"].p, 2*time.Second, pane.StateAttached) {
		t.Fatal("no attach")
	}
	enterSidebar(t, a)
	press(t, a, tea.KeyPressMsg{Code: 'j', Text: "j"})
	press(t, a, tea.KeyPressMsg{Code: 'k', Text: "k"})
	press(t, a, tea.KeyPressMsg{Code: 'x', Text: "x"})
	press(t, a, tea.KeyPressMsg{Code: 'b', Mod: tea.ModCtrl}) // not nested
	press(t, a, tea.KeyPressMsg{Code: 'v', Text: "v"})        // no split through the list
	press(t, a, tea.KeyPressMsg{Code: 'w', Text: "w"})
	press(t, a, tea.KeyPressMsg{Code: tea.KeyPgUp}) // scrollback stays behind it too
	if a.mode != modeSidebar {
		t.Fatalf("mode = %v, want the list still owning keys", a.mode)
	}
	if got := len(c.Runs()); got != 0 {
		t.Fatalf("terminal.run calls = %d, want 0", got)
	}
	time.Sleep(150 * time.Millisecond)
	for _, f := range c.Frames() {
		if f.Kind == 0 && f.Ch == 7 {
			t.Fatalf("key leaked to the pane while the list had the keyboard: %q", f.Payload)
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
func TestSidebarEmptyRoom(t *testing.T) {
	_, a := newApp(t, `[]`)
	enterSidebar(t, a)
	if a.nav.sel != "" {
		t.Fatalf("cursor = %q over an empty room, want none", a.nav.sel)
	}
	v := sidebar(&a.room, a.focus, a.openPanes(), a.nav)
	if !strings.Contains(v, "no terminals") {
		t.Fatalf("rail = %q, want the empty-room line", v)
	}
	a.View() // the rail renders over the placeholder body too

	press(t, a, tea.KeyPressMsg{Code: 'q', Text: "q"})
	if a.mode != modeSidebar {
		t.Fatal("q left an empty list; only esc should")
	}
	press(t, a, tea.KeyPressMsg{Code: 'j', Text: "j"})
	press(t, a, tea.KeyPressMsg{Code: tea.KeyEnter})
	if a.mode != modeSidebar || a.tree != nil {
		t.Fatalf("enter built something from nothing: mode = %v tree = %v", a.mode, a.tree)
	}
	press(t, a, tea.KeyPressMsg{Code: tea.KeyEscape})
	if a.mode != modePassthrough || a.nav != nil {
		t.Fatalf("esc left mode = %v nav = %v", a.mode, a.nav)
	}
}

// The list is live while it has the keyboard: created and removed events
// change the rows, and the cursor keeps the terminal it was on through a
// reorder or growth; a removed one falls back to the top.
func TestSidebarCursorKeepsTerminalThroughRoomChanges(t *testing.T) {
	c, a := newApp(t, `[{"address":"t7","title":"vim","cols":80,"rows":24}]`)
	enterSidebar(t, a)
	if got := roomRows(a); len(got) != 1 {
		t.Fatalf("rows = %v", got)
	}

	ev := sendCoreEvent(t, c, a.sess, 10, "created", "t44", `{"address":"t44","title":"spare","cols":80,"rows":24}`)
	a.Update(eventMsg{ev})
	if got := roomRows(a); len(got) != 2 || got[1] != "t44" {
		t.Fatalf("rows after created = %v", got)
	}

	press(t, a, tea.KeyPressMsg{Code: 'j', Text: "j"}) // cursor on t44
	ev = sendCoreEvent(t, c, a.sess, 11, "created", "t45", `{"address":"t45","title":"other","cols":80,"rows":24}`)
	a.Update(eventMsg{ev})
	if got := roomRows(a); len(got) != 3 {
		t.Fatalf("rows after second created = %v", got)
	}
	if a.nav.sel != "t44" {
		t.Fatalf("cursor = %q, want to stay on t44 through the refresh", a.nav.sel)
	}

	// A new open pane jumps the queue ahead of the cursor's row: the cursor
	// stays on t44, and j from the now-last row wraps to the top instead of
	// landing on t45.
	a.panes["t45"] = &paneSlot{id: "t45"} // render-only: the sort reads the panes map
	if got := roomRows(a); len(got) != 3 || got[0] != "t7" || got[1] != "t45" {
		t.Fatalf("rows with t45 open = %v, want the open panes t7, t45 first", got)
	}
	if a.nav.sel != "t44" {
		t.Fatalf("cursor = %q after the reorder, want t44", a.nav.sel)
	}
	press(t, a, tea.KeyPressMsg{Code: 'j', Text: "j"}) // past the end wraps to the top: t7
	if a.nav.sel != "t7" {
		t.Fatalf("cursor = %q after j from the last row, want the top t7", a.nav.sel)
	}
	press(t, a, tea.KeyPressMsg{Code: 'k', Text: "k"}) // back to t44
	if a.nav.sel != "t44" {
		t.Fatalf("cursor = %q after k, want t44", a.nav.sel)
	}
	delete(a.panes, "t45")

	ev = sendCoreEvent(t, c, a.sess, 13, "removed", "t44", `{}`)
	a.Update(eventMsg{ev})
	if got := roomRows(a); len(got) != 2 || got[0] != "t7" || got[1] != "t45" {
		t.Fatalf("rows after removed = %v", got)
	}
	if a.nav.sel != "t7" { // the terminal under the cursor is gone: top
		t.Fatalf("cursor = %q, want the top row t7 after the row vanished", a.nav.sel)
	}
}

// A click anywhere hands the keyboard back first, then lands with
// passthrough semantics: over a pane it focuses that pane, over the rail it
// changes nothing. Keys reach the focused pane afterwards.
func TestSidebarClickHandsKeyboardBack(t *testing.T) {
	c, a := splitApp(t, true)
	a.Update(tea.WindowSizeMsg{Width: 120, Height: 40}) // t7 x∈[20,70), t9 x∈[70,120)
	a.Update(eventMsg{<-a.sess.Events()})               // the run's created event: the room learns t9
	enterSidebar(t, a)
	if a.nav.sel != "t9" {
		t.Fatalf("cursor = %q on entry, want the focused t9", a.nav.sel)
	}

	press(t, a, tea.MouseClickMsg{X: 30, Y: 5, Button: tea.MouseLeft}) // over t7
	if a.mode != modePassthrough || a.nav != nil {
		t.Fatalf("click left mode = %v nav = %v", a.mode, a.nav)
	}
	if a.focus != "t7" {
		t.Fatalf("click focus = %v, want t7", a.focus)
	}
	press(t, a, tea.KeyPressMsg{Code: 'x', Text: "x"})
	waitForFrame(t, c, 2*time.Second, func(f testcore.Frame) bool {
		return f.Kind == 0 && f.Ch == 7 && string(f.Payload) == "x"
	})

	enterSidebar(t, a)
	press(t, a, tea.MouseClickMsg{X: 5, Y: 5, Button: tea.MouseLeft}) // over the rail: no pane there
	if a.mode != modePassthrough || a.nav != nil {
		t.Fatalf("rail click left mode = %v nav = %v", a.mode, a.nav)
	}
	if a.focus != "t7" {
		t.Fatalf("rail click moved focus to %v", a.focus)
	}
	n := len(c.Frames())
	press(t, a, tea.KeyPressMsg{Code: 'y', Text: "y"})
	waitForFrame(t, c, 2*time.Second, func(f testcore.Frame) bool {
		return f.Kind == 0 && f.Ch == 7 && string(f.Payload) == "y"
	})
	time.Sleep(100 * time.Millisecond)
	for _, f := range c.Frames()[n:] {
		if f.Kind == 0 && f.Ch == 8 {
			t.Fatalf("key reached the unfocused pane: %q", f.Payload)
		}
	}
}

// The rail carries the contract's columns: header, open marks (▸ focused,
// · open elsewhere, blank unopened), title with name fallback, and the OSC
// 7501 status badge. With the list active the header and the cursor row
// are inverse bars the width of the rail and the hint shows; ▸ follows the
// pane focus, the bar follows the cursor.
func TestSidebarViewRendersMarksBadgeAndCursor(t *testing.T) {
	c, a := newApp(t, `[{"address":"t7","title":"vim","cols":80,"rows":24},{"address":"t44","name":"loomspare","cols":80,"rows":24}]`)
	ev := sendCoreEvent(t, c, a.sess, 10, "status", "t44", `{"status":{"state":"building"}}`)
	a.Update(eventMsg{ev})

	enterSidebar(t, a)
	v := sidebar(&a.room, a.focus, a.openPanes(), a.nav)
	lines := strings.Split(ansi.Strip(v), "\n")
	if len(lines) != 4 { // header, two rows, hint
		t.Fatalf("view = %q", v)
	}
	if got := strings.TrimSpace(lines[0]); got != sidebarHeader {
		t.Fatalf("header = %q", lines[0])
	}
	if !strings.Contains(lines[1], "▸ vim") {
		t.Fatalf("focused row lacks ▸: %q", lines[1])
	}
	if !strings.Contains(lines[2], "loomspare building") || strings.Contains(lines[2], "·") {
		t.Fatalf("unopened row wants the name fallback and badge, no open mark: %q", lines[2])
	}
	if !strings.Contains(lines[3], "esc") {
		t.Fatalf("hint = %q", lines[3])
	}
	// Highlighted: the header and the cursor row (on the focused vim), each
	// a bar the rail's width; and both survive bloom's render.
	if got := inverseRuns(v); len(got) != 2 || strings.TrimSpace(got[0]) != sidebarHeader || strings.TrimSpace(got[1]) != "▸ vim" {
		t.Fatalf("inverse runs = %q, want the header and the cursor row", got)
	}
	for _, run := range inverseRuns(v) {
		if ansi.StringWidth(run) != sidebarWidth {
			t.Fatalf("inverse bar %q is %d cells, want the rail's %d", run, ansi.StringWidth(run), sidebarWidth)
		}
	}
	out := bloom.Render(sidebarWidth, 4, bloom.Fixed(sidebarWidth, bloom.Text(v)))
	if got := strings.Count(out, "\x1b[7m"); got != 2 {
		t.Fatalf("bloom rendered %d inverse runs, want 2:\n%q", got, out)
	}

	// A second open pane renders ·; the bar follows the cursor, ▸ the focus.
	press(t, a, tea.KeyPressMsg{Code: tea.KeyEscape})
	a.panes["t44"] = &paneSlot{id: "t44"} // render-only: the marker reads the panes map
	enterSidebar(t, a)
	press(t, a, tea.KeyPressMsg{Code: 'j', Text: "j"}) // cursor on the · row
	v = sidebar(&a.room, a.focus, a.openPanes(), a.nav)
	if plain := ansi.Strip(v); !strings.Contains(plain, "▸ vim") || !strings.Contains(plain, "· loomspare") {
		t.Fatalf("marks wrong with a second open pane: %q", plain)
	}
	if got := inverseRuns(v); len(got) != 2 || strings.TrimSpace(got[1]) != "· loomspare building" {
		t.Fatalf("inverse runs = %q, want the bar on the · row", got)
	}
}

// A paste is keys: while the sidebar owns them it is swallowed, not typed
// into the focused pane. A leak would show as DATA on the focused pane's
// stream channel.
func TestSidebarSwallowsPaste(t *testing.T) {
	c, a := splitApp(t, true) // t7 (ch7) + t9 (ch8), focus on t9
	enterSidebar(t, a)
	a.Update(tea.PasteMsg{Content: "leaked"})
	time.Sleep(150 * time.Millisecond)
	if c.Has(func(f testcore.Frame) bool { return f.Kind == 0 && f.Ch == 8 }) {
		t.Fatal("paste reached the focused pane while the sidebar owned keys")
	}
}
