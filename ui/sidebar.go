package ui

import (
	"strings"

	"loom/hc"
	"loom/room"
)

// sidebar renders the room for the left rail: one line per terminal with its
// title and a badge. The badge is the OSC 7501 status word when the program
// set one, else the room lifecycle state when it is not plain Listed. The
// focused pane's terminal is marked ▸ and the other open panes ·, so several
// attachments read at a glance.
func sidebar(r *room.Room, focused hc.TerminalID, open map[hc.TerminalID]bool) string {
	ts := r.Terminals()
	if len(ts) == 0 {
		return "no terminals"
	}
	lines := make([]string, 0, len(ts))
	for _, t := range ts {
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
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func badgeOf(t room.Terminal) string {
	// Lifecycle outranks the last program status: a restarting terminal's
	// status word is stale, the program is gone.
	if t.State != room.Listed {
		return t.State.String()
	}
	return t.Info.Status
}
