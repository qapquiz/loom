package ui

// Benchmarks for the per-changed-frame path. Two legs, measured separately
// and synchronous, so the work provably happens inside the timed region:
//
//   - BenchmarkPaneDelta: emulator leg. VTWrite of a full styled 200x60
//     screen, RenderState.Update, Screen build, publish. Called directly on
//     the pane with no concurrent session traffic, so the single-goroutine
//     handle contract holds.
//   - BenchmarkRenderScreen: node leg. Screen to styled string, one pass of
//     SGR assembly.
//
// The third leg, bloom parsing the returned string, is bloom's own measured
// number (about 0.3 ms for a mostly-changed 200x60 frame), not repeated here.

import (
	"testing"

	"loom/hc"
	"loom/internal/testcore"
	"loom/pane"
	"time"
)

// benchPayload builds a full styled 200x60 screen: per-row 256-color
// foreground and background, bold on every third row, one wide-glyph row,
// lowercase text everywhere else.
func benchPayload() []byte {
	var b []byte
	for row := 0; row < 60; row++ {
		b = append(b, "\x1b[0m\x1b[38;5;"+itoa(17+row%200)+";48;5;"+itoa(235+row%20)+"m"...)
		if row%3 == 0 {
			b = append(b, "\x1b[1m"...)
		}
		for col := 0; col < 200; col++ {
			if row == 30 && col%2 == 0 {
				b = append(b, "漢"...)
				continue
			}
			b = append(b, byte('a'+(row+col)%26))
		}
		b = append(b, "\x1b[0m\r\n"...)
	}
	return b
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var d []byte
	for n > 0 {
		d = append([]byte{byte('0' + n%10)}, d...)
		n /= 10
	}
	return string(d)
}

// BenchmarkPaneDelta measures one full publish of a 200x60 styled screen.
// Each iteration mutates one byte before writing, so the dirty check passes
// and the Screen build runs every time.
func BenchmarkPaneDelta(b *testing.B) {
	_, _, p := wirePane(b)
	p.Change(pane.ChangeAttach)
	if !waitForPhase(b, p, 2*time.Second, pane.StateAttached) {
		b.Fatal("pane never attached")
	}
	payload := benchPayload()
	p.Delta(payload)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		payload[100+i%8000] = byte('A' + i%26)
		p.Delta(payload)
	}
	b.StopTimer()
}

// BenchmarkRenderScreen measures the node leg at the same size, cursor
// visible, so the inverse-video transition is included.
func BenchmarkRenderScreen(b *testing.B) {
	s := &pane.Screen{Cols: 200, Rows: 60, Cells: make([]pane.Cell, 200*60)}
	for row := 0; row < 60; row++ {
		for col := 0; col < 200; col++ {
			c := &s.Cells[row*200+col]
			c.Text = string(rune('a' + (row+col)%26))
			c.S.FG = uint32(0x100000 + row*0x1000)
			c.S.BG = uint32(0x010000 + col*0x100)
			if row%3 == 0 {
				c.S.Attr = pane.AttrBold
			}
		}
	}
	s.Cursor = pane.Cursor{Col: 100, Row: 30, Visible: true}
	b.ReportAllocs()
	b.ResetTimer()
	var sink string
	for i := 0; i < b.N; i++ {
		sink = renderScreen(s)
	}
	if len(sink) == 0 {
		b.Fatal("empty render")
	}
}

// wirePaneB is the benchmark twin of wirePane: ServeTB core, real socket,
// real session, pane wired the way the App wires it.
func wirePaneB(b *testing.B) (*testcore.Core, *hc.Session, *pane.Pane) {
	core := testcore.ServeTB(b)
	sess, err := hc.Dial(core.SocketPath(), hc.Features{})
	if err != nil {
		b.Fatal("Dial:", err)
	}
	b.Cleanup(func() { sess.Close() })
	p := pane.New("t7", nil)
	p.SetPump(sess.Do)
	p.SetAttachFn(func() { sess.Attach("t7", hcStream{p: p}) })
	return core, sess, p
}
