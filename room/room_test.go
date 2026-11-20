package room

import (
	"reflect"
	"testing"

	"loom/hc"
)

func snap(seq uint64, infos ...hc.TerminalInfo) hc.Event {
	return hc.Event{Kind: hc.EventSnapshot, Seq: seq, All: infos}
}

func info(id string) hc.TerminalInfo {
	return hc.TerminalInfo{ID: hc.TerminalID(id), Title: id, Cols: 80, Rows: 24}
}

func TestRoomApplyTable(t *testing.T) {
	r := &Room{}

	steps := []struct {
		name    string
		ev      hc.Event
		changed bool
		check   func(t *testing.T)
	}{
		{"snapshot", snap(1, info("a"), info("b")), true, func(t *testing.T) {
			ts := r.Terminals()
			if len(ts) != 2 || ts[0].ID != "a" || ts[1].ID != "b" || ts[0].State != Listed {
				t.Fatalf("terminals = %+v", ts)
			}
		}},
		{"created appends", hc.Event{Kind: hc.EventCreated, ID: "c", Info: info("c")}, true, func(t *testing.T) {
			got, ok := r.Get("c")
			if !ok || got.State != Listed {
				t.Fatalf("c = %+v ok=%v", got, ok)
			}
			if len(r.Terminals()) != 3 || r.Terminals()[2].ID != "c" {
				t.Fatalf("order = %+v", r.Terminals())
			}
		}},
		{"status field", hc.Event{Kind: hc.EventStatus, ID: "a", Info: hc.TerminalInfo{ID: "a", Status: "working"}}, true, func(t *testing.T) {
			got, _ := r.Get("a")
			if got.Info.Status != "working" || got.Info.Title != "a" || got.State != Listed {
				t.Fatalf("a = %+v", got)
			}
		}},
		{"same status again", hc.Event{Kind: hc.EventStatus, ID: "a", Info: hc.TerminalInfo{ID: "a", Status: "working"}}, false, nil},
		{"title field", hc.Event{Kind: hc.EventTitle, ID: "b", Info: hc.TerminalInfo{ID: "b", Title: "vim"}}, true, func(t *testing.T) {
			got, _ := r.Get("b")
			if got.Info.Title != "vim" {
				t.Fatalf("b = %+v", got)
			}
		}},
		{"size field", hc.Event{Kind: hc.EventSize, ID: "b", Info: hc.TerminalInfo{ID: "b", Cols: 120, Rows: 40}}, true, func(t *testing.T) {
			got, _ := r.Get("b")
			if got.Info.Cols != 120 || got.Info.Rows != 40 {
				t.Fatalf("b = %+v", got)
			}
		}},
		{"labels field", hc.Event{Kind: hc.EventLabels, ID: "a", Info: hc.TerminalInfo{ID: "a", Labels: map[string]string{"k": "v"}}}, true, func(t *testing.T) {
			got, _ := r.Get("a")
			if !reflect.DeepEqual(got.Info.Labels, map[string]string{"k": "v"}) {
				t.Fatalf("a labels = %+v", got.Info.Labels)
			}
		}},
		{"size field unknown id", hc.Event{Kind: hc.EventSize, ID: "zz", Info: hc.TerminalInfo{ID: "zz", Cols: 1, Rows: 1}}, false, nil},
		{"lost", hc.Event{Kind: hc.EventLost, ID: "b"}, true, func(t *testing.T) {
			got, _ := r.Get("b")
			if got.State != Restarting {
				t.Fatalf("b state = %v", got.State)
			}
		}},
		{"lost unknown id", hc.Event{Kind: hc.EventLost, ID: "zz"}, false, nil},
		{"restarted returns to listed", hc.Event{Kind: hc.EventRestarted, ID: "b", Info: info("b2")}, true, func(t *testing.T) {
			got, _ := r.Get("b")
			if got.State != Listed || got.Info.Title != "b2" {
				t.Fatalf("b = %+v", got)
			}
		}},
		{"finished", hc.Event{Kind: hc.EventFinished, ID: "a", Exit: &hc.CloseReason{Reason: "exited", ExitCode: 0}}, true, func(t *testing.T) {
			got, _ := r.Get("a")
			if got.State != Exited {
				t.Fatalf("a state = %v", got.State)
			}
		}},
		{"keep no-op", hc.Event{Kind: hc.EventKeep, Seq: 9}, false, func(t *testing.T) {
			if len(r.Terminals()) != 3 {
				t.Fatal("keep changed membership")
			}
		}},
		{"removed", hc.Event{Kind: hc.EventRemoved, ID: "a"}, true, func(t *testing.T) {
			if _, ok := r.Get("a"); ok {
				t.Fatal("a still present")
			}
			if len(r.Terminals()) != 2 || r.Terminals()[0].ID != "b" || r.Terminals()[1].ID != "c" {
				t.Fatalf("order = %+v", r.Terminals())
			}
		}},
		{"removed unknown id", hc.Event{Kind: hc.EventRemoved, ID: "zz"}, false, nil},
	}

	for _, s := range steps {
		t.Run(s.name, func(t *testing.T) {
			if got := r.Apply(s.ev); got != s.changed {
				t.Fatalf("Apply(%s) changed = %v, want %v", s.name, got, s.changed)
			}
			if s.check != nil {
				s.check(t)
			}
		})
	}
}

func TestRoomMissedResyncRebuildsWholesale(t *testing.T) {
	r := &Room{}
	r.Apply(snap(1, info("a"), info("b")))
	r.Apply(hc.Event{Kind: hc.EventLost, ID: "a"})
	r.Apply(hc.Event{Kind: hc.EventCreated, ID: "c", Info: info("c")})

	// Subscriber fell behind: missed carries a fresh snapshot; the room
	// rebuilds, dropping c and un-lossing a, in snapshot order.
	if changed := r.Apply(snap(20, hc.TerminalInfo{ID: "a", Title: "a", Cols: 100, Rows: 30})); !changed {
		t.Fatal("missed resync reported no change")
	}
	ts := r.Terminals()
	if len(ts) != 1 || ts[0].ID != "a" || ts[0].State != Listed || ts[0].Info.Cols != 100 {
		t.Fatalf("after resync = %+v", ts)
	}
	// Events above the new baseline still apply.
	if !r.Apply(hc.Event{Kind: hc.EventLost, ID: "a"}) {
		t.Fatal("post-resync lost did not change")
	}
	if got, _ := r.Get("a"); got.State != Restarting {
		t.Fatalf("a state = %v", got.State)
	}
}
