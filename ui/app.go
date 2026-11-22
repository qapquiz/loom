package ui

import (
	"encoding/json"
	"os"

	tea "charm.land/bubbletea/v2"

	"bloomfx.dev/bloom"
	"golang.org/x/sys/unix"

	"loom/hc"
	"loom/pane"
	"loom/room"
)

// App is the tea.Model: it pumps hc events and pane wakes onto the tea loop,
// owns the room and the one attached pane, and renders the sidebar and pane
// as a bloom tree. Room and App fields are touched only on the tea
// goroutine; the pane syncs through the session on its own.
type App struct {
	sess   *hc.Session
	ui     *bloom.Root
	room   room.Room
	node   *PaneNode
	pane   *pane.Pane
	active hc.TerminalID

	wake   chan struct{}
	width  int
	height int

	cellW, cellH uint32 // terminal cell px; 1x1 until the kernel reports real geometry
	sentW, sentH int    // last pane size posted from frame geometry
	focused      bool   // terminal focus; gates the passive size-follow rule
	paneLiveSeen bool
}

type paneRectID string

const paneRect paneRectID = "pane"

const sidebarWidth = 20

func New(sess *hc.Session) *App {
	return &App{
		sess:    sess,
		ui:      bloom.New(),
		wake:    make(chan struct{}, 1),
		focused: true,
		cellW:   1,
		cellH:   1,
	}
}

// notify is the pane's wake hook, called on the session goroutine; the
// buffered channel coalesces a burst of publishes into one redraw.
func (a *App) notify() {
	select {
	case a.wake <- struct{}{}:
	default:
	}
}

func (a *App) Init() tea.Cmd {
	return tea.Batch(a.ui.Init(), a.waitEvent(), a.waitWake())
}

func (a *App) waitEvent() tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-a.sess.Events()
		if !ok {
			return sessionClosedMsg{}
		}
		return eventMsg{ev}
	}
}

func (a *App) waitWake() tea.Cmd {
	return func() tea.Msg {
		<-a.wake
		return wakeMsg{}
	}
}

func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	cmds := []tea.Cmd{a.ui.Update(msg)}
	switch msg := msg.(type) {
	case eventMsg:
		a.onEvent(msg.ev)
		cmds = append(cmds, a.waitEvent())
	case wakeMsg:
		a.onWake()
		cmds = append(cmds, a.waitWake())
	case sessionClosedMsg:
		return a, tea.Quit
	case tea.WindowSizeMsg:
		a.width, a.height = msg.Width, msg.Height
		a.setCellPx(measureCellPx(msg.Width, msg.Height))
		a.postSize(msg.Width, msg.Height)
	case tea.KeyPressMsg:
		if msg.Code == 'q' && msg.Mod&tea.ModCtrl != 0 {
			return a, tea.Quit
		}
		if (msg.Code == tea.KeyPgUp || msg.Code == tea.KeyPgDown) && a.scrollPage(msg.Code) {
			break // scrollback owns the key; the pane never sees it
		}
		if a.pane != nil {
			a.pane.Enqueue(pane.Op{Kind: pane.OpKey, Key: toPaneKey(tea.Key(msg))})
		}
	case tea.PasteMsg:
		if a.pane != nil {
			a.pane.Enqueue(pane.Op{Kind: pane.OpKey, Key: pane.Key{Text: msg.Content}})
		}
	case tea.MouseClickMsg, tea.MouseReleaseMsg, tea.MouseMotionMsg, tea.MouseWheelMsg:
		a.routeMouse(msg.(tea.MouseMsg))
	case tea.FocusMsg:
		a.focused = true
		if a.pane != nil {
			a.pane.Enqueue(pane.Op{Kind: pane.OpActive, Active: true})
		}
	case tea.BlurMsg:
		a.focused = false
		if a.pane != nil {
			a.pane.Enqueue(pane.Op{Kind: pane.OpActive, Active: false})
		}
	}
	return a, tea.Batch(cmds...)
}

func (a *App) onEvent(ev hc.Event) {
	if a.room.Apply(ev) && a.pane == nil {
		a.attachFirst()
	}
	if ev.ID != a.active {
		return
	}
	switch ev.Kind {
	case hc.EventLost:
		a.paneChange(pane.ChangeLost)
	case hc.EventRestarted:
		a.paneChange(pane.ChangeRestarted)
	case hc.EventFinished:
		a.paneChange(pane.ChangeFinished)
	case hc.EventRemoved:
		a.paneChange(pane.ChangeRemoved)
	case hc.EventCreated:
		a.paneChange(pane.ChangeCreated)
	case hc.EventSize:
		// Passive viewers follow the core's size; while active our sent
		// resize is authoritative in flight (the last-active rule).
		if a.pane != nil && !a.focused {
			a.pane.Enqueue(pane.Op{Kind: pane.OpResize, Cols: ev.Info.Cols, Rows: ev.Info.Rows})
		}
	}
}

func (a *App) attachFirst() {
	id := pickAttach(&a.room, os.Getenv("LOOM_ATTACH"))
	if id == "" {
		return
	}
	p := pane.New(string(id), a.notify)
	p.SetPump(a.sess.Do)
	p.SetAttachFn(func() { a.sess.Attach(id, hcStream{p: p}) })
	a.pane = p
	a.node = NewPaneNode(p)
	a.active = id
	a.sentW, a.sentH = 0, 0
	a.paneLiveSeen = false
	p.Change(pane.ChangeAttach)
	// The initial WindowSizeMsg usually predates the snapshot, so re-post
	// the stored size here: OpResize records want while not live, and
	// Connected's reconcile imposes it (the pane's own idempotence keeps
	// this an echo, not a fight). Cell px rides the same re-post: the store
	// was dropped before SetPump existed.
	a.pushCellPx()
	a.postSize(a.width, a.height)
}

// pickAttach chooses the auto-attach target: LOOM_ATTACH by terminal id or
// title when set and present, else the first Listed. A set target that has
// not appeared yet returns empty, so loom waits instead of attaching an
// unrelated terminal; a later snapshot re-runs the pick.
func pickAttach(r *room.Room, want string) hc.TerminalID {
	ts := r.Terminals()
	if want != "" {
		for _, t := range ts {
			if t.State == room.Listed && (string(t.ID) == want || t.Info.Title == want || t.Info.Name == want) {
				return t.ID
			}
		}
		return ""
	}
	for _, t := range ts {
		if t.State == room.Listed {
			return t.ID
		}
	}
	return ""
}

func (a *App) paneChange(ch pane.Change) {
	if a.pane != nil {
		a.pane.Change(ch)
	}
}

// onWake reasserts focus on the first live frame: OpActive(true) queued
// before the attach was confirmed recorded intent only, so the FOCUS frame
// and forced reconcile still owe the stream after Connected.
func (a *App) onWake() {
	if a.pane != nil && !a.paneLiveSeen && a.pane.Phase() == pane.StateAttached {
		a.paneLiveSeen = true
		a.pane.Enqueue(pane.Op{Kind: pane.OpActive, Active: true})
		// Attach confirmed: the Display facts queued while not live were
		// dropped by the pane's phase gate, so the report is owed now — the
		// same debt FOCUS pays on this first live wake.
		a.sendFacts()
	}
}

// measureCellPx reads the terminal's pixel geometry straight from the
// kernel: bubbletea v2.1.2's WindowSizeMsg is cells-only (x/term's GetSize
// drops TIOCGWINSZ xpixel/ypixel), so loom does its own ioctl beside it.
// Zero pixels, a failed ioctl or a sub-cell quotient (rounded to 0) leave
// the 1x1 synthetic cell — the headless compromise.
func measureCellPx(cols, rows int) (uint32, uint32) {
	if cols <= 0 || rows <= 0 {
		return 1, 1
	}
	ws, err := unix.IoctlGetWinsize(int(os.Stdin.Fd()), unix.TIOCGWINSZ)
	if err != nil || ws.Xpixel == 0 || ws.Ypixel == 0 {
		return 1, 1
	}
	w := uint32(ws.Xpixel) / uint32(cols)
	h := uint32(ws.Ypixel) / uint32(rows)
	if w == 0 || h == 0 {
		return 1, 1
	}
	return w, h
}

// setCellPx threads pixel truth into the pane and reports it as Display
// facts whenever it changed; the stored value survives pane swaps (it is
// the terminal's truth, not the pane's).
func (a *App) setCellPx(w, h uint32) {
	if w == 0 || h == 0 {
		w, h = 1, 1
	}
	if w == a.cellW && h == a.cellH {
		return
	}
	a.cellW, a.cellH = w, h
	a.pushCellPx()
	a.sendFacts()
}

func (a *App) pushCellPx() {
	if a.pane != nil {
		a.pane.SetCellPx(a.cellW, a.cellH)
	}
}

// displayFacts is the schema's Display type, reported by the client: only
// the fields loom holds truth for are set (cell px); bg/fg/cursor stay
// null until a color source exists.
type displayFacts struct {
	Cell []uint32 `json:"cell"`
}

// sendFacts queues the Display report for the wire. The pane is the
// phase-gated writer: while not live the op is dropped, and the pending
// truth rides the attach-confirmation send instead (onWake).
func (a *App) sendFacts() {
	if a.pane == nil {
		return
	}
	raw, err := json.Marshal(displayFacts{Cell: []uint32{a.cellW, a.cellH}})
	if err != nil {
		return
	}
	a.pane.Enqueue(pane.Op{Kind: pane.OpFacts, Facts: string(raw)})
}

// postSize solves the tree at w×h and posts a RESIZE when the pane rect
// drifted. Solve mirrors View's layout, so this works before any frame
// exists on screen.
func (a *App) postSize(w, h int) {
	if w <= 0 || h <= 0 || a.pane == nil {
		return
	}
	if r, ok := bloom.Solve(w, h, a.layout()).Rect(paneRect); ok {
		if r.Dx() != a.sentW || r.Dy() != a.sentH {
			a.sentW, a.sentH = r.Dx(), r.Dy()
			a.pane.Enqueue(pane.Op{Kind: pane.OpResize, Cols: uint16(r.Dx()), Rows: uint16(r.Dy())})
		}
	}
}

// routeMouse lets wheel events over the pane drive loom's scrollback while
// the program inside does not own the mouse; everything else — and every
// mouse event while the program owns it — forwards as before.
func (a *App) routeMouse(msg tea.MouseMsg) {
	if w, ok := msg.(tea.MouseWheelMsg); ok && a.scrollWheel(w) {
		return
	}
	a.forwardMouse(msg)
}

// wheelLines is the scroll step per wheel event, the three-line xterm
// convention.
const wheelLines = 3

// scrollWheel turns one wheel event over the pane rect into a scrollback op
// when the program does not own the mouse. Reports whether it consumed the
// event.
func (a *App) scrollWheel(w tea.MouseWheelMsg) bool {
	if a.pane == nil {
		return false
	}
	if s := a.pane.Screen(); s == nil || s.MouseOn {
		return false
	}
	if !a.mouseOverPane(w.Mouse()) {
		return false
	}
	n := -wheelLines
	if w.Button == tea.MouseWheelDown {
		n = wheelLines
	}
	a.pane.Enqueue(pane.Op{Kind: pane.OpScrollDelta, N: n})
	return true
}

// scrollPage pages the pane's scrollback on PageUp/PageDown while the
// program does not own the mouse; with it owned the page keys stay program
// keys, exactly as before. The page is the pane's own Rows. Reports whether
// it consumed the key.
func (a *App) scrollPage(code rune) bool {
	if a.pane == nil {
		return false
	}
	s := a.pane.Screen()
	if s == nil || s.MouseOn {
		return false
	}
	page := s.Rows
	if page < 1 {
		page = 1
	}
	n := -page
	if code == tea.KeyPgDown {
		n = page
	}
	a.pane.Enqueue(pane.Op{Kind: pane.OpScrollDelta, N: n})
	return true
}

// forwardMouse maps a screen event to pane-relative cells and forwards it
// only while the program inside owns the mouse; otherwise the event belongs
// to the terminal's own selection and the pane stands down.
func (a *App) forwardMouse(msg tea.MouseMsg) {
	if a.pane == nil {
		return
	}
	s := a.pane.Screen()
	if s == nil || !s.MouseOn {
		return
	}
	if !a.mouseOverPane(msg.Mouse()) {
		return
	}
	r, ok := a.ui.Frame().Rect(paneRect)
	if !ok {
		return
	}
	m := msg.Mouse()
	a.pane.Enqueue(mouseToOp(msg, m.X-r.Min.X, m.Y-r.Min.Y))
}

// mouseOverPane reports whether a screen point sits inside the pane rect.
func (a *App) mouseOverPane(m tea.Mouse) bool {
	r, ok := a.ui.Frame().Rect(paneRect)
	if !ok {
		return false
	}
	return m.X >= r.Min.X && m.X < r.Max.X && m.Y >= r.Min.Y && m.Y < r.Max.Y
}

func (a *App) layout() bloom.Child {
	body := bloom.Child(bloom.Text("attaching…"))
	if a.node != nil {
		body = a.node
	}
	return bloom.Column(
		bloom.Fill(
			bloom.Row(
				bloom.Fixed(sidebarWidth, bloom.Text(sidebar(&a.room, a.active))),
				bloom.Fill(body).ID(paneRect),
			),
		),
	)
}

// View forces MouseModeAllMotion while the inner program owns the mouse:
// forwarding needs motion reporting, and MouseModeNone otherwise leaves the
// terminal's own selection working (bloom computes the least mode itself).
func (a *App) View() tea.View {
	v := a.ui.View(a.width, a.height, a.layout())
	v.AltScreen = true
	v.ReportFocus = true
	if s := a.paneScreen(); s != nil && s.MouseOn {
		v.MouseMode = tea.MouseModeAllMotion
	}
	return v
}

func (a *App) paneScreen() *pane.Screen {
	if a.pane == nil {
		return nil
	}
	return a.pane.Screen()
}

type eventMsg struct{ ev hc.Event }
type wakeMsg struct{}
type sessionClosedMsg struct{}
