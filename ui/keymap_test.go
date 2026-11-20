package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"go.mitchellh.com/libghostty"

	"loom/pane"
)

// The positional letter/digit ranges lean on the bindings' enum being
// contiguous; the canonical names are the library's own ground truth.
func TestGhosttyKeyRangesContiguous(t *testing.T) {
	for i := 0; i < 26; i++ {
		k := libghostty.KeyA + libghostty.Key(i)
		if want := "key_" + string(rune('a'+i)); k.String() != want {
			t.Fatalf("KeyA+%d = %s, want %s", i, k.String(), want)
		}
	}
	for i := 0; i < 10; i++ {
		k := libghostty.KeyDigit0 + libghostty.Key(i)
		if want := "digit_" + string(rune('0'+i)); k.String() != want {
			t.Fatalf("KeyDigit0+%d = %s, want %s", i, k.String(), want)
		}
	}
}

func TestGhosttyWheelButtonsContiguous(t *testing.T) {
	names := []string{"four", "five", "six", "seven", "eight", "nine", "ten", "eleven"}
	for i, want := range names {
		b := libghostty.MouseButtonFour + libghostty.MouseButton(i)
		if b.String() != want {
			t.Fatalf("MouseButtonFour+%d = %s, want %s", i, b.String(), want)
		}
	}
}

func TestToPaneKeyTable(t *testing.T) {
	cases := []struct {
		name string
		in   tea.Key
		want pane.Key
	}{
		{"plain letter", tea.Key{Code: 'x', Text: "x"},
			pane.Key{Text: "x", Code: libghostty.KeyX}},
		{"ctrl-a", tea.Key{Code: 'a', Mod: tea.ModCtrl},
			pane.Key{Code: libghostty.KeyA, Mods: libghostty.ModCtrl}},
		{"ctrl-shift-c", tea.Key{Code: 'c', Mod: tea.ModCtrl | tea.ModShift},
			pane.Key{Code: libghostty.KeyC, Mods: libghostty.ModCtrl | libghostty.ModShift}},
		{"alt-period", tea.Key{Code: '.', Mod: tea.ModAlt},
			pane.Key{Code: libghostty.KeyPeriod, Mods: libghostty.ModAlt}},
		{"shifted capital", tea.Key{Code: 'X', Text: "X", Mod: tea.ModShift},
			pane.Key{Text: "X", Mods: libghostty.ModShift}},
		{"enter", tea.Key{Code: tea.KeyEnter},
			pane.Key{Code: libghostty.KeyEnter}},
		{"escape with ctrl", tea.Key{Code: tea.KeyEscape, Mod: tea.ModCtrl},
			pane.Key{Code: libghostty.KeyEscape, Mods: libghostty.ModCtrl}},
		{"f5", tea.Key{Code: tea.KeyF5},
			pane.Key{Code: libghostty.KeyF5}},
		{"digit", tea.Key{Code: '7', Text: "7"},
			pane.Key{Text: "7", Code: libghostty.KeyDigit7}},
		{"meta maps to super", tea.Key{Code: 'v', Text: "v", Mod: tea.ModMeta},
			pane.Key{Text: "v", Code: libghostty.KeyV, Mods: libghostty.ModSuper}},
		{"space", tea.Key{Code: tea.KeySpace, Text: " "},
			pane.Key{Text: " ", Code: libghostty.KeySpace}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := toPaneKey(c.in); got != c.want {
				t.Fatalf("toPaneKey(%+v) = %+v, want %+v", c.in, got, c.want)
			}
		})
	}
}

func TestMouseToOp(t *testing.T) {
	click := tea.MouseClickMsg{X: 5, Y: 3, Button: tea.MouseLeft}
	op := mouseToOp(click, 5, 3)
	want := pane.Op{Kind: pane.OpMouse, Mouse: pane.Mouse{
		Action: libghostty.MouseActionPress, Button: libghostty.MouseButtonLeft,
		Col: 5, Row: 3,
	}}
	if op != want {
		t.Fatalf("mouseToOp = %+v, want %+v", op, want)
	}

	wheel := tea.MouseWheelMsg{X: 1, Y: 1, Button: tea.MouseWheelUp}
	op = mouseToOp(wheel, 1, 1)
	if op.Mouse.Action != libghostty.MouseActionPress || op.Mouse.Button != libghostty.MouseButtonFour {
		t.Fatalf("wheel op = %+v", op.Mouse)
	}

	motion := tea.MouseMotionMsg{X: 7, Y: 9, Button: tea.MouseLeft}
	op = mouseToOp(motion, 7, 9)
	if op.Mouse.Action != libghostty.MouseActionMotion || op.Mouse.Button != libghostty.MouseButtonLeft {
		t.Fatalf("motion op = %+v", op.Mouse)
	}
}
