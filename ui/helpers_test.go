package ui

// Loopback wiring shared by the pane-node and app tests, and by benchmarks:
// a scripted core behind a real socket, an hc session over it, and a pane
// with the same pump/attach wiring the App uses.

import (
	"strings"
	"testing"
	"time"

	"loom/hc"
	"loom/internal/testcore"
	"loom/pane"
)

func wirePane(tb testing.TB) (*testcore.Core, *hc.Session, *pane.Pane) {
	tb.Helper()
	core := testcore.ServeTB(tb)
	sess, err := hc.Dial(core.SocketPath(), hc.Features{})
	if err != nil {
		tb.Fatalf("Dial: %v", err)
	}
	tb.Cleanup(func() { sess.Close() })
	p := pane.New("t7", nil)
	p.SetPump(sess.Do)
	p.SetAttachFn(func() { sess.Attach("t7", &hcStream{p: p}) })
	return core, sess, p
}

func waitForPhase(tb testing.TB, p *pane.Pane, d time.Duration, want pane.State) bool {
	tb.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if p.Phase() == want {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return false
}

func waitForScreenText(tb testing.TB, p *pane.Pane, d time.Duration, want string) {
	tb.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if strings.Contains(plainText(p.Screen()), want) {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	tb.Fatalf("screen never showed %q", want)
}

func plainText(s *pane.Screen) string {
	if s == nil {
		return ""
	}
	var b strings.Builder
	skip := false
	for _, c := range s.Cells {
		if skip {
			skip = false
			continue
		}
		skip = c.Wide
		if c.Text == "" {
			b.WriteByte(' ')
		} else {
			b.WriteString(c.Text)
		}
	}
	return b.String()
}
