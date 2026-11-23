package pane

// The boundary: Screen is the ONE type that crosses from emulator state to
// the UI. Published immutable; the UI may hold and read it on any goroutine,
// forever. No hc frame, no JSON-RPC, no binding handle ever crosses.

import (
	"sync/atomic"

	"go.mitchellh.com/libghostty"
)

type Cell struct {
	// Text is the cell's full UTF-8 grapheme cluster, base codepoint first
	// with combining marks riding inside it; empty means blank, rendered as
	// a space.
	Text string
	S    Style
	Wide bool // head cell of a double-width rune; the tail spacer reads as empty
}

type Style struct {
	FG   uint32 // 0xRRGGBB, 0 = default
	BG   uint32
	Attr uint16
}

const (
	AttrBold          uint16 = 1 << 0
	AttrFaint         uint16 = 1 << 1
	AttrItalic        uint16 = 1 << 2
	AttrUnderline     uint16 = 1 << 3
	AttrStrikethrough uint16 = 1 << 4
	AttrInverse       uint16 = 1 << 5
)

type Cursor struct {
	Col, Row int
	Visible  bool
}

type Screen struct {
	Cols, Rows int
	Cells      []Cell // row-major, len == Cols*Rows, pre-parsed and blit-ready
	Cursor     Cursor
	Title      string // last TitleChanged effect
	Status     string // last ProgramStatus effect (OSC 7501); never screen-scraped
	MouseOn    bool   // program owns the mouse: bloom's MouseMode/selection stand down
	Scrolled   bool   // viewport is parked off the cursor in scrollback; only OpScrollBottom returns live
}

type OpKind uint8

const (
	OpKey OpKind = iota
	OpMouse
	OpResize
	OpActive
	OpFacts
	OpScrollDelta  // local viewport scroll; N lines, negative up
	OpScrollBottom // return the viewport to the cursor (live tail)
)

// Key and Mouse are plain data on purpose: libghostty's KeyEvent/MouseEvent
// are C handles, single-goroutine, and must never cross to the UI goroutine
// (the synthesis sketch's ghostty.KeyInput does not exist in the pinned API;
// these structs are the data-not-handles shape it intended).
type Key struct {
	Text string
	Code libghostty.Key
	Mods libghostty.Mods
}

type Mouse struct {
	Action   libghostty.MouseAction
	Button   libghostty.MouseButton
	Mods     libghostty.Mods
	Col, Row uint16 // cell-local coords
}

type Op struct {
	Kind   OpKind
	Key    Key
	Mouse  Mouse
	Cols   uint16
	Rows   uint16
	Active bool   // OpActive: loom became (or stopped being) the active viewer
	Facts  string // OpFacts: raw Display JSON; the app owns the payload, the pane transports it (string keeps Op comparable, like Key.Text)
	N      int    // OpScrollDelta: lines to scroll, negative up
}

// Pane implements Stream. All emulator mutation happens on the session
// read-loop goroutine: Stream callbacks arrive there, and Enqueue/Change run
// their work through the pump (the enqueue-only UI→session path), so exactly
// one goroutine owns the state at any moment. Screen is the only lock-free
// read.
type Pane struct {
	id   string
	wake func()

	pump    func(func()) // the session runner; set by SetPump before first use
	screen  atomic.Pointer[Screen]
	state   atomic.Uint32 // machine state; written only on the session goroutine, read from any
	closed  atomic.Bool   // set by Close on the pump; makes enqueue a no-op afterwards
	out     StreamOut
	active  bool
	wantCol uint16
	wantRow uint16
	sentCol uint16
	sentRow uint16
	cellW   uint32 // cell px; 1x1 until the kernel reports real geometry. Session-goroutine only
	cellH   uint32

	emu      *emulator
	attachFn func() // wave-2 seam: the app wires the transport's attach
}

func New(id string, wake func()) *Pane {
	p := &Pane{
		id:      id,
		wake:    wake,
		wantCol: 80,
		wantRow: 24,
		sentCol: 80,
		sentRow: 24,
		cellW:   1,
		cellH:   1,
	}
	p.state.Store(uint32(StateIdle))
	p.resetEmulator()
	return p
}

func (p *Pane) ID() string { return p.id }

// Phase reports the machine state; writes are gated on it (graft 4), never on
// a parallel flag.
func (p *Pane) Phase() State { return State(p.state.Load()) }

// Screen returns the last published frame, nil before the first paint.
func (p *Pane) Screen() *Screen { return p.screen.Load() }

// Enqueue queues an op; non-blocking, dropped when the session queue is full.
func (p *Pane) Enqueue(op Op) { p.enqueue(op) }

// Change feeds a transport lifecycle change (lost/restarted/finished/...).
// The app routes transport events here; the machine is the only place they land.
func (p *Pane) Change(ch Change) { p.enqueue(ch) }

// SetPump wires the session-goroutine runner (the session's Do). Must be called
// before the first Enqueue/Change; before that they are dropped.
func (p *Pane) SetPump(fn func(func())) { p.pump = fn }

// SetCellPx records the terminal's real cell size in pixels (loom's own
// TIOCGWINSZ; the pane keeps the 1x1 synthetic cell when there is none).
// Stored on the session goroutine — publish, Resize and the size-report
// effect read it there, honoring the bindings' single-goroutine contract.
// Dropped before SetPump; callers re-post (the app pushes its stored value
// at attach time, mirroring postSize).
func (p *Pane) SetCellPx(w, h uint32) {
	if w == 0 || h == 0 {
		w, h = 1, 1
	}
	p.enqueue(cellSize{w, h})
}

// cellSize is the pump-side store request for SetCellPx.
type cellSize struct{ w, h uint32 }

func (p *Pane) enqueue(v any) {
	if p.pump == nil {
		return
	}
	p.pump(func() {
		if p.closed.Load() {
			return
		}
		switch v := v.(type) {
		case closeOp:
			p.closed.Store(true)
			if p.emu != nil {
				p.emu.close()
				p.emu = nil
			}
			p.out = nil
		case Op:
			p.apply(v)
		case Change:
			p.applyChange(v)
		case cellSize:
			p.cellW, p.cellH = v.w, v.h
			p.syncMouseSize(p.wantCol, p.wantRow) // the encoder's px context must not wait for the next dirty publish
		}
	})
}

// --- Stream — called on the session goroutine ---

func (p *Pane) Connected(out StreamOut, autoInput bool) {
	p.out = out
	p.applyChange(ChangeAttachOK)
	p.reconcile(false) // impose want if it drifted from the attach size
}

func (p *Pane) Delta(b []byte) {
	if p.emu == nil {
		return
	}
	p.emu.write(b)
	p.publish(false)
}

func (p *Pane) Closed(r CloseReason) {
	p.out = nil
	switch r.Reason {
	case "exited":
		p.applyChange(ChangeCloseExited)
	case "detached", "attach_failed":
		p.applyChange(ChangeCloseDetached)
	default:
		// Loss closes streams with its reason; want-attach survives it.
		p.applyChange(ChangeCloseLost)
	}
	p.wakeOnce()
}

func (p *Pane) applyChange(ch Change) {
	st, effects := advance(State(p.state.Load()), ch)
	p.state.Store(uint32(st))
	for _, eff := range effects {
		switch eff {
		case EffAttach:
			if p.attachFn != nil {
				p.attachFn()
			}
		case EffReset:
			p.resetEmulator()
		}
	}
}

// SetAttachFn wires the attach effect to a session; called once at app
// setup before any machine run.
func (p *Pane) SetAttachFn(fn func()) { p.attachFn = fn }

// Close tears the pane down on the pump goroutine: the emulator handles are
// released and every later enqueued op or change is dropped. Safe to call
// more than once and from any goroutine. The pane keeps its last Screen so a
// caller that still holds it renders something instead of nothing.
func (p *Pane) Close() {
	p.enqueue(closeOp{})
}

// closeOp rides the pump queue like an Op so teardown is ordered after work
// already queued and never races a Delta on the handles.
type closeOp struct{}

func (p *Pane) wakeOnce() {
	if p.wake != nil {
		p.wake()
	}
}
