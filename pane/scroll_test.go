package pane

// Scrollback: OpScrollDelta / OpScrollBottom move the emulator's viewport,
// publish renders the scrolled rows (the RenderState row iterator follows the
// viewport — the fact TestViewportFollowsScroll pins), Scrolled reports the
// cursor being outside it, and new output while scrolled leaves the viewport
// anchored. Only OpScrollBottom returns to the live tail.

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"loom/internal/testcore"
)

// paintLines writes n numbered lines, enough to push all but the last screen
// into scrollback on the default 80x24 emulator.
func paintLines(n int) []byte {
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "LINE-%02d\r\n", i)
	}
	return []byte(b.String())
}

func topRow(s *Screen) string {
	rows := strings.Split(screenText(s), "\n")
	if len(rows) == 0 {
		return ""
	}
	return rows[0]
}

// The regression pin for the probe: RenderState's row iterator yields the
// SCROLLED viewport, so a -10 delta moves the top row exactly ten lines back.
func TestViewportFollowsScroll(t *testing.T) {
	core := testcore.Serve(t)
	_, p := wirePane(t, core, false)
	attach(t, p)

	core.Delta(paintLines(48))
	if !waitFor(2*time.Second, func() bool {
		return topRow(p.Screen()) == "LINE-25"
	}) {
		t.Fatalf("live top row = %q, want LINE-25", topRow(p.Screen()))
	}

	p.Enqueue(Op{Kind: OpScrollDelta, N: -10})
	if !waitFor(2*time.Second, func() bool {
		return topRow(p.Screen()) == "LINE-15"
	}) {
		t.Fatalf("top row after -10 = %q, want LINE-15 (ten lines back)", topRow(p.Screen()))
	}
}

func TestScrollDeltaAndBottomAroundScrolledFlag(t *testing.T) {
	core := testcore.Serve(t)
	_, p := wirePane(t, core, false)
	attach(t, p)

	core.Delta(paintLines(40))
	if !waitFor(2*time.Second, func() bool {
		s := p.Screen()
		return s != nil && strings.Contains(screenText(s), "LINE-39") && !s.Scrolled
	}) {
		t.Fatalf("live screen wrong: %q scrolled=%v", screenText(p.Screen()), screenScrolled(p))
	}

	// A large negative delta clamps at the top of history; the screen shows
	// the earliest lines and Scrolled is on.
	p.Enqueue(Op{Kind: OpScrollDelta, N: -1000})
	if !waitFor(2*time.Second, func() bool {
		s := p.Screen()
		return s != nil && strings.Contains(screenText(s), "LINE-00") && s.Scrolled
	}) {
		t.Fatalf("scrolled screen wrong: %q scrolled=%v", screenText(p.Screen()), screenScrolled(p))
	}
	s := p.Screen()
	if strings.Contains(screenText(s), "LINE-39") {
		t.Fatal("bottom line still visible after clamped scroll")
	}

	// OpScrollBottom returns to the live tail and clears the flag.
	p.Enqueue(Op{Kind: OpScrollBottom})
	if !waitFor(2*time.Second, func() bool {
		s := p.Screen()
		return s != nil && strings.Contains(screenText(s), "LINE-39") && !s.Scrolled
	}) {
		t.Fatalf("bottom screen wrong: %q scrolled=%v", screenText(p.Screen()), screenScrolled(p))
	}
}

// While scrolled, new output publishes but the viewport stays anchored: no
// auto-jump to bottom, the fresh text lands below the viewport.
func TestScrollStaysAnchoredOnNewOutput(t *testing.T) {
	core := testcore.Serve(t)
	_, p := wirePane(t, core, false)
	attach(t, p)

	core.Delta(paintLines(40))
	if !waitFor(2*time.Second, func() bool {
		return strings.Contains(screenText(p.Screen()), "LINE-39")
	}) {
		t.Fatal("lines never painted")
	}

	p.Enqueue(Op{Kind: OpScrollDelta, N: -1000})
	if !waitFor(2*time.Second, func() bool {
		return strings.Contains(screenText(p.Screen()), "LINE-00")
	}) {
		t.Fatal("scroll never landed")
	}

	core.Delta([]byte("\r\nFRESH-OUTPUT"))
	if !waitFor(2*time.Second, func() bool {
		s := p.Screen()
		return s.Scrolled && strings.Contains(screenText(s), "LINE-00")
	}) {
		t.Fatalf("viewport left its anchor: %q scrolled=%v", screenText(p.Screen()), screenScrolled(p))
	}
	if strings.Contains(screenText(p.Screen()), "FRESH-OUTPUT") {
		t.Fatal("new output jumped the anchored viewport to the tail")
	}

	// Only OpScrollBottom returns live, with the fresh output in view.
	p.Enqueue(Op{Kind: OpScrollBottom})
	if !waitFor(2*time.Second, func() bool {
		s := p.Screen()
		return !s.Scrolled && strings.Contains(screenText(s), "FRESH-OUTPUT")
	}) {
		t.Fatalf("bottom after fresh output: %q scrolled=%v", screenText(p.Screen()), screenScrolled(p))
	}
}

func screenScrolled(p *Pane) bool {
	if s := p.Screen(); s != nil {
		return s.Scrolled
	}
	return false
}
