package ui

import (
	"encoding/json"
	"image"
	"os"

	tea "charm.land/bubbletea/v2"

	"bloomfx.dev/bloom"
	"bloomfx.dev/bloom/fx"
	"golang.org/x/sys/unix"

	"loom/hc"
	"loom/pane"
	"loom/room"
)

// App is the tea.Model: it pumps hc events and pane wakes onto the tea loop,
// owns the room, the pane tree and the input mode machine, and renders the
// sidebar and panes as a bloom tree. Room and App fields are touched only on
// the tea goroutine; panes sync through the session on their own.
type App struct {
	sess *hc.Session
	ui   *bloom.Root
	room room.Room

	tree  *layoutNode                 // pane layout; nil until the first attach
	panes map[hc.TerminalID]*paneSlot // every open slot, by terminal id
	focus hc.TerminalID               // the pane keys and paste go to
	mode  inputMode                   // keyboard state machine

	chooser *chooserState // the open window chooser; non-nil iff mode == modeChooser

	wake          chan struct{}
	width, height int

	cellW, cellH uint32 // terminal cell px; 1x1 until the kernel reports real geometry
	termFocus    bool   // terminal focus; gates the passive size-follow rule
}

const sidebarWidth = 20

func New(sess *hc.Session) *App {
	return &App{
		sess:      sess,
		ui:        bloom.New(),
		panes:     map[hc.TerminalID]*paneSlot{},
		wake:      make(chan struct{}, 1),
		termFocus: true,
		cellW:     1,
		cellH:     1,
	}
}

// notify is a pane's wake hook, called on the session goroutine; the
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
	case runMsg:
		// A failed run leaves the layout single-pane; the room's events are
		// the surface for what happened (the core reports the spawn).
		if msg.err == nil {
			a.addPane(msg.id, msg.vert)
		}
	case sessionClosedMsg:
		return a, tea.Quit
	case tea.WindowSizeMsg:
		a.width, a.height = msg.Width, msg.Height
		a.setCellPx(measureCellPx(msg.Width, msg.Height))
		a.postSize(msg.Width, msg.Height)
	case tea.KeyPressMsg:
		if cmd := a.key(msg); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case tea.PasteMsg:
		a.sendKey(pane.Key{Text: msg.Content})
	case tea.MouseClickMsg, tea.MouseReleaseMsg, tea.MouseMotionMsg, tea.MouseWheelMsg:
		a.routeMouse(msg.(tea.MouseMsg))
	case tea.FocusMsg:
		a.termFocus = true
		a.activeOp(pane.Op{Kind: pane.OpActive, Active: true})
	case tea.BlurMsg:
		a.termFocus = false
		a.activeOp(pane.Op{Kind: pane.OpActive, Active: false})
	}
	return a, tea.Batch(cmds...)
}

// key runs the input mode machine over one keypress and returns the command
// it spawned (nil for everything routed straight to a pane). ctrl+q quits
// from any mode. In prefix mode the key is swallowed either way: bound →
// command, unbound → dropped, second ctrl+b → a literal 0x02 forwarded to
// the focused pane (tmux semantics). ctrl+b enters prefix only from
// passthrough — the chooser owns its keys and swallows all of them: nothing
// reaches the panes, and the prefix machinery never nests inside it.
func (a *App) key(msg tea.KeyPressMsg) tea.Cmd {
	if msg.Code == 'q' && msg.Mod&tea.ModCtrl != 0 {
		a.mode = modePassthrough
		return tea.Quit
	}
	if a.mode == modeChooser {
		a.chooserKey(msg)
		return nil
	}
	if a.mode == modePrefix {
		a.mode = modePassthrough
		if msg.Code == 'b' && msg.Mod&tea.ModCtrl != 0 {
			a.sendKey(pane.Key{Text: "\x02"})
		} else if r, ok := bindRune(msg); ok {
			if cmd, bound := bindings[r]; bound {
				return a.runCommand(cmd)
			}
		}
		return nil
	}
	if a.mode == modePassthrough && msg.Code == 'b' && msg.Mod&tea.ModCtrl != 0 {
		a.mode = modePrefix
		return nil
	}
	if (msg.Code == tea.KeyPgUp || msg.Code == tea.KeyPgDown) && a.scrollPage(msg.Code) {
		return nil // scrollback owns the key; the pane never sees it
	}
	a.sendKey(toPaneKey(tea.Key(msg)))
	return nil
}

// runCommand executes one bound command. Splits spawn a shell in a new
// terminal and graft it beside the focused pane once Run answers; the
// chooser opens its window list over the pane area.
func (a *App) runCommand(cmd command) tea.Cmd {
	switch cmd {
	case cmdSplitVertical:
		return a.split(true)
	case cmdSplitHorizontal:
		return a.split(false)
	case cmdChooseWindow:
		a.openChooser()
	}
	return nil
}

// split asks the core for a fresh shell terminal (bg, cwd $HOME); runMsg
// grafts it in. Run blocks on the RPC reply, so it runs inside the command,
// never on the tea goroutine.
func (a *App) split(vert bool) tea.Cmd {
	sess, cwd := a.sess, homeDir()
	return func() tea.Msg {
		id, err := sess.Run(nil, cwd)
		return runMsg{id: id, vert: vert, err: err}
	}
}

func homeDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "."
	}
	return home
}

// sendKey routes one key op to the focused pane only.
func (a *App) sendKey(k pane.Key) {
	if s := a.panes[a.focus]; s != nil {
		s.p.Enqueue(pane.Op{Kind: pane.OpKey, Key: k})
	}
}

// activeOp addresses the focused pane with a viewer-activity op: loom's
// window focus is the focused pane's activity; the others stay passive.
func (a *App) activeOp(op pane.Op) {
	if s := a.panes[a.focus]; s != nil {
		s.p.Enqueue(op)
	}
}

// setFocus moves pane focus: the old pane drops to a passive viewer
// (FOCUS 0 on the wire), the new one takes viewer activity (OpActive
// records intent while its attach is still in flight; the first live wake
// pays the FOCUS debt).
func (a *App) setFocus(id hc.TerminalID) {
	if a.focus == id {
		return
	}
	if old := a.panes[a.focus]; old != nil {
		old.p.Enqueue(pane.Op{Kind: pane.OpActive, Active: false})
	}
	a.focus = id
	if s := a.panes[id]; s != nil {
		s.p.Enqueue(pane.Op{Kind: pane.OpActive, Active: true})
	}
}

func (a *App) onEvent(ev hc.Event) {
	changed := a.room.Apply(ev)
	if changed {
		if a.tree == nil {
			a.attachFirst()
		}
		if a.chooser != nil {
			a.chooser.refresh(&a.room, a.openPanes()) // the list is live while open
		}
	}
	s := a.panes[ev.ID]
	if s == nil {
		return
	}
	switch ev.Kind {
	case hc.EventLost:
		s.p.Change(pane.ChangeLost)
	case hc.EventRestarted:
		s.p.Change(pane.ChangeRestarted)
	case hc.EventFinished:
		s.p.Change(pane.ChangeFinished)
	case hc.EventRemoved:
		s.p.Change(pane.ChangeRemoved)
	case hc.EventCreated:
		s.p.Change(pane.ChangeCreated)
	case hc.EventSize:
		// Passive viewers follow the core's size: every pane but the
		// focused one, and every pane while loom's window is blurred. The
		// focused pane's in-flight resize stays authoritative (the
		// last-active rule).
		if !a.termFocus || ev.ID != a.focus {
			s.p.Enqueue(pane.Op{Kind: pane.OpResize, Cols: ev.Info.Cols, Rows: ev.Info.Rows})
		}
	}
}

// attachFirst auto-attaches the one initial pane once the room knows a
// candidate; later panes only ever come from splits.
func (a *App) attachFirst() {
	if a.tree != nil {
		return
	}
	id := pickAttach(&a.room, os.Getenv("LOOM_ATTACH"))
	if id == "" {
		return
	}
	a.addPane(id, false)
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

// newSlot builds one pane slot for id: the pane, its stream adapter and its
// bloom leaf, wired to the session exactly like every other pane. addPane
// splits it in beside the focus; replaceFocused swaps it into the focused
// leaf; the wiring is one shape either way.
func (a *App) newSlot(id hc.TerminalID) *paneSlot {
	p := pane.New(string(id), a.notify)
	p.SetPump(a.sess.Do)
	st := &hcStream{p: p}
	p.SetAttachFn(func() { a.sess.Attach(id, st) })
	return &paneSlot{id: id, p: p, node: NewPaneNode(p), stream: st}
}

// addPane grafts a terminal into the tree — the first pane becomes the
// whole tree, later ones split the focused leaf — and focuses it. The
// room's created event follows on its own; the attach goes straight to the
// id Run returned (or pickAttach chose), never through the room.
func (a *App) addPane(id hc.TerminalID, vert bool) {
	if id == "" {
		return
	}
	if _, dup := a.panes[id]; dup {
		return
	}
	s := a.newSlot(id)
	a.panes[id] = s
	if a.tree == nil {
		a.tree = &layoutNode{leaf: s}
	} else {
		a.tree.split(a.focus, &layoutNode{leaf: s}, vert)
	}
	a.setFocus(id)
	p := s.p
	p.Change(pane.ChangeAttach)
	// The attach's standing debts, the same ones the first pane pays: the
	// cell px the pane was born without, and the size that predates the
	// attach — recorded while not live, imposed by Connected's reconcile
	// (the pane's own idempotence makes the re-post an echo, not a fight).
	p.SetCellPx(a.cellW, a.cellH)
	a.postSize(a.width, a.height)
}

// onWake pays each pane's attach-confirmation debts on its first live
// frame: viewer activity (the focused pane active, the others passive) and
// the Display facts queued while not live, which the pane's phase gate
// dropped — the same debt the single-pane app paid on its one first wake.
func (a *App) onWake() {
	for _, s := range a.panes {
		if !s.liveSeen && s.p.Phase() == pane.StateAttached {
			s.liveSeen = true
			s.p.Enqueue(pane.Op{Kind: pane.OpActive, Active: s.id == a.focus})
			a.sendFacts(s)
		}
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

// setCellPx threads pixel truth into every pane and reports it as Display
// facts whenever it changed; the stored value survives pane swaps (it is
// the terminal's truth, not any pane's).
func (a *App) setCellPx(w, h uint32) {
	if w == 0 || h == 0 {
		w, h = 1, 1
	}
	if w == a.cellW && h == a.cellH {
		return
	}
	a.cellW, a.cellH = w, h
	for _, s := range a.panes {
		s.p.SetCellPx(w, h)
		a.sendFacts(s)
	}
}

// displayFacts is the schema's Display type, reported by the client: only
// the fields loom holds truth for are set (cell px); bg/fg/cursor stay
// null until a color source exists.
type displayFacts struct {
	Cell []uint32 `json:"cell"`
}

// sendFacts queues the Display report for one pane's stream. The pane is
// the phase-gated writer: while not live the op is dropped, and the pending
// truth rides the attach-confirmation send instead (onWake).
func (a *App) sendFacts(s *paneSlot) {
	if s == nil {
		return
	}
	raw, err := json.Marshal(displayFacts{Cell: []uint32{a.cellW, a.cellH}})
	if err != nil {
		return
	}
	s.p.Enqueue(pane.Op{Kind: pane.OpFacts, Facts: string(raw)})
}

// postSize solves the tree at w×h and posts each pane's RESIZE when its own
// rect drifted: every pane is its own viewer of its own terminal, keyed by
// the pane id as the bloom rect id. Solve mirrors View's layout, so this
// works before any frame exists on screen.
func (a *App) postSize(w, h int) {
	if w <= 0 || h <= 0 || a.tree == nil {
		return
	}
	f := bloom.Solve(w, h, a.layout())
	for _, s := range a.panes {
		r, ok := f.Rect(s.id)
		if !ok || (r.Dx() == s.sentW && r.Dy() == s.sentH) {
			continue
		}
		s.sentW, s.sentH = r.Dx(), r.Dy()
		s.p.Enqueue(pane.Op{Kind: pane.OpResize, Cols: uint16(r.Dx()), Rows: uint16(r.Dy())})
	}
}

// routeMouse hit-tests the pane rects first: a click focuses the pane it
// landed in before any forwarding decision, and wheel scrolling and MouseOn
// forwarding apply to that hit pane, not the focused one. The solve mirrors
// View's layout, so routing works before a frame exists on screen.
func (a *App) routeMouse(msg tea.MouseMsg) {
	hit := a.hitPane(msg.Mouse())
	if _, ok := msg.(tea.MouseClickMsg); ok && hit != nil {
		a.setFocus(hit.id)
	}
	if w, ok := msg.(tea.MouseWheelMsg); ok && a.scrollWheel(hit, w) {
		return
	}
	a.forwardMouse(hit, msg)
}

// wheelLines is the scroll step per wheel event, the three-line xterm
// convention.
const wheelLines = 3

// scrollWheel turns one wheel event over the hit pane's rect into that
// pane's scrollback op when its program does not own the mouse. Reports
// whether it consumed the event.
func (a *App) scrollWheel(s *paneSlot, w tea.MouseWheelMsg) bool {
	if s == nil {
		return false
	}
	scr := s.p.Screen()
	if scr == nil || scr.MouseOn {
		return false
	}
	n := -wheelLines
	if w.Button == tea.MouseWheelDown {
		n = wheelLines
	}
	s.p.Enqueue(pane.Op{Kind: pane.OpScrollDelta, N: n})
	return true
}

// scrollPage pages the focused pane's scrollback on PageUp/PageDown while
// its program does not own the mouse; with it owned the page keys stay
// program keys, exactly as before. The page is the pane's own Rows.
// Reports whether it consumed the key.
func (a *App) scrollPage(code rune) bool {
	s := a.panes[a.focus]
	if s == nil {
		return false
	}
	scr := s.p.Screen()
	if scr == nil || scr.MouseOn {
		return false
	}
	page := scr.Rows
	if page < 1 {
		page = 1
	}
	n := -page
	if code == tea.KeyPgDown {
		n = page
	}
	s.p.Enqueue(pane.Op{Kind: pane.OpScrollDelta, N: n})
	return true
}

// forwardMouse maps a screen event to pane-relative cells and forwards it to
// the hit pane only while the program inside owns the mouse; otherwise the
// event belongs to the terminal's own selection and the pane stands down.
func (a *App) forwardMouse(s *paneSlot, msg tea.MouseMsg) {
	if s == nil {
		return
	}
	scr := s.p.Screen()
	if scr == nil || !scr.MouseOn {
		return
	}
	r, ok := a.paneRect(s.id)
	if !ok {
		return
	}
	m := msg.Mouse()
	s.p.Enqueue(mouseToOp(msg, m.X-r.Min.X, m.Y-r.Min.Y))
}

// paneRect solves the current layout for one pane's rect — the same solve
// postSize and the hit-test use.
func (a *App) paneRect(id hc.TerminalID) (image.Rectangle, bool) {
	if a.tree == nil || a.width <= 0 || a.height <= 0 {
		return image.Rectangle{}, false
	}
	return bloom.Solve(a.width, a.height, a.layout()).Rect(id)
}

// hitPane finds the pane whose solved rect contains the screen point, in
// layout order (a before b), or nil when the point is over none.
func (a *App) hitPane(m tea.Mouse) *paneSlot {
	if a.tree == nil || a.width <= 0 || a.height <= 0 {
		return nil
	}
	f := bloom.Solve(a.width, a.height, a.layout())
	var hit *paneSlot
	a.tree.leaves(func(s *paneSlot) {
		if hit != nil {
			return
		}
		if r, ok := f.Rect(s.id); ok && m.X >= r.Min.X && m.X < r.Max.X && m.Y >= r.Min.Y && m.Y < r.Max.Y {
			hit = s
		}
	})
	return hit
}

func (a *App) layout() bloom.Child {
	body := bloom.Child(bloom.Text("attaching…"))
	if a.tree != nil {
		body = a.tree.child()
	}
	if a.chooser != nil {
		// The chooser is a centered layer over the pane area, not a full
		// modal: the panes keep rendering beneath a light dim, and the
		// sidebar stays bright beside it.
		body = bloom.Overlay(body,
			bloom.Layer(bloom.Text(a.chooser.view(a))).
				Margin(1).Backdrop(fx.Dim{Amount: chooserDim}))
	}
	return bloom.Column(
		bloom.Fill(
			bloom.Row(
				bloom.Fixed(sidebarWidth, bloom.Text(sidebar(&a.room, a.focus, a.openPanes()))),
				bloom.Fill(body),
			),
		),
	)
}

// openPanes is the sidebar's attachment view: which room records have a
// pane in loom's tree right now.
func (a *App) openPanes() map[hc.TerminalID]bool {
	open := make(map[hc.TerminalID]bool, len(a.panes))
	for id := range a.panes {
		open[id] = true
	}
	return open
}

// View forces MouseModeAllMotion while the focused pane's program owns the
// mouse: forwarding needs motion reporting, and MouseModeNone otherwise
// leaves the terminal's own selection working (bloom computes the least
// mode itself).
func (a *App) View() tea.View {
	v := a.ui.View(a.width, a.height, a.layout())
	v.AltScreen = true
	v.ReportFocus = true
	if s := a.paneScreen(); s != nil && s.MouseOn {
		v.MouseMode = tea.MouseModeAllMotion
	}
	return v
}

// paneScreen is the focused pane's screen — the one whose mouse ownership
// decides loom's own mouse reporting.
func (a *App) paneScreen() *pane.Screen {
	if s := a.panes[a.focus]; s != nil {
		return s.p.Screen()
	}
	return nil
}

type eventMsg struct{ ev hc.Event }
type wakeMsg struct{}
type sessionClosedMsg struct{}
type runMsg struct {
	id   hc.TerminalID // the address terminal.run returned; "" never reaches here
	vert bool          // graft orientation for the new slot
	err  error
}
