package main

// Diagnostic probe for U5: walks the real attach path against the live core,
// link by link. Dial, snapshot, attach t44 (loomvim), stream callbacks,
// deltas. Uncommitted scaffolding; delete after diagnosis.

import (
	"fmt"
	"time"

	"loom/hc"
)

type rec struct {
	connected bool
	deltas    int
	bytes     int
}

func (r *rec) Connected(out hc.StreamOut, auto bool) {
	r.connected = true
	fmt.Println("Connected: autoInput =", auto)
}

func (r *rec) Delta(p []byte) {
	r.deltas++
	r.bytes += len(p)
}

func (r *rec) Closed(rr hc.CloseReason) {
	fmt.Printf("Closed: %+v\n", rr)
}

func main() {
	sess, err := hc.Dial(hc.DefaultSocket(), hc.Features{AutomaticInput: true})
	if err != nil {
		fmt.Println("dial err:", err)
		return
	}
	defer sess.Close()
	r := &rec{}
	sess.Attach(hc.TerminalID("t44"), r)
	timeout := time.After(6 * time.Second)
	tick := time.NewTicker(700 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case ev := <-sess.Events():
			fmt.Println("event kind:", ev.Kind, "id:", ev.ID, "seq:", ev.Seq)
			if ev.Kind == hc.EventSnapshot {
				for _, t := range ev.All {
					fmt.Printf("  %s title=%q status=%q\n", t.ID, t.Title, t.Status)
				}
			}
		case <-tick.C:
			fmt.Printf("probe: connected=%v deltas=%d bytes=%d\n", r.connected, r.deltas, r.bytes)
		case <-timeout:
			fmt.Println("done")
			return
		}
	}
}
