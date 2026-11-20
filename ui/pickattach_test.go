package ui

import (
	"testing"

	"loom/hc"
	"loom/room"
)

func listed(id, title string) hc.TerminalInfo {
	return hc.TerminalInfo{ID: hc.TerminalID(id), Title: title, Cols: 80, Rows: 24}
}

func roomWith(infos ...hc.TerminalInfo) *room.Room {
	r := &room.Room{}
	r.Apply(hc.Event{Kind: hc.EventSnapshot, All: infos})
	return r
}

func TestPickAttach(t *testing.T) {
	r := roomWith(listed("t7", "bash"), listed("t9", "vim"))

	cases := []struct {
		name string
		want string
		exp  hc.TerminalID
	}{
		{"unset falls back to first listed", "", "t7"},
		{"by id", "t9", "t9"},
		{"by title", "vim", "t9"},
		{"title wins first match", "bash", "t7"},
		{"unknown name waits", "emacs", ""},
		{"id matches despite title", "t7", "t7"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := pickAttach(r, c.want); got != c.exp {
				t.Fatalf("pickAttach(%q) = %q, want %q", c.want, got, c.exp)
			}
		})
	}

	// A target that exists but is not attachable is not a match.
	r.Apply(hc.Event{Kind: hc.EventLost, ID: "t9"})
	if got := pickAttach(r, "vim"); got != "" {
		t.Fatalf("picked restarting terminal %q", got)
	}
	// Unset still finds the remaining Listed terminal.
	if got := pickAttach(r, ""); got != "t7" {
		t.Fatalf("fallback after loss = %q, want t7", got)
	}
}
