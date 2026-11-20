// Package room is the lifecycle state machine over hc's event vocabulary —
// the sidebar source. It is pure domain: imports only hc, no bloom, no
// emulator. The pane keeps its own machine over its stream (two machines, one
// per source, never cross-checked; they may transiently disagree by design).
package room

import (
	"log"

	"loom/hc"
)

type State uint8

const (
	Missing    State = iota
	Listed           // live and attachable; plain/agent alike
	Restarting       // lost: program gone, record kept, restart pending
	Exited           // finished: exit code known, record may vanish soon
)

func (s State) String() string {
	switch s {
	case Listed:
		return "listed"
	case Restarting:
		return "restarting"
	case Exited:
		return "exited"
	default:
		return "missing"
	}
}

type Terminal struct {
	ID    hc.TerminalID
	State State
	Info  hc.TerminalInfo
}

// Room is the one place hc events map to transitions. Zero value is ready.
// Owned by one goroutine (the UI's): reads need no lock, Apply is the only
// writer.
type Room struct {
	order []hc.TerminalID
	byID  map[hc.TerminalID]*Terminal
}

// Apply applies one event and reports whether anything observable changed
// (membership, order, state, or fields), so callers can skip idle redraws.
func (r *Room) Apply(ev hc.Event) (changed bool) {
	switch ev.Kind {
	case hc.EventSnapshot:
		// Wholesale replacement; always a redraw candidate.
		r.order = r.order[:0]
		r.byID = map[hc.TerminalID]*Terminal{}
		for _, info := range ev.All {
			info := info
			r.order = append(r.order, info.ID)
			r.byID[info.ID] = &Terminal{ID: info.ID, State: Listed, Info: info}
		}
		return true
	case hc.EventCreated, hc.EventRestarted:
		t := r.get(ev.ID)
		if t == nil {
			r.order = append(r.order, ev.ID)
			r.byID[ev.ID] = &Terminal{ID: ev.ID, State: Listed, Info: ev.Info}
			return true
		}
		t.Info = ev.Info
		if t.State != Listed {
			t.State = Listed
			return true
		}
		return true // record replaced even when state held
	case hc.EventLost:
		t := r.get(ev.ID)
		if t == nil {
			// Illegal transition: lost for an unknown ID. Logged no-op.
			log.Printf("room: lost for unknown terminal %s", ev.ID)
			return false
		}
		if t.State != Restarting {
			t.State = Restarting
			return true
		}
		return false
	case hc.EventFinished:
		t := r.get(ev.ID)
		if t == nil {
			log.Printf("room: finished for unknown terminal %s", ev.ID)
			return false
		}
		if t.State != Exited {
			t.State = Exited
			return true
		}
		return false
	case hc.EventRemoved:
		if r.get(ev.ID) == nil {
			return false
		}
		delete(r.byID, ev.ID)
		for i, id := range r.order {
			if id == ev.ID {
				r.order = append(r.order[:i], r.order[i+1:]...)
				break
			}
		}
		return true
	case hc.EventStatus:
		return r.updateField(ev.ID, func(t *Terminal) bool {
			if t.Info.Status == ev.Info.Status {
				return false
			}
			t.Info.Status = ev.Info.Status
			return true
		})
	case hc.EventTitle:
		return r.updateField(ev.ID, func(t *Terminal) bool {
			if t.Info.Title == ev.Info.Title {
				return false
			}
			t.Info.Title = ev.Info.Title
			return true
		})
	case hc.EventSize:
		return r.updateField(ev.ID, func(t *Terminal) bool {
			if t.Info.Cols == ev.Info.Cols && t.Info.Rows == ev.Info.Rows {
				return false
			}
			t.Info.Cols = ev.Info.Cols
			t.Info.Rows = ev.Info.Rows
			return true
		})
	case hc.EventLabels:
		return r.updateField(ev.ID, func(t *Terminal) bool {
			if mapsEqual(t.Info.Labels, ev.Info.Labels) {
				return false
			}
			t.Info.Labels = ev.Info.Labels
			return true
		})
	case hc.EventKeep:
		return false
	default:
		return false
	}
}

func (r *Room) updateField(id hc.TerminalID, f func(*Terminal) bool) bool {
	t := r.get(id)
	if t == nil {
		return false
	}
	return f(t)
}

func (r *Room) get(id hc.TerminalID) *Terminal {
	if r.byID == nil {
		return nil
	}
	return r.byID[id]
}

// Terminals returns the terminals in snapshot order (created appends, removed
// deletes). A fresh slice every call: the sidebar may hold it.
func (r *Room) Terminals() []Terminal {
	out := make([]Terminal, 0, len(r.order))
	for _, id := range r.order {
		if t, ok := r.byID[id]; ok {
			out = append(out, *t)
		}
	}
	return out
}

func (r *Room) Get(id hc.TerminalID) (Terminal, bool) {
	t := r.get(id)
	if t == nil {
		return Terminal{}, false
	}
	return *t, true
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
