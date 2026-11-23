package ui

// The prefix state machine as a table over the real loopback app: mode
// transitions, swallowed keys, the literal 0x02, and ctrl+q surviving the
// new mode. runMsg side effects (the split) land in multipane_test.go.

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"loom/internal/testcore"
	"loom/pane"
)

func TestAppPrefixStateMachine(t *testing.T) {
	ctrlB := tea.KeyPressMsg{Code: 'b', Mod: tea.ModCtrl}
	ctrlQ := tea.KeyPressMsg{Code: 'q', Mod: tea.ModCtrl}
	cases := []struct {
		name string
		keys []tea.KeyPressMsg
		want inputMode
		data string // DATA the focused pane received ("" = none)
		runs int
		quit bool
	}{
		{"passthrough forwards", []tea.KeyPressMsg{{Code: 'x', Text: "x"}}, modePassthrough, "x", 0, false},
		{"prefix swallows itself", []tea.KeyPressMsg{ctrlB}, modePrefix, "", 0, false},
		{"prefix then bound split runs", []tea.KeyPressMsg{ctrlB, {Code: 'v', Text: "v"}}, modePassthrough, "", 1, false},
		{"prefix then w hands the keyboard to the sidebar", []tea.KeyPressMsg{ctrlB, {Code: 'w', Text: "w"}}, modeSidebar, "", 0, false},
		{"prefix drops unbound key", []tea.KeyPressMsg{ctrlB, {Code: 'z', Text: "z"}}, modePassthrough, "", 0, false},
		{"prefix passes exactly one key", []tea.KeyPressMsg{ctrlB, {Code: 'z', Text: "z"}, {Code: 'x', Text: "x"}}, modePassthrough, "x", 0, false},
		{"double prefix sends literal 0x02", []tea.KeyPressMsg{ctrlB, ctrlB}, modePassthrough, "\x02", 0, false},
		{"ctrl+q quits from prefix mode", []tea.KeyPressMsg{ctrlB, ctrlQ}, modePassthrough, "", 0, true},
		{"pgup stays a pane key in passthrough", []tea.KeyPressMsg{{Code: tea.KeyPgUp}}, modePassthrough, "\x1b[5~", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, a := newRunApp(t, "t9")
			if !waitForPhase(t, a.panes["t7"].p, 2*time.Second, pane.StateAttached) {
				t.Fatal("no attach")
			}
			var quit bool
			for _, k := range tc.keys {
				_, cmd := a.Update(k)
				quit = quit || producesQuit(cmd)
			}
			if a.mode != tc.want {
				t.Fatalf("mode = %v, want %v", a.mode, tc.want)
			}
			if quit != tc.quit {
				t.Fatalf("quit = %v, want %v", quit, tc.quit)
			}
			if got := len(c.Runs()); got != tc.runs {
				t.Fatalf("terminal.run calls = %d, want %d", got, tc.runs)
			}
			// Data on the pane channel: wait out the session queue, then
			// scan for DATA frames on channel 7.
			if tc.data != "" {
				waitForFrame(t, c, 2*time.Second, func(f testcore.Frame) bool {
					return f.Kind == 0 && f.Ch == 7 && string(f.Payload) == tc.data
				})
			} else {
				time.Sleep(150 * time.Millisecond)
				for _, f := range c.Frames() {
					if f.Kind == 0 && f.Ch == 7 {
						t.Fatalf("key leaked to the pane: %q", f.Payload)
					}
				}
			}
		})
	}
}

// producesQuit reports whether cmd (batch included, like the tea runtime
// unwraps it) yields a quit message.
func producesQuit(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	switch msg := cmd().(type) {
	case tea.QuitMsg:
		return true
	case tea.BatchMsg:
		for _, sub := range msg {
			if producesQuit(sub) {
				return true
			}
		}
	}
	return false
}
