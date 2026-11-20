package hc

// Event vocabulary and the seq/missed rules, per the M1 synthesis. The room
// (package room) is the consumer; unknown hc delta types (turn, foreground,
// doc) are dropped here because the synthesis vocabulary has no slot for them
// and nothing in wave 1 renders them — seq still advances past them.

import "encoding/json"

type TerminalID string

type TerminalInfo struct {
	ID     TerminalID
	Title  string
	Name   string // hc record name ("loomvim"), set independently of the program title
	Status string // OSC 7501 root status word (state) relayed by hc; never scraped
	Cols   uint16
	Rows   uint16
	Labels map[string]string
}

type CloseReason struct {
	Reason   string // "exited", "detached", loss reasons ("signaled", "holder_lost", ...)
	ExitCode int
}

type EventKind uint8

const (
	EventSnapshot EventKind = iota // wholesale replacement, carries All
	EventCreated
	EventLost
	EventRestarted
	EventFinished // Exit set
	EventRemoved
	EventStatus // field update
	EventTitle  // field update
	EventSize   // field update
	EventLabels // field update
	EventKeep   // heartbeat and keep-flag notices; no-op for the room
)

type Event struct {
	Kind EventKind
	Seq  uint64
	ID   TerminalID
	Info TerminalInfo // full record on create/restart; only the changed fields on field updates
	All  []TerminalInfo
	Exit *CloseReason
}

// Notification shapes from hc (schema README): {"method":"event","params":
// {"seq":N,"type":"...","terminal":"t7","data":{...}}}, heartbeat carries only
// seq, missed carries seq + fresh snapshot.
type eventNote struct {
	Method string          `json:"method"`
	Params eventNoteParams `json:"params"`
}

type eventNoteParams struct {
	Seq       uint64          `json:"seq"`
	Type      string          `json:"type"`
	Terminal  string          `json:"terminal"`
	Data      json.RawMessage `json:"data"`
	Terminals json.RawMessage `json:"terminals"`
}

func (p eventNoteParams) TerminalID() TerminalID { return TerminalID(p.Terminal) }

// eventSnapshotJSON is the missed notification's fresh snapshot shape; it
// matches the events.subscribe reply body too.
type eventSnapshotJSON struct {
	Seq       uint64           `json:"seq"`
	Terminals []terminalRecord `json:"terminals"`
	Channel   uint32           `json:"channel"`
}

type eventFeed struct {
	lastSeq uint64
	primed  bool // seq comparisons are meaningless before the first snapshot
}

// snapshot seeds lastSeq from a subscribe reply or missed notification.
func (f *eventFeed) snapshot(seq uint64) {
	f.lastSeq = seq
	f.primed = true
}

// note maps one notification to zero or more Events. Events whose seq is not
// above the last known seq are dropped; a missed notification becomes one
// wholesale EventSnapshot and resets the baseline.
func (f *eventFeed) note(n eventNote) []Event {
	switch n.Method {
	case "heartbeat":
		if !f.fresh(n.Params.Seq) {
			return nil
		}
		return []Event{{Kind: EventKeep, Seq: n.Params.Seq}}
	case "missed":
		var snap eventSnapshotJSON
		if err := json.Unmarshal(n.Params.Terminals, &snap.Terminals); err != nil {
			return nil
		}
		f.snapshot(n.Params.Seq)
		ev := Event{Kind: EventSnapshot, Seq: n.Params.Seq}
		for _, rec := range snap.Terminals {
			ev.All = append(ev.All, rec.info())
		}
		return []Event{ev}
	case "event":
		if !f.fresh(n.Params.Seq) {
			return nil
		}
		return mapEvent(n.Params)
	default:
		return nil
	}
}

func (f *eventFeed) fresh(seq uint64) bool {
	if f.primed && seq <= f.lastSeq {
		return false
	}
	f.lastSeq = seq
	f.primed = true
	return true
}

func mapEvent(p eventNoteParams) []Event {
	switch p.Type {
	case "created", "restarted":
		rec, ok := parseRecord(p.Data)
		if !ok {
			return nil
		}
		kind := EventCreated
		if p.Type == "restarted" {
			kind = EventRestarted
		}
		return []Event{{Kind: kind, Seq: p.Seq, ID: p.TerminalID(), Info: rec.info()}}
	case "lost":
		return []Event{{Kind: EventLost, Seq: p.Seq, ID: p.TerminalID()}}
	case "finished":
		var d struct {
			Reason   string `json:"reason"`
			ExitCode *int32 `json:"exit_code"`
		}
		json.Unmarshal(p.Data, &d)
		ev := Event{Kind: EventFinished, Seq: p.Seq, ID: p.TerminalID()}
		if d.Reason != "" || d.ExitCode != nil {
			cr := CloseReason{Reason: d.Reason}
			if d.ExitCode != nil {
				cr.ExitCode = int(*d.ExitCode)
			}
			ev.Exit = &cr
		}
		return []Event{ev}
	case "removed":
		return []Event{{Kind: EventRemoved, Seq: p.Seq, ID: p.TerminalID()}}
	case "status":
		var d struct {
			Status *statusRecord `json:"status"`
		}
		json.Unmarshal(p.Data, &d)
		info := TerminalInfo{ID: p.TerminalID()}
		if d.Status != nil {
			info.Status = d.Status.State
		}
		return []Event{{Kind: EventStatus, Seq: p.Seq, ID: p.TerminalID(), Info: info}}
	case "title":
		var d struct {
			Title string `json:"title"`
		}
		json.Unmarshal(p.Data, &d)
		return []Event{{Kind: EventTitle, Seq: p.Seq, ID: p.TerminalID(), Info: TerminalInfo{ID: p.TerminalID(), Title: d.Title}}}
	case "size":
		var d struct {
			Cols uint16 `json:"cols"`
			Rows uint16 `json:"rows"`
		}
		json.Unmarshal(p.Data, &d)
		return []Event{{Kind: EventSize, Seq: p.Seq, ID: p.TerminalID(), Info: TerminalInfo{ID: p.TerminalID(), Cols: d.Cols, Rows: d.Rows}}}
	case "labels":
		var d struct {
			Labels map[string]string `json:"labels"`
		}
		json.Unmarshal(p.Data, &d)
		return []Event{{Kind: EventLabels, Seq: p.Seq, ID: p.TerminalID(), Info: TerminalInfo{ID: p.TerminalID(), Labels: d.Labels}}}
	case "keep":
		// Keep-flag changes are outside the spike's vocabulary; dropped with
		// seq advanced (fresh() already ran) so nothing falls behind.
		return []Event{{Kind: EventKeep, Seq: p.Seq, ID: p.TerminalID()}}
	default:
		// turn, foreground, doc: not in the synthesis vocabulary. Dropped;
		// lastSeq already advanced.
		return nil
	}
}

// Terminal record subset the sidebar and pane need; hc records carry far more.
type terminalRecord struct {
	Address  string            `json:"address"`
	Title    string            `json:"title"`
	Name     string            `json:"name"`
	Cols     uint16            `json:"cols"`
	Rows     uint16            `json:"rows"`
	Labels   map[string]string `json:"labels"`
	Status   *statusRecord     `json:"status"`
	State    string            `json:"state"`
	Reason   string            `json:"reason"`
	ExitCode *int32            `json:"exit_code"`
}

type statusRecord struct {
	State string `json:"state"`
	Msg   string `json:"msg"`
}

func parseRecord(data json.RawMessage) (terminalRecord, bool) {
	var rec terminalRecord
	if err := json.Unmarshal(data, &rec); err != nil || rec.Address == "" {
		return terminalRecord{}, false
	}
	return rec, true
}

func (r terminalRecord) info() TerminalInfo {
	info := TerminalInfo{
		ID:     TerminalID(r.Address),
		Title:  r.Title,
		Name:   r.Name,
		Cols:   r.Cols,
		Rows:   r.Rows,
		Labels: r.Labels,
	}
	if r.Status != nil {
		info.Status = r.Status.State
	}
	return info
}
