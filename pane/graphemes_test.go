package pane

// Combining marks and ZWJ sequences used to collapse to the base codepoint;
// the Screen must carry the full grapheme cluster per cell.

import (
	"strings"
	"testing"
	"time"

	"loom/internal/testcore"
)

// Combining marks and ZWJ sequences used to collapse to the base codepoint;
// the Screen must carry the full grapheme cluster per cell.
func TestGraphemeClustersSurviveInScreen(t *testing.T) {
	core := testcore.Serve(t)
	_, p := wirePane(t, core, false)
	attach(t, p)

	// "cafe" + U+0301 combining acute, then the ZWJ family emoji.
	core.Delta([]byte("cafe\xcc\x81 \xf0\x9f\x91\xa8\xe2\x80\x8d\xf0\x9f\x91\xa9\xe2\x80\x8d\xf0\x9f\x91\xa7"))
	if !waitFor(2*time.Second, func() bool {
		return strings.Contains(screenText(p.Screen()), "caf")
	}) {
		t.Fatal("paint never landed")
	}

	s := p.Screen()
	var acute, family *Cell
	for i := range s.Cells {
		switch {
		case s.Cells[i].Text == "e\u0301":
			acute = &s.Cells[i]
		case strings.ContainsRune(s.Cells[i].Text, 0x200D):
			family = &s.Cells[i]
		}
	}
	if acute == nil {
		t.Fatal("combining acute lost: no cell holds e+U+0301")
	}
	if family == nil {
		t.Fatal("ZWJ family emoji lost: no cell holds the cluster")
	}
	if !family.Wide {
		t.Fatal("ZWJ emoji cell not flagged wide")
	}
	// Verbatim: the screen keeps the decomposed e+U+0301, not a composed é.
	if !strings.HasPrefix(screenText(s), "cafe\u0301 ") {
		t.Fatalf("screen text = %q, want decomposed e+U+0301 preserved", screenText(s))
	}
}
