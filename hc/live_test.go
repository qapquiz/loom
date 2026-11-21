package hc_test

// Live-gated integration tests against the real hc core (M2 item 2), grown
// from cmd/loomprobe. Every test skips fast unless LOOM_TEST_HC=1. Each test
// creates its own scratch terminal via the hc CLI and kills it in cleanup;
// terminals this file did not create are never attached. Discovery rides
// hc.DefaultSocket(); the core must already be running.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"loom/hc"
	"loom/pane"

	"go.mitchellh.com/libghostty"
)

func requireLive(t *testing.T) {
	t.Helper()
	if os.Getenv("LOOM_TEST_HC") != "1" {
		t.Skip("set LOOM_TEST_HC=1 to run live core integration tests")
	}
}

// waitUntil polls fn until it holds or the deadline passes; plain sleeps are
// never the only witness. Fatals on timeout.
func waitUntil(t *testing.T, d time.Duration, what string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	if fn() {
		return
	}
	t.Fatalf("timed out after %v waiting for %s", d, what)
}

var liveSeq atomic.Uint32

// trimArray unwraps a one-element JSON array, so single-object and
// list-shaped replies parse the same.
func trimArray(b []byte) []byte {
	s := strings.TrimSpace(string(b))
	if !strings.HasPrefix(s, "[") {
		return b
	}
	var arr []json.RawMessage
	if err := json.Unmarshal([]byte(s), &arr); err != nil || len(arr) != 1 {
		return b
	}
	return arr[0]
}

// scratchTerm runs its own terminal (printf MARKER then cat) and registers
// the kill in cleanup. Names embed the test binary pid and a counter, so no
// two runs — or two tests — collide, and only ids returned here are attached.
func scratchTerm(t *testing.T) (hc.TerminalID, string) {
	t.Helper()
	name := fmt.Sprintf("loom-it-%d-%d", os.Getpid(), liveSeq.Add(1))
	bin := hcBin(t)
	out, err := exec.Command(bin, "run", "--bg", "--name", name, "sh", "-c", "printf MARKER; cat").Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			t.Fatalf("hc run: %v: %s", err, ee.Stderr)
		}
		t.Fatalf("hc run: %v", err)
	}
	var rec struct {
		Address string `json:"address"`
		Name    string `json:"name"`
	}
	// The CLI prints one record, or a one-record list; accept both.
	if err := json.Unmarshal(trimArray(out), &rec); err != nil || rec.Address == "" {
		t.Fatalf("hc run output not a terminal record: %q", out)
	}
	id := hc.TerminalID(rec.Address)
	t.Cleanup(func() { exec.Command(bin, "kill", string(id)).Run() })
	return id, name
}

func hcBin(t *testing.T) string {
	t.Helper()
	if p, err := exec.LookPath("hc"); err == nil {
		return p
	}
	home, _ := os.UserHomeDir()
	p := filepath.Join(home, ".local", "bin", "hc") // grounding doc: hc 0.1.21 lives here
	if _, err := os.Stat(p); err == nil {
		return p
	}
	t.Fatal("hc binary not found on PATH or in ~/.local/bin")
	return ""
}

// dialLive dials the real core the way the app does, with automatic_input
// requested so attach can negotiate it.
func dialLive(t *testing.T) *hc.Session {
	t.Helper()
	sess, err := hc.Dial(hc.DefaultSocket(), hc.Features{AutomaticInput: true})
	if err != nil {
		t.Fatalf("Dial live core: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	return sess
}

// drainEvents keeps the event channel from filling on the shared core: other
// terminals' notifications must never block the read loop that carries paint.
// The goroutine exits when Close tears the session down.
func drainEvents(sess *hc.Session) {
	go func() {
		for range sess.Events() {
		}
	}()
}

// listSize reads one terminal's cols/rows via a fresh plain-mode
// terminal.list call on its own connection — the same RPC `hc ls` uses.
func listSize(name string) (cols, rows uint16, ok bool) {
	conn, err := net.Dial("unix", hc.DefaultSocket())
	if err != nil {
		return 0, 0, false
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(3 * time.Second))
	req, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "terminal.list", "params": map[string]any{}})
	if _, err := conn.Write(append(req, '\n')); err != nil {
		return 0, 0, false
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return 0, 0, false
	}
	var msg struct {
		Result struct {
			Terminals []struct {
				Name string `json:"name"`
				Cols uint16 `json:"cols"`
				Rows uint16 `json:"rows"`
			} `json:"terminals"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(line), &msg); err != nil {
		return 0, 0, false
	}
	for _, tm := range msg.Result.Terminals {
		if tm.Name == name {
			return tm.Cols, tm.Rows, true
		}
	}
	return 0, 0, false
}

// liveRec is the probe's recorder with locking for -race: callbacks arrive on
// the session read-loop goroutine, assertions run on the test goroutine.
type liveRec struct {
	mu        sync.Mutex
	connected bool
	auto      bool
	out       hc.StreamOut
	bytes     int
	closed    []hc.CloseReason
}

func (r *liveRec) Connected(out hc.StreamOut, autoInput bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.connected, r.out, r.auto = true, out, autoInput
}

func (r *liveRec) Delta(p []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.bytes += len(p)
}

func (r *liveRec) Closed(rr hc.CloseReason) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = append(r.closed, rr)
}

func (r *liveRec) state() (connected, auto bool, n int, closed []hc.CloseReason) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.connected, r.auto, r.bytes, r.closed
}

// attachLive attaches a recorder and waits for the reply, surfacing any close
// reason instead of a bare timeout.
func attachLive(t *testing.T, sess *hc.Session, id hc.TerminalID) *liveRec {
	t.Helper()
	rec := &liveRec{}
	sess.Attach(id, rec)
	waitUntil(t, 5*time.Second, "attach reply", func() bool {
		connected, _, _, closed := rec.state()
		return connected || len(closed) > 0
	})
	if connected, _, _, closed := rec.state(); !connected {
		t.Fatalf("attach refused: %+v", closed)
	}
	return rec
}

func screenText(s *pane.Screen) string {
	if s == nil {
		return ""
	}
	var b strings.Builder
	for _, c := range s.Cells {
		if c.Text == "" {
			b.WriteByte(' ')
		} else {
			b.WriteString(c.Text)
		}
	}
	return b.String()
}

func TestLiveSnapshotContainsScratchTerminal(t *testing.T) {
	requireLive(t)
	id, name := scratchTerm(t)

	sess := dialLive(t)
	// Dial guarantees the snapshot is the first event on the channel.
	ev := <-sess.Events()
	drainEvents(sess)
	if ev.Kind != hc.EventSnapshot {
		t.Fatalf("first event = %v, want snapshot", ev.Kind)
	}
	for _, ti := range ev.All {
		if ti.Name == name {
			if ti.ID != id {
				t.Fatalf("snapshot name %q maps to %s, created %s", name, ti.ID, id)
			}
			if ti.Cols == 0 || ti.Rows == 0 {
				t.Fatalf("scratch terminal geometry = %dx%d", ti.Cols, ti.Rows)
			}
			return
		}
	}
	t.Fatalf("snapshot of %d terminals has no record named %q", len(ev.All), name)
}

func TestLiveAttachFirstPaintAndAutoInput(t *testing.T) {
	requireLive(t)
	id, _ := scratchTerm(t)

	sess := dialLive(t)
	drainEvents(sess)
	rec := attachLive(t, sess, id)

	if _, auto, _, _ := rec.state(); !auto {
		t.Fatal("autoInput negotiation = false, want true (hello grant AND attach confirmation)")
	}
	waitUntil(t, 3*time.Second, "first paint bytes", func() bool {
		_, _, n, _ := rec.state()
		return n > 0
	})
}

func TestLivePaneEndToEndTypedEcho(t *testing.T) {
	requireLive(t)
	id, name := scratchTerm(t)

	sess := dialLive(t)
	drainEvents(sess)
	// The app wiring, verbatim: pump the ops through the session, attach via
	// the machine's effect.
	p := pane.New(id, nil)
	p.SetPump(sess.Do)
	p.SetAttachFn(func() { sess.Attach(id, p) })
	p.Change(pane.ChangeAttach) // idle -> attaching, runs the attach effect
	waitUntil(t, 5*time.Second, "pane to attach", func() bool { return p.Phase() == pane.StateAttached })

	waitUntil(t, 5*time.Second, "first paint of MARKER", func() bool {
		return strings.Contains(screenText(p.Screen()), "MARKER")
	})

	// Type a marker line through the pane's public API, Enter like the keymap
	// sends it (code, no text), and read the echo off the published Screen.
	marker := "typed-" + name
	p.Enqueue(pane.Op{Kind: pane.OpKey, Key: pane.Key{Text: marker}})
	p.Enqueue(pane.Op{Kind: pane.OpKey, Key: pane.Key{Code: libghostty.KeyEnter}})
	waitUntil(t, 5*time.Second, "typed marker to echo on screen", func() bool {
		return strings.Contains(screenText(p.Screen()), marker)
	})
}

func TestLiveResizeObservedByTerminalList(t *testing.T) {
	requireLive(t)
	id, name := scratchTerm(t)

	sess := dialLive(t)
	drainEvents(sess)
	rec := attachLive(t, sess, id)

	// Odd size: it can never match the inherited geometry by accident.
	const wantCols, wantRows = 97, 31
	rec.out.Resize(wantCols, wantRows)
	waitUntil(t, 5*time.Second, fmt.Sprintf("terminal.list to report %dx%d", wantCols, wantRows), func() bool {
		cols, rows, ok := listSize(name)
		return ok && cols == wantCols && rows == wantRows
	})
}
