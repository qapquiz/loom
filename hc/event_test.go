package hc

import (
	"encoding/json"
	"reflect"
	"testing"
)

func noteJSON(t *testing.T, line string) eventNote {
	t.Helper()
	var n eventNote
	if err := json.Unmarshal([]byte(line), &n); err != nil {
		t.Fatalf("unmarshal %q: %v", line, err)
	}
	return n
}

func TestEventFeedSeqAndMissed(t *testing.T) {
	f := eventFeed{}
	// No baseline yet: the drop rule binds only after a snapshot, so a
	// pre-snapshot event is delivered (the snapshot rebuilds over it anyway).
	got := f.note(noteJSON(t, `{"method":"event","params":{"seq":3,"type":"title","terminal":"t7","data":{"title":"early"}}}`))
	if len(got) != 1 || got[0].Kind != EventTitle || got[0].Seq != 3 {
		t.Fatalf("pre-snapshot event = %+v", got)
	}

	f.snapshot(5) // subscribe reply: seq 5 baseline

	steps := []struct {
		line string
		want []Event
	}{
		// seq above baseline: pass, advance.
		{`{"method":"event","params":{"seq":6,"type":"created","terminal":"t7","data":{"address":"t7","title":"bash","cols":80,"rows":24,"labels":{},"state":"live","status":{"state":"idle"}}}}`,
			[]Event{{Kind: EventCreated, Seq: 6, ID: "t7", Info: TerminalInfo{ID: "t7", Title: "bash", Cols: 80, Rows: 24, Status: "idle", Labels: map[string]string{}}}}},
		// heartbeat at current seq: stale, dropped.
		{`{"method":"heartbeat","params":{"seq":6}}`, nil},
		{`{"method":"event","params":{"seq":7,"type":"size","terminal":"t7","data":{"cols":100,"rows":30}}}`,
			[]Event{{Kind: EventSize, Seq: 7, ID: "t7", Info: TerminalInfo{ID: "t7", Cols: 100, Rows: 30}}}},
		// replayed seq 6 after 7: dropped.
		{`{"method":"event","params":{"seq":6,"type":"title","terminal":"t7","data":{"title":"stale"}}}`, nil},
		// gap: subscriber fell behind, missed resets the world.
		{`{"method":"missed","params":{"seq":20,"terminals":[{"address":"t8","title":"vim","cols":120,"rows":40,"labels":{"k":"v"},"state":"live"}]}}`,
			[]Event{{Kind: EventSnapshot, Seq: 20, All: []TerminalInfo{{ID: "t8", Title: "vim", Cols: 120, Rows: 40, Labels: map[string]string{"k": "v"}}}}}},
		// seq above the missed baseline: pass.
		{`{"method":"event","params":{"seq":21,"type":"title","terminal":"t8","data":{"title":"nvim"}}}`,
			[]Event{{Kind: EventTitle, Seq: 21, ID: "t8", Info: TerminalInfo{ID: "t8", Title: "nvim"}}}},
		// seq at the missed baseline: dropped.
		{`{"method":"event","params":{"seq":20,"type":"title","terminal":"t8","data":{"title":"stale"}}}`, nil},
		{`{"method":"heartbeat","params":{"seq":20}}`, nil},
		{`{"method":"heartbeat","params":{"seq":22}}`, []Event{{Kind: EventKeep, Seq: 22}}},
		// unknown delta type: dropped, but seq advanced (seq 23 below must pass).
		{`{"method":"event","params":{"seq":23,"type":"turn","terminal":"t8","data":{"turn":1}}}`, nil},
		{`{"method":"event","params":{"seq":24,"type":"finished","terminal":"t8","data":{"reason":"exited","exit_code":3}}}`,
			[]Event{{Kind: EventFinished, Seq: 24, ID: "t8", Exit: &CloseReason{Reason: "exited", ExitCode: 3}}}},
	}

	for i, s := range steps {
		got := f.note(noteJSON(t, s.line))
		if !reflect.DeepEqual(got, s.want) {
			t.Fatalf("step %d %q:\n got %+v\nwant %+v", i, s.line, got, s.want)
		}
	}
}
