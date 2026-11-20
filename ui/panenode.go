package ui

// PaneNode is the bloom leaf for one terminal. bloom's node contract is
// View() string (nodes have no per-cell draw API; canvas.Surface is for
// effects and would break the unchanged-frame skip), so the pane's Screen is
// assembled into a styled string by direct SGR emission from Cells, one
// pass, no lipgloss.
//
// The string is cached by Screen pointer identity. The pane publishes a
// fresh immutable Screen only when the frame actually changed, so pointer
// identity is the frame generation: an idle pane returns the cached string
// and costs bloom one unchanged-frame comparison.

import (
	"strconv"
	"strings"

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

// renderScreen rows join with \n, each exactly Cols cells wide: a wide rune
// is one glyph whose tail spacer column is skipped, because a cell-based
// parser gives the glyph two columns. The cursor cell renders inverse so the
// pane shows the terminal cursor without a tea.Cursor on the view.
func renderScreen(s *pane.Screen) string {
	if s == nil || s.Cols <= 0 || s.Rows <= 0 {
		return ""
	}
	var b strings.Builder
	cur := styleState{}
	for row := 0; row < s.Rows; row++ {
		skip := false
		base := row * s.Cols
		for col := 0; col < s.Cols; col++ {
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
				b.WriteString("\x1b[0m")
				if p := want.sgrParams(); p != "" {
					b.WriteString("\x1b[")
					b.WriteString(p)
					b.WriteString("m")
				}
				cur = want
			}
			if c.R == 0 {
				b.WriteByte(' ')
			} else {
				b.WriteRune(c.R)
			}
		}
		// End every styled run at the row edge so lines stay independent.
		if cur != (styleState{}) {
			b.WriteString("\x1b[0m")
			cur = styleState{}
		}
		if row < s.Rows-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// styleState mirrors the SGR state a cell parser holds. Zero value is the
// terminal default; equality decides when a transition is emitted.
type styleState struct {
	fg, bg uint32
	attr   uint16
}

var attrSGR = []struct {
	bit  uint16
	code string
}{
	{pane.AttrBold, "1"},
	{pane.AttrFaint, "2"},
	{pane.AttrItalic, "3"},
	{pane.AttrUnderline, "4"},
	{pane.AttrStrikethrough, "9"},
	{pane.AttrInverse, "7"},
}

// sgrParams sets everything that differs from default. A reset is always
// emitted before it, so nothing needs an explicit off code.
func (s styleState) sgrParams() string {
	var parts []string
	for _, a := range attrSGR {
		if s.attr&a.bit != 0 {
			parts = append(parts, a.code)
		}
	}
	if s.fg != 0 {
		parts = append(parts, "38;2;"+rgb(s.fg))
	}
	if s.bg != 0 {
		parts = append(parts, "48;2;"+rgb(s.bg))
	}
	return strings.Join(parts, ";")
}

func rgb(c uint32) string {
	return strconv.Itoa(int(c>>16&0xff)) + ";" + strconv.Itoa(int(c>>8&0xff)) + ";" + strconv.Itoa(int(c&0xff))
}
