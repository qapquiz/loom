package ui

import (
	"strings"
	"testing"
	"time"

	"bloomfx.dev/bloom"
	"github.com/charmbracelet/x/ansi"

	"loom/pane"
)

func blankScreen(w, h int) *pane.Screen {
	return &pane.Screen{Cols: w, Rows: h, Cells: make([]pane.Cell, w*h)}
}

// putLine writes one run of runes starting at col, two cells for a wide
// rune (head plus zeroed tail), so callers don't fight byte offsets.
func putLine(s *pane.Screen, row, col int, text string, st pane.Style) {
	for _, r := range text {
		if r >= 0x4E00 && r <= 0x9FFF { // the CJK range the tests use
			s.Cells[row*s.Cols+col] = pane.Cell{R: r, S: st, Wide: true}
			col += 2
			continue
		}
		s.Cells[row*s.Cols+col] = pane.Cell{R: r, S: st}
		col++
	}
}

func TestRenderScreenGolden(t *testing.T) {
	s := blankScreen(10, 4)
	putLine(s, 0, 0, "plain here", pane.Style{})
	red := pane.Style{FG: 0xFF0000, Attr: pane.AttrBold}
	putLine(s, 1, 0, "lo", pane.Style{})
	putLine(s, 1, 2, "mo", red)
	putLine(s, 2, 0, "ok 世界", pane.Style{})
	putLine(s, 3, 0, "vim emacs ", pane.Style{})
	s.Cursor = pane.Cursor{Col: 4, Row: 3, Visible: true}

	want := "plain here\n" +
		"lo\x1b[0m\x1b[1;38;2;255;0;0mmo\x1b[0m      \n" +
		"ok 世界   \n" +
		"vim \x1b[0m\x1b[7me\x1b[0mmacs "
	if got := renderScreen(s); got != want {
		t.Fatalf("renderScreen:\n got %q\nwant %q", got, want)
	}

	// An invisible cursor renders the row plain.
	s.Cursor = pane.Cursor{Col: 4, Row: 3, Visible: false}
	if got := renderScreen(s); strings.Contains(got, "\x1b[7m") {
		t.Fatalf("invisible cursor styled: %q", got)
	}
}

func TestRenderScreenStyledRunToEndOfRow(t *testing.T) {
	s := blankScreen(4, 1)
	putLine(s, 0, 0, "abcd", pane.Style{Attr: pane.AttrBold})
	want := "\x1b[0m\x1b[1mabcd\x1b[0m"
	if got := renderScreen(s); got != want {
		t.Fatalf("renderScreen = %q, want %q", got, want)
	}
}

func TestRenderScreenEmpty(t *testing.T) {
	if got := renderScreen(nil); got != "" {
		t.Fatalf("nil screen = %q", got)
	}
	if got := renderScreen(blankScreen(0, 0)); got != "" {
		t.Fatalf("empty screen = %q", got)
	}
}

// The rendered string must parse back into an aligned cell grid: wide glyphs
// take their two columns, styles stay inside their runs.
func TestRenderScreenParsesThroughBloom(t *testing.T) {
	s := blankScreen(10, 4)
	putLine(s, 0, 0, "plain here", pane.Style{})
	red := pane.Style{FG: 0xFF0000, Attr: pane.AttrBold}
	putLine(s, 1, 0, "lo", pane.Style{})
	putLine(s, 1, 2, "mo", red)
	putLine(s, 2, 0, "ok 世界", pane.Style{})
	putLine(s, 3, 0, "vim emacs ", pane.Style{})

	out := ansi.Strip(bloom.Render(10, 4, bloom.Fixed(10, bloom.Text(renderScreen(s)))))
	lines := strings.Split(out, "\n")
	want := []string{"plain here", "lomo      ", "ok 世界   ", "vim emacs "}
	if len(lines) != len(want) {
		t.Fatalf("bloom render gave %d lines: %q", len(lines), lines)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Fatalf("line %d = %q, want %q", i, lines[i], want[i])
		}
	}
}

func TestPaneNodeCachesByScreenIdentity(t *testing.T) {
	core, _, p := wirePane(t)
	p.Change(pane.ChangeAttach)
	if !waitForPhase(t, p, 2*time.Second, pane.StateAttached) {
		t.Fatalf("attach: phase = %v", p.Phase())
	}
	core.Delta([]byte("paint"))
	waitForScreenText(t, p, 2*time.Second, "paint")

	n := NewPaneNode(p)
	v1 := n.View()
	n.View()
	n.View()
	if n.builds != 1 {
		t.Fatalf("idle pane rebuilt %d times", n.builds)
	}

	core.Delta([]byte("!"))
	waitForScreenText(t, p, 2*time.Second, "paint!")
	v2 := n.View()
	if n.builds != 2 {
		t.Fatalf("changed frame rebuilt %d times, want 2", n.builds)
	}
	if v1 == v2 || !strings.Contains(v2, "paint!") {
		t.Fatalf("cached view after change: %q then %q", v1, v2)
	}
}
