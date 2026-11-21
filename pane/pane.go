package pane

// The boundary: Screen is the ONE type that crosses from emulator state to
// the UI. Published immutable; the UI may hold and read it on any goroutine,
// forever. No hc frame, no JSON-RPC, no binding handle ever crosses.

import (
	"sync/atomic"

	"go.mitchellh.com/libghostty"

	"loom/hc"
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
}

type OpKind uint8

const (
	OpKey OpKind = iota
	OpMouse
	OpResize
	OpActive
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
	Active bool // OpActive: loom became (or stopped being) the active viewer
}

// Pane implements hc.Stream. All emulator mutation happens on hc's read-loop
// goroutine: Stream callbacks arrive there, and Enqueue/Change run their work
// through Session.Do (the enqueue-only UI→session path), so exactly one
// goroutine owns the state at any moment. Screen is the only lock-free read.
type Pane struct {
	id   hc.TerminalID
	wake func()

	pump    func(func()) // hc.Session.Do; set by SetPump before first use
	screen  atomic.Pointer[Screen]
	state   atomic.Uint32 // machine state; written only on the session goroutine, read from any
	out     hc.StreamOut
	active  bool
	wantCol uint16
	wantRow uint16
	sentCol uint16
	sentRow uint16

	emu      *emulator
	attachFn func() // wave-2 seam: the app sets sess.Attach(id, p)
}

func New(id hc.TerminalID, wake func()) *Pane {
	p := &Pane{
		id:      id,
		wake:    wake,
		wantCol: 80,
		wantRow: 24,
		sentCol: 80,
		sentRow: 24,
	}
	p.state.Store(uint32(StateIdle))
	p.resetEmulator()
	return p
}

func (p *Pane) ID() hc.TerminalID { return p.id }

// Phase reports the machine state; writes are gated on it (graft 4), never on
// a parallel flag.
func (p *Pane) Phase() State { return State(p.state.Load()) }

// Screen returns the last published frame, nil before the first paint.
func (p *Pane) Screen() *Screen { return p.screen.Load() }

// Enqueue queues an op; non-blocking, dropped when the session queue is full.
func (p *Pane) Enqueue(op Op) { p.enqueue(op) }

// Change feeds an hc-derived lifecycle change (lost/restarted/finished/...).
// The app routes hc events here; the machine is the only place they land.
func (p *Pane) Change(ch Change) { p.enqueue(ch) }

// SetPump wires the session-goroutine runner (hc.Session.Do). Must be called
// before the first Enqueue/Change; before that they are dropped.
func (p *Pane) SetPump(fn func(func())) { p.pump = fn }

func (p *Pane) enqueue(v any) {
	if p.pump == nil {
		return
	}
	p.pump(func() {
		switch v := v.(type) {
		case Op:
			p.apply(v)
		case Change:
			p.applyChange(v)
		}
	})
}

// --- hc.Stream — called on the session goroutine ---

func (p *Pane) Connected(out hc.StreamOut, autoInput bool) {
	p.out = out
	p.applyChange(ChangeAttachOK)
	p.reconcile(false) // impose want if it drifted from the attach size
}

func (p *Pane) Delta(b []byte) {
	if p.emu == nil {
		return
	}
	p.emu.write(b)
	p.publish()
}

func (p *Pane) Closed(r hc.CloseReason) {
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

func (p *Pane) wakeOnce() {
	if p.wake != nil {
		p.wake()
	}
}
