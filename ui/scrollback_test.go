package ui

// Scrollback routing at the app boundary: wheel over the pane and PageUp/
// PageDown drive the pane's scrollback while the program does not own the
// mouse; with it owned, wheels forward as mouse bytes and page keys stay
// program keys. The pane node paints the scrolled indicator.

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"bloomfx.dev/bloom"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"loom/internal/testcore"
	"loom/pane"
)

func paintLines(n int) []byte {
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "LINE-%02d\r\n", i)
	}
	return []byte(b.String())
}

func waitForScreen(t *testing.T, a *App, d time.Duration, fn func(s *pane.Screen) bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if s := a.paneScreen(); s != nil && fn(s) {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("screen never reached state: %q scrolled=%v", plainText(a.paneScreen()), a.paneScreen() != nil && a.paneScreen().Scrolled)
}

func attachedApp(t *testing.T) (*testcore.Core, *App) {
	t.Helper()
	c, a := newApp(t, `[{"address":"t7","title":"vim","cols":80,"rows":24}]`)
	if !waitForPhase(t, a.pane, 2*time.Second, pane.StateAttached) {
		t.Fatalf("no attach; phase = %v", a.pane.Phase())
	}
	// 100 wide minus the sidebar leaves the pane 80x30; a frame must exist
	// for the pane rect the wheel routing tests against.
	a.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	a.View()
	return c, a
}

func TestAppWheelScrollsPaneWhenMouseNotOwned(t *testing.T) {
	c, a := attachedApp(t)
	c.Delta(paintLines(80))
	waitForScreen(t, a, 2*time.Second, func(s *pane.Screen) bool {
		return strings.Contains(plainText(s), "LINE-79")
	})

	// Wheel up over the pane: scrollback op, no DATA on the wire.
	n := len(c.Frames())
	a.Update(tea.MouseWheelMsg{X: 50, Y: 10, Button: tea.MouseWheelUp})
	waitForScreen(t, a, 2*time.Second, func(s *pane.Screen) bool {
		return s.Scrolled
	})
	time.Sleep(50 * time.Millisecond)
	for _, f := range c.Frames()[n:] {
		if f.Kind == 0 && f.Ch == 7 {
			t.Fatalf("wheel forwarded without mouse ownership: %q", f.Payload)
		}
	}

	// Wheel down climbs back; at the bottom the flag clears.
	for i := 0; i < 4; i++ {
		a.Update(tea.MouseWheelMsg{X: 50, Y: 10, Button: tea.MouseWheelDown})
	}
	waitForScreen(t, a, 2*time.Second, func(s *pane.Screen) bool {
		return !s.Scrolled && strings.Contains(plainText(s), "LINE-79")
	})

	// A wheel outside the pane rect (over the sidebar) is not a scroll.
	n = len(c.Frames())
	topBefore := plainText(a.paneScreen())
	a.Update(tea.MouseWheelMsg{X: 5, Y: 10, Button: tea.MouseWheelUp})
	time.Sleep(100 * time.Millisecond)
	if s := a.paneScreen(); s.Scrolled || plainText(s) != topBefore {
		t.Fatal("wheel over sidebar scrolled the pane")
	}
}

func TestAppWheelForwardsWhenMouseOwned(t *testing.T) {
	c, a := attachedApp(t)
	c.Delta([]byte("\x1b[?1000h\x1b[?1006h"))
	waitMouseOn(t, a)

	// Screen (25,3) over a rect at x=20 is pane cell (5,3); SGR is 1-based
	// and the wheel is button 64, so the wire must carry <64;6;4.
	a.Update(tea.MouseWheelMsg{X: 25, Y: 3, Button: tea.MouseWheelUp})
	waitForFrame(t, c, 2*time.Second, func(f testcore.Frame) bool {
		return f.Kind == 0 && f.Ch == 7 && strings.Contains(string(f.Payload), "\x1b[<64;6;4M")
	})
}

func TestAppPageKeysScrollByPaneRows(t *testing.T) {
	c, a := attachedApp(t)
	c.Delta(paintLines(80))
	waitForScreen(t, a, 2*time.Second, func(s *pane.Screen) bool {
		return strings.Contains(plainText(s), "LINE-79")
	})

	// PageUp scrolls one pane page (the pane's own 30 rows): the top of
	// history comes into view and the flag is on.
	a.Update(tea.KeyPressMsg{Code: tea.KeyPgUp})
	waitForScreen(t, a, 2*time.Second, func(s *pane.Screen) bool {
		return s.Scrolled && strings.Contains(plainText(s), "LINE-50") && !strings.Contains(plainText(s), "LINE-79")
	})

	// PageDown restores the live tail exactly.
	a.Update(tea.KeyPressMsg{Code: tea.KeyPgDown})
	waitForScreen(t, a, 2*time.Second, func(s *pane.Screen) bool {
		return !s.Scrolled && strings.Contains(plainText(s), "LINE-79")
	})
}

func TestAppPageKeysForwardWhenMouseOwned(t *testing.T) {
	c, a := attachedApp(t)
	c.Delta([]byte("\x1b[?1000h"))
	waitMouseOn(t, a)

	// The program owns the mouse: PageUp stays a program key (xterm 5~),
	// never a scroll.
	a.Update(tea.KeyPressMsg{Code: tea.KeyPgUp})
	waitForFrame(t, c, 2*time.Second, func(f testcore.Frame) bool {
		return f.Kind == 0 && f.Ch == 7 && strings.Contains(string(f.Payload), "\x1b[5~")
	})
}

// The scrolled indicator: inverse "↑ scrollback" over row 0's right edge,
// only while Scrolled, and only when the screen is wide enough for it.
func TestRenderScreenScrolledIndicator(t *testing.T) {
	s := blankScreen(40, 3)
	putLine(s, 0, 0, "row zero content here pad", pane.Style{})
	s.Scrolled = true
	got := renderScreen(s)
	if !strings.Contains(got, "\x1b[7m ↑ scrollback\x1b[0m") {
		t.Fatalf("scrolled row missing indicator: %q", got)
	}
	// The indicator replaced content cells only right of col 27, so the row
	// still parses Cols wide through bloom's cell parser.
	lines := strings.Split(ansi.Strip(bloom.Render(40, 3, bloom.Fixed(40, bloom.Text(got)))), "\n")
	if !strings.HasPrefix(lines[0], "row zero content here pad") {
		t.Fatalf("indicator clobbered the wrong cells: %q", lines[0])
	}
	if !strings.Contains(lines[0], "↑ scrollback") {
		t.Fatalf("indicator lost through bloom parse: %q", lines[0])
	}

	s.Scrolled = false
	if got := renderScreen(s); strings.Contains(got, "scrollback") {
		t.Fatalf("indicator rendered while live: %q", got)
	}

	// Too narrow: the marker stands down rather than truncating row 0.
	narrow := blankScreen(10, 2)
	putLine(narrow, 0, 0, "0123456789", pane.Style{})
	narrow.Scrolled = true
	if got := renderScreen(narrow); strings.Contains(got, "scrollback") {
		t.Fatalf("indicator rendered on narrow screen: %q", got)
	}
}
