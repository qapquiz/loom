package ui

// The window chooser (<leader> w): a bloom Overlay layer over the pane area
// listing every room terminal, one cursor, and the pick semantics. The mode
// enum owns key routing (app.key routes modeChooser here before anything
// reaches a pane); chooserState is data only — cursor and a row snapshot,
// no parallel booleans.

import (
	"sort"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"loom/hc"
	"loom/pane"
	"loom/room"
)

// chooserDim is the backdrop strength while the chooser is open: light
// enough that the panes keep reading beneath it.
const chooserDim = 0.35

// chooserState is the chooser's whole domain: the cursor index and a row
// snapshot sorted open-panes-first (stable), the room's own row shape so
// the badge can honor lifecycle state. The snapshot refreshes on open and
// on every room change while open; markers are computed at render from
// the panes map, so a focus move under an open chooser re-renders correctly
// without touching the snapshot.
type chooserState struct {
	cursor int
	rows   []room.Terminal
}

// refresh rebuilds the row snapshot: the room's terminals, open panes
// first, stable within each group. The cursor follows the terminal it was
// on; when that terminal is gone it falls back to the top.
func (c *chooserState) refresh(r *room.Room, open map[hc.TerminalID]bool) {
	keep := hc.TerminalID("")
	if c.cursor < len(c.rows) {
		keep = c.rows[c.cursor].ID
	}
	ts := r.Terminals()
	sort.SliceStable(ts, func(i, j int) bool { return open[ts[i].ID] && !open[ts[j].ID] })
	c.rows = ts
	c.cursor = 0
	for i, t := range c.rows {
		if t.ID == keep {
			c.cursor = i
			break
		}
	}
}

// move steps the cursor by d rows and wraps at both ends: one motion, one
// list, no dead keys.
func (c *chooserState) move(d int) {
	n := len(c.rows)
	if n == 0 {
		return
	}
	c.cursor = ((c.cursor+d)%n + n) % n
}

// chooserCursorCol prefixes the row under the cursor; every other row gets
// the blank of the same width so the markers stay in one column.
const chooserCursorCol = "> "

// chooserHeader and chooserHint frame the list: what it is, how to drive it.
const (
	chooserHeader = "windows"
	chooserHint   = "j/k move · enter open · esc close"
)

// chooserInverse is the cursor row's style; chooserFaint dims the hint.
var (
	chooserInverse = styleState{attr: pane.AttrInverse}
	chooserFaint   = styleState{attr: pane.AttrFaint}
)

// view renders the chooser body as one ANSI string: a header, one row per
// room terminal, and a hint footer. Each row is the cursor column, the
// open-in-pane marker (▸ focused pane, · open in another pane, blank
// otherwise), the title falling back to the name, and the OSC 7501 status
// word when set. The cursor row renders inverse, padded to the list width
// so the bar spans the box; bloom.Text parses the SGR, the same contract
// PaneNode's frames ride.
func (c *chooserState) view(a *App) string {
	if len(c.rows) == 0 {
		return "no terminals"
	}
	type rowLine struct {
		text string
		w    int
	}
	rows := make([]rowLine, 0, len(c.rows))
	for _, t := range c.rows {
		mark := "  "
		if _, open := a.panes[t.ID]; open {
			mark = "· "
		}
		if t.ID == a.focus {
			mark = "▸ "
		}
		title := t.Info.Title
		if title == "" {
			title = t.Info.Name
		}
		txt := mark + title
		if badge := badgeOf(t); badge != "" {
			txt += " " + badge // the status word, as the sidebar renders it
		}
		rows = append(rows, rowLine{txt, utf8.RuneCountInString(txt)})
	}
	w := len(chooserHeader)
	if n := len(chooserHint); n > w {
		w = n
	}
	for _, r := range rows {
		if n := r.w + len(chooserCursorCol); n > w {
			w = n
		}
	}
	b := make([]byte, 0, (len(rows)+2)*(w+8))
	b = append(b, chooserHeader...)
	b = append(b, '\n')
	for i, r := range rows {
		if i == c.cursor {
			b = appendSGR(b, chooserInverse)
		}
		col := "  "
		if i == c.cursor {
			col = chooserCursorCol
		}
		b = append(b, col...)
		b = append(b, r.text...)
		for pad := r.w + len(col); pad < w; pad++ {
			b = append(b, ' ')
		}
		if i == c.cursor {
			b = append(b, "\x1b[0m"...)
		}
		b = append(b, '\n')
	}
	b = appendSGR(b, chooserFaint)
	b = append(b, chooserHint...)
	b = append(b, "\x1b[0m"...)
	return string(b)
}

// openChooser enters the chooser mode over the room's current truth.
func (a *App) openChooser() {
	a.mode = modeChooser
	c := &chooserState{}
	c.refresh(&a.room, a.openPanes())
	a.chooser = c
}

// closeChooser leaves the chooser mode; keys return to the panes.
func (a *App) closeChooser() {
	a.mode = modePassthrough
	a.chooser = nil
}

// chooserKey runs one keypress against the open chooser. Every key is
// swallowed: nothing reaches the panes while the list is up, ctrl+b never
// nests (the prefix machinery is passthrough's), and the page keys never
// scroll — the chooser branch sits ahead of both in app.key. On an empty
// room only esc leaves; movement, pick and q are dead until a row exists.
func (a *App) chooserKey(msg tea.KeyPressMsg) {
	if msg.Code == tea.KeyEscape {
		a.closeChooser()
		return
	}
	if a.chooser == nil || len(a.chooser.rows) == 0 {
		return
	}
	if msg.Mod != 0 {
		return // chords are not chooser keys; swallowed all the same
	}
	switch msg.Code {
	case 'q':
		a.closeChooser()
	case 'j', tea.KeyDown:
		a.chooser.move(1)
	case 'k', tea.KeyUp:
		a.chooser.move(-1)
	case tea.KeyEnter:
		a.chooserPick()
	}
}

// chooserPick executes the row under the cursor. A terminal already open in
// a pane just takes the focus — nothing swaps. A fresh one replaces the
// focused slot's pane entirely (replaceFocused). Either way the chooser
// closes first, so its keys stop owning the keyboard before any pick work.
func (a *App) chooserPick() {
	c := a.chooser
	if c == nil || c.cursor >= len(c.rows) {
		return
	}
	id := c.rows[c.cursor].ID
	a.closeChooser()
	if _, open := a.panes[id]; open {
		a.setFocus(id) // already on screen: focus it, swap nothing
		return
	}
	a.replaceFocused(id)
}

// replaceFocused grafts id into the focused pane's slot: a fresh pane.Pane
// takes the leaf (the slot's geometry and siblings stay), the old pane's
// stream CLOSEs — detached cleanly, the ack walks it back to idle — and the
// replaced terminal keeps running in the core, untouched. Focus moves
// without a goodbye FOCUS 0: the CLOSE supersedes it. The debts mirror
// addPane's: attach intent, cell px, and the solved size.
func (a *App) replaceFocused(id hc.TerminalID) {
	if id == "" {
		return
	}
	if _, dup := a.panes[id]; dup {
		return
	}
	old := a.panes[a.focus]
	s := a.newSlot(id)
	a.panes[id] = s
	switch {
	case old == nil || a.tree == nil:
		a.tree = &layoutNode{leaf: s} // the empty-room first pane
		a.bootstrapped = true
	case a.tree.replace(old.id, s):
		// the focused leaf swapped in place
	default:
		a.tree = &layoutNode{leaf: s} // unreachable: panes and leaves are one set
	}
	a.focus = id
	if old != nil {
		delete(a.panes, old.id)
		old.stream.Detach()
	}
	s.p.Change(pane.ChangeAttach)
	s.p.Enqueue(pane.Op{Kind: pane.OpActive, Active: true}) // the focus debt, paid while not live
	s.p.SetCellPx(a.cellW, a.cellH)
	a.postSize(a.width, a.height)
}
