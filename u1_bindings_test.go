package loom_test

// U1: prove the cgo bindings work on this machine against the locally built
// libghostty-vt. Throwsaway check, not a design artifact. Moves into the
// package layout the design synthesis picks.

import (
	"strings"
	"testing"

	"go.mitchellh.com/libghostty"
)

func TestBindingsHello(t *testing.T) {
	term, err := libghostty.NewTerminal(libghostty.WithSize(80, 24))
	if err != nil {
		t.Fatalf("NewTerminal: %v", err)
	}
	defer term.Close()

	if _, err := term.Write([]byte("Hello, \x1b[1;32mworld\x1b[0m!\r\n")); err != nil {
		t.Fatalf("VTWrite: %v", err)
	}

	f, err := libghostty.NewFormatter(term,
		libghostty.WithFormatterFormat(libghostty.FormatterFormatPlain),
		libghostty.WithFormatterTrim(true),
	)
	if err != nil {
		t.Fatalf("NewFormatter: %v", err)
	}
	defer f.Close()

	out, err := f.FormatString()
	if err != nil {
		t.Fatalf("FormatString: %v", err)
	}
	if strings.TrimSpace(out) != "Hello, world!" {
		t.Fatalf("screen text = %q, want Hello, world!", out)
	}
	t.Logf("screen: %q", out)
}
