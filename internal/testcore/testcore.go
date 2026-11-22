// Package testcore is the loopback seam (graft 2): a scripted hc core served
// over a real net.Conn. It is deliberately a protocol peer, not a mock of hc:
// it implements the wire from the schema README independently, so every test
// exercises hc's real handshake, demux and credit against a second reading of
// the protocol.
package testcore

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"

	"path/filepath"
	"sync"
	"testing"
)

const (
	kindData      = 0
	kindCredit    = 1
	kindClose     = 2
	kindResize    = 3
	kindFocus     = 4
	kindFacts     = 5
	kindAutoInput = 6
	eventsChannel = 2
	streamChannel = 7
	maxPayload    = 16 << 20
)

// Frame is one frame received from the viewer (the client under test).
type Frame struct {
	Ch      uint32
	Kind    uint8
	Payload []byte
}

type Core struct {
	t        *testing.T
	syncPath string

	// Script knobs, set before Dial.
	AutoInput     bool   // advertise automatic_input in hello, confirm on attach
	AttachErrKind string // non-empty: reply to terminal.attach with this error kind
	ListResult    string // JSON for terminal.list result; "" means empty list
	Seq           uint64 // baseline seq for the events.subscribe snapshot
	Terminals     string // JSON array for subscribe snapshot terminals

	// RunAddresses scripts terminal.run replies, popped in order (the last
	// entry repeats). RunErrKind fails the call instead. A successful run
	// also emits the created event a real core sends, so subscribers learn
	// the record asynchronously from the reply.
	RunAddresses []string
	RunErrKind   string

	mu         sync.Mutex
	frames     []Frame
	conns      []net.Conn
	nextStream uint32 // stream channel allocator: first attach gets 7, then 8, 9…
	runIdx     int
	runCalls   []RunCall
	runSeq     uint64 // created-event seqs handed out past Seq
}

// Serve listens on a Unix socket in a temp dir; tests use Core.SocketPath with
// hc.Dial.
func Serve(t *testing.T) *Core {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "core.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	c := &Core{t: t, syncPath: path, nextStream: streamChannel}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			c.serveConn(conn)
		}
	}()
	return c
}

func (c *Core) SocketPath() string { return c.syncPath }

// ServeConn scripts one pre-connected pair (net.Pipe); no socket file.
func ServeConn(t *testing.T, conn net.Conn) *Core {
	c := &Core{t: t}
	c.serveConn(conn)
	return c
}

func (c *Core) serveConn(conn net.Conn) {
	c.mu.Lock()
	c.conns = append(c.conns, conn)
	c.mu.Unlock()
	go c.run(conn)
}

func (c *Core) run(conn net.Conn) {
	br := bufio.NewReader(conn)
	framed := false
	for {
		var payload []byte
		if !framed {
			line, err := br.ReadString('\n')
			if err != nil {
				return
			}
			payload = []byte(line)
		} else {
			f, err := readFrame(br)
			if err != nil {
				return
			}
			c.record(f)
			switch {
			case f.kind == kindClose && f.ch == 0:
				return
			case f.kind == kindData && f.ch == 0:
				payload = f.payload
			case f.kind == kindData:
				continue // stream keys etc.: recorded only
			case f.kind == kindClose:
				// Viewer detaches; ack with an empty CLOSE.
				c.write(conn, frame{ch: f.ch, kind: kindClose})
				continue
			case f.kind == kindFacts:
				// Viewer Display report; recorded only.
				continue
			default:
				continue
			}
		}
		if !c.rpc(conn, payload, framed) {
			return
		}
		framed = true
	}
}

// rpc handles one JSON-RPC request. Replies echo the connection mode: plain
// JSON line until hello{framed} completes, channel-0 DATA frame after.
func (c *Core) rpc(conn net.Conn, payload []byte, framed bool) bool {
	var req struct {
		ID     uint64          `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		return false
	}
	switch req.Method {
	case "hello":
		var p struct {
			Framed   bool     `json:"framed"`
			Features []string `json:"features"`
		}
		json.Unmarshal(req.Params, &p)
		features := []string{}
		if c.AutoInput && p.Framed {
			features = append(features, "automatic_input")
		}
		c.reply(conn, req.ID, mustJSON(map[string]any{
			"generation": 1,
			"version":    "0.1.21-testcore",
			"core":       map[string]any{"name": "testcore"},
			"framed":     p.Framed,
			"methods":    []string{"terminal.attach", "events.subscribe"},
			"features":   features,
		}), nil, framed)
		return true
	case "events.subscribe":
		terminals := "[]"
		if c.Terminals != "" {
			terminals = c.Terminals
		}
		c.reply(conn, req.ID, json.RawMessage(`{"seq":`+uitoa(c.Seq)+`,"terminals":`+terminals+`,"channel":`+uitoa(eventsChannel)+`}`), nil, framed)
		return true
	case "terminal.attach":
		if c.AttachErrKind != "" {
			c.reply(conn, req.ID, nil, mustJSON(map[string]any{
				"code": -32000, "message": "no",
				"data": map[string]any{"kind": c.AttachErrKind},
			}), framed)
			return true
		}
		ch := c.allocStream()
		c.reply(conn, req.ID, json.RawMessage(`{"channel":`+uitoa(uint64(ch))+`,"terminal":{},"resize_notifications":false,"automatic_input":`+boolJSON(c.AutoInput)+`}`), nil, framed)
		return true
	case "terminal.run":
		c.runRPC(conn, req.ID, req.Params, framed)
		return true
	default:
		c.reply(conn, req.ID, json.RawMessage(`{}`), nil, framed)
		return true
	}
}

func (c *Core) reply(conn net.Conn, id uint64, result, errObj json.RawMessage, framed bool) {
	m := map[string]any{"jsonrpc": "2.0", "id": id}
	if errObj != nil {
		// errObj is embedded raw so the JSON-RPC error object keeps its shape.
		m["error"] = errObj
	} else {
		m["result"] = result
	}
	body := mustJSON(m)
	if framed {
		c.write(conn, frame{ch: 0, kind: kindData, payload: body})
	} else {
		conn.Write(append(body, '\n'))
	}
}

// RunCall records one terminal.run the viewer made.
type RunCall struct {
	Argv []string
	Cwd  string
}

// allocStream hands out the next stream channel; the first attach keeps the
// historical channel 7, later attaches get their own, like a real core.
// streamCh reports the latest one, so single-pane helpers (Delta,
// CloseStream) follow a pane that reattached.
func (c *Core) allocStream() uint32 {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := c.nextStream
	c.nextStream++
	return ch
}

func (c *Core) streamCh() uint32 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.nextStream - 1
}

// runRPC answers terminal.run from the script: an address from RunAddresses
// (repeating the last when the script runs dry), the scripted error kind, or
// a refusal when nothing was scripted. A success also emits the created
// event, after the reply, the way a live core does.
func (c *Core) runRPC(conn net.Conn, id uint64, params json.RawMessage, framed bool) {
	var p struct {
		Argv []string `json:"argv"`
		Cwd  string   `json:"cwd"`
	}
	json.Unmarshal(params, &p)
	fail := func(kind string) {
		c.reply(conn, id, nil, mustJSON(map[string]any{
			"code": -32000, "message": "run failed",
			"data": map[string]any{"kind": kind},
		}), framed)
	}
	if c.RunErrKind != "" {
		fail(c.RunErrKind)
		return
	}
	if len(c.RunAddresses) == 0 {
		fail("unavailable")
		return
	}
	c.mu.Lock()
	addr := c.RunAddresses[c.runIdx]
	if c.runIdx < len(c.RunAddresses)-1 {
		c.runIdx++
	}
	c.runCalls = append(c.runCalls, RunCall{Argv: p.Argv, Cwd: p.Cwd})
	seq := c.Seq + c.runSeq + 1
	c.runSeq++
	c.mu.Unlock()
	rec := map[string]any{
		"address": addr, "argv": p.Argv, "cwd": p.Cwd, "state": "live",
		"labels": map[string]string{}, "created_ms": 0, "screen": "exact",
		"cols": 80, "rows": 24,
	}
	c.reply(conn, id, mustJSON(rec), nil, framed)
	// The record follows asynchronously on the events channel.
	c.Event("event", map[string]any{
		"seq": seq, "type": "created", "terminal": addr, "data": rec,
	})
}

// Runs snapshots every terminal.run the viewer made, in order.
func (c *Core) Runs() []RunCall {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]RunCall, len(c.runCalls))
	copy(out, c.runCalls)
	return out
}

// Emit sends one JSON-RPC notification as channel-0 DATA (plain-mode shape;
// hc tolerates it framed).
func (c *Core) Emit(method string, params any) {
	c.writeTo0(frame{ch: 0, kind: kindData, payload: mustJSON(map[string]any{
		"jsonrpc": "2.0", "method": method, "params": params,
	})})
}

// Event sends a notification on the events channel as one DATA frame.
func (c *Core) Event(method string, params any) {
	c.writeTo0(frame{ch: eventsChannel, kind: kindData, payload: mustJSON(map[string]any{
		"jsonrpc": "2.0", "method": method, "params": params,
	})})
}

// Delta sends stream bytes on the current attach channel once the viewer
// attached (the latest one, for panes that reattached).
func (c *Core) Delta(b []byte) {
	c.writeTo0(frame{ch: c.streamCh(), kind: kindData, payload: b})
}

// DeltaCh sends stream bytes on a chosen attach channel — the second and
// later panes, whose attaches got channels past 7.
func (c *Core) DeltaCh(ch uint32, b []byte) {
	c.writeTo0(frame{ch: ch, kind: kindData, payload: b})
}

// CloseStream sends CLOSE with a JSON reason on the current attach channel.
func (c *Core) CloseStream(reasonJSON string) {
	c.writeTo0(frame{ch: c.streamCh(), kind: kindClose, payload: []byte(reasonJSON)})
}

// CloseStreamCh sends CLOSE with a JSON reason on a chosen attach channel.
func (c *Core) CloseStreamCh(ch uint32, reasonJSON string) {
	c.writeTo0(frame{ch: ch, kind: kindClose, payload: []byte(reasonJSON)})
}

func (c *Core) writeTo0(f frame) {
	c.mu.Lock()
	conns := c.conns
	c.mu.Unlock()
	if len(conns) == 0 {
		c.t.Fatal("testcore: no connection")
	}
	c.write(conns[len(conns)-1], f)
}

func (c *Core) write(conn net.Conn, f frame) {
	if len(f.payload) > maxPayload {
		panic("testcore: payload too large")
	}
	var buf []byte
	buf = appendFrame(buf, f)
	conn.Write(buf)
}

func (c *Core) record(f frame) {
	c.mu.Lock()
	c.frames = append(c.frames, Frame{Ch: f.ch, Kind: uint8(f.kind), Payload: f.payload})
	c.mu.Unlock()
}

// Frames snapshots every frame the viewer sent.
func (c *Core) Frames() []Frame {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Frame, len(c.frames))
	copy(out, c.frames)
	return out
}

// Has reports whether a frame matching pred was received.
func (c *Core) Has(pred func(Frame) bool) bool {
	for _, f := range c.Frames() {
		if pred(f) {
			return true
		}
	}
	return false
}

// --- independent wire codec (server side) ---

type frame struct {
	ch      uint32
	kind    uint8
	payload []byte
}

func appendFrame(dst []byte, f frame) []byte {
	var hdr [9]byte
	binary.LittleEndian.PutUint32(hdr[0:4], f.ch)
	hdr[4] = f.kind
	binary.LittleEndian.PutUint32(hdr[5:9], uint32(len(f.payload)))
	dst = append(dst, hdr[:]...)
	return append(dst, f.payload...)
}

func readFrame(br *bufio.Reader) (frame, error) {
	var hdr [9]byte
	if _, err := io.ReadFull(br, hdr[:]); err != nil {
		return frame{}, err
	}
	f := frame{
		ch:   binary.LittleEndian.Uint32(hdr[0:4]),
		kind: hdr[4],
	}
	n := binary.LittleEndian.Uint32(hdr[5:9])
	if n > 0 {
		f.payload = make([]byte, n)
		if _, err := io.ReadFull(br, f.payload); err != nil {
			return frame{}, err
		}
	}
	return f, nil
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func uitoa(n uint64) string { b, _ := json.Marshal(n); return string(b) }
func boolJSON(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// ServeTB is Serve for benchmarks and other testing.TB users. Identical
// behavior; c.t stays nil, so only happy-path cores should use it.
func ServeTB(tb testing.TB) *Core {
	tb.Helper()
	dir := tb.TempDir()
	path := filepath.Join(dir, "core.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		tb.Fatalf("listen: %v", err)
	}
	c := &Core{syncPath: path, nextStream: streamChannel}
	tb.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			c.serveConn(conn)
		}
	}()
	return c
}
