package ui

// Loopback wiring shared by the pane-node and app tests: a scripted core
// behind a real socket, an hc session over it, and a pane with the same
// pump/attach wiring the App uses.

import (
	"strings"
	"testing"
	"time"

	"loom/hc"
	"loom/internal/testcore"
	"loom/pane"
)

func wirePane(t testing.TB) (*testcore.Core, *hc.Session, *pane.Pane) {
	t.Helper()
	core := testcore.ServeTB(t)
	sess, err := hc.Dial(core.SocketPath(), hc.Features{})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	p := pane.New("t7", nil)
	p.SetPump(sess.Do)
	p.SetAttachFn(func() { sess.Attach("t7", p) })
	return core, sess, p
}

func waitForPhase(t testing.TB, p *pane.Pane, d time.Duration, want pane.State) bool {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if p.Phase() == want {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return false
}

func waitForScreenText(t *testing.T, p *pane.Pane, d time.Duration, want string) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if strings.Contains(plainText(p.Screen()), want) {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("screen never showed %q", want)
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
		if c.R == 0 {
			b.WriteByte(' ')
		} else {
			b.WriteRune(c.R)
		}
	}
	return b.String()
}
