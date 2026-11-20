package ui

import (
	"strings"

	"loom/hc"
	"loom/room"
)

// sidebar renders the room for the left rail: one line per terminal with its
// title and a badge. The badge is the OSC 7501 status word when the program
// set one, else the room lifecycle state when it is not plain Listed. The
// attached pane's terminal is marked.
func sidebar(r *room.Room, active hc.TerminalID) string {
	ts := r.Terminals()
	if len(ts) == 0 {
		return "no terminals"
	}
	lines := make([]string, 0, len(ts))
	for _, t := range ts {
		mark := "  "
		if t.ID == active {
			mark = "› " // herdr's own cursor glyph
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
