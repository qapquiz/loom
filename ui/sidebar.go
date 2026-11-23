package ui

// The sidebar is the list (<leader> w hands it the keyboard): one row order
// for rendering and for the cursor alike, one cursor, and the pick
// semantics. The mode enum owns key routing (app.key routes modeSidebar
// here before anything reaches a pane); sidebarNav is data only — the
// terminal under the cursor, no row snapshot and no parallel booleans.

import (
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"loom/hc"
	"loom/pane"
	"loom/room"
)

// sidebarHeader titles the rail, plain while the panes have the keyboard
// and inverse while the list does; sidebarHint is the list's key hint,
// shown only while the list has the keyboard.
const (
	sidebarHeader = "windows"
	sidebarHint   = "j/k · enter · esc"
)

var (
	sidebarInverse = styleState{attr: pane.AttrInverse}
	sidebarFaint   = styleState{attr: pane.AttrFaint}
)

// sidebarNav is the sidebar's cursor while the list owns the keyboard: the
// terminal under the cursor, "" only over an empty room. An identity rather
// than an index, so a room change that reorders or grows the list leaves
// the cursor on its terminal; the rows come from the room on demand.
type sidebarNav struct {
	sel hc.TerminalID
}

// sidebarRows is the list in its one order: the room's terminals, open
// panes first, stable within each group.
func sidebarRows(r *room.Room, open map[hc.TerminalID]bool) []room.Terminal {
	ts := r.Terminals()
	sort.SliceStable(ts, func(i, j int) bool { return open[ts[i].ID] && !open[ts[j].ID] })
	return ts
}

// place is the cursor's one placement rule: on id's row when id is one of
// the rows, else on the top row ("" over an empty room). Entering the list
// passes the focused pane, so the cursor starts where the user is; a room
// change passes the cursor itself, so it keeps its terminal and a removed
// one falls back to the top.
func (n *sidebarNav) place(rows []room.Terminal, id hc.TerminalID) {
	n.sel = ""
	if len(rows) == 0 {
		return
	}
	n.sel = rows[0].ID
	for _, t := range rows {
		if t.ID == id {
			n.sel = id
			return
		}
	}
}

// move steps the cursor by d rows and wraps at both ends: one motion, one
// list, no dead keys.
func (n *sidebarNav) move(rows []room.Terminal, d int) {
	count := len(rows)
	if count == 0 {
		return
	}
	i := 0
	for k, t := range rows {
		if t.ID == n.sel {
			i = k
			break
		}
	}
	n.sel = rows[((i+d)%count+count)%count].ID
}

// enterSidebar hands the keyboard to the list: the cursor lands on the
// focused pane's row.
func (a *App) enterSidebar() {
	a.mode = modeSidebar
	a.nav = &sidebarNav{}
	a.nav.place(sidebarRows(&a.room, a.openPanes()), a.focus)
}

// leaveSidebar returns the keyboard to the panes. The list never moved the
// focus, so the pane that had it keeps it.
func (a *App) leaveSidebar() {
	a.mode = modePassthrough
	a.nav = nil
}

// sidebarKey runs one keypress against the list. Every key is swallowed:
// nothing reaches the panes while the list has the keyboard, ctrl+b never
// nests (the prefix machinery is passthrough's), and the page keys never
// scroll — the sidebar branch sits ahead of both in app.key. On an empty
// room only esc leaves; movement, pick and q are dead until a row exists.
func (a *App) sidebarKey(msg tea.KeyPressMsg) {
	if msg.Code == tea.KeyEscape {
		a.leaveSidebar()
		return
	}
	rows := sidebarRows(&a.room, a.openPanes())
	if len(rows) == 0 {
		return
	}
	if msg.Mod != 0 {
		return // chords are not list keys; swallowed all the same
	}
	switch msg.Code {
	case 'q':
		a.leaveSidebar()
	case 'j', tea.KeyDown:
		a.nav.move(rows, 1)
	case 'k', tea.KeyUp:
		a.nav.move(rows, -1)
	case tea.KeyEnter:
		a.sidebarPick()
	}
}

// sidebarPick executes the row under the cursor. A terminal already open in
// a pane just takes the focus — nothing swaps. A fresh one replaces the
// focused slot's pane entirely (replaceFocused). Either way the list hands
// the keyboard back first, so its keys stop owning it before any pick work.
func (a *App) sidebarPick() {
	id := a.nav.sel
	if id == "" {
		return
	}
	a.leaveSidebar()
	if _, open := a.panes[id]; open {
		a.setFocus(id) // already on screen: focus it, swap nothing
		return
	}
	a.replaceFocused(id)
}

// sidebar renders the rail as one ANSI string: the header, one line per
// row, and the hint. Each row is the open-in-pane marker (▸ focused pane,
// · open in another pane, blank otherwise), the title falling back to the
// name, and the OSC 7501 status word when set. With the list active
// (nav != nil) the header and the cursor row render inverse, padded to the
// rail's width so the bars span it, and the hint shows faint; bloom.Text
// parses the SGR, the same contract PaneNode's frames ride.
func sidebar(r *room.Room, focused hc.TerminalID, open map[hc.TerminalID]bool, nav *sidebarNav) string {
	rows := sidebarRows(r, open)
	lines := make([]string, 0, len(rows)+2)
	if nav != nil {
		lines = append(lines, string(bar(nil, sidebarHeader)))
	} else {
		lines = append(lines, sidebarHeader)
	}
	if len(rows) == 0 {
		lines = append(lines, "no terminals")
	}
	for _, t := range rows {
		mark := "  "
		if open[t.ID] {
			mark = "· " // open in a pane, not the focused one
		}
		if t.ID == focused {
			mark = "▸ " // the pane keys and paste go to
		}
		title := t.Info.Title
		if title == "" {
			title = t.Info.Name
		}
		line := mark + title
		if badge := badgeOf(t); badge != "" {
			line += " " + badge // trailing plain word, as herdr renders status
		}
		if nav != nil && t.ID == nav.sel {
			line = string(bar(nil, line)) // the cursor row
		}
		lines = append(lines, line)
	}
	if nav != nil {
		lines = append(lines, string(appendSGR(nil, sidebarFaint))+sidebarHint+"\x1b[0m")
	}
	return strings.Join(lines, "\n")
}

// bar appends text as an inverse run padded to the rail's width.
func bar(b []byte, text string) []byte {
	b = appendSGR(b, sidebarInverse)
	b = append(b, text...)
	for w := ansi.StringWidth(text); w < sidebarWidth; w++ {
		b = append(b, ' ')
	}
	return append(b, "\x1b[0m"...)
}

func badgeOf(t room.Terminal) string {
	// Lifecycle outranks the last program status: a restarting terminal's
	// status word is stale, the program is gone.
	if t.State != room.Listed {
		return t.State.String()
	}
	return t.Info.Status
}
