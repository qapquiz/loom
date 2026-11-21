package pane

// Emulator ownership: every libghostty handle of this pane lives here and is
// touched only on the session goroutine (hc's read loop), per the bindings'
// single-goroutine contract.

import (
	"strings"

	"go.mitchellh.com/libghostty"
)

type emulator struct {
	term     *libghostty.Terminal
	rs       *libghostty.RenderState
	rows     *libghostty.RenderStateRowIterator
	cells    *libghostty.RenderStateRowCells
	keyEnc   *libghostty.KeyEncoder
	mouseEnc *libghostty.MouseEncoder
	title    string
	status   string
}

func (p *Pane) resetEmulator() {
	if p.emu != nil {
		p.emu.close()
	}
	p.emu = newEmulator(p)
}

// newEmulator wires the effects. WritePty routes to AUTOMATIC_INPUT through
// hc's negotiated gate (the pane never re-derives negotiation); DeviceAttributes
// and ProgramStatus are set because ghostty only answers those queries when
// their effects are registered.
func newEmulator(p *Pane) *emulator {
	e := &emulator{}
	term, err := libghostty.NewTerminal(libghostty.WithSize(p.wantCol, p.wantRow))
	if err != nil {
		// A dead emulator paints nothing; keep the pane alive for the machine.
		return e
	}
	rs, err := libghostty.NewRenderState()
	if err != nil {
		term.Close()
		return e
	}
	rows, err := libghostty.NewRenderStateRowIterator()
	if err != nil {
		term.Close()
		rs.Close()
		return e
	}
	cells, err := libghostty.NewRenderStateRowCells()
	if err != nil {
		term.Close()
		rs.Close()
		rows.Close()
		return e
	}
	keyEnc, err := libghostty.NewKeyEncoder()
	if err != nil {
		term.Close()
		rs.Close()
		rows.Close()
		cells.Close()
		return e
	}
	mouseEnc, err := libghostty.NewMouseEncoder()
	if err != nil {
		term.Close()
		rs.Close()
		rows.Close()
		cells.Close()
		keyEnc.Close()
		return e
	}

	term.SetEffectWritePty(func(_ *libghostty.Terminal, data []byte) {
		if p.out != nil {
			p.out.AutoReply(data)
		}
	})
	term.SetEffectDeviceAttributes(func(_ *libghostty.Terminal) (libghostty.DeviceAttributes, bool) {
		return libghostty.DeviceAttributes{}, true
	})
	term.SetEffectTitleChanged(func(t *libghostty.Terminal) {
		if title, err := t.Title(); err == nil {
			e.title = title
		}
	})
	term.SetEffectProgramStatus(func(_ *libghostty.Terminal, status libghostty.ProgramStatus) {
		e.status = statusWord(status.State)
	})
	// Size-report truth: rows/cols from the emulator itself, cell px from the
	// stored terminal geometry. One handler derives every report style
	// (CSI 14/16/18/19 t, mode 2048) from the single SizeReportSize.
	term.SetEffectSize(func(t *libghostty.Terminal) (libghostty.SizeReportSize, bool) {
		cols, err := t.Cols()
		if err != nil {
			return libghostty.SizeReportSize{}, false
		}
		rows, err := t.Rows()
		if err != nil {
			return libghostty.SizeReportSize{}, false
		}
		return libghostty.SizeReportSize{
			Rows:       rows,
			Columns:    cols,
			CellWidth:  p.cellW,
			CellHeight: p.cellH,
		}, true
	})

	e.term = term
	e.rs = rs
	e.rows = rows
	e.cells = cells
	e.keyEnc = keyEnc
	e.mouseEnc = mouseEnc
	return e
}

func (e *emulator) close() {
	if e.term != nil {
		e.term.Close()
	}
	if e.rs != nil {
		e.rs.Close()
	}
	if e.rows != nil {
		e.rows.Close()
	}
	if e.cells != nil {
		e.cells.Close()
	}
	if e.keyEnc != nil {
		e.keyEnc.Close()
	}
	if e.mouseEnc != nil {
		e.mouseEnc.Close()
	}
	*e = emulator{}
}

func (e *emulator) write(b []byte) {
	if e.term == nil {
		return
	}
	e.term.VTWrite(b)
}

func statusWord(s libghostty.ProgramStatusState) string {
	switch s {
	case libghostty.ProgramStatusStateIdle:
		return "idle"
	case libghostty.ProgramStatusStateWorking:
		return "working"
	case libghostty.ProgramStatusStateDone:
		return "done"
	case libghostty.ProgramStatusStateBlocked:
		return "blocked"
	case libghostty.ProgramStatusStateError:
		return "error"
	default:
		return ""
	}
}

// publish snapshots the render state into an immutable Screen (graft 3: the
// publish is skipped entirely when ghostty reports the frame clean — idle
// panes cost nothing). force bypasses that skip: viewport scrolling may not
// mark the frame dirty, but the rows the iterator yields have changed, so
// the scroll ops pay a publish unconditionally.
func (p *Pane) publish(force bool) {
	e := p.emu
	if e == nil || e.rs == nil || e.term == nil {
		return
	}
	if err := e.rs.Update(e.term); err != nil {
		return
	}
	if !force {
		if dirty, err := e.rs.Dirty(); err == nil && dirty == libghostty.RenderStateDirtyFalse {
			return
		}
	}
	// Encoder modes (application cursor keys, kitty flags, mouse tracking)
	// come from the terminal itself; resync after every Update.
	e.keyEnc.SetFromTerminal(e.term)
	e.mouseEnc.SetFromTerminal(e.term)

	cols, err := e.rs.Cols()
	if err != nil {
		return
	}
	rows, err := e.rs.Rows()
	if err != nil {
		return
	}
	p.syncMouseSize(uint16(cols), uint16(rows))
	screen := &Screen{
		Cols:   int(cols),
		Rows:   int(rows),
		Cells:  make([]Cell, 0, int(cols)*int(rows)),
		Title:  e.title,
		Status: e.status,
	}
	if err := e.rs.RowIterator(e.rows); err == nil {
		for e.rows.Next() && len(screen.Cells) < int(cols)*int(rows) {
			if err := e.rows.Cells(e.cells); err != nil {
				break
			}
			for x := 0; x < int(cols); x++ {
				if !e.cells.Next() {
					break
				}
				screen.Cells = append(screen.Cells, readCell(e.cells))
			}
		}
	}
	if cur, err := e.rs.Cursor(); err == nil {
		screen.Cursor = Cursor{
			Col:     int(cur.ViewportX),
			Row:     int(cur.ViewportY),
			Visible: cur.ViewportHasValue && cur.Visible,
		}
		// Scrolled truth: a cursor exists but sits outside the viewport. A
		// failed cursor read is no-cursor, not scrolled. While scrolled the
		// viewport stays anchored — publish keeps rendering history rows and
		// this flag stays true until OpScrollBottom runs.
		screen.Scrolled = !cur.ViewportHasValue
	}
	if on, err := e.term.MouseTracking(); err == nil {
		screen.MouseOn = on
	}
	p.screen.Store(screen)
	// Mark the frame consumed so the next Update reports DirtyFalse when the
	// terminal is idle — that skip is the whole graft-3 win.
	e.rs.Clean()
	p.wakeOnce()
}

// asciiCellText interns the single-ASCII-byte cluster strings so the common
// screen content costs no allocation per cell.
var asciiCellText = func() [128]string {
	var a [128]string
	for i := range a {
		a[i] = string(rune(i))
	}
	return a
}()

// graphemeScratch is reused across cells; readCell runs only on the session
// goroutine.
var graphemeScratch = make([]uint32, 0, 8)

func readCell(rc *libghostty.RenderStateRowCells) Cell {
	c := Cell{}
	cp, err := rc.Raw()
	if err != nil {
		return c
	}
	// Blanks skip the grapheme read: on real screens they dominate.
	if hasText, err := cp.HasText(); err == nil && hasText {
		cps, err := rc.GraphemesInto(graphemeScratch[:0])
		if err == nil && len(cps) > 0 {
			if len(cps) == 1 && cps[0] < 128 {
				c.Text = asciiCellText[cps[0]]
			} else {
				var b strings.Builder
				for _, r := range cps {
					b.WriteRune(rune(r))
				}
				c.Text = b.String()
			}
		}
	}
	if wide, err := cp.Wide(); err == nil && wide == libghostty.CellWideWide {
		c.Wide = true
	}
	var snap libghostty.RenderCellStyle
	if err := rc.StyleInto(&snap); err == nil {
		if snap.HasForeground {
			c.S.FG = packRGB(snap.Foreground)
		}
		if snap.HasBackground {
			c.S.BG = packRGB(snap.Background)
		}
		if snap.Bold {
			c.S.Attr |= AttrBold
		}
		if snap.Faint {
			c.S.Attr |= AttrFaint
		}
		if snap.Italic {
			c.S.Attr |= AttrItalic
		}
		if snap.Underline {
			c.S.Attr |= AttrUnderline
		}
		if snap.Strikethrough {
			c.S.Attr |= AttrStrikethrough
		}
		if snap.Inverse {
			c.S.Attr |= AttrInverse
		}
	}
	return c
}

func packRGB(c libghostty.ColorRGB) uint32 {
	return uint32(c.R)<<16 | uint32(c.G)<<8 | uint32(c.B)
}

// syncMouseSize feeds the encoder the current geometry: screen px are
// cols×cellW and rows×cellH. The compromise is conditional — real cell px
// when loom's own TIOCGWINSZ reported it, the 1x1 synthetic cell headless,
// where cells and pixels coincide so cell-coordinate protocols (X10, SGR,
// URxvt) still encode correctly and only pixel SGR stays approximate. The
// encoder refuses to emit without a non-zero cell size; cellW/cellH are
// guarded to ≥1 at the store (SetCellPx), never 0 here.
func (p *Pane) syncMouseSize(cols, rows uint16) {
	if p.emu == nil || p.emu.mouseEnc == nil {
		return
	}
	p.emu.mouseEnc.SetSize(libghostty.MouseEncoderSize{
		ScreenWidth:  uint32(cols) * p.cellW,
		ScreenHeight: uint32(rows) * p.cellH,
		CellWidth:    p.cellW,
		CellHeight:   p.cellH,
	})
}
