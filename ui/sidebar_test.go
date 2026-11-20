package ui

import (
	"strings"
	"testing"

	"loom/hc"
	"loom/room"
)

func roomFixture() *room.Room {
	r := &room.Room{}
	r.Apply(hc.Event{Kind: hc.EventSnapshot, All: []hc.TerminalInfo{
		{ID: "t7", Title: "vim", Cols: 80, Rows: 24},
		{ID: "t9", Title: "build", Status: "working"},
	}})
	return r
}

func TestSidebarShowsTitleAndStatus(t *testing.T) {
	s := sidebar(roomFixture(), "t7")
	lines := strings.Split(s, "\n")
	if len(lines) != 2 {
		t.Fatalf("sidebar = %q", s)
	}
	if !strings.Contains(lines[0], "vim") {
		t.Fatalf("first line missing title: %q", lines[0])
	}
	if !strings.Contains(lines[0], "›") {
		t.Fatalf("active terminal not marked: %q", lines[0])
	}
	if !strings.Contains(lines[1], "build") || !strings.Contains(lines[1], "working") {
		t.Fatalf("status badge missing: %q", lines[1])
	}
	if strings.Contains(lines[0], "listed") {
		t.Fatalf("plain listed state badges: %q", lines[0])
	}
}

// hc programs often set no title; the record name carries the identity then.
func TestSidebarShowsNameWhenTitleEmpty(t *testing.T) {
	vim := hc.TerminalInfo{ID: "t44", Cols: 80, Rows: 24, Name: "loomvim"}
	r := roomWith(vim)
	if got := sidebar(r, "t44"); got != "› loomvim" {
		t.Fatalf("sidebar = %q, want › loomvim", got)
	}
}

func TestSidebarLifecycleBadge(t *testing.T) {
	r := roomFixture()
	r.Apply(hc.Event{Kind: hc.EventLost, ID: "t9"})
	s := sidebar(r, "")
	if !strings.Contains(s, "restarting") {
		t.Fatalf("lost terminal not badged: %q", s)
	}
	if strings.Contains(s, "›") {
		t.Fatalf("marked active with no pane: %q", s)
	}
}

func TestSidebarEmpty(t *testing.T) {
	if s := sidebar(&room.Room{}, ""); s != "no terminals" {
		t.Fatalf("empty room sidebar = %q", s)
	}
}
