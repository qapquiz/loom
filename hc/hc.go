package hc

// Session: one connection to an hc core. Dial performs the plain JSON-lines
// hello and upgrades to framed, then subscribes to events; the read-loop
// goroutine owns the socket reader, frame demux, RPC correlation, credit, and
// every Stream callback (all pane mutation happens there). Everything else
// only enqueues or writes under a mutex.
//
// Socket discovery confirmed against hc 0.1.21 on this machine (docs/security.md):
// HC_SOCKET wins, then HC_TERMINAL_SOCKET (set inside hc terminals), then
// $XDG_RUNTIME_DIR/herdr-core/<session>.sock, then /run/user/<uid>/herdr-core/,
// falling back to the session's state directory.

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Features struct{ AutomaticInput bool }

func (f Features) list() []string {
	if f.AutomaticInput {
		return []string{"automatic_input"}
	}
	return nil
}

func DefaultSocket() string {
	if p := os.Getenv("HC_SOCKET"); p != "" {
		return p
	}
	if p := os.Getenv("HC_TERMINAL_SOCKET"); p != "" {
		return p
	}
	session := os.Getenv("HC_SESSION")
	if session == "" {
		session = "default"
	}
	if r := os.Getenv("XDG_RUNTIME_DIR"); r != "" {
		return filepath.Join(r, "herdr-core", session+".sock")
	}
	if _, err := os.Stat("/run/user/" + strconv.Itoa(os.Getuid())); err == nil {
		return filepath.Join("/run/user", strconv.Itoa(os.Getuid()), "herdr-core", session+".sock")
	}
	state := os.Getenv("XDG_STATE_HOME")
	if state == "" {
		home, _ := os.UserHomeDir()
		state = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(state, "herdr-core", session, session+".sock")
}

type pendingCall struct {
	ch chan rpcResult  // buffered 1; nil for async calls
	fn func(rpcResult) // run on the read-loop goroutine
}

type rpcResult struct {
	res json.RawMessage
	err error
}

type Session struct {
	conn net.Conn
	br   *bufio.Reader

	writeMu sync.Mutex
	work    chan func() // closures for the read-loop goroutine (Session.Do)

	mu      sync.Mutex
	pending map[uint64]*pendingCall
	nextID  uint64
	streams map[uint32]*streamState
	evChID  uint32

	helloAuto bool // automatic_input granted in hello.features

	feed eventFeed
	evCh chan Event

	closeOnce sync.Once
	closed    chan struct{}
}

func Dial(socket string, f Features) (*Session, error) {
	conn, err := net.Dial("unix", socket)
	if err != nil {
		return nil, err
	}
	return DialConn(conn, f)
}

// DialConn runs the real handshake over any net.Conn — the loopback test seam
// (graft 2): every unit test exercises the same plain-to-framed upgrade, demux
// and credit as the live socket path.
func DialConn(conn net.Conn, f Features) (*Session, error) {
	br := bufio.NewReader(conn)
	req, err := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: 1, Method: "hello", Params: helloParams{Framed: true, Features: f.list()}})
	if err != nil {
		conn.Close()
		return nil, err
	}
	if _, err := conn.Write(append(req, '\n')); err != nil {
		conn.Close()
		return nil, err
	}
	line, err := br.ReadString('\n')
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("hc: hello reply: %w", err)
	}
	var msg rpcMessage
	if err := json.Unmarshal([]byte(line), &msg); err != nil {
		conn.Close()
		return nil, fmt.Errorf("hc: hello reply: %w", err)
	}
	if msg.Error != nil {
		conn.Close()
		return nil, msg.Error
	}
	var hello helloResult
	if err := json.Unmarshal(msg.Result, &hello); err != nil {
		conn.Close()
		return nil, fmt.Errorf("hc: hello result: %w", err)
	}
	if !hello.Framed {
		conn.Close()
		return nil, fmt.Errorf("hc: core did not confirm framed mode")
	}

	s := &Session{
		conn:      conn,
		br:        br,
		work:      make(chan func(), 1024),
		pending:   map[uint64]*pendingCall{},
		streams:   map[uint32]*streamState{},
		helloAuto: f.AutomaticInput && hasFeature(hello.Features, "automatic_input"),
		evCh:      make(chan Event, 64),
		closed:    make(chan struct{}),
	}
	go s.readLoop()

	res, err := s.call("events.subscribe", struct{}{})
	if err != nil {
		s.Close()
		return nil, fmt.Errorf("hc: events.subscribe: %w", err)
	}
	var sub eventSnapshotJSON
	if err := json.Unmarshal(res, &sub); err != nil {
		s.Close()
		return nil, fmt.Errorf("hc: subscribe result: %w", err)
	}
	s.mu.Lock()
	s.evChID = sub.Channel
	s.mu.Unlock()
	s.feed.snapshot(sub.Seq)
	ev := Event{Kind: EventSnapshot, Seq: sub.Seq}
	for _, rec := range sub.Terminals {
		ev.All = append(ev.All, rec.info())
	}
	s.evCh <- ev
	return s, nil
}

func (s *Session) Events() <-chan Event {
	return s.evCh
}

// Do runs fn on the read-loop goroutine between frames — the enqueue-only
// UI→session path the pane's op/machine queue rides. Fire-and-forget: dropped
// when the queue is full (UI-rate work only). The read loop drains the queue
// before each blocking read and re-checks on a short read deadline, so queued
// work runs even when no traffic is flowing (idle keystrokes included).

// Attach enqueues terminal.attach on the events-free channel 0; the result
// arrives via in.Connected / in.Closed on the read-loop goroutine.
func (s *Session) Attach(id TerminalID, in Stream) {
	// First paint lands at the attach size; a viewer RESIZE follows once the
	// machine goes live (pane reconcile). One frame of 80x24 is accepted
	// (synthesis tradeoff).
	params := struct {
		Terminal string `json:"terminal"`
		Cols     uint16 `json:"cols"`
		Rows     uint16 `json:"rows"`
	}{string(id), 80, 24}
	s.callAsync("terminal.attach", params, func(r rpcResult) {
		if r.err != nil {
			in.Closed(CloseReason{Reason: "attach_failed"})
			return
		}
		var res struct {
			Channel        uint32 `json:"channel"`
			AutomaticInput bool   `json:"automatic_input"`
		}
		if err := json.Unmarshal(r.res, &res); err != nil {
			in.Closed(CloseReason{Reason: "attach_failed"})
			return
		}
		// Double negotiation: hello feature granted AND attach reply confirms.
		auto := s.helloAuto && res.AutomaticInput
		st := &streamState{
			in:  in,
			out: streamOut{s: s, ch: res.Channel, autoInput: auto},
		}
		s.mu.Lock()
		s.streams[res.Channel] = st
		s.mu.Unlock()
		in.Connected(st.out, auto)
	})
}

func (s *Session) Close() error {
	s.closeOnce.Do(func() { close(s.closed) })
	return s.conn.Close()
}

// --- calls ---

func (s *Session) call(method string, params any) (json.RawMessage, error) {
	pc, _, err := s.send(method, params, nil)
	if err != nil {
		return nil, err
	}
	select {
	case r := <-pc.ch:
		return r.res, r.err
	case <-s.closed:
		return nil, fmt.Errorf("hc: session closed while %s in flight", method)
	}
}

func (s *Session) callAsync(method string, params any, fn func(rpcResult)) {
	if _, _, err := s.send(method, params, fn); err != nil {
		fn(rpcResult{err: err})
	}
}

// send registers a pending call and writes the request on channel 0. A nil fn
// means the caller waits on the reply channel; otherwise the read loop runs fn
// with the reply.
func (s *Session) send(method string, params any, fn func(rpcResult)) (*pendingCall, uint64, error) {
	s.mu.Lock()
	id := s.nextID
	s.nextID++
	pc := &pendingCall{}
	if fn != nil {
		pc.fn = fn
	} else {
		pc.ch = make(chan rpcResult, 1)
	}
	s.pending[id] = pc
	s.mu.Unlock()

	req, err := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params})
	if err == nil {
		err = s.writeFrame(frame{ch: 0, kind: frameData, payload: req})
	}
	if err != nil {
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
		return nil, 0, err
	}
	return pc, id, nil
}

// --- read loop ---

// readLoop: the session goroutine. Owns the socket reader, frame demux, RPC
// correlation, credit, stream callbacks, and Do closures.
func (s *Session) readLoop() {
	for {
		s.drainWork()
		s.conn.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
		f, err := readFrame(s.br)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			break
		}
		if !s.dispatch(f) {
			break
		}
	}
	s.teardown()
}

func (s *Session) drainWork() {
	for {
		select {
		case fn := <-s.work:
			fn()
		default:
			return
		}
	}
}

// dispatch returns false when the session is over (channel-0 CLOSE).
func (s *Session) dispatch(f frame) bool {
	switch f.kind {
	case frameClose:
		if f.ch == 0 {
			return false
		}
		s.mu.Lock()
		st := s.streams[f.ch]
		delete(s.streams, f.ch)
		s.mu.Unlock()
		if st != nil {
			st.in.Closed(parseClose(f.payload))
		}
		return true
	case frameData:
		switch {
		case f.ch == 0:
			s.rpcData(f.payload)
		case s.isEventCh(f.ch):
			s.eventData(f.ch, f.payload)
		default:
			s.streamData(f.ch, f.payload)
		}
	case frameResize:
		// Core-to-observer RESIZE arrives only with resize_notifications,
		// which the spike does not negotiate. Size is tracked via events.
	case frameCredit:
		// Core credits our tiny writes; the spike never blocks on this.
	}
	return true
}

func (s *Session) isEventCh(ch uint32) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return ch == s.evChID && s.evChID != 0
}

func (s *Session) rpcData(payload []byte) {
	var msg rpcMessage
	if err := json.Unmarshal(payload, &msg); err != nil {
		return
	}
	if msg.ID == nil {
		// Notifications on channel 0 (plain-mode shape); tolerate on framed.
		var n eventNote
		if json.Unmarshal(payload, &n) == nil && n.Method != "" {
			s.deliver(s.feed.note(n)...)
		}
		return
	}
	s.mu.Lock()
	pc := s.pending[*msg.ID]
	delete(s.pending, *msg.ID)
	s.mu.Unlock()
	if pc == nil {
		return
	}
	r := rpcResult{res: msg.Result}
	if msg.Error != nil {
		r.err = msg.Error
	}
	if pc.ch != nil {
		pc.ch <- r
	} else if pc.fn != nil {
		pc.fn(r)
	}
}

func (s *Session) eventData(ch uint32, payload []byte) {
	for _, line := range strings.Split(string(payload), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var n eventNote
		if json.Unmarshal([]byte(line), &n) != nil || n.Method == "" {
			continue
		}
		s.deliver(s.feed.note(n)...)
	}
	s.credit(ch, len(payload))
}

func (s *Session) streamData(ch uint32, payload []byte) {
	s.mu.Lock()
	st := s.streams[ch]
	s.mu.Unlock()
	if st == nil {
		return
	}
	// The callback processes synchronously; credit flows without pane
	// cooperation (synthesis rule).
	st.in.Delta(payload)
	s.credit(ch, len(payload))
}

func (s *Session) credit(ch uint32, n int) {
	if n == 0 {
		return
	}
	var payload [4]byte
	binary.LittleEndian.PutUint32(payload[:], uint32(n))
	s.writeFrame(frame{ch: ch, kind: frameCredit, payload: payload[:]})
}

func (s *Session) deliver(evs ...Event) {
	for _, ev := range evs {
		s.evCh <- ev
	}
}

func (s *Session) Do(fn func()) {
	select {
	case s.work <- fn:
	default:
	}
}

func (s *Session) teardown() {
	s.mu.Lock()
	streams := make([]*streamState, 0, len(s.streams))
	for _, st := range s.streams {
		streams = append(streams, st)
	}
	s.streams = map[uint32]*streamState{}
	s.mu.Unlock()
	for _, st := range streams {
		st.in.Closed(CloseReason{Reason: "session_closed"})
	}
	close(s.evCh)
}

func (s *Session) writeFrame(f frame) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return writeFrame(s.conn, f)
}
