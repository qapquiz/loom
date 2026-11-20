package ui

// Input mapping: bubbletea events to pane's data-only Key and Mouse. tea's
// key Code is a rune; ghostty wants W3C physical key codes plus mods, so the
// table below carries the functional keys and the punctuation, while letters
// and digits map positionally (contiguity is pinned by a test against the
// bindings' own canonical names). Printable text rides Text so the KeyEncoder
// sees pure UTF-8 alongside the code it needs for the kitty protocol.

import (
	tea "charm.land/bubbletea/v2"

	"go.mitchellh.com/libghostty"

	"loom/pane"
)

var keyCodes = map[rune]libghostty.Key{
	tea.KeyEnter:     libghostty.KeyEnter,
	tea.KeyTab:       libghostty.KeyTab,
	tea.KeyBackspace: libghostty.KeyBackspace,
	tea.KeyEscape:    libghostty.KeyEscape,
	tea.KeySpace:     libghostty.KeySpace,
	tea.KeyUp:        libghostty.KeyArrowUp,
	tea.KeyDown:      libghostty.KeyArrowDown,
	tea.KeyLeft:      libghostty.KeyArrowLeft,
	tea.KeyRight:     libghostty.KeyArrowRight,
	tea.KeyHome:      libghostty.KeyHome,
	tea.KeyEnd:       libghostty.KeyEnd,
	tea.KeyPgUp:      libghostty.KeyPageUp,
	tea.KeyPgDown:    libghostty.KeyPageDown,
	tea.KeyDelete:    libghostty.KeyDelete,
	tea.KeyInsert:    libghostty.KeyInsert,
	tea.KeyF1:        libghostty.KeyF1,
	tea.KeyF2:        libghostty.KeyF2,
	tea.KeyF3:        libghostty.KeyF3,
	tea.KeyF4:        libghostty.KeyF4,
	tea.KeyF5:        libghostty.KeyF5,
	tea.KeyF6:        libghostty.KeyF6,
	tea.KeyF7:        libghostty.KeyF7,
	tea.KeyF8:        libghostty.KeyF8,
	tea.KeyF9:        libghostty.KeyF9,
	tea.KeyF10:       libghostty.KeyF10,
	tea.KeyF11:       libghostty.KeyF11,
	tea.KeyF12:       libghostty.KeyF12,
	'`':              libghostty.KeyBackquote,
	'-':              libghostty.KeyMinus,
	'=':              libghostty.KeyEqual,
	'[':              libghostty.KeyBracketLeft,
	']':              libghostty.KeyBracketRight,
	'\\':             libghostty.KeyBackslash,
	';':              libghostty.KeySemicolon,
	'\'':             libghostty.KeyQuote,
	',':              libghostty.KeyComma,
	'.':              libghostty.KeyPeriod,
	'/':              libghostty.KeySlash,
}

func toPaneKey(k tea.Key) pane.Key {
	pk := pane.Key{Text: k.Text}
	if code, ok := keyCodes[k.Code]; ok {
		pk.Code = code
	} else if k.Code >= 'a' && k.Code <= 'z' {
		pk.Code = libghostty.KeyA + libghostty.Key(k.Code-'a')
	} else if k.Code >= '0' && k.Code <= '9' {
		pk.Code = libghostty.KeyDigit0 + libghostty.Key(k.Code-'0')
	}
	pk.Mods = modsOf(k.Mod)
	return pk
}

func modsOf(m tea.KeyMod) libghostty.Mods {
	var out libghostty.Mods
	if m&tea.ModShift != 0 {
		out |= libghostty.ModShift
	}
	if m&tea.ModAlt != 0 {
		out |= libghostty.ModAlt
	}
	if m&tea.ModCtrl != 0 {
		out |= libghostty.ModCtrl
	}
	if m&(tea.ModMeta|tea.ModHyper|tea.ModSuper) != 0 {
		out |= libghostty.ModSuper
	}
	return out
}

// mouseToOp maps a screen mouse event to a pane-relative op. Cell coords,
// because that is the boundary data shape pane.Mouse documents.
func mouseToOp(msg tea.MouseMsg, x, y int) pane.Op {
	m := msg.Mouse()
	action, button := mouseAction(msg)
	return pane.Op{
		Kind: pane.OpMouse,
		Mouse: pane.Mouse{
			Action: action,
			Button: button,
			Mods:   modsOf(m.Mod),
			Col:    uint16(x),
			Row:    uint16(y),
		},
	}
}

func mouseAction(msg tea.MouseMsg) (libghostty.MouseAction, libghostty.MouseButton) {
	switch m := msg.(type) {
	case tea.MouseClickMsg:
		return libghostty.MouseActionPress, ghosttyButton(m.Button)
	case tea.MouseReleaseMsg:
		return libghostty.MouseActionRelease, ghosttyButton(m.Button)
	case tea.MouseWheelMsg:
		// xterm convention: the wheel is a press of buttons four and up.
		return libghostty.MouseActionPress, ghosttyButton(m.Button)
	case tea.MouseMotionMsg:
		return libghostty.MouseActionMotion, ghosttyButton(m.Button)
	}
	return libghostty.MouseActionMotion, libghostty.MouseButtonUnknown
}

func ghosttyButton(b tea.MouseButton) libghostty.MouseButton {
	switch b {
	case tea.MouseLeft: // ansi MouseButton1
		return libghostty.MouseButtonLeft
	case tea.MouseMiddle: // ansi MouseButton2
		return libghostty.MouseButtonMiddle
	case tea.MouseRight: // ansi MouseButton3
		return libghostty.MouseButtonRight
	}
	if b >= tea.MouseWheelUp && b <= 11 {
		return libghostty.MouseButtonFour + libghostty.MouseButton(int(b)-int(tea.MouseWheelUp))
	}
	return libghostty.MouseButtonUnknown
}
