package ui

// PaneNode is the bloom leaf for one terminal. bloom's node contract is
// View() string (nodes have no per-cell draw API; canvas.Surface is for
// effects and would break the unchanged-frame skip), so the pane's Screen is
// assembled into a styled string by direct SGR emission from Cells, no
// lipgloss.
//
// The string is cached by Screen pointer identity. The pane publishes a
// fresh immutable Screen only when the frame actually changed, so pointer
// identity is the frame generation: an idle pane returns the cached string
// and costs bloom one unchanged-frame comparison.

import (
	"loom/pane"
)

type PaneNode struct {
	pane   *pane.Pane
	last   *pane.Screen
	view   string
	builds int // rebuild count; the observable behind the idle-pane contract
}

func NewPaneNode(p *pane.Pane) *PaneNode { return &PaneNode{pane: p} }

func (n *PaneNode) View() string {
	s := n.pane.Screen()
	if s == n.last {
		return n.view
	}
	n.last = s
	n.builds++
	n.view = renderScreen(s)
	return n.view
}

// scrollbackMark is the scrolled indicator: drawn over the top-right cells
// of row 0 while the viewport is parked in scrollback. scrollMarkStyle is a
// fixed inverse style so it reads as a marker over whatever content sits
// underneath. All marker cells are single-width, so a wide head just before
// the zone is simply overwritten (its tail column becomes marker text);
// scrollbackMarkCells is the rune slice because '↑' is three UTF-8 bytes.
const scrollbackMark = " ↑ scrollback"

var scrollbackMarkCells = []rune(scrollbackMark)

var scrollMarkStyle = styleState{attr: pane.AttrInverse}

// renderScreen rows join with \n, each exactly Cols cells wide: a wide rune
// is one glyph whose tail spacer column is skipped, because a cell-based
// parser gives the glyph two columns. The cursor cell renders inverse so the
// pane shows the terminal cursor without a tea.Cursor on the view.
func renderScreen(s *pane.Screen) string {
	if s == nil || s.Cols <= 0 || s.Rows <= 0 {
		return ""
	}
	// Capacity from a one-pass worst-case estimate (sizeScreen), so append
	// allocates once and never regrows: a dense frame spends ~40 SGR bytes
	// per cell, far past any per-cell guess, and regrowth is a second alloc
	// plus a memcpy the emit pass already paid for once.
	b := make([]byte, 0, sizeScreen(s))
	cur := styleState{}
	markStart := s.Cols // no marker: col >= Cols never fires
	if s.Scrolled && s.Cols >= len(scrollbackMarkCells) {
		markStart = s.Cols - len(scrollbackMarkCells)
	}
	for row := 0; row < s.Rows; row++ {
		skip := false
		base := row * s.Cols
		for col := 0; col < s.Cols; col++ {
			if row == 0 && col >= markStart {
				if scrollMarkStyle != cur {
					b = appendSGR(b, scrollMarkStyle)
					cur = scrollMarkStyle
				}
				b = append(b, string(scrollbackMarkCells[col-markStart])...)
				continue
			}
			c := s.Cells[base+col]
			if skip {
				skip = false
				continue
			}
			skip = c.Wide
			want := styleState{fg: c.S.FG, bg: c.S.BG, attr: c.S.Attr}
			if s.Cursor.Visible && row == s.Cursor.Row && col == s.Cursor.Col {
				want.attr |= pane.AttrInverse
			}
			if want != cur {
				b = appendSGR(b, want)
				cur = want
			}
			if c.Text == "" {
				b = append(b, ' ')
			} else {
				b = append(b, c.Text...)
			}
		}
		// End every styled run at the row edge so lines stay independent.
		if cur != (styleState{}) {
			b = append(b, "\x1b[0m"...)
			cur = styleState{}
		}
		if row < s.Rows-1 {
			b = append(b, '\n')
		}
	}
	return string(b)
}

// sizeScreen bounds the bytes renderScreen will emit for s, so the emit
// pass allocates once. It shares only emit's transition state machine
// (which cells change style); the SGR byte count is a worst-case constant
// instead of a second copy of appendSGR's arithmetic. Every term
// over-counts on purpose (wide-tail cells are priced like real cells, each
// text byte carries one spare), so drift from emit can only waste
// capacity, never under-allocate and regrow.
func sizeScreen(s *pane.Screen) int {
	n := s.Rows * 5 // a newline plus at most one 4-byte row-end reset per row
	transitions := 0
	cur := styleState{}
	markStart := s.Cols // no marker: col >= Cols never fires
	if s.Scrolled && s.Cols >= len(scrollbackMarkCells) {
		markStart = s.Cols - len(scrollbackMarkCells)
	}
	for row := 0; row < s.Rows; row++ {
		base := row * s.Cols
		for col := 0; col < s.Cols; col++ {
			if row == 0 && col >= markStart {
				n += 3 // marker cell bytes: most are 1, '↑' is 3 UTF-8 bytes
				if scrollMarkStyle != cur {
					transitions++
					cur = scrollMarkStyle
				}
				continue
			}
			c := s.Cells[base+col]
			n += len(c.Text) + 1 // cluster bytes, or the space when empty
			want := styleState{fg: c.S.FG, bg: c.S.BG, attr: c.S.Attr}
			if s.Cursor.Visible && row == s.Cursor.Row && col == s.Cursor.Col {
				want.attr |= pane.AttrInverse
			}
			if want != cur {
				transitions++
				cur = want
			}
		}
		cur = styleState{} // emit ends every styled run at the row edge
	}
	return n + transitions*sgrMaxBytes + 64
}

// sgrMaxBytes bounds one appendSGR emission: reset plus CSI, every attr bit
// at its code-plus-separator, two color triples at their three-digit max
// (8 prefix/separator bytes + 9 digits). Extending appendSGR past this
// costs capacity at worst.
var sgrMaxBytes = 4 + 2 + 2*len(attrSGR) + 2*(8+9)

// styleState mirrors the SGR state a cell parser holds. Zero value is the
// terminal default; equality decides when a transition is emitted.
type styleState struct {
	fg, bg uint32
	attr   uint16
}

var attrSGR = []struct {
	bit  uint16
	code byte // SGR codes for the attrs are all single digits
}{
	{pane.AttrBold, '1'},
	{pane.AttrFaint, '2'},
	{pane.AttrItalic, '3'},
	{pane.AttrUnderline, '4'},
	{pane.AttrStrikethrough, '9'},
	{pane.AttrInverse, '7'},
}

// appendSGR emits the transition to s straight into b: a reset, then one
// SGR sequence when s sets anything. A reset always precedes it, so nothing
// needs an explicit off code. Written as bytes, no strconv, no Join.
func appendSGR(b []byte, s styleState) []byte {
	b = append(b, "\x1b[0m"...)
	mark := len(b)
	b = append(b, "\x1b["...)
	for _, a := range attrSGR {
		if s.attr&a.bit != 0 {
			b = append(b, a.code, ';')
		}
	}
	if s.fg != 0 {
		b = appendRGB(b, "38;2;", s.fg)
	}
	if s.bg != 0 {
		b = appendRGB(b, "48;2;", s.bg)
	}
	if len(b) == mark+2 {
		return b[:mark] // nothing set: the reset alone restores the default
	}
	b[len(b)-1] = 'm' // the trailing ';' becomes the terminator
	return b
}

func appendRGB(b []byte, pfx string, c uint32) []byte {
	b = append(b, pfx...)
	b = appendUint(b, c>>16&0xff)
	b = append(b, ';')
	b = appendUint(b, c>>8&0xff)
	b = append(b, ';')
	b = appendUint(b, c&0xff)
	return append(b, ';')
}

func appendUint(b []byte, v uint32) []byte {
	if v == 0 {
		return append(b, '0')
	}
	var tmp [10]byte // room for any uint32
	i := len(tmp)
	for v > 0 {
		i--
		tmp[i] = byte('0' + v%10)
		v /= 10
	}
	return append(b, tmp[i:]...)
}
