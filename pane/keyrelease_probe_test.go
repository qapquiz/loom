package pane

import (
	"testing"

	"go.mitchellh.com/libghostty"
)

// Scratch: does the KeyEncoder emit a release when the terminal has kitty
// flags, independent of the pane?
func TestProbeKeyReleaseEncoder(t *testing.T) {
	term, err := libghostty.NewTerminal(libghostty.WithSize(20, 5))
	if err != nil {
		t.Fatalf("NewTerminal: %v", err)
	}
	defer term.Close()
	term.VTWrite([]byte("\x1b[>3u"))

	enc, err := libghostty.NewKeyEncoder()
	if err != nil {
		t.Fatalf("NewKeyEncoder: %v", err)
	}
	defer enc.Close()
	if err := enc.SetFromTerminal(term); err != nil {
		t.Fatalf("SetFromTerminal: %v", err)
	}

	ev, err := libghostty.NewKeyEvent()
	if err != nil {
		t.Fatalf("NewKeyEvent: %v", err)
	}
	defer ev.Close()
	ev.SetAction(libghostty.KeyActionRelease)
	ev.SetKey(libghostty.KeyA)
	b, err := enc.Encode(ev)
	t.Logf("release with flags>3: %q err=%v", b, err)

	term.VTWrite([]byte("\x1b[>0u"))
	enc.SetFromTerminal(term)
	b2, err2 := enc.Encode(ev)
	t.Logf("release with flags>0: %q err=%v", b2, err2)

	// Press for contrast under the same flags.
	ev2, _ := libghostty.NewKeyEvent()
	defer ev2.Close()
	ev2.SetAction(libghostty.KeyActionPress)
	ev2.SetKey(libghostty.KeyA)
	b3, err3 := enc.Encode(ev2)
	t.Logf("press with flags>3: %q err=%v", b3, err3)
}
