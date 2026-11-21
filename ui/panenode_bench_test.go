package ui

import (
	"testing"
	"time"

	"loom/pane"
)

// benchScreen: a 200x60 pane with plain runs, styled runs, and wide glyphs,
// the shapes a vim frame actually carries. One op = the full assemble pass
// the node does for a changed frame.
func benchScreen() *pane.Screen {
	s := &pane.Screen{Cols: 200, Rows: 60, Cells: make([]pane.Cell, 200*60)}
	style := pane.Style{FG: 0x5FD7AF, Attr: pane.AttrBold}
	for row := 0; row < s.Rows; row++ {
		base := row * s.Cols
		for col := 0; col < s.Cols; col++ {
			switch {
			case col%37 == 0:
				s.Cells[base+col] = pane.Cell{Text: "世", S: style, Wide: true}
			case col%5 == 0:
				s.Cells[base+col] = pane.Cell{Text: string(rune('a' + col%26)), S: style}
			default:
				s.Cells[base+col] = pane.Cell{Text: string(rune('a' + col%26))}
			}
		}
	}
	return s
}

func BenchmarkRenderScreenChanged(b *testing.B) {
	s := benchScreen()
	for b.Loop() {
		if renderScreen(s) == "" {
			b.Fatal("empty render")
		}
	}
}

// The idle pane: the Screen pointer is unchanged, so View returns the cached
// string after one atomic load and a pointer compare.
func BenchmarkPaneNodeViewIdle(b *testing.B) {
	core, _, p := wirePane(b)
	p.Change(pane.ChangeAttach)
	if !waitForPhase(b, p, 2*time.Second, pane.StateAttached) {
		b.Fatalf("phase = %v", p.Phase())
	}
	core.Delta([]byte("bench"))
	waitForScreenText(b, p, 2*time.Second, "bench")
	n := NewPaneNode(p)
	n.View()
	for b.Loop() {
		if n.View() == "" {
			b.Fatal("empty view")
		}
	}
}
