package ui

// The multi-pane data shapes (the wave-1 keybinding contract): the input
// mode machine, the binding table, the layout tree, and the pane slot.
// Everything else in ui consumes these; nothing here consumes the app.

import (
	"bloomfx.dev/bloom"
	tea "charm.land/bubbletea/v2"

	"loom/hc"
	"loom/pane"
)

// inputMode is the keyboard state machine, not booleans: passthrough feeds
// the focused pane, prefix swallows exactly one key, and the chooser is
// reserved for wave 2's window picker.
type inputMode uint8

const (
	modePassthrough inputMode = iota
	modePrefix
	modeChooser // wave 2: cmdChooseWindow enters it; nothing does yet
)

// command is what a bound key means. The enum, not the rune, is the
// vocabulary the app switches on; wave 2 adds commands without touching the
// input machine.
type command uint8

const (
	cmdSplitVertical   command = iota // 'v': side-by-side halves (Row)
	cmdSplitHorizontal                // '-': stacked halves (Column)
	cmdChooseWindow                   // 'w': wave 2 chooser; bound but inert
)

// bindings is the single source of key meaning: rune to command. Shifted
// capitals and chords never reach this table (bindRune's contract).
var bindings = map[rune]command{
	'v': cmdSplitVertical,
	'-': cmdSplitHorizontal,
	'w': cmdChooseWindow,
}

// bindRune extracts the binding-table key from a keypress: one printable
// rune, no chord modifiers. tmux reads the bare key, so 'V' is not 'v'.
func bindRune(msg tea.KeyPressMsg) (rune, bool) {
	if msg.Mod != 0 {
		return 0, false
	}
	if r := []rune(msg.Text); len(r) == 1 {
		return r[0], true
	}
	if msg.Code >= 0x20 && msg.Code < 0x7f {
		return msg.Code, true
	}
	return 0, false
}

// layoutNode is the pane tree: a leaf (leaf set, a/b nil) or a binary split.
// vert=true lays the halves side by side (bloom Row); false stacks them
// (bloom Column). v1 splits are fixed 50/50 Fill/Fill.
type layoutNode struct {
	leaf *paneSlot
	vert bool
	a, b *layoutNode
}

// paneSlot is one attached terminal's UI side: the pane itself, its bloom
// leaf, and the per-pane viewer bookkeeping. Every pane is its own viewer
// of its own terminal, so the sent-size debt and the attach-confirmation
// wake are per slot, not per app.
type paneSlot struct {
	id   hc.TerminalID // doubles as the bloom rect id: postSize and hit-testing key on it
	p    *pane.Pane
	node *PaneNode

	sentW, sentH int  // last size posted from solved frame geometry
	liveSeen     bool // first live wake paid this pane's FOCUS/facts debts yet
}

// child renders the subtree as bloom children: leaf slots are Fill cells
// carrying the pane id as their rect id, splits are Fill/Fill halves.
func (n *layoutNode) child() bloom.Child {
	if n.leaf != nil {
		return bloom.Fill(n.leaf.node).ID(n.leaf.id)
	}
	a, b := n.a.child(), n.b.child()
	if n.vert {
		return bloom.Row(a, b)
	}
	return bloom.Column(a, b)
}

// split replaces the leaf holding id with a node pairing it (as a) with
// fresh (as b) and reports whether the tree changed. The focused pane keeps
// its half; the new slot takes the other.
func (n *layoutNode) split(id hc.TerminalID, fresh *layoutNode, vert bool) bool {
	if n.leaf != nil {
		if n.leaf.id != id {
			return false
		}
		*n = layoutNode{vert: vert, a: &layoutNode{leaf: n.leaf}, b: fresh}
		return true
	}
	if n.a.split(id, fresh, vert) {
		return true
	}
	return n.b.split(id, fresh, vert)
}

// leaves walks the tree's slots in layout order (a before b).
func (n *layoutNode) leaves(fn func(*paneSlot)) {
	if n.leaf != nil {
		fn(n.leaf)
		return
	}
	n.a.leaves(fn)
	n.b.leaves(fn)
}
